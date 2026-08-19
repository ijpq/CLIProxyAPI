package billing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestModelAccessGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		allowed     []string
		wantStatus  int
		wantHandler bool
	}{
		{name: "allowed JSON model", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"model-a"}`, allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "allowed thinking suffix", method: http.MethodPost, path: "/v1/responses", body: `{"model":"model-a(high)"}`, allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "forbidden JSON model", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"model-b"}`, allowed: []string{"model-a"}, wantStatus: http.StatusForbidden},
		{name: "allowed Gemini path model", method: http.MethodPost, path: "/v1beta/models/model-a:generateContent", allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "forbidden Gemini path model", method: http.MethodPost, path: "/v1beta/models/model-b:generateContent", allowed: []string{"model-a"}, wantStatus: http.StatusForbidden},
		{name: "unrestricted", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"model-b"}`, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "model resolved later", method: http.MethodGet, path: "/v1/responses", allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handled := false
			router := gin.New()
			router.Use(func(c *gin.Context) {
				ctx := WithAllowedModels(c.Request.Context(), tt.allowed)
				c.Request = c.Request.WithContext(ctx)
			}, ModelAccessGuard())
			router.Any("/*path", func(c *gin.Context) {
				handled = true
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
			if handled != tt.wantHandler {
				t.Fatalf("downstream handled = %t, want %t", handled, tt.wantHandler)
			}
		})
	}
}

func TestValidModelsMatchesCatalogCaseInsensitively(t *testing.T) {
	SetModelLister(func() []string { return []string{"GPT-REALTIME"} })
	t.Cleanup(func() { SetModelLister(nil) })

	models := ValidModels([]string{"gpt-realtime", "unknown-model"})
	if len(models) != 1 || models[0] != "gpt-realtime" {
		t.Fatalf("ValidModels() = %v, want [gpt-realtime]", models)
	}
}

func TestRequestedModelRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"model-a","input":"hello"}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))

	if got := requestedModel(ctx); got != "model-a" {
		t.Fatalf("requestedModel() = %q, want model-a", got)
	}
	if got := requestedModel(ctx); got != "model-a" {
		t.Fatalf("requestedModel() after body restore = %q, want model-a", got)
	}
	restored, errRead := io.ReadAll(ctx.Request.Body)
	if errRead != nil {
		t.Fatalf("read restored body: %v", errRead)
	}
	if got := string(restored); got != body {
		t.Fatalf("restored body = %q, want %q", got, body)
	}
}

type trackedRequestBody struct {
	reader *strings.Reader
	read   int
	closed bool
}

func (b *trackedRequestBody) Read(payload []byte) (int, error) {
	n, errRead := b.reader.Read(payload)
	b.read += n
	return n, errRead
}

func (b *trackedRequestBody) Close() error {
	b.closed = true
	return nil
}

func TestRequestedModelInspectsBoundedPrefixAndPreservesClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := `{"model":"model-a","input":"` + strings.Repeat("x", requestedModelInspectionLimit*2) + `"}`
	originalBody := &trackedRequestBody{reader: strings.NewReader(payload)}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Body = originalBody

	if got := requestedModel(ctx); got != "model-a" {
		t.Fatalf("requestedModel() = %q, want model-a", got)
	}
	if originalBody.read > requestedModelInspectionLimit {
		t.Fatalf("requestedModel() read %d bytes, limit %d", originalBody.read, requestedModelInspectionLimit)
	}
	if originalBody.closed {
		t.Fatal("requestedModel() closed the original body")
	}
	restored, errRead := io.ReadAll(ctx.Request.Body)
	if errRead != nil {
		t.Fatalf("read restored body: %v", errRead)
	}
	if got := string(restored); got != payload {
		t.Fatalf("restored body length = %d, want %d", len(got), len(payload))
	}
	if errClose := ctx.Request.Body.Close(); errClose != nil {
		t.Fatalf("close restored body: %v", errClose)
	}
	if !originalBody.closed {
		t.Fatal("closing the restored body did not close the original body")
	}
}

func TestModelAndAuthAllowedFailClosedWhenRestricted(t *testing.T) {
	ctx := WithAllowedModels(context.Background(), []string{"model-a"})
	ctx = WithAllowedAuthIDs(ctx, []string{"auth-a"})

	if !ModelAllowed(ctx, "MODEL-A(high)") {
		t.Fatal("ModelAllowed() rejected an allowed model with a thinking suffix")
	}
	if ModelAllowed(ctx, "") || ModelAllowed(ctx, "model-b") {
		t.Fatal("ModelAllowed() accepted an unresolved or disallowed model")
	}
	if !AuthAllowed(ctx, "auth-a") {
		t.Fatal("AuthAllowed() rejected an allowed auth ID")
	}
	if AuthAllowed(ctx, "") || AuthAllowed(ctx, "AUTH-A") || AuthAllowed(ctx, "auth-b") {
		t.Fatal("AuthAllowed() accepted an unresolved or disallowed auth ID")
	}
}

func TestExecutionAccessMetadataCopiesRestrictions(t *testing.T) {
	allowedAuthIDs := []string{"auth-a"}
	allowedModels := []string{"model-a"}
	ctx := WithAllowedAuthIDs(context.Background(), allowedAuthIDs)
	ctx = WithAllowedModels(ctx, allowedModels)
	base := map[string]any{coreexecutor.PinnedAuthMetadataKey: "auth-a"}

	metadata := ExecutionAccessMetadata(ctx, base, "model-a(high)")
	if metadata[coreexecutor.PinnedAuthMetadataKey] != "auth-a" {
		t.Fatalf("pinned auth = %#v", metadata[coreexecutor.PinnedAuthMetadataKey])
	}
	if metadata[coreexecutor.RequestedModelMetadataKey] != "model-a(high)" {
		t.Fatalf("requested model = %#v", metadata[coreexecutor.RequestedModelMetadataKey])
	}
	gotAuthIDs, _ := metadata[coreexecutor.AllowedAuthIDsMetadataKey].([]string)
	gotModels, _ := metadata[coreexecutor.AllowedModelsMetadataKey].([]string)
	if len(gotAuthIDs) != 1 || gotAuthIDs[0] != "auth-a" || len(gotModels) != 1 || gotModels[0] != "model-a" {
		t.Fatalf("metadata restrictions = auth:%#v models:%#v", gotAuthIDs, gotModels)
	}
	allowedAuthIDs[0] = "mutated"
	allowedModels[0] = "mutated"
	base[coreexecutor.PinnedAuthMetadataKey] = "mutated"
	if gotAuthIDs[0] != "auth-a" || gotModels[0] != "model-a" || metadata[coreexecutor.PinnedAuthMetadataKey] != "auth-a" {
		t.Fatalf("metadata shares mutable inputs: %#v", metadata)
	}
}
