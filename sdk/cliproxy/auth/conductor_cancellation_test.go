package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestWrapStreamResultDoesNotPenalizeAuthForClientCancellation(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{"type": "codex"}}
	if _, errRegister := mgr.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Err: context.Canceled}
	close(remaining)

	result := mgr.wrapStreamResult(ctx, auth, "codex", "gpt-5", "gpt-5", nil, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{})
	for range result.Chunks {
	}

	gotAuth, ok := mgr.GetByID(auth.ID)
	if !ok || gotAuth == nil {
		t.Fatal("registered auth not found")
	}
	if gotAuth.Failed != 0 {
		t.Fatalf("auth failed count = %d, want 0", gotAuth.Failed)
	}
}
