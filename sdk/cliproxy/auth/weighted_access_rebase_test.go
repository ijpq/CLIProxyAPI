package auth

import (
	"fmt"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedRoutingPreservesAllowedAuthIDs(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			for _, websocket := range []bool{false, true} {
				for _, pin := range []string{"", "allowed", "blocked"} {
					t.Run(fmt.Sprintf("legacy=%t/mixed=%t/websocket=%t/pin=%s", legacy, mixed, websocket, pin), func(t *testing.T) {
						manager := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
						manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
						manager.RegisterExecutor(schedulerTestExecutor{provider: "gemini"})
						for _, auth := range []*Auth{
							{ID: "allowed", Provider: "codex", Attributes: map[string]string{AttributeWeight: "1", "websockets": "true"}},
							{ID: "blocked", Provider: "codex", Attributes: map[string]string{AttributeWeight: "100", "websockets": "true"}},
							{ID: "blocked-other-provider", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "100"}},
						} {
							if _, errRegister := manager.Register(WithSkipPersist(t.Context()), auth); errRegister != nil {
								t.Fatal(errRegister)
							}
						}
						ctx := t.Context()
						if websocket {
							ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
						}
						for range 12 {
							opts := cliproxyexecutor.Options{Metadata: map[string]any{
								cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{"allowed"},
								cliproxyexecutor.PinnedAuthMetadataKey:     pin,
							}}
							var selected *Auth
							var errPick error
							switch {
							case legacy && mixed:
								selected, _, _, errPick = manager.pickNextMixedLegacy(ctx, []string{"codex", "gemini"}, "", opts, nil)
							case legacy:
								selected, _, errPick = manager.pickNextLegacy(ctx, "codex", "", opts, nil)
							case mixed:
								selected, _, _, errPick = manager.pickNextMixed(ctx, []string{"codex", "gemini"}, "", opts, nil)
							default:
								selected, _, errPick = manager.pickNext(ctx, "codex", "", opts, nil)
							}
							if pin == "blocked" {
								if selected != nil || errPick == nil {
									t.Fatalf("disallowed pin accepted: auth=%v err=%v", selected, errPick)
								}
								continue
							}
							if errPick != nil || selected == nil || selected.ID != "allowed" {
								t.Fatalf("restricted weighted selection: auth=%v err=%v", selected, errPick)
							}
						}
					})
				}
			}
		}
	}
}
