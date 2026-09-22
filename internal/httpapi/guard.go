package httpapi

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// client is who sent a request, as far as the manager can tell: the peer
// address, or the address a trusted reverse proxy forwarded.
type client struct {
	IP    netip.Addr
	HTTPS bool
}

type clientKey struct{}

func clientOf(r *http.Request) client {
	c, _ := r.Context().Value(clientKey{}).(client)
	return c
}

func clientIP(r *http.Request) string {
	if ip := clientOf(r).IP; ip.IsValid() {
		return ip.String()
	}
	return ""
}

// Access decides which addresses may reach the manager.
type Access struct {
	// Allowed lists the networks that may use the manager; empty allows all.
	Allowed []netip.Prefix
	// TrustedProxies are reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto are believed. Anyone else could forge them.
	TrustedProxies []netip.Prefix
}

func contains(list []netip.Prefix, ip netip.Addr) bool {
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// resolve finds the client of a request. Behind trusted proxies the client is
// the right-most forwarded address that is not itself a trusted proxy: the
// entries left of it were written by the client and prove nothing.
func (acc Access) resolve(r *http.Request) client {
	c := client{HTTPS: r.TLS != nil}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return c
	}
	c.IP = peer.Addr().Unmap()
	if !contains(acc.TrustedProxies, c.IP) {
		return c
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		c.IP = ip.Unmap()
		if !contains(acc.TrustedProxies, c.IP) {
			break
		}
	}
	if protos := strings.Split(r.Header.Get("X-Forwarded-Proto"), ","); len(protos) > 0 {
		switch strings.ToLower(strings.TrimSpace(protos[len(protos)-1])) {
		case "https":
			c.HTTPS = true
		case "http":
			c.HTTPS = r.TLS != nil
		}
	}
	return c
}

// guard resolves the client, keeps out addresses not on the allowlist and
// slows down any single address that floods the API.
func guard(next http.Handler, acc Access, limit *rateLimiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := acc.resolve(r)
		r = r.WithContext(context.WithValue(r.Context(), clientKey{}, c))
		if c.HTTPS {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if len(acc.Allowed) > 0 && (!c.IP.IsValid() || !contains(acc.Allowed, c.IP)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this address is not allowed to use the manager"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !limit.allow(c.IP.String(), time.Now()) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimiter is a token bucket per key.
type rateLimiter struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newRateLimiter(perSecond, burst float64) *rateLimiter {
	return &rateLimiter{rate: perSecond, burst: burst, buckets: map[string]*bucket{}}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	if l == nil || l.rate <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > time.Minute {
		// A bucket idle long enough to be full again carries no state.
		full := time.Duration(l.burst / l.rate * float64(time.Second))
		for k, b := range l.buckets {
			if now.Sub(b.at) > full {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// loginGuard counts failed logins per address. Too many within the window
// block the address for a while; it is per address, not per user name, so
// nobody can lock an administrator out by guessing at their name.
type loginGuard struct {
	max    int
	window time.Duration
	block  time.Duration

	mu     sync.Mutex
	counts map[string]*failures
}

type failures struct {
	n       int
	first   time.Time
	blocked time.Time
}

func newLoginGuard() *loginGuard {
	return &loginGuard{max: 10, window: 15 * time.Minute, block: 15 * time.Minute, counts: map[string]*failures{}}
}

// wait reports how long the address is still blocked.
func (g *loginGuard) wait(key string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f, ok := g.counts[key]; ok && now.Before(f.blocked) {
		return f.blocked.Sub(now)
	}
	return 0
}

// fail counts a failed login and reports whether it blocked the address.
func (g *loginGuard) fail(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, f := range g.counts {
		if now.Sub(f.first) > g.window && now.After(f.blocked) {
			delete(g.counts, k)
		}
	}
	f, ok := g.counts[key]
	if !ok {
		f = &failures{first: now}
		g.counts[key] = f
	}
	f.n++
	if f.n >= g.max {
		f.blocked = now.Add(g.block)
		f.n, f.first = 0, now
		return true
	}
	return false
}

func (g *loginGuard) succeed(key string) {
	g.mu.Lock()
	delete(g.counts, key)
	g.mu.Unlock()
}
