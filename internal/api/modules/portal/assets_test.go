package portal

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPortalServesProductionScripts(t *testing.T) {
	router := gin.New()
	New(nil, nil, nil).RegisterRoutes(router.Group("/portal"))
	for _, name := range []string{"app.js", "docs.js"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/portal/ui/"+name, nil))
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Body.Len() == 0 {
			t.Fatalf("script %s was not served: %d %s", name, w.Code, w.Header().Get("Content-Type"))
		}
	}
}
