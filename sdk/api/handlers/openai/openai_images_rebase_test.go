package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type bootstrapFailureWriter struct {
	gin.ResponseWriter
	err error
}

func (w *bootstrapFailureWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestImagesBootstrapWriteFailureCancelsAndJoinsExecutor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ticks := make(chan time.Time)
	restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() {}
	})
	t.Cleanup(restore)
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 10, SendKeepAliveBeforeFT: true},
	}, nil))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRequest()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(requestCtx)
	writeErr := errors.New("synthetic pre-first-token write failure")
	c.Writer = &bootstrapFailureWriter{ResponseWriter: c.Writer, err: writeErr}
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer cliCancel()
	started := make(chan struct{})
	release := make(chan struct{})
	var exited atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, _, errWait := h.waitImagesStreamExecution(c, c.Writer, cliCancel, func() imagesStreamExecutionResult {
			close(started)
			<-cliCtx.Done()
			<-release
			exited.Store(true)
			return imagesStreamExecutionResult{}
		})
		done <- errWait
	}()

	<-started
	ticks <- time.Time{}
	<-cliCtx.Done()
	select {
	case errWait := <-done:
		close(release)
		t.Fatalf("bootstrap returned before its executor exited: %v", errWait)
	default:
	}
	close(release)
	select {
	case errWait := <-done:
		if !errors.Is(errWait, writeErr) {
			t.Fatalf("bootstrap error = %v, want original client write error %v", errWait, writeErr)
		}
		if !exited.Load() {
			t.Fatal("bootstrap returned before its executor exited")
		}
	case <-requestCtx.Done():
		t.Fatal("bootstrap did not complete after its executor was released")
	}
}
