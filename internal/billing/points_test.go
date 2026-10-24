package billing

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type pointTestReader struct{ failed bool }

func (p pointTestReader) ListPointPrices(context.Context) ([]store.PointPrice, error) {
	if p.failed {
		return nil, errors.New("test price store outage")
	}
	return []store.PointPrice{{Model: "test-model", Input: "10", Output: "20", CacheRead: "0", CacheWrite: "0", Enabled: true, Version: 7}}, nil
}

func TestPointsAdmissionRejectsUnpricedAndAmbiguousRequests(t *testing.T) {
	for _, tc := range []struct {
		name, body, path string
		failed, unbilled bool
		want             int
	}{
		{"priced", `{"model":"test-model","messages":[]}`, "/v1/chat/completions", false, false, 200},
		{"suffix", `{"model":"test-model(high)"}`, "/v1/chat/completions", false, false, 200},
		{"missing model", `{}`, "/v1/chat/completions", false, false, 400},
		{"unpriced", `{"model":"private-model"}`, "/v1/chat/completions", false, false, 403},
		{"outage", `{"model":"test-model"}`, "/v1/chat/completions", true, false, 503},
		{"duplicate model", `{"model":"test-model","model":"private-model"}`, "/v1/chat/completions", false, false, 400},
		{"duplicate field", `{"model":"test-model","stream":false,"stream":true}`, "/v1/chat/completions", false, false, 400},
		{"malformed", `{"model":"test-model",`, "/v1/chat/completions", false, false, 400},
		{"trailing JSON", `{"model":"test-model"}{}`, "/v1/chat/completions", false, false, 400},
		{"model type", `{"model":123}`, "/v1/chat/completions", false, false, 400},
		{"array", `[]`, "/v1/chat/completions", false, false, 400},
		{"native URL", `{"contents":[]}`, "/v1beta/models/test-model:generateContent", false, false, 200},
		{"large body", `{"model":"test-model","padding":"` + strings.Repeat("x", 8<<20) + `"}`, "/v1/chat/completions", false, false, 400},
		{"unbilled", `{"model":"private-model"}`, "/v1/chat/completions", true, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				ctx := WithUserID(c.Request.Context(), "test-user")
				if tc.unbilled {
					ctx = WithUnbilled(ctx)
				}
				c.Request = c.Request.WithContext(ctx)
			}, PointPriceGuard(pointTestReader{failed: tc.failed}))
			r.POST(tc.path, func(c *gin.Context) {
				body, _ := io.ReadAll(c.Request.Body)
				if string(body) != tc.body {
					t.Error("admission changed downstream JSON")
				}
				c.Status(200)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d response=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
