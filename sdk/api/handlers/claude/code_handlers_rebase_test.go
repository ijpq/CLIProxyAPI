package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

type boundaryFailureWriter struct {
	gin.ResponseWriter
	err      error
	attempts int
}

func (w *boundaryFailureWriter) Write([]byte) (int, error) {
	w.attempts++
	return 0, w.err
}

func TestClaudeStreamBoundaryWriteFailureCancelsDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		chunk string
	}{
		{"truncated", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\""},
		{"completed", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n"},
		{"upstream_error", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic\"}}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			writeErr := errors.New("synthetic client disconnect")
			writer := &boundaryFailureWriter{ResponseWriter: c.Writer, err: writeErr}
			c.Writer = writer
			state := &claudeSSEStreamState{}
			state.ObserveChunk([]byte(tc.chunk))
			data := make(chan []byte)
			close(data)
			var canceled error
			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})

			handler.forwardClaudeStreamWithState(c, writer, func(err error) {
				canceled = err
			}, data, nil, state)

			if !errors.Is(canceled, writeErr) {
				t.Fatalf("cancel error = %v, want client write error %v", canceled, writeErr)
			}
			if writer.attempts != 1 {
				t.Fatalf("write attempts = %d, want no further output after disconnect", writer.attempts)
			}
		})
	}
}
