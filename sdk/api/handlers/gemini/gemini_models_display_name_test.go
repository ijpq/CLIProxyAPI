package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestGeminiModelsFiltersBillingAllowlist(t *testing.T) {
	const (
		clientID      = "gemini-billing-model-list-test"
		allowedModel  = "gemini-billing-visible-model"
		filteredModel = "gemini-billing-hidden-model"
	)
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "gemini", []*registry.ModelInfo{
		{ID: allowedModel, Name: allowedModel},
		{ID: filteredModel, Name: filteredModel},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	requestCtx := billing.WithAllowedModels(context.Background(), []string{allowedModel + "(high)"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil).WithContext(requestCtx)
	NewGeminiAPIHandler(&handlers.BaseAPIHandler{}).GeminiModels(ctx)

	var response struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if len(response.Models) != 1 || response.Models[0].Name != "models/"+allowedModel {
		t.Fatalf("listed models = %#v, want only models/%s", response.Models, allowedModel)
	}
}

func TestGeminiModelDetailFiltersBillingAllowlist(t *testing.T) {
	const (
		clientID      = "gemini-billing-model-detail-test"
		allowedModel  = "gemini-billing-visible-detail"
		filteredModel = "gemini-billing-hidden-detail"
	)
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "gemini", []*registry.ModelInfo{
		{ID: allowedModel, Name: allowedModel},
		{ID: filteredModel, Name: filteredModel},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})
	handler := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})
	requestCtx := billing.WithAllowedModels(context.Background(), []string{allowedModel})

	for _, tc := range []struct {
		model  string
		status int
	}{
		{model: allowedModel, status: http.StatusOK},
		{model: filteredModel, status: http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Params = gin.Params{{Key: "action", Value: tc.model}}
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/"+tc.model, nil).WithContext(requestCtx)
		handler.GeminiGetHandler(ginCtx)
		if recorder.Code != tc.status {
			t.Fatalf("model %s status = %d, want %d; body = %s", tc.model, recorder.Code, tc.status, recorder.Body.String())
		}
	}
}

func TestGeminiModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "gemini-display-name-catalog-test"
	const modelID = "gemini-display-name-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "gemini", []*registry.ModelInfo{{
		ID: modelID, Name: modelID, DisplayName: "Configured Gemini Name",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewGeminiAPIHandler(&handlers.BaseAPIHandler{}).GeminiModels(ctx)

	var response struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Models {
		if model.Name == "models/"+modelID {
			if model.DisplayName != "Configured Gemini Name" {
				t.Fatalf("displayName = %q, want Configured Gemini Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}
