// Package fault is a tiny HTTP client for the fault-injector service.
// Same pattern as lab 3-3, but keyed on `service` rather than `dep`,
// and with `latency` and `fail` modes (the topic guide's vocabulary).
package fault

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Mode names supported by the injector.
const (
	ModeNone    = ""
	ModeLatency = "latency"
	ModeFail    = "fail"
)

// Spec is the JSON shape on the wire.
type Spec struct {
	Mode      string  `json:"mode"`
	P99MS     int     `json:"p99_ms,omitempty"`
	ErrorRate float64 `json:"error_rate,omitempty"`
}

// IsNone reports whether the spec means "no fault".
func (s Spec) IsNone() bool { return s.Mode == ModeNone }

// Client caches the dep's current fault spec for ~200ms so we don't
// flood the injector with per-request GETs.
type Client struct {
	baseURL string
	hc      *http.Client

	mu       sync.RWMutex
	cur      Spec
	cachedAt time.Time
	cacheTTL time.Duration

	pollCount uint64
}

// New constructs a client. baseURL is e.g. http://fault-injector:9000.
func New(baseURL string) *Client {
	return &Client{
		baseURL:  baseURL,
		hc:       &http.Client{Timeout: 200 * time.Millisecond},
		cacheTTL: 200 * time.Millisecond,
	}
}

// Get returns the current fault spec for the named service.
func (c *Client) Get(service string) Spec {
	c.mu.RLock()
	if time.Since(c.cachedAt) < c.cacheTTL {
		spec := c.cur
		c.mu.RUnlock()
		return spec
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.cachedAt) < c.cacheTTL {
		return c.cur
	}
	spec, err := c.fetch(service)
	atomic.AddUint64(&c.pollCount, 1)
	if err != nil {
		// Stay with whatever we had so an injector outage doesn't
		// look like a product outage.
		return c.cur
	}
	c.cur = spec
	c.cachedAt = time.Now()
	return spec
}

func (c *Client) fetch(service string) (Spec, error) {
	url := fmt.Sprintf("%s/faults/%s", c.baseURL, service)
	resp, err := c.hc.Get(url)
	if err != nil {
		return Spec{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Spec{Mode: ModeNone}, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Spec{}, fmt.Errorf("fault injector returned %d: %s", resp.StatusCode, string(body))
	}
	var s Spec
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Spec{}, err
	}
	return s, nil
}
