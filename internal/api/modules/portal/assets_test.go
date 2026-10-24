package portal

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPortalServesProductionAssets(t *testing.T) {
	router := gin.New()
	New(nil, nil, nil).RegisterRoutes(router.Group("/portal"))
	for _, asset := range []struct{ name, contentType, contains string }{
		{"app.js", "javascript", "function"},
		{"docs.js", "javascript", "fetch("},
		{"portal.css", "text/css", `url("InterVariable.woff2")`},
		{"InterVariable.woff2", "font/woff2", "wOF2"},
	} {
		for _, prefix := range []string{"/portal/ui/", "/portal/site/"} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", prefix+asset.name, nil))
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), asset.contentType) || !strings.Contains(w.Body.String(), asset.contains) {
				t.Fatalf("asset %s%s was not served correctly: %d %s", prefix, asset.name, w.Code, w.Header().Get("Content-Type"))
			}
		}
	}
}
