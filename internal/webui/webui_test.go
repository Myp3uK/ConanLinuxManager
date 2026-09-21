package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestPageAndAssetsAreServedUnderAStrictPolicy(t *testing.T) {
	page := get(t, http.MethodGet, "/")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `<script src="/ui/app.js"`) {
		t.Fatalf("page: %d %q", page.Code, page.Body.String()[:min(200, page.Body.Len())])
	}
	policy := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "script-src 'self'") || strings.Contains(policy, "unsafe-inline") {
		t.Fatalf("policy %q", policy)
	}
	for path, kind := range map[string]string{"/ui/app.js": "javascript", "/ui/app.css": "text/css"} {
		w := get(t, http.MethodGet, path)
		if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), kind) {
			t.Errorf("%s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

func TestOnlyKnownPathsAndReadsAreServed(t *testing.T) {
	for _, path := range []string{"/nope", "/ui/", "/ui/../webui.go", "/ui/missing.js"} {
		if w := get(t, http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := get(t, http.MethodPost, "/"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d", w.Code)
	}
}

// No inline script or style may creep into the page: the policy would block
// it in the browser, and the page would silently lose that part.
func TestPageHasNoInlineCode(t *testing.T) {
	body := get(t, http.MethodGet, "/").Body.String()
	for _, bad := range []string{"<script>", "style=", " onclick=", "<style"} {
		if strings.Contains(body, bad) {
			t.Errorf("index.html contains %q", bad)
		}
	}
}
