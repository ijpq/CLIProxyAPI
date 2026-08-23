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
	provider       string
	failAuthID     string
	executeCalls   int
	countCalls     int
	streamCalls    int
	executeAuthIDs []string
	countAuthIDs   []string
	streamAuthIDs  []string
}

type outOfBandSelector struct {
	selected *Auth
}

type inPlaceMutatingSelector struct {
	mutate func(*Auth)
}

type metadataMutatingSelector struct {
	replacement string
}

type selectorCredentialMetadata struct {
	AccessToken string
	Headers     map[string]string
}

type selectorOpaqueCredentialMetadata struct {
	values map[string]string
}

func (m *selectorOpaqueCredentialMetadata) Set(key, value string) {
	if m.values == nil {
		m.values = make(map[string]string)
	}
	m.values[key] = value
}

func (m *selectorOpaqueCredentialMetadata) Value(key string) string {
	if m == nil {
		return ""
	}
	return m.values[key]
}

func (s *outOfBandSelector) Pick(context.Context, string, string, cliproxyexecutor.Options, []*Auth) (*Auth, error) {
	return s.selected, nil
}

func (s *inPlaceMutatingSelector) Pick(_ context.Context, _ string, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, nil
	}
	if s != nil && s.mutate != nil {
		s.mutate(auths[0])
	}
	return auths[0], nil
}

func (s *metadataMutatingSelector) Pick(_ context.Context, _ string, _ string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if allowed, ok := opts.Metadata[cliproxyexecutor.AllowedAuthIDsMetadataKey].([]string); ok && len(allowed) > 0 {
		allowed[0] = s.replacement
	}
	if pinned, ok := opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey].([]byte); ok {
		copy(pinned, s.replacement)
	}
	opts.Metadata[cliproxyexecutor.AllowedAuthIDsMetadataKey] = []string{s.replacement}
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = s.replacement
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

func (e *accessLimitExecutor) Identifier() string { return e.provider }

func (e *accessLimitExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	e.executeAuthIDs = append(e.executeAuthIDs, auth.ID)
	if auth.ID == e.failAuthID {
		return cliproxyexecutor.Response{}, errors.New("retryable access-limit failure")
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *accessLimitExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls++
	e.streamAuthIDs = append(e.streamAuthIDs, auth.ID)
	if auth.ID == e.failAuthID {
		return nil, errors.New("retryable access-limit failure")
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *accessLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *accessLimitExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls++
	e.countAuthIDs = append(e.countAuthIDs, auth.ID)
	if auth.ID == e.failAuthID {
		return cliproxyexecutor.Response{}, errors.New("retryable access-limit failure")
	}
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

func TestManagerAllowedAuthIDsRejectOutOfBandSelectorResult(t *testing.T) {
	const (
		provider = "access-custom-selector-provider"
		model    = "access-custom-selector-model"
	)
	registerSchedulerModels(t, provider, model, "auth-allowed", "auth-denied")
	denied := &Auth{ID: "auth-denied", Provider: provider}
	manager := NewManager(nil, &outOfBandSelector{selected: denied}, nil)
	manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
	for _, candidate := range []*Auth{{ID: "auth-allowed", Provider: provider}, denied} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{"auth-allowed"},
	}}

	selected, errPick := manager.SelectAuth(context.Background(), provider, model, opts)
	if selected != nil {
		t.Fatalf("SelectAuth() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("SelectAuth() error = %#v, want auth_not_found", errPick)
	}
}

func TestManagerCustomSelectorCannotMutateAccessLimitsAcrossRetries(t *testing.T) {
	const (
		allowedAuthID = "selector-auth-a"
		deniedAuthID  = "selector-auth-b"
	)

	modes := []struct {
		name    string
		run     func(*Manager, string, cliproxyexecutor.Options) error
		authIDs func(*accessLimitExecutor) []string
	}{
		{
			name: "execute",
			run: func(manager *Manager, model string, opts cliproxyexecutor.Options) error {
				_, errExecute := manager.Execute(context.Background(), []string{"selector-access-provider"}, cliproxyexecutor.Request{Model: model}, opts)
				return errExecute
			},
			authIDs: func(executor *accessLimitExecutor) []string { return executor.executeAuthIDs },
		},
		{
			name: "count",
			run: func(manager *Manager, model string, opts cliproxyexecutor.Options) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"selector-access-provider"}, cliproxyexecutor.Request{Model: model}, opts)
				return errCount
			},
			authIDs: func(executor *accessLimitExecutor) []string { return executor.countAuthIDs },
		},
		{
			name: "stream",
			run: func(manager *Manager, model string, opts cliproxyexecutor.Options) error {
				stream, errStream := manager.ExecuteStream(context.Background(), []string{"selector-access-provider"}, cliproxyexecutor.Request{Model: model}, opts)
				if errStream != nil {
					return errStream
				}
				for range stream.Chunks {
				}
				return nil
			},
			authIDs: func(executor *accessLimitExecutor) []string { return executor.streamAuthIDs },
		},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			const provider = "selector-access-provider"
			model := "selector-access-model-" + mode.name
			registerSchedulerModels(t, provider, model, allowedAuthID, deniedAuthID)
			executor := &accessLimitExecutor{provider: provider, failAuthID: allowedAuthID}
			manager := NewManager(nil, &metadataMutatingSelector{replacement: deniedAuthID}, nil)
			manager.RegisterExecutor(executor)
			for _, authID := range []string{allowedAuthID, deniedAuthID} {
				if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", authID, errRegister)
				}
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{allowedAuthID},
				cliproxyexecutor.PinnedAuthMetadataKey:     []byte(allowedAuthID),
			}}

			if errRun := mode.run(manager, model, opts); errRun == nil {
				t.Fatal("execution error = nil, want allowed auth failure")
			}
			if got := mode.authIDs(executor); len(got) != 1 || got[0] != allowedAuthID {
				t.Fatalf("executed auth IDs = %v, want only %q", got, allowedAuthID)
			}
		})
	}
}

func TestManagerCustomSelectorCannotMutateEligibleAuthsAcrossModes(t *testing.T) {
	pickers := []struct {
		name string
		pick func(*Manager, string, string, cliproxyexecutor.Options) (*Auth, error)
	}{
		{
			name: "single",
			pick: func(manager *Manager, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
				return manager.SelectAuth(context.Background(), provider, model, opts)
			},
		},
		{
			name: "mixed",
			pick: func(manager *Manager, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
				selected, _, _, errPick := manager.pickNextMixedLegacy(context.Background(), []string{provider}, model, opts, nil)
				return selected, errPick
			},
		},
	}

	for _, picker := range pickers {
		t.Run(picker.name+"/changed_id", func(t *testing.T) {
			provider := "selector-mutation-" + picker.name
			model := "selector-mutation-model-" + picker.name
			const authID = "auth-allowed"
			registerSchedulerModels(t, provider, model, authID)
			selector := &inPlaceMutatingSelector{mutate: func(auth *Auth) {
				auth.ID = "auth-denied"
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{authID},
			}}

			selected, errPick := picker.pick(manager, provider, model, opts)
			if selected != nil {
				t.Fatalf("selected auth = %#v, want nil", selected)
			}
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr.Code != "auth_not_found" {
				t.Fatalf("selection error = %#v, want auth_not_found", errPick)
			}
		})

		t.Run(picker.name+"/changed_fields", func(t *testing.T) {
			provider := "selector-canonical-" + picker.name
			model := "selector-canonical-model-" + picker.name
			const authID = "auth-allowed"
			registerSchedulerModels(t, provider, model, authID)
			selector := &inPlaceMutatingSelector{mutate: func(auth *Auth) {
				auth.Provider = "other-provider"
				auth.Attributes[AttributeAuthKind] = AuthKindAPIKey
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
			candidate := &Auth{
				ID:         authID,
				Provider:   provider,
				Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
			}
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{authID},
			}}

			selected, errPick := picker.pick(manager, provider, model, opts)
			if errPick != nil {
				t.Fatalf("selection error = %v", errPick)
			}
			if selected == nil || selected.ID != authID || selected.Provider != provider || selected.AuthKind() != AuthKindOAuth {
				t.Fatalf("selected auth = %#v, want canonical %s/%s OAuth auth", selected, provider, authID)
			}
		})

		t.Run(picker.name+"/nested_metadata", func(t *testing.T) {
			provider := "selector-metadata-" + picker.name
			model := "selector-metadata-model-" + picker.name
			const authID = "auth-allowed"
			registerSchedulerModels(t, provider, model, authID)
			selector := &inPlaceMutatingSelector{mutate: func(auth *Auth) {
				token, _ := auth.Metadata["token"].(map[string]any)
				token["access_token"] = "mutated-token"
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
			candidate := &Auth{
				ID:       authID,
				Provider: provider,
				Metadata: map[string]any{
					"token": map[string]any{"access_token": "original-token"},
				},
			}
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{authID},
			}}

			selected, errPick := picker.pick(manager, provider, model, opts)
			if errPick != nil {
				t.Fatalf("selection error = %v", errPick)
			}
			if selected == nil {
				t.Fatal("selected auth is nil")
			}
			selectedToken, _ := selected.Metadata["token"].(map[string]any)
			if selectedToken["access_token"] != "original-token" {
				t.Fatalf("selected nested token = %#v, want original-token", selectedToken["access_token"])
			}
			stored, ok := manager.GetByID(authID)
			if !ok {
				t.Fatal("stored auth is missing")
			}
			storedToken, _ := stored.Metadata["token"].(map[string]any)
			if storedToken["access_token"] != "original-token" {
				t.Fatalf("stored nested token = %#v, want original-token", storedToken["access_token"])
			}
		})

		t.Run(picker.name+"/pointer_scalar_metadata", func(t *testing.T) {
			provider := "selector-pointer-scalar-" + picker.name
			model := "selector-pointer-scalar-model-" + picker.name
			const authID = "auth-allowed"
			registerSchedulerModels(t, provider, model, authID)
			selector := &inPlaceMutatingSelector{mutate: func(auth *Auth) {
				token, _ := auth.Metadata["token"].(*string)
				*token = "mutated-token"
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
			originalToken := "original-token"
			candidate := &Auth{
				ID:       authID,
				Provider: provider,
				Metadata: map[string]any{"token": &originalToken},
			}
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{authID},
			}}

			selected, errPick := picker.pick(manager, provider, model, opts)
			if errPick != nil {
				t.Fatalf("selection error = %v", errPick)
			}
			selectedToken, _ := selected.Metadata["token"].(*string)
			if selectedToken == nil || *selectedToken != "original-token" {
				t.Fatalf("selected pointer token = %#v, want original-token", selectedToken)
			}
			stored, ok := manager.GetByID(authID)
			if !ok {
				t.Fatal("stored auth is missing")
			}
			storedToken, _ := stored.Metadata["token"].(*string)
			if storedToken == nil || *storedToken != "original-token" || originalToken != "original-token" {
				t.Fatalf("stored pointer token = %#v and source = %q, want original-token", storedToken, originalToken)
			}
		})

		t.Run(picker.name+"/pointer_struct_metadata", func(t *testing.T) {
			provider := "selector-pointer-struct-" + picker.name
			model := "selector-pointer-struct-model-" + picker.name
			const authID = "auth-allowed"
			registerSchedulerModels(t, provider, model, authID)
			selector := &inPlaceMutatingSelector{mutate: func(auth *Auth) {
				credential, _ := auth.Metadata["credential"].(*selectorCredentialMetadata)
				credential.AccessToken = "mutated-token"
				credential.Headers["authorization"] = "mutated-header"
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
			credential := &selectorCredentialMetadata{
				AccessToken: "original-token",
				Headers:     map[string]string{"authorization": "original-header"},
			}
			candidate := &Auth{
				ID:       authID,
				Provider: provider,
				Metadata: map[string]any{"credential": credential},
			}
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{authID},
			}}

			selected, errPick := picker.pick(manager, provider, model, opts)
			if errPick != nil {
				t.Fatalf("selection error = %v", errPick)
			}
			selectedCredential, _ := selected.Metadata["credential"].(*selectorCredentialMetadata)
			if selectedCredential == nil || selectedCredential.AccessToken != "original-token" || selectedCredential.Headers["authorization"] != "original-header" {
				t.Fatalf("selected credential = %#v, want original values", selectedCredential)
			}
			stored, ok := manager.GetByID(authID)
			if !ok {
				t.Fatal("stored auth is missing")
			}
			storedCredential, _ := stored.Metadata["credential"].(*selectorCredentialMetadata)
			if storedCredential == nil || storedCredential.AccessToken != "original-token" || storedCredential.Headers["authorization"] != "original-header" {
				t.Fatalf("stored credential = %#v, want original values", storedCredential)
			}
			if credential.AccessToken != "original-token" || credential.Headers["authorization"] != "original-header" {
				t.Fatalf("source credential = %#v, want original values", credential)
			}
		})

		t.Run(picker.name+"/opaque_struct_metadata", func(t *testing.T) {
			provider := "selector-opaque-struct-" + picker.name
			model := "selector-opaque-struct-model-" + picker.name
			const authID = "auth-allowed"
			registerSchedulerModels(t, provider, model, authID)
			selector := &inPlaceMutatingSelector{mutate: func(auth *Auth) {
				credential, _ := auth.Metadata["credential"].(*selectorOpaqueCredentialMetadata)
				credential.Set("access_token", "mutated-token")
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(&accessLimitExecutor{provider: provider})
			credential := &selectorOpaqueCredentialMetadata{
				values: map[string]string{"access_token": "original-token"},
			}
			candidate := &Auth{
				ID:       authID,
				Provider: provider,
				Metadata: map[string]any{"credential": credential},
			}
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.AllowedAuthIDsMetadataKey: []string{authID},
			}}

			selected, errPick := picker.pick(manager, provider, model, opts)
			if errPick != nil {
				t.Fatalf("selection error = %v", errPick)
			}
			selectedCredential, _ := selected.Metadata["credential"].(*selectorOpaqueCredentialMetadata)
			if selectedCredential == nil || selectedCredential.Value("access_token") != "original-token" {
				t.Fatalf("selected opaque credential = %#v, want original-token", selectedCredential)
			}
			stored, ok := manager.GetByID(authID)
			if !ok {
				t.Fatal("stored auth is missing")
			}
			storedCredential, _ := stored.Metadata["credential"].(*selectorOpaqueCredentialMetadata)
			if storedCredential == nil || storedCredential.Value("access_token") != "original-token" {
				t.Fatalf("stored opaque credential = %#v, want original-token", storedCredential)
			}
			if credential.Value("access_token") != "original-token" {
				t.Fatalf("source opaque credential = %#v, want original-token", credential)
			}
		})
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

func TestManagerHomeExecutionEnforcesPinnedAuthAcrossModes(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errExecute := manager.Execute(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
				return errExecute
			},
		},
		{
			name: "count",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
				return errCount
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				stream, errStream := manager.ExecuteStream(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
				if errStream != nil {
					return errStream
				}
				for range stream.Chunks {
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &authKindHomeDispatcher{auths: []Auth{
				{ID: "home-other", Provider: "home-access", Metadata: map[string]any{"access_token": "token-a"}},
				{ID: "home-pinned", Provider: "home-access", Metadata: map[string]any{"access_token": "token-b"}},
			}}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			registry := executionregistry.New()
			manager.PublishHomeDispatch(dispatcher, registry, 1)
			manager.RegisterExecutor(&accessLimitExecutor{provider: "home-access"})
			selectedAuthID := ""
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.PinnedAuthMetadataKey: "home-pinned",
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
					selectedAuthID = authID
				},
			}}

			if errRun := test.run(manager, opts); errRun != nil {
				t.Fatalf("%s error = %v", test.name, errRun)
			}
			if selectedAuthID != "home-pinned" {
				t.Fatalf("selected auth = %q, want home-pinned", selectedAuthID)
			}
			if got := dispatcher.counts; len(got) != 2 || got[0] != 1 || got[1] != 2 {
				t.Fatalf("Home auth counts = %v, want [1 2]", got)
			}
			if errDrain := registry.Drain(context.Background()); errDrain != nil {
				t.Fatalf("Drain() error = %v", errDrain)
			}
		})
	}
}

func TestManagerHomeExecutionStopsOnRepeatedIneligibleAuthAcrossModes(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errExecute := manager.Execute(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
				return errExecute
			},
		},
		{
			name: "count",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
				return errCount
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errStream := manager.ExecuteStream(context.Background(), []string{"home-access"}, cliproxyexecutor.Request{Model: "home-model"}, opts)
				return errStream
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &authKindHomeDispatcher{auths: []Auth{
				{ID: "home-other", Provider: "home-access", Metadata: map[string]any{"access_token": "token-a"}},
				{ID: "home-other", Provider: "home-access", Metadata: map[string]any{"access_token": "token-a"}},
			}}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			registry := executionregistry.New()
			manager.PublishHomeDispatch(dispatcher, registry, 1)
			executor := &accessLimitExecutor{provider: "home-access"}
			manager.RegisterExecutor(executor)
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.PinnedAuthMetadataKey: "home-pinned",
			}}

			errRun := test.run(manager, opts)
			var authErr *Error
			if !errors.As(errRun, &authErr) || authErr.Code != homeRequestRetryExceededErrorCode {
				t.Fatalf("%s error = %#v, want %s", test.name, errRun, homeRequestRetryExceededErrorCode)
			}
			if got := dispatcher.counts; len(got) != 2 || got[0] != 1 || got[1] != 2 {
				t.Fatalf("Home auth counts = %v, want [1 2]", got)
			}
			if executor.executeCalls != 0 || executor.countCalls != 0 || executor.streamCalls != 0 {
				t.Fatalf("ineligible auth reached executor: execute=%d count=%d stream=%d", executor.executeCalls, executor.countCalls, executor.streamCalls)
			}
			if errDrain := registry.Drain(context.Background()); errDrain != nil {
				t.Fatalf("Drain() error = %v", errDrain)
			}
		})
	}
}
