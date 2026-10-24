package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type journalTestSink struct {
	fail   bool
	events []store.MeteredUsage
}

func (s *journalTestSink) RecordUsageAndDebit(_ context.Context, event store.MeteredUsage) error {
	if s.fail {
		return errors.New("synthetic database outage")
	}
	s.events = append(s.events, event)
	return nil
}

func journalGuardStatus(j *DurableMeterSink, billed bool) int {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx := WithUserID(c.Request.Context(), "test-user")
		if !billed {
			ctx = WithUnbilled(ctx)
		}
		c.Request = c.Request.WithContext(ctx)
	}, j.Guard())
	r.GET("/", func(c *gin.Context) { c.Status(200) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	return w.Code
}

func TestDurableMeterRecoversDatabaseOutageAcrossRestart(t *testing.T) {
	ctx := context.Background()
	sink := &journalTestSink{fail: true}
	dir := t.TempDir()
	j, err := NewDurableMeterSink(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	event := store.MeteredUsage{EventID: "synthetic-event", UserID: "test-user", Cost: "1.25"}
	if err = j.RecordUsageAndDebit(ctx, event); err == nil {
		t.Fatal("database outage not returned")
	}
	if status := journalGuardStatus(j, true); status != 503 {
		t.Fatalf("paid status=%d", status)
	}
	if status := journalGuardStatus(j, false); status != 200 {
		t.Fatalf("unbilled status=%d", status)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending events=%d err=%v", len(entries), err)
	}
	info, err := entries[0].Info()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal file not private: %v", err)
	}
	sink.fail = false
	restarted, err := NewDurableMeterSink(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	var invalidated string
	restarted.SetReplayHook(func(id string) { invalidated = id })
	if err = restarted.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 || sink.events[0].EventID != event.EventID || invalidated != event.UserID {
		t.Fatal("pending event identity or cache invalidation was lost")
	}
	if status := journalGuardStatus(restarted, true); status != 200 {
		t.Fatalf("recovered paid status=%d", status)
	}
	if err = restarted.Replay(ctx); err != nil || len(sink.events) != 1 {
		t.Fatal("empty journal replay repeated event")
	}
}

func TestDurableMeterPreservesDraftAndRejectsCorruptJournal(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable draft", true: "corrupt draft"}[corrupt], func(t *testing.T) {
			dir := t.TempDir()
			sink := &journalTestSink{}
			j, err := NewDurableMeterSink(sink, dir)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(store.MeteredUsage{EventID: "interrupted-rename", UserID: "test-user", Cost: "2"})
			if corrupt {
				data = []byte("{")
			}
			if err = os.WriteFile(filepath.Join(dir, ".event-interrupted"), data, 0600); err != nil {
				t.Fatal(err)
			}
			err = j.Replay(context.Background())
			if corrupt {
				if err == nil || journalGuardStatus(j, true) != 503 {
					t.Fatal("corrupt event did not pause billing")
				}
			} else if err != nil || len(sink.events) != 1 {
				t.Fatalf("draft was not recovered: %v", err)
			}
		})
	}
}

func TestDurableMeterWriteFailurePausesBilling(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	j, err := NewDurableMeterSink(&journalTestSink{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Replay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err = j.RecordUsageAndDebit(context.Background(), store.MeteredUsage{EventID: "event", UserID: "test-user", Cost: "1"}); err == nil {
		t.Fatal("missing journal directory was ignored")
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = j.Replay(context.Background()); err == nil {
		t.Fatal("lost journal write resumed without reconciliation")
	}
	if journalGuardStatus(j, true) != 503 {
		t.Fatal("write failure did not pause paid requests")
	}
}
