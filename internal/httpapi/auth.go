package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Myp3uK/ConanLinuxManager/internal/audit"
	"github.com/Myp3uK/ConanLinuxManager/internal/panel"
)

type userKey struct{}
type sessionKey struct{}

// currentUser is the administrator who made the request.
func currentUser(r *http.Request) string {
	user, _ := r.Context().Value(userKey{}).(string)
	return user
}

// currentSession is the browser session of the request; nil for Basic Auth.
func currentSession(r *http.Request) *panel.Session {
	s, _ := r.Context().Value(sessionKey{}).(*panel.Session)
	return s
}

const (
	// A cookie named __Host- is refused by browsers unless it is Secure,
	// host-only and for the whole site, so nothing can plant one over HTTP.
	cookieSecure = "__Host-conan_session"
	cookiePlain  = "conan_session"
	csrfHeader   = "X-CSRF-Token"
)

// authn logs administrators in. The browser holds a session cookie and sends
// a CSRF token with every change; scripts may still use Basic Auth.
type authn struct {
	admins   *panel.Admins
	sessions *panel.Sessions
	logins   *loginGuard
	audit    *audit.Log
	logger   *slog.Logger
	// basic accepts HTTP Basic Auth for scripts and curl.
	basic bool
}

// public paths work without a login: the page itself (it holds no data and
// shows the login form), its assets, the health check and the login call.
func public(r *http.Request) bool {
	p := r.URL.Path
	if p == "/healthz" || (p == "/api/v1/login" && r.Method == http.MethodPost) {
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return p == "/" || strings.HasPrefix(p, "/ui/")
}

func mutating(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (au *authn) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !au.admins.Configured(r.Context()) {
			// Never expose the management API without credentials. This also
			// makes a broken or incomplete environment file fail closed.
			writeStatus(w, http.StatusServiceUnavailable)
			return
		}
		if token := sessionToken(r); token != "" {
			if s, ok := au.sessions.Lookup(r.Context(), token); ok {
				// A page on another site can make the browser send the cookie,
				// but it cannot read the token that must come with it.
				if mutating(r.Method) && !equalToken(r.Header.Get(csrfHeader), s.CSRF) {
					action, target, _ := describe(r.Method, r.URL.Path)
					au.record(r, audit.Entry{Username: s.Username, Action: "csrf.refused", Target: target,
						Status: http.StatusForbidden, Details: action})
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf token missing or wrong; reload the page"})
					return
				}
				ctx := context.WithValue(r.Context(), userKey{}, s.Username)
				ctx = context.WithValue(ctx, sessionKey{}, &s)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		if user, pass, ok := r.BasicAuth(); ok && au.basic {
			if wait := au.logins.wait(clientIP(r), time.Now()); wait > 0 {
				tooMany(w, wait)
				return
			}
			if !au.admins.Check(r.Context(), user, pass) {
				au.failed(r, user, "basic")
				// No WWW-Authenticate: the browser must not pop up its own
				// login box over the page's.
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong username or password"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
	})
}

func equalToken(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func sessionToken(r *http.Request) string {
	for _, name := range []string{cookieSecure, cookiePlain} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	seconds := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many failed logins, try later", "retry_after": seconds})
}

// failed counts and records a failed login.
func (au *authn) failed(r *http.Request, username, how string) {
	ip := clientIP(r)
	blocked := au.logins.fail(ip, time.Now())
	au.record(r, audit.Entry{Username: printable(username), Action: "login.failed", Status: http.StatusUnauthorized, Details: how})
	if blocked {
		au.logger.Warn("login blocked after repeated failures", "ip", ip)
		au.record(r, audit.Entry{Username: printable(username), Action: "login.blocked", Status: http.StatusTooManyRequests,
			Details: au.logins.block.String()})
	}
}

func (au *authn) record(r *http.Request, e audit.Entry) {
	if au.audit == nil {
		return
	}
	if e.IP == "" {
		e.IP = clientIP(r)
	}
	if err := au.audit.Record(context.WithoutCancel(r.Context()), e); err != nil {
		au.logger.Error("audit record", "error", err)
	}
}

// printable keeps a user-typed name safe to store and show.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

func (au *authn) setCookie(w http.ResponseWriter, r *http.Request, token string, maxAge time.Duration) {
	secure := clientOf(r).HTTPS
	name := cookiePlain
	if secure {
		name = cookieSecure
	}
	c := &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
	}
	http.SetCookie(w, c)
}

func clearCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookiePlain, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: cookieSecure, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

// login checks a password and starts a browser session.
func (a *api) login(w http.ResponseWriter, r *http.Request) {
	au := a.auth
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decodeStrict(r, &in); err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	ip := clientIP(r)
	if wait := au.logins.wait(ip, time.Now()); wait > 0 {
		tooMany(w, wait)
		return
	}
	if !au.admins.Configured(r.Context()) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no administrator is configured"})
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !au.admins.Check(r.Context(), in.Username, in.Password) {
		au.failed(r, in.Username, "form")
		if wait := au.logins.wait(ip, time.Now()); wait > 0 {
			tooMany(w, wait)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong username or password"})
		return
	}
	au.logins.succeed(ip)
	token, s, err := au.sessions.Create(r.Context(), in.Username, ip, r.UserAgent())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	// Without "remember" the cookie ends with the browser; the session
	// itself expires on the server either way.
	var keep time.Duration
	if in.Remember {
		keep = time.Until(s.ExpiresAt)
	}
	au.setCookie(w, r, token, keep)
	au.record(r, audit.Entry{Username: s.Username, Action: "login", Status: http.StatusOK, Target: s.ID})
	a.logger.Info("administrator logged in", "username", s.Username, "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"username": s.Username, "csrf": s.CSRF, "session": s.ID})
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	if s := currentSession(r); s != nil {
		if err := a.auth.sessions.End(r.Context(), s.ID); err != nil && !errors.Is(err, panel.ErrSessionNotFound) {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		a.auth.record(r, audit.Entry{Username: s.Username, Action: "logout", Status: http.StatusOK, Target: s.ID})
	}
	clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

type sessionView struct {
	panel.Session
	Current bool `json:"current"`
}

// sessions serves GET /sessions, POST /sessions/end-others and
// DELETE /sessions/{id}.
func (a *api) sessionRoutes(w http.ResponseWriter, r *http.Request) {
	current := ""
	if s := currentSession(r); s != nil {
		current = s.ID
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
		list, err := a.auth.sessions.List(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		views := make([]sessionView, 0, len(list))
		for _, s := range list {
			views = append(views, sessionView{Session: s, Current: s.ID == current})
		}
		writeJSON(w, http.StatusOK, views)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/end-others":
		n, err := a.auth.sessions.EndOthers(r.Context(), currentUser(r), current)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, err)
			return
		}
		noteAudit(r, currentUser(r), strconv.FormatInt(n, 10)+" ended")
		writeJSON(w, http.StatusOK, map[string]int64{"ended": n})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
		if err := a.auth.sessions.End(r.Context(), id); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, panel.ErrSessionNotFound) {
				status = http.StatusNotFound
			}
			a.fail(w, r, status, err)
			return
		}
		if id == current {
			clearCookies(w)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		a.fail(w, r, http.StatusNotFound, nil)
	}
}

// renewOwnSession gives the caller a new session after their own password
// changed, which ends every session made with the old one.
func (a *api) renewOwnSession(w http.ResponseWriter, r *http.Request) {
	s := currentSession(r)
	if s == nil {
		return
	}
	token, fresh, err := a.auth.sessions.Create(r.Context(), s.Username, clientIP(r), r.UserAgent())
	if err != nil {
		a.logger.Error("renew session", "error", err)
		return
	}
	a.auth.setCookie(w, r, token, time.Until(fresh.ExpiresAt))
}

// ---------- audit ----------

type auditNote struct {
	target  string
	details []string
}

type auditKey struct{}

// noteAudit adds to the audit entry of the request: a target when the path
// does not name one, and details such as the keys of changed settings.
func noteAudit(r *http.Request, target, details string) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNote); ok {
		if target != "" {
			n.target = target
		}
		if details != "" {
			n.details = append(n.details, details)
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// auditTrail records every change made through the API, with its result.
// Reads are not recorded; neither are request bodies, which carry passwords
// and webhook URLs.
func (au *authn) auditTrail(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mutating(r.Method) || !strings.HasPrefix(r.URL.Path, "/api/v1/") ||
			r.URL.Path == "/api/v1/login" || r.URL.Path == "/api/v1/logout" {
			next.ServeHTTP(w, r)
			return
		}
		note := &auditNote{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), auditKey{}, note)))
		action, target, details := describe(r.Method, r.URL.Path)
		if note.target != "" {
			target = note.target
		}
		if r.URL.RawQuery != "" {
			details = append(details, r.URL.RawQuery)
		}
		details = append(details, note.details...)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		au.record(r, audit.Entry{Username: currentUser(r), Action: action, Target: target, Status: status,
			Details: strings.Join(details, "; ")})
	})
}

// collections are path segments followed by a name or number.
var collections = map[string]bool{"servers": true, "backups": true, "config": true, "versions": true,
	"notifications": true, "admins": true, "sessions": true, "jobs": true}

// describe turns a request path into an action with placeholders, such as
// "POST /servers/{}/backups/{}/restore", the first name in it as the target,
// and the other names.
func describe(method, path string) (string, string, []string) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/v1"), "/"), "/")
	var target string
	var rest []string
	for i := 1; i < len(parts); i++ {
		if !collections[parts[i-1]] || parts[i] == "" || parts[i] == "end-others" {
			continue
		}
		if target == "" {
			target = parts[i]
		} else {
			rest = append(rest, parts[i])
		}
		parts[i] = "{}"
	}
	return method + " /" + strings.Join(parts, "/"), target, rest
}
