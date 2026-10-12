package openai

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

func TestOpenAIModelsFiltersBillingAllowlist(t *testing.T) {
	const (
		clientID      = "openai-billing-model-list-test"
		allowedModel  = "billing-visible-model"
		filteredModel = "billing-hidden-model"
	)
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: allowedModel, Object: "model", OwnedBy: "test"},
		{ID: filteredModel, Object: "model", OwnedBy: "test"},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	handler := NewOpenAIAPIHandler(&handlers.BaseAPIHandler{})
	for _, path := range []string{"/v1/models", "/v1/models?client_version=0.153.4"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			requestCtx := billing.WithAllowedModels(context.Background(), []string{allowedModel + "(high)"})
			ctx.Request = httptest.NewRequest(http.MethodGet, path, nil).WithContext(requestCtx)

			handler.OpenAIModels(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}

			var response struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				Models []struct {
					Slug string `json:"slug"`
				} `json:"models"`
			}
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
				t.Fatalf("decode response: %v", errUnmarshal)
			}
			listed := make([]string, 0, len(response.Data)+len(response.Models))
			for _, model := range response.Data {
				listed = append(listed, model.ID)
			}
			for _, model := range response.Models {
				listed = append(listed, model.Slug)
			}
			if len(listed) != 1 || listed[0] != allowedModel {
				t.Fatalf("listed models = %#v, want [%s]", listed, allowedModel)
			}
		})
	}
}

func TestOpenAIResponsesModelsFiltersBillingAllowlist(t *testing.T) {
	const (
		clientID      = "responses-billing-model-list-test"
		allowedModel  = "responses-billing-visible-model"
		filteredModel = "responses-billing-hidden-model"
	)
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: allowedModel, Object: "model", OwnedBy: "test"},
		{ID: filteredModel, Object: "model", OwnedBy: "test"},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	requestCtx := billing.WithAllowedModels(context.Background(), []string{allowedModel + "(high)"})
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(requestCtx)
	NewOpenAIResponsesAPIHandler(&handlers.BaseAPIHandler{}).OpenAIResponsesModels(ginCtx)

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if len(response.Data) != 1 || response.Data[0].ID != allowedModel {
		t.Fatalf("listed models = %#v, want only %s", response.Data, allowedModel)
	}
}
