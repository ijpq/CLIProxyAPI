package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

// DurableMeterSink journals accounting events before the database is touched.
// The directory must be on a persistent volume dedicated to this instance.
type DurableMeterSink struct {
	sink        MeterSink
	dir         string
	mu          sync.Mutex
	healthy     atomic.Bool
	writeFailed bool
	onReplay    func(string)
}

func NewDurableMeterSink(sink MeterSink, dir string) (*DurableMeterSink, error) {
	if sink == nil || strings.TrimSpace(dir) == "" {
		return nil, errors.New("billing: configure BILLING_EVENT_DIR on a persistent volume")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("billing: event directory must be a real directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	j := &DurableMeterSink{sink: sink, dir: dir}
	probe, err := os.CreateTemp(dir, ".probe-")
	if err != nil {
		return nil, err
	}
	name := probe.Name()
	syncErr := probe.Sync()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if err = errors.Join(syncErr, closeErr, removeErr, j.syncDir()); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *DurableMeterSink) SetReplayHook(fn func(string)) { j.onReplay = fn }

func (j *DurableMeterSink) syncDir() error {
	d, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func (j *DurableMeterSink) RecordUsageAndDebit(ctx context.Context, u store.MeteredUsage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if strings.TrimSpace(u.EventID) == "" {
		j.healthy.Store(false)
		return errors.New("billing: accounting event ID is required")
	}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	path := filepath.Join(j.dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(u.EventID))))
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(existing, data) {
			j.healthy.Store(false)
			return errors.New("billing: conflicting journal event")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		j.healthy.Store(false)
		j.writeFailed = true
		return readErr
	} else {
		file, createErr := os.CreateTemp(j.dir, ".event-")
		if createErr != nil {
			j.healthy.Store(false)
			j.writeFailed = true
			return createErr
		}
		temp := file.Name()
		_, writeErr := file.Write(data)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err == nil {
			err = os.Rename(temp, path)
		}
		if err == nil {
			err = j.syncDir()
		}
		if err != nil {
			j.healthy.Store(false)
			j.writeFailed = true
			return err
		}
	}
	if err = j.sink.RecordUsageAndDebit(ctx, u); err != nil {
		j.healthy.Store(false)
		return err
	}
	if j.onReplay != nil {
		j.onReplay(u.UserID)
	}
	if err = os.Remove(path); err == nil {
		err = j.syncDir()
	}
	if err != nil {
		j.healthy.Store(false)
	}
	return err
}

// Replay is safe after an uncertain commit: the database deduplicates EventID.
func (j *DurableMeterSink) Replay(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		j.healthy.Store(false)
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") && !strings.HasPrefix(entry.Name(), ".event-") {
			continue
		}
		path := filepath.Join(j.dir, entry.Name())
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			j.healthy.Store(false)
			return errors.New("billing: invalid journal file; reconciliation required")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			j.healthy.Store(false)
			return readErr
		}
		var event store.MeteredUsage
		if err = json.Unmarshal(data, &event); err != nil || event.EventID == "" {
			j.healthy.Store(false)
			return errors.New("billing: corrupt journal event; reconciliation required")
		}
		if err = j.sink.RecordUsageAndDebit(ctx, event); err != nil {
			j.healthy.Store(false)
			return err
		}
		if err = os.Remove(path); err != nil {
			j.healthy.Store(false)
			return err
		}
		if j.onReplay != nil {
			j.onReplay(event.UserID)
		}
	}
	if err = j.syncDir(); err != nil {
		j.healthy.Store(false)
		return err
	}
	if j.writeFailed {
		return errors.New("billing: a journal write failed; manual reconciliation required before resuming")
	}
	j.healthy.Store(true)
	return nil
}

func (j *DurableMeterSink) Guard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if UserIDFromContext(c.Request.Context()) != "" && !UnbilledFromContext(c.Request.Context()) && !j.healthy.Load() {
			c.AbortWithStatusJSON(503, gin.H{"error": "accounting recovery in progress; paid requests are paused"})
		}
	}
}
