package usage

import (
	"context"
	"sync"
	"testing"
)

type synchronousTestPlugin struct {
	mu      sync.Mutex
	records []Record
}

func (p *synchronousTestPlugin) SynchronousUsage() bool { return true }
func (p *synchronousTestPlugin) HandleUsage(_ context.Context, record Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, record)
}

func TestAccountingPluginPersistsBeforePublishReturns(t *testing.T) {
	m := NewManager(0)
	defer m.Stop()
	p := &synchronousTestPlugin{}
	m.Register(p)
	for i := 0; i < 2; i++ {
		m.Publish(context.Background(), Record{RequestID: "shared-parent-execution"})
		p.mu.Lock()
		if len(p.records) != i+1 {
			t.Errorf("synchronous events=%d", len(p.records))
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.records[0].EventID == "" || p.records[0].EventID == p.records[1].EventID {
		t.Fatal("separate usage events share an idempotency ID")
	}
	if p.records[0].RequestID != "shared-parent-execution" {
		t.Fatal("accounting event overwrote request identity")
	}
}
