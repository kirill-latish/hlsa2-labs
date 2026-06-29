// cache-proxy is ONE instrumented Go caching-proxy binary that plays
// two roles, selected by the ROLE env var:
//
//	ROLE=pop     an edge Point-of-Presence cache. On a miss it fetches
//	             from the shield (if shield routing is on) or straight
//	             from the origin.
//	ROLE=shield  the single mid-tier "origin shield" cache between the
//	             PoPs and the origin. It collapses many PoP misses for
//	             the same object into ~one origin fetch.
//
// We use one purpose-built Go binary instead of Varnish/NGINX so the
// lab has total control over the cache key, can emit a precise
// cache-status per response (HIT/MISS/EXPIRED/STALE/BYPASS), and can
// turn request collapsing, origin shielding, stale-if-error, and the
// personalized-content key policy into runtime knobs the make targets
// flip via POST /admin/config. Varnish (VCL) or NGINX (proxy_cache_*)
// are perfectly valid in production; see the README for the trade-off.
//
// HTTP surface:
//
//	GET    /<anything>      proxied + cached request (the data plane)
//	GET    /healthz         liveness
//	GET    /metrics         Prometheus
//	GET    /admin/config    current config + node/role
//	POST   /admin/config    partial config update (between bench runs)
//	POST   /admin/purge     {"path":"/obj/s3"} expire one object's entries
//	POST   /admin/flush     drop the whole cache
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hlsa2-labs/lab6-2/internal/cache"
	"github.com/hlsa2-labs/lab6-2/internal/metrics"
	"golang.org/x/sync/singleflight"
)

// trackingParams are query-string keys that change the URL but never
// change the response (shared-link / ad-attribution noise). When the
// cache key strips down to the allowlist, these are exactly what gets
// removed - which is the whole point of the cache-key-fragmentation
// experiment.
var trackingParams = map[string]bool{
	"utm_source":   true,
	"utm_medium":   true,
	"utm_campaign": true,
	"utm_term":     true,
	"utm_content":  true,
	"fbclid":       true,
	"gclid":        true,
	"mc_cid":       true,
	"igshid":       true,
}

const (
	keyFullQuerystring = "full-querystring"
	keyStripped        = "stripped-allowlist"

	personalizedBroad   = "broad-key-ignores-auth"
	personalizedPrivate = "private-no-store"
	personalizedPerUser = "per-user-key"

	statusHit     = "HIT"
	statusMiss    = "MISS"
	statusExpired = "EXPIRED"
	statusStale   = "STALE"
	statusBypass  = "BYPASS"
)

// Config is the runtime, hot-swappable proxy configuration.
type Config struct {
	CacheKeyMode      string   `json:"cache_key_mode"`     // full-querystring | stripped-allowlist
	Allowlist         []string `json:"allowlist"`          // query params that DO change content
	TTLSeconds        int      `json:"ttl_seconds"`        // freshness lifetime for cached entries
	Vary              []string `json:"vary"`               // request headers folded into the cache key
	RequestCollapsing bool     `json:"request_collapsing"` // singleflight concurrent misses for one key
	StaleIfError      bool     `json:"stale_if_error"`     // serve last-known-good on upstream error
	PersonalizedMode  string   `json:"personalized_mode"`  // broad-key-ignores-auth | private-no-store | per-user-key
	ShieldRouting     bool     `json:"shield_routing"`     // pop only: route misses through the shield
}

type holder struct {
	mu      sync.RWMutex
	current Config
}

func (h *holder) snapshot() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

func (h *holder) set(c Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.current = c
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(os.Getenv(key))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func envList(key string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// upstreamResp is the snapshot of an upstream fetch shared by
// singleflight-collapsed callers.
type upstreamResp struct {
	Status int
	Header http.Header
	Body   []byte
}

type proxy struct {
	node    string
	role    string // pop | shield
	shield  string // upstream shield URL (pop only)
	origin  string // origin URL
	store   *cache.Store
	edge    *metrics.Edge
	cfg     *holder
	client  *http.Client
	flights singleflight.Group
}

func main() {
	role := envOrDefault("ROLE", "pop")
	node := envOrDefault("NODE", role)
	port := envOrDefault("PORT", "8080")

	p := &proxy{
		node:   node,
		role:   role,
		shield: envOrDefault("SHIELD_URL", "http://shield:8080"),
		origin: envOrDefault("ORIGIN_URL", "http://origin:8088"),
		store:  cache.New(),
		edge:   metrics.NewEdge(),
		cfg:    &holder{},
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxConnsPerHost:     512,
				MaxIdleConnsPerHost: 128,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}

	p.cfg.set(Config{
		CacheKeyMode:      envOrDefault("CACHE_KEY_MODE", keyStripped),
		Allowlist:         defaultList(envList("ALLOWLIST"), []string{"v"}),
		TTLSeconds:        envInt("TTL_SECONDS", 60),
		Vary:              envList("VARY"),
		RequestCollapsing: envBool("REQUEST_COLLAPSING", true),
		StaleIfError:      envBool("STALE_IF_ERROR", false),
		PersonalizedMode:  envOrDefault("PERSONALIZED_MODE", personalizedPrivate),
		ShieldRouting:     envBool("SHIELD_ROUTING", true),
	})
	p.publishConfig(p.cfg.snapshot())

	httpMetrics := metrics.NewHTTPMetrics(node)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(httpMetrics.Middleware(map[string]bool{"/metrics": true, "/healthz": true}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", metrics.Handler())

	r.Get("/admin/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"node":          p.node,
			"role":          p.role,
			"shield_url":    p.shield,
			"origin_url":    p.origin,
			"cache_entries": p.store.Len(),
			"config":        p.cfg.snapshot(),
		})
	})

	r.Post("/admin/config", p.handleConfig)
	r.Post("/admin/purge", p.handlePurge)
	r.Post("/admin/flush", func(w http.ResponseWriter, _ *http.Request) {
		n := p.store.Flush()
		p.edge.CacheEntries.WithLabelValues(p.node).Set(0)
		writeJSON(w, http.StatusOK, map[string]any{"flushed": n})
	})

	// Everything else is the data plane.
	r.NotFound(p.handleProxy)
	r.MethodNotAllowed(p.handleProxy)

	// Keep the cardinality gauge live even when no traffic flows.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			p.edge.CacheEntries.WithLabelValues(p.node).Set(float64(p.store.Len()))
		}
	}()

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		c := p.cfg.snapshot()
		log.Printf("cache-proxy node=%s role=%s listening on :%s key=%s ttl=%ds collapse=%t stale_if_error=%t personalized=%s shield_routing=%t",
			p.node, p.role, port, c.CacheKeyMode, c.TTLSeconds, c.RequestCollapsing, c.StaleIfError, c.PersonalizedMode, c.ShieldRouting)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func defaultList(got, def []string) []string {
	if len(got) == 0 {
		return def
	}
	return got
}

// handleConfig applies a partial config update. Missing fields keep
// their current values, so a bench script can flip exactly one knob.
func (p *proxy) handleConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CacheKeyMode      *string   `json:"cache_key_mode"`
		Allowlist         *[]string `json:"allowlist"`
		TTLSeconds        *int      `json:"ttl_seconds"`
		Vary              *[]string `json:"vary"`
		RequestCollapsing *bool     `json:"request_collapsing"`
		StaleIfError      *bool     `json:"stale_if_error"`
		PersonalizedMode  *string   `json:"personalized_mode"`
		ShieldRouting     *bool     `json:"shield_routing"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	next := p.cfg.snapshot()
	if body.CacheKeyMode != nil {
		next.CacheKeyMode = *body.CacheKeyMode
	}
	if body.Allowlist != nil {
		next.Allowlist = *body.Allowlist
	}
	if body.TTLSeconds != nil {
		next.TTLSeconds = *body.TTLSeconds
	}
	if body.Vary != nil {
		next.Vary = *body.Vary
	}
	if body.RequestCollapsing != nil {
		next.RequestCollapsing = *body.RequestCollapsing
	}
	if body.StaleIfError != nil {
		next.StaleIfError = *body.StaleIfError
	}
	if body.PersonalizedMode != nil {
		next.PersonalizedMode = *body.PersonalizedMode
	}
	if body.ShieldRouting != nil {
		next.ShieldRouting = *body.ShieldRouting
	}
	p.cfg.set(next)
	p.publishConfig(next)
	log.Printf("admin/config[%s]: %+v", p.node, next)
	writeJSON(w, http.StatusOK, next)
}

func (p *proxy) handlePurge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		http.Error(w, "body must be {\"path\":\"/obj/...\"}", http.StatusBadRequest)
		return
	}
	n := p.store.PurgePath(body.Path)
	p.edge.CacheEntries.WithLabelValues(p.node).Set(float64(p.store.Len()))
	log.Printf("admin/purge[%s]: path=%s removed=%d", p.node, body.Path, n)
	writeJSON(w, http.StatusOK, map[string]any{"path": body.Path, "removed": n})
}

// publishConfig mirrors the active config into Prometheus gauges so the
// dashboard can annotate which run is which.
func (p *proxy) publishConfig(c Config) {
	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	p.edge.Setting.WithLabelValues(p.node, "ttl_seconds").Set(float64(c.TTLSeconds))
	p.edge.Setting.WithLabelValues(p.node, "request_collapsing").Set(b2f(c.RequestCollapsing))
	p.edge.Setting.WithLabelValues(p.node, "stale_if_error").Set(b2f(c.StaleIfError))
	p.edge.Setting.WithLabelValues(p.node, "shield_routing").Set(b2f(c.ShieldRouting))

	for _, m := range []string{keyFullQuerystring, keyStripped} {
		v := 0.0
		if c.CacheKeyMode == m {
			v = 1
		}
		p.edge.Mode.WithLabelValues(p.node, "cache_key", m).Set(v)
	}
	for _, m := range []string{personalizedBroad, personalizedPrivate, personalizedPerUser} {
		v := 0.0
		if c.PersonalizedMode == m {
			v = 1
		}
		p.edge.Mode.WithLabelValues(p.node, "personalized", m).Set(v)
	}
}

// isPersonalizedPath reports whether the path is the personalized,
// auth-bearing route. Keeping it path-based keeps the lab readable.
func isPersonalizedPath(path string) bool {
	return path == "/account"
}

func userID(r *http.Request) string {
	if c, err := r.Cookie("uid"); err == nil {
		return c.Value
	}
	return ""
}

// cacheKey builds the key this request will be cached under, honouring
// the cache-key mode, vary headers, and (for personalized requests) the
// per-user-key policy.
func (p *proxy) cacheKey(r *http.Request, c Config, personalized bool, uid string) string {
	q := r.URL.Query()
	allow := make(map[string]bool, len(c.Allowlist))
	for _, a := range c.Allowlist {
		allow[a] = true
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if c.CacheKeyMode == keyFullQuerystring {
			keys = append(keys, k)
		} else if allow[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(r.URL.Path)
	b.WriteByte('?')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(q.Get(k))
	}
	for _, h := range c.Vary {
		b.WriteString("|")
		b.WriteString(h)
		b.WriteByte('=')
		b.WriteString(r.Header.Get(h))
	}
	if personalized && c.PersonalizedMode == personalizedPerUser {
		b.WriteString("|uid=")
		b.WriteString(uid)
	}
	return b.String()
}

// cacheable decides whether an upstream response may be stored. The
// rules mirror a real CDN: a Set-Cookie or a no-store/no-cache/private
// Cache-Control makes content uncacheable (-> BYPASS). For personalized
// content the proxy's personalized_mode overrides the default: broad
// key and per-user key both choose to cache anyway (the broad one is
// the bug), private/no-store never caches.
func cacheable(resp *upstreamResp, c Config, personalized bool) bool {
	if personalized {
		switch c.PersonalizedMode {
		case personalizedBroad, personalizedPerUser:
			return true
		default:
			return false
		}
	}
	if resp.Status < 200 || resp.Status >= 400 {
		return false
	}
	if resp.Header.Get("Set-Cookie") != "" {
		return false
	}
	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") || strings.Contains(cc, "private") {
		return false
	}
	return true
}

func (p *proxy) handleProxy(w http.ResponseWriter, r *http.Request) {
	c := p.cfg.snapshot()
	uid := userID(r)
	personalized := uid != "" && isPersonalizedPath(r.URL.Path)
	now := time.Now()

	// Personalized + private/no-store: never touches the shared cache.
	if personalized && c.PersonalizedMode == personalizedPrivate {
		resp, err := p.fetch(r, c)
		if err != nil {
			p.serveError(w, err)
			return
		}
		p.serve(w, resp, statusBypass, false)
		return
	}

	key := p.cacheKey(r, c, personalized, uid)

	if entry, ok := p.store.Get(key); ok {
		if entry.Fresh(now) {
			p.serveEntry(w, entry, statusHit)
			return
		}
		// Expired: try to revalidate by refetching upstream.
		resp, err := p.fetchCollapsed(key, r, c)
		if err != nil {
			if c.StaleIfError {
				p.serveEntry(w, entry, statusStale)
				return
			}
			p.serveError(w, err)
			return
		}
		if cacheable(resp, c, personalized) {
			p.store.Set(key, p.entryFrom(r, resp, c))
		}
		p.serve(w, resp, statusExpired, false)
		return
	}

	// Cold miss.
	resp, err := p.fetchCollapsed(key, r, c)
	if err != nil {
		p.serveError(w, err)
		return
	}
	if cacheable(resp, c, personalized) {
		p.store.Set(key, p.entryFrom(r, resp, c))
		p.serve(w, resp, statusMiss, false)
		return
	}
	// Reachable but uncacheable -> the silent "caching nothing" failure.
	p.serve(w, resp, statusBypass, false)
}

func (p *proxy) entryFrom(r *http.Request, resp *upstreamResp, c Config) *cache.Entry {
	return &cache.Entry{
		Path:     r.URL.Path,
		Status:   resp.Status,
		Header:   resp.Header.Clone(),
		Body:     resp.Body,
		StoredAt: time.Now(),
		TTL:      time.Duration(c.TTLSeconds) * time.Second,
	}
}

// fetchCollapsed wraps fetch in a singleflight group keyed by the cache
// key when request collapsing is on, so a burst of concurrent misses
// for the same object becomes ONE upstream fetch (request collapsing at
// a PoP; origin shielding at the shield). With collapsing off, every
// miss fetches independently - the un-bounded fan-in the herd step
// reproduces.
func (p *proxy) fetchCollapsed(key string, r *http.Request, c Config) (*upstreamResp, error) {
	if !c.RequestCollapsing {
		return p.fetch(r, c)
	}
	v, err, _ := p.flights.Do(key, func() (any, error) {
		return p.fetch(r, c)
	})
	if err != nil {
		return nil, err
	}
	return v.(*upstreamResp), nil
}

// fetch performs the actual upstream request. A PoP goes to the shield
// when shield routing is on, otherwise straight to the origin; the
// shield always goes to the origin.
func (p *proxy) fetch(r *http.Request, c Config) (*upstreamResp, error) {
	base := p.origin
	upstream := "origin"
	if p.role == "pop" && c.ShieldRouting {
		base = p.shield
		upstream = "shield"
	}
	p.edge.UpstreamRequests.WithLabelValues(p.node, upstream).Inc()

	target := base + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	// Forward identity + vary-relevant headers so the upstream can
	// personalize and so the shield keys consistently.
	if ck, err := r.Cookie("uid"); err == nil {
		req.AddCookie(ck)
	}
	for _, h := range c.Vary {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 500 {
		return nil, errors.New("upstream " + strconv.Itoa(resp.StatusCode))
	}
	return &upstreamResp{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body}, nil
}

func (p *proxy) serveEntry(w http.ResponseWriter, e *cache.Entry, status string) {
	p.serve(w, &upstreamResp{Status: e.Status, Header: e.Header, Body: e.Body}, status, true)
}

// serve writes the response, records the cache-status metric, attributes
// bytes to edge vs origin, and stamps the status onto a response header
// so the shell probes (verify-cacheability, edge-status) can read it.
func (p *proxy) serve(w http.ResponseWriter, resp *upstreamResp, status string, fromEdge bool) {
	p.edge.CacheResponses.WithLabelValues(p.node, p.role, status).Inc()
	source := "origin"
	if fromEdge {
		source = "edge"
	}
	p.edge.BytesServed.WithLabelValues(p.node, source).Add(float64(len(resp.Body)))

	for k, vals := range resp.Header {
		// Don't leak hop-by-hop or upstream cache-status headers.
		if strings.EqualFold(k, "X-Cache-Status") || strings.EqualFold(k, "X-Cache-Node") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Cache-Status", status)
	w.Header().Set("X-Cache-Node", p.node)
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

func (p *proxy) serveError(w http.ResponseWriter, err error) {
	w.Header().Set("X-Cache-Status", statusBypass)
	w.Header().Set("X-Cache-Node", p.node)
	http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
