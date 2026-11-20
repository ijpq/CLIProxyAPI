package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Continuity and cooldown scans must use the same account boundary as dispatch.
func TestBillingWhitelistRestrictsContinuityAndCooldownCandidates(t *testing.T) {
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
	now := time.Now()
	recoverAt := now.Add(time.Hour)
	const model = "billing-continuity-test-model"
	for _, a := range []*Auth{
		{ID: "billing-continuity-allowed", Provider: "codex", Status: StatusActive,
			Unavailable: true, NextRetryAfter: recoverAt,
			Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt}},
		{ID: "billing-continuity-hidden", Provider: "codex", Status: StatusActive},
	} {
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{"billing-continuity-allowed"},
	}}
	candidates := manager.buildCodexContinuityCandidates(context.Background(), []string{"codex"}, model, opts, 0, false, 0, now)
	if len(candidates) != 1 || candidates[0].ID != "billing-continuity-allowed" {
		t.Errorf("continuity candidates = %+v; expected only the permitted account", candidates)
	}
	deadline, available, cooling := manager.earliestPoolCooldownForModelAt(context.Background(), []string{"codex"}, model, opts, now)
	if available || !cooling || !deadline.Equal(recoverAt) {
		t.Errorf("pool state = (%v, %v, %v); hidden account must not suppress permitted account cooldown", deadline, available, cooling)
	}
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "billing-continuity-hidden"
	if got := manager.buildCodexContinuityCandidates(context.Background(), []string{"codex"}, model, opts, 0, false, 0, now); len(got) != 0 {
		t.Errorf("pin outside the whitelist exposed candidates: %+v", got)
	}
}
