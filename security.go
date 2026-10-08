package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SecurityConfig holds the edge protections applied in front of every route.
type SecurityConfig struct {
	CORSOrigins    []string // exact origins allowed to call the API cross-origin; empty = same-origin only
	ClientIPHeader string   // e.g. CF-Connecting-IP; only trust it when the origin is reachable through the proxy alone
	RatePerMinute  int      // per client IP; 0 disables rate limiting
	RateBurst      int
	BlockBots      bool
}

const sessionCookie = "itinerary_session"

// Crawlers and scrapers that have no business on a private family app.
// Agents using the API/MCP with a token are unaffected.
var badBotMarkers = []string{
	"gptbot", "ccbot", "claudebot", "anthropic-ai", "bytespider", "perplexitybot", "amazonbot",
	"google-extended", "applebot-extended", "meta-externalagent", "facebookbot", "omgili",
	"ahrefsbot", "semrushbot", "mj12bot", "dotbot", "petalbot", "blexbot", "dataforseobot",
	"masscan", "zgrab", "nikto", "sqlmap", "nuclei",
}

func isBadBot(userAgent string) bool {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	return ua == "" || slices.ContainsFunc(badBotMarkers, func(m string) bool { return strings.Contains(ua, m) })
}

func parseOrigins(s string) []string {
	var out []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			out = append(out, o)
		}
	}
	return out
}

func clientIP(r *http.Request, header string) string {
	if header != "" {
		// Parse rather than trust: arbitrary header values would each get a bucket.
		if addr, err := netip.ParseAddr(strings.TrimSpace(strings.Split(r.Header.Get(header), ",")[0])); err == nil {
			return addr.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sessionMAC is the read-only UI credential: derived from the API token so
// rotating the token invalidates every issued cookie.
func sessionMAC(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("ui-read-session"))
	return hex.EncodeToString(mac.Sum(nil))
}

// ---------- Rate limiting (token bucket per client IP) ----------

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu        sync.Mutex
	perSec    float64
	burst     float64
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newRateLimiter(perMinute, burst int) *rateLimiter {
	return &rateLimiter{
		perSec:  float64(perMinute) / 60,
		burst:   float64(max(burst, 1)),
		buckets: map[string]*bucket{},
	}
}

// allow consumes one token for key; when empty it reports how long until the next one.
func (l *rateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSec)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// sweep drops buckets that have refilled completely, so memory stays bounded.
func (l *rateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	full := time.Duration(l.burst / l.perSec * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}

// ---------- Middleware ----------

func protect(cfg SecurityConfig, next http.Handler) http.Handler {
	var limiter *rateLimiter
	if cfg.RatePerMinute > 0 {
		limiter = newRateLimiter(cfg.RatePerMinute, cfg.RateBurst)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Cache-Control", "no-store") // handlers opt in to caching explicitly

		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if cfg.BlockBots && r.URL.Path != "/robots.txt" && isBadBot(r.UserAgent()) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if limiter != nil {
			if ok, wait := limiter.allow(clientIP(r, cfg.ClientIPHeader), time.Now()); !ok {
				h.Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		if handleCORS(w, r, cfg.CORSOrigins) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleCORS sets CORS headers for allowed origins and answers preflights.
// It reports whether the request was fully handled.
func handleCORS(w http.ResponseWriter, r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	api := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp"
	if origin == "" || !api {
		return false
	}
	h := w.Header()
	h.Add("Vary", "Origin")
	ok := slices.Contains(allowed, origin)
	if ok {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "Location, Retry-After, WWW-Authenticate")
	}
	if r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
		return false // simple request: the browser enforces the missing allow header
	}
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func robotsTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}
