package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type accessLimitExecutor struct {
	provider     string
	executeCalls int
	countCalls   int
	streamCalls  int
}

func (e *accessLimitExecutor) Identifier() string { return e.provider }

func (e *accessLimitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *accessLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls++
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *accessLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *accessLimitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls++
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*accessLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerAllowedModelsEnforcedAcrossExecutionModes(t *testing.T) {
	const (
		provider = "access-limit-provider"
		authID   = "access-limit-auth"
		model    = "access-limit-model"
	)
	registerSchedulerModels(t, provider, model, authID)
	executor := &accessLimitExecutor{provider: provider}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	forbiddenOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedModelsMetadataKey: []string{"another-model"},
	}}
	assertForbidden := func(t *testing.T, err error) {
		t.Helper()
		var authErr *Error
		if !errors.As(err, &authErr) || authErr.Code != "model_not_allowed" || authErr.HTTPStatus != http.StatusForbidden {
			t.Fatalf("error = %#v, want model_not_allowed HTTP 403", err)
		}
	}

	t.Run("execute", func(t *testing.T) {
		_, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, forbiddenOpts)
		assertForbidden(t, errExecute)
	})
	t.Run("count", func(t *testing.T) {
		_, errCount := manager.ExecuteCount(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, forbiddenOpts)
		assertForbidden(t, errCount)
	})
	t.Run("stream", func(t *testing.T) {
		_, errStream := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, forbiddenOpts)
		assertForbidden(t, errStream)
	})
	if executor.executeCalls != 0 || executor.countCalls != 0 || executor.streamCalls != 0 {
		t.Fatalf("forbidden request reached executor: execute=%d count=%d stream=%d", executor.executeCalls, executor.countCalls, executor.streamCalls)
	}

	allowedOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedModelsMetadataKey: []string{model},
	}}
	if _, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model + "(high)"}, allowedOpts); errExecute != nil {
		t.Fatalf("Execute(allowed suffix model) error = %v", errExecute)
	}
	if executor.executeCalls != 1 {
		t.Fatalf("allowed execute calls = %d, want 1", executor.executeCalls)
	}
}

func TestManagerAllowedModelsUsesClientVisibleModelBeforeRouting(t *testing.T) {
	const (
		provider = "access-route-provider"
		authID   = "access-route-auth"
		route    = "upstream-route-model"
	)
	registerSchedulerModels(t, provider, route, authID)
	executor := &accessLimitExecutor{provider: provider}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedModelsMetadataKey:      []string{"client-alias"},
		cliproxyexecutor.RequestedModelMetadataKey:     "client-alias",
		cliproxyexecutor.AuthSelectionModelMetadataKey: route,
	}}
	if _, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: route}, opts); errExecute != nil {
		t.Fatalf("Execute(client-visible allowed alias) error = %v", errExecute)
	}

	opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = "other-client-model"
	if _, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: route}, opts); errExecute == nil {
		t.Fatal("Execute(disallowed client-visible model) error = nil")
	}
}

func TestManagerAllowedAuthIDsRestrictSchedulerToAuthFileModels(t *testing.T) {
	const provider = "access-auth-provider"
	registerSchedulerModels(t, provider, "model-a", "auth-a")
	registerSchedulerModels(t, provider, "model-b", "auth-b")

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
	for _, candidate := range []*Auth{{ID: "auth-a", Provider: provider}, {ID: "auth-b", Provider: provider}} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{"auth-b"},
	}}

	selected, _, errPick := manager.pickNext(context.Background(), provider, "model-b", opts, nil)
	if errPick != nil {
		t.Fatalf("pickNext(model-b) error = %v", errPick)
	}
	if selected == nil || selected.ID != "auth-b" {
		t.Fatalf("pickNext(model-b) auth = %#v, want auth-b", selected)
	}

	selected, _, errPick = manager.pickNext(context.Background(), provider, "model-a", opts, nil)
	if selected != nil {
		t.Fatalf("pickNext(model-a) auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("pickNext(model-a) error = %#v, want auth_not_found", errPick)
	}
}

func TestManagerAllowedAuthIDsRestrictMixedProviderScheduler(t *testing.T) {
	const model = "mixed-access-model"
	registerSchedulerModels(t, "gemini", model, "gemini-auth")
	registerSchedulerModels(t, "claude", model, "claude-auth")
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&accessLimitExecutor{provider: "gemini"})
	manager.RegisterExecutor(&accessLimitExecutor{provider: "claude"})
	for _, candidate := range []*Auth{{ID: "gemini-auth", Provider: "gemini"}, {ID: "claude-auth", Provider: "claude"}} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedAuthIDsMetadataKey: "claude-auth",
	}}
	for index := 0; index < 4; index++ {
		selected, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, model, opts, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if selected == nil || selected.ID != "claude-auth" || provider != "claude" {
			t.Fatalf("pickNextMixed() #%d = %#v/%q, want claude-auth/claude", index, selected, provider)
		}
	}
}

func TestManagerHomeExecutionSkipsDisallowedAuthIDs(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{
		{ID: "home-disallowed", Provider: "home-access", Metadata: map[string]any{"access_token": "token-a"}},
		{ID: "home-allowed", Provider: "home-access", Metadata: map[string]any{"access_token": "token-b"}},
	}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(&accessLimitExecutor{provider: "home-access"})
	selectedAuthID := ""
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{"home-allowed"},
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selectedAuthID = authID
		},
	}}
	if _, errExecute := manager.Execute(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if selectedAuthID != "home-allowed" {
		t.Fatalf("selected auth = %q, want home-allowed", selectedAuthID)
	}
	if got := dispatcher.counts; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("Home auth counts = %v, want [1 2]", got)
	}

	selectedAuthID = ""
	stream, errStream := manager.ExecuteStream(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range stream.Chunks {
	}
	if selectedAuthID != "home-allowed" {
		t.Fatalf("stream selected auth = %q, want home-allowed", selectedAuthID)
	}
	if got := dispatcher.counts; len(got) != 4 || got[2] != 1 || got[3] != 2 {
		t.Fatalf("Home auth counts after stream = %v, want [1 2 1 2]", got)
	}
}
