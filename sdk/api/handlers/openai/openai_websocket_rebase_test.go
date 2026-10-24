package openai

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestForwardResponsesWebsocketKeepsTerminalErrorAtEOF(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		for _, duplex := range []bool{false, true} {
			t.Run(fmt.Sprintf("status_%d/duplex_%t", status, duplex), func(t *testing.T) {
				// Both channels are ready: exercise either select ordering without
				// sleeps or relying on the producer still being scheduled.
				for range 32 {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
					data := make(chan []byte)
					errs := make(chan *interfaces.ErrorMessage, 1)
					want := &interfaces.ErrorMessage{StatusCode: status, Error: errors.New("test credential failure")}
					errs <- want
					close(errs)
					close(data)
					h := NewOpenAIResponsesAPIHandler(&handlers.BaseAPIHandler{})
					var cancellation error
					_, _, _, got, errForward := h.forwardResponsesWebsocket(c, nil, func(values ...interface{}) {
						if len(values) == 1 {
							cancellation, _ = values[0].(error)
						}
					}, data, errs, nil, "test-session", responsesWebsocketForwardOptions{
						duplexStream:  func() bool { return duplex },
						suppressError: shouldReplayResponsesWebsocketPinnedAuthFailure,
					})
					if got != want || errForward != nil || cancellation != want.Error {
						t.Fatalf("terminal error lost at EOF: got=%v forward=%v cancellation=%v", got, errForward, cancellation)
					}
				}
			})
		}
	}
}
