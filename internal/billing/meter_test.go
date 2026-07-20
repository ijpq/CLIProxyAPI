package billing

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type captureMeterSink struct {
	records []store.MeteredUsage
}

func (s *captureMeterSink) RecordUsageAndDebit(_ context.Context, record store.MeteredUsage) error {
	s.records = append(s.records, record)
	return nil
}

func TestMeterRecordsUsageForEveryBillingKeyPermission(t *testing.T) {
	tests := []struct {
		name     string
		context  func(context.Context) context.Context
		unbilled bool
	}{
		{
			name:    "billed account",
			context: func(ctx context.Context) context.Context { return ctx },
		},
		{
			name: "unbilled account",
			context: func(ctx context.Context) context.Context {
				return WithUnbilled(ctx)
			},
			unbilled: true,
		},
		{
			name: "unbilled account with model and upstream restrictions",
			context: func(ctx context.Context) context.Context {
				ctx = WithUnbilled(ctx)
				ctx = WithAllowedModels(ctx, []string{"model-1"})
				return WithAllowedAuthIDs(ctx, []string{"account-1"})
			},
			unbilled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &captureMeterSink{}
			meter := NewMeterPlugin(sink, nil)
			ctx := WithUserID(context.Background(), "user-1")
			ctx = WithAPIKeyID(ctx, "key-1")
			ctx = tt.context(ctx)

			meter.HandleUsage(ctx, usage.Record{
				Provider: "openai",
				Model:    "model-1",
				AuthID:   "account-1",
				Detail: usage.Detail{
					InputTokens:  12,
					OutputTokens: 7,
				},
			})

			if len(sink.records) != 1 {
				t.Fatalf("recorded usage rows = %d, want 1", len(sink.records))
			}
			got := sink.records[0]
			if got.UserID != "user-1" || got.APIKeyID != "key-1" {
				t.Fatalf("record identity = user %q key %q", got.UserID, got.APIKeyID)
			}
			if got.SkipDebit != tt.unbilled {
				t.Errorf("SkipDebit = %t, want %t", got.SkipDebit, tt.unbilled)
			}
			if got.InputTokens != 12 || got.OutputTokens != 7 {
				t.Errorf("recorded tokens = %d/%d, want 12/7", got.InputTokens, got.OutputTokens)
			}
		})
	}
}
