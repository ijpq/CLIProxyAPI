package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
)

func TestPortalKeyLimitConcurrentCreationAndRevocation(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "key-limit@example.test")
	const attempts = 24
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, attempts)
	for range attempts {
		go func() { <-start; results <- f.rawRequest("POST", "/api-keys", token, []byte(`{"name":"concurrent"}`)) }()
	}
	close(start)
	created := 0
	for range attempts {
		w := <-results
		if w.Code == 201 {
			created++
		} else {
			requirePortalStatus(t, w, 409)
		}
	}
	if created != 10 {
		t.Fatalf("created %d keys, want exactly 10", created)
	}
	keys, err := f.store.ListAPIKeys(context.Background(), u.ID)
	if err != nil || len(keys) != 10 {
		t.Fatalf("keys=%d err=%v", len(keys), err)
	}
	requirePortalStatus(t, f.request(t, "DELETE", "/api-keys/"+keys[0].ID, token, nil), 204)
	requirePortalStatus(t, f.request(t, "POST", "/api-keys", token, gin.H{"name": "replacement"}), 201)
	w := f.request(t, "GET", "/api-keys", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"max_active_keys":10`) || strings.Contains(w.Body.String(), `"key":`) {
		t.Fatal("key listing exposed a secret or lost the cap")
	}
}

func TestPortalKeyPolicyCatalogEnforcementAndUpdates(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "key-policy@example.test")
	_, otherToken := f.register(t, "other-key-owner@example.test")
	ctx := context.Background()
	for _, name := range []string{"public-a", "public-b", "hidden-model"} {
		_, err := f.store.SavePointPrice(ctx, store.PointPrice{Model: name, Input: "1", Output: "2", CacheRead: "0", CacheWrite: "0", Enabled: name != "hidden-model"}, "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	w := f.request(t, "POST", "/api-keys", token, gin.H{"name": "one-model", "model_scope": "selected", "allowed_models": []string{"public-a"}})
	requirePortalStatus(t, w, 201)
	var key struct {
		ID  string `json:"id"`
		Raw string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("key-policy-test", "openai", []*registry.ModelInfo{{ID: "public-a", Object: "model"}, {ID: "public-b", Object: "model"}, {ID: "hidden-model", Object: "model"}})
	t.Cleanup(func() { registryRef.UnregisterClient("key-policy-test") })
	lookup := func() store.APIKeyLookup {
		t.Helper()
		value, err := f.store.LookupAPIKey(ctx, store.HashAPIKey(key.Raw))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	check := func(want ...string) {
		t.Helper()
		policy := lookup()
		if policy.AllowedModels == nil {
			t.Fatal("paid key became unrestricted")
		}
		requestCtx := billing.WithAllowedModels(ctx, policy.AllowedModels)
		for _, path := range []string{"/v1/models", "/v1/models?client_version=0.153.4"} {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("GET", path, nil).WithContext(requestCtx)
			openai.NewOpenAIAPIHandler(&handlers.BaseAPIHandler{}).OpenAIModels(c)
			requirePortalStatus(t, recorder, 200)
			var catalog struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				Models []struct {
					Slug string `json:"slug"`
				} `json:"models"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
				t.Fatal(err)
			}
			listed := make([]string, 0)
			for _, m := range catalog.Data {
				listed = append(listed, m.ID)
			}
			for _, m := range catalog.Models {
				listed = append(listed, m.Slug)
			}
			if len(listed) != len(want) {
				t.Fatalf("catalog=%v want=%v", listed, want)
			}
			for _, m := range listed {
				if !containsModel(want, m) {
					t.Fatalf("leaked %s", m)
				}
			}
		}
		for _, model := range []string{"public-a", "public-b", "hidden-model", "unknown-model"} {
			router := gin.New()
			forwarded := false
			router.Use(billing.ModelAccessGuard())
			router.POST("/v1/responses", func(c *gin.Context) { forwarded = true; c.Status(204) })
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q}`, model))).WithContext(requestCtx)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, r)
			if forwarded != containsModel(want, model) {
				t.Fatalf("model=%s forwarded=%t", model, forwarded)
			}
			if !forwarded {
				requirePortalStatus(t, res, http.StatusForbidden)
			}
		}
	}
	check("public-a")
	path := "/api-keys/" + key.ID + "/models"
	requirePortalStatus(t, f.request(t, "PUT", path, otherToken, gin.H{"model_scope": "all"}), 404)
	for _, policy := range []gin.H{{}, {"model_scope": "unknown"}, {"model_scope": "selected", "allowed_models": []string{}}, {"model_scope": "all", "allowed_models": []string{"public-a"}}} {
		requirePortalStatus(t, f.request(t, "PUT", path, token, policy), 400)
	}
	requirePortalStatus(t, f.rawRequest("PUT", path, token, []byte("{")), 400)
	requirePortalStatus(t, f.request(t, "PUT", "/api-keys/not-uuid/models", token, gin.H{"model_scope": "none"}), 404)
	requirePortalStatus(t, f.request(t, "POST", "/api-keys", token, gin.H{"model_scope": "selected"}), 400)
	for _, scope := range []string{"none", "all"} {
		requirePortalStatus(t, f.request(t, "PUT", path, token, gin.H{"model_scope": scope}), 200)
		if scope == "none" {
			check()
		} else {
			check("public-a", "public-b")
		}
	}
	if err := f.store.SetUserLimits(ctx, u.ID, false, []string{"PUBLIC-B(high)"}, nil, false); err != nil {
		t.Fatal(err)
	}
	check("public-b")
	w = f.request(t, "GET", "/api-key-models", token, nil)
	requirePortalStatus(t, w, 200)
	if w.Body.String() != `{"models":["public-b"]}` {
		t.Fatal(w.Body.String())
	}
	requirePortalStatus(t, f.request(t, "PUT", path, token, gin.H{"model_scope": "selected", "allowed_models": []string{"public-a"}}), 200)
	check()
	requirePortalStatus(t, f.request(t, "DELETE", "/api-keys/"+key.ID, token, nil), 204)
	requirePortalStatus(t, f.request(t, "PUT", path, token, gin.H{"model_scope": "all"}), 404)
	if _, err := f.store.LookupAPIKeyByID(ctx, key.ID); !errors.Is(err, store.ErrAPIKeyNotFound) {
		t.Fatalf("revoked key lookup=%v", err)
	}
}

func containsModel(models []string, model string) bool {
	for _, candidate := range models {
		if candidate == model {
			return true
		}
	}
	return false
}

func (s *failureStore) UpdateAPIKeyPolicy(ctx context.Context, userID, keyID string, policy store.APIKeyPolicy) (store.APIKeyRecord, error) {
	if s.operation == "UpdateAPIKeyPolicy" {
		return store.APIKeyRecord{}, s.failure
	}
	return s.Store.UpdateAPIKeyPolicy(ctx, userID, keyID, policy)
}

func TestPortalKeyPolicyStorageFailures(t *testing.T) {
	f := newPortalDBFixture(t)
	_, token := f.register(t, "key-failures@example.test")
	for _, tc := range []struct {
		operation, path, method string
		body                    any
		status                  int
	}{
		{"UpdateAPIKeyPolicy", "/api-keys/00000000-0000-0000-0000-000000000001/models", "PUT", gin.H{"model_scope": "none"}, 500},
		{"ListPointPrices", "/api-key-models", "GET", nil, 503},
	} {
		f.module.store = &failureStore{Store: f.store, operation: tc.operation, failure: errors.New("test storage unavailable")}
		requirePortalStatus(t, f.request(t, tc.method, tc.path, token, tc.body), tc.status)
	}
	// The handler must also fail safely if its account lookup becomes unavailable.
	f.module.store = &failureStore{Store: f.store, operation: "GetUserByID", failure: errors.New("unavailable")}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api-key-models", nil)
	f.module.handleKeyModels(c)
	requirePortalStatus(t, w, 503)
}
