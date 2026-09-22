package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/config"
	"github.com/example/conan-manager/internal/store"
)

// browser keeps cookies between requests, as a browser does.
type browser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
	csrf    string
	remote  string
}

func newBrowser(t *testing.T, h http.Handler) *browser {
	return &browser{t: t, h: h, cookies: map[string]*http.Cookie{}, remote: "198.51.100.7:40000"}
}

func (b *browser) do(method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = b.remote
	for _, c := range b.cookies {
		request.AddCookie(c)
	}
	if b.csrf != "" && method != http.MethodGet {
		request.Header.Set(csrfHeader, b.csrf)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response := httptest.NewRecorder()
	b.h.ServeHTTP(response, request)
	for _, c := range response.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return response
}

func (b *browser) login(user, pass string) *httptest.ResponseRecorder {
	b.t.Helper()
	r := b.do(http.MethodPost, "/api/v1/login", map[string]any{"username": user, "password": pass})
	if r.Code == http.StatusOK {
		var out struct{ CSRF string }
		_ = json.Unmarshal(r.Body.Bytes(), &out)
		b.csrf = out.CSRF
	}
	return r
}

func TestSessionLogin(t *testing.T) {
	h := panelHandler(t)
	b := newBrowser(t, h)

	// The page loads without a login; the data does not, and no browser
	// login box is asked for.
	if r := b.do(http.MethodGet, "/", nil); r.Code != http.StatusOK {
		t.Fatalf("page: %d", r.Code)
	}
	r := b.do(http.MethodGet, "/api/v1/servers", nil)
	if r.Code != http.StatusUnauthorized || r.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("anonymous: %d %q", r.Code, r.Header().Get("WWW-Authenticate"))
	}
	if r := b.login("admin", "wrong-password"); r.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.Code)
	}
	r = b.login("admin", "test-password")
	if r.Code != http.StatusOK || b.csrf == "" {
		t.Fatalf("login: %d %s", r.Code, r.Body)
	}
	cookie := b.cookies[cookiePlain]
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 0 {
		t.Fatalf("cookie: %+v", cookie)
	}
	if r := b.do(http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), b.csrf) {
		t.Fatalf("me: %d %s", r.Code, r.Body)
	}

	// A change without the token is what a forged cross-site request looks like.
	token := b.csrf
	b.csrf = ""
	if r := b.do(http.MethodPost, "/api/v1/admins", map[string]string{"username": "x"}); r.Code != http.StatusForbidden {
		t.Fatalf("no csrf: %d", r.Code)
	}
	b.csrf = "wrong"
	if r := b.do(http.MethodPost, "/api/v1/admins", map[string]string{"username": "x"}); r.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: %d", r.Code)
	}
	b.csrf = token
	if r := b.do(http.MethodPost, "/api/v1/admins", map[string]string{"username": "moderator", "password": "a-long-password"}); r.Code != http.StatusCreated {
		t.Fatalf("with csrf: %d %s", r.Code, r.Body)
	}

	if r := b.do(http.MethodPost, "/api/v1/logout", nil); r.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", r.Code)
	}
	if len(b.cookies) != 0 {
		t.Fatalf("cookies kept: %v", b.cookies)
	}
	if r := b.do(http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", r.Code)
	}
}

func TestSessionEndsWithPassword(t *testing.T) {
	h := panelHandler(t)
	if r := call(t, h, "admin", "test-password", http.MethodPost, "/api/v1/admins", map[string]string{"username": "moderator", "password": "a-long-password"}); r.Code != http.StatusCreated {
		t.Fatal(r.Code)
	}
	other := newBrowser(t, h)
	if r := other.login("moderator", "a-long-password"); r.Code != http.StatusOK {
		t.Fatal(r.Code)
	}
	self := newBrowser(t, h)
	if r := self.login("moderator", "a-long-password"); r.Code != http.StatusOK {
		t.Fatal(r.Code)
	}
	// Changing one's own password keeps one's own session, renewed, and
	// ends every other one.
	if r := self.do(http.MethodPut, "/api/v1/admins/moderator/password", map[string]string{"password": "another-long-one"}); r.Code != http.StatusNoContent {
		t.Fatalf("change: %d %s", r.Code, r.Body)
	}
	r := self.do(http.MethodGet, "/api/v1/me", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("own session after change: %d", r.Code)
	}
	var me struct{ CSRF string }
	_ = json.Unmarshal(r.Body.Bytes(), &me)
	if me.CSRF == "" || me.CSRF == self.csrf {
		t.Fatalf("renewed session keeps the old token")
	}
	if r := other.do(http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("other session after change: %d", r.Code)
	}
	// Removing the administrator ends the renewed session too.
	if r := call(t, h, "admin", "test-password", http.MethodDelete, "/api/v1/admins/moderator", nil); r.Code != http.StatusNoContent {
		t.Fatal(r.Code)
	}
	if r := self.do(http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("session of removed admin: %d", r.Code)
	}
}

func TestLoginBlockedAfterFailures(t *testing.T) {
	h := panelHandler(t)
	b := newBrowser(t, h)
	for i := 0; i < 9; i++ {
		if r := b.login("admin", "wrong"); r.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, r.Code)
		}
	}
	r := b.login("admin", "wrong")
	if r.Code != http.StatusTooManyRequests || r.Header().Get("Retry-After") == "" {
		t.Fatalf("tenth failure: %d", r.Code)
	}
	// Even the right password waits now, from that address only.
	if r := b.login("admin", "test-password"); r.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked right password: %d", r.Code)
	}
	if r := call(t, h, "admin", "test-password", http.MethodGet, "/api/v1/me", nil); r.Code != http.StatusOK {
		t.Fatalf("other address: %d", r.Code)
	}
	other := newBrowser(t, h)
	other.remote = "203.0.113.9:5000"
	if r := other.login("admin", "test-password"); r.Code != http.StatusOK {
		t.Fatalf("other address login: %d", r.Code)
	}
}

func TestAuditTrail(t *testing.T) {
	h := panelHandler(t)
	b := newBrowser(t, h)
	b.login("admin", "wrong")
	b.login("admin", "test-password")
	b.do(http.MethodPost, "/api/v1/admins", map[string]string{"username": "moderator", "password": "a-long-password"})
	b.do(http.MethodPut, "/api/v1/panel/settings", map[string]string{"CONAN_MANAGER_BACKUP_KEEP": "5"})
	b.do(http.MethodDelete, "/api/v1/admins/nobody", nil)

	r := b.do(http.MethodGet, "/api/v1/audit", nil)
	var list []struct {
		Username, IP, Action, Target, Details string
		Status                                int
	}
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil {
		t.Fatal(err, r.Body)
	}
	got := []string{}
	for i := len(list) - 1; i >= 0; i-- {
		e := list[i]
		got = append(got, strings.Join([]string{e.Username, e.Action, e.Target, e.Details, http.StatusText(e.Status)}, "|"))
		if e.IP != "198.51.100.7" {
			t.Errorf("ip %q", e.IP)
		}
	}
	want := []string{
		"admin|login.failed|||Unauthorized",
		"admin|login|" + list[len(list)-2].Target + "||OK",
		"admin|POST /admins|||Created",
		"admin|PUT /panel/settings||CONAN_MANAGER_BACKUP_KEEP|OK",
		"admin|DELETE /admins/{}|nobody||Not Found",
	}
	for i := range want {
		want[i] = strings.Replace(want[i], "||Unauthorized", "|form|Unauthorized", 1)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, e := range list {
		if strings.Contains(e.Details, "a-long-password") {
			t.Fatal("password in the audit log")
		}
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct{ method, path, action, target, rest string }{
		{"POST", "/api/v1/servers/ue5/start", "POST /servers/{}/start", "ue5", ""},
		{"POST", "/api/v1/servers/ue5/backups/12/restore", "POST /servers/{}/backups/{}/restore", "ue5", "12"},
		{"POST", "/api/v1/servers/ue5/config/Game.ini/versions/3/restore", "POST /servers/{}/config/{}/versions/{}/restore", "ue5", "Game.ini,3"},
		{"PUT", "/api/v1/admins/bob/password", "PUT /admins/{}/password", "bob", ""},
		{"POST", "/api/v1/servers", "POST /servers", "", ""},
		{"POST", "/api/v1/sessions/end-others", "POST /sessions/end-others", "", ""},
	} {
		action, target, rest := describe(c.method, c.path)
		if action != c.action || target != c.target || strings.Join(rest, ",") != c.rest {
			t.Errorf("%s %s: %q %q %v", c.method, c.path, action, target, rest)
		}
	}
}

func TestResolveClient(t *testing.T) {
	proxies, _ := config.ParsePrefixes("127.0.0.1, 10.0.0.0/8")
	acc := Access{TrustedProxies: proxies}
	request := func(remote, xff, proto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	for _, c := range []struct {
		remote, xff, proto, ip string
		https                  bool
	}{
		// Headers from an untrusted peer are ignored.
		{"203.0.113.5:1000", "1.2.3.4", "https", "203.0.113.5", false},
		{"127.0.0.1:1000", "198.51.100.1", "https", "198.51.100.1", true},
		// The client cannot hide behind an address it wrote itself.
		{"127.0.0.1:1000", "6.6.6.6, 198.51.100.1", "", "198.51.100.1", false},
		{"127.0.0.1:1000", "198.51.100.1, 10.1.2.3", "http", "198.51.100.1", false},
		{"[::ffff:127.0.0.1]:1000", "", "", "127.0.0.1", false},
	} {
		got := acc.resolve(request(c.remote, c.xff, c.proto))
		if got.IP.String() != c.ip || got.HTTPS != c.https {
			t.Errorf("%s %q: %v %v", c.remote, c.xff, got.IP, got.HTTPS)
		}
	}
}

func TestAllowlist(t *testing.T) {
	t.Setenv("CONAN_MANAGER_ADMIN_USERNAME", "admin")
	t.Setenv("CONAN_MANAGER_ADMIN_PASSWORD", "test-password")
	db, err := store.Open(filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.LoadFrom(func(k string) string {
		switch k {
		case "CONAN_MANAGER_ADMIN_USERNAME":
			return "admin"
		case "CONAN_MANAGER_ADMIN_PASSWORD":
			return "test-password"
		case "CONAN_MANAGER_ALLOWED_IPS":
			return "192.168.1.0/24 198.51.100.7"
		}
		return ""
	})
	h, _ := NewManaged(db, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	allowed := newBrowser(t, h)
	if r := allowed.login("admin", "test-password"); r.Code != http.StatusOK {
		t.Fatalf("allowed: %d", r.Code)
	}
	denied := newBrowser(t, h)
	denied.remote = "192.168.2.1:1"
	if r := denied.do(http.MethodGet, "/", nil); r.Code != http.StatusForbidden {
		t.Fatalf("denied page: %d", r.Code)
	}
	if r := denied.login("admin", "test-password"); r.Code != http.StatusForbidden {
		t.Fatalf("denied login: %d", r.Code)
	}
}

func TestParsePrefixes(t *testing.T) {
	list, err := config.ParsePrefixes("10.0.0.0/8, 192.168.1.5;2001:db8::/32 ::ffff:1.2.3.4")
	if err != nil || len(list) != 4 || list[1] != netip.MustParsePrefix("192.168.1.5/32") || list[3] != netip.MustParsePrefix("1.2.3.4/32") {
		t.Fatal(list, err)
	}
	if _, err := config.ParsePrefixes("10.0.0.0/33"); err == nil {
		t.Fatal("bad prefix accepted")
	}
	if _, err := config.ParsePrefixes("example.com"); err == nil {
		t.Fatal("name accepted")
	}
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(2, 3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allow("a", now) {
			t.Fatal("burst refused", i)
		}
	}
	if l.allow("a", now) || !l.allow("b", now) {
		t.Fatal("bucket not per key")
	}
	if !l.allow("a", now.Add(600*time.Millisecond)) {
		t.Fatal("not refilled")
	}
}
