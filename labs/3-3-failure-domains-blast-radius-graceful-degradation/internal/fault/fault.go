// Package fault is a tiny HTTP client for the fault-injector service.
// Both the deps and the gateway hold one of these; it caches the
// current fault spec for 200ms so we don't flood the injector with
// per-request GETs while still giving the topic guide an end-to-end
// "inject a fault and see it propagate in <1 second" experience.
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

// Mode names supported by the injector. Mirrors topic-246's vocabulary.
const (
	ModeNone    = ""
	ModeDown    = "down"
	ModeLatency = "latency"
	ModeErrors  = "errors"
)

// Spec is the JSON shape on the wire.
type Spec struct {
	Mode      string  `json:"mode"`
	P99MS     int     `json:"p99_ms,omitempty"`
	ErrorRate float64 `json:"error_rate,omitempty"`
}

// IsNone reports whether the spec means "no fault".
func (s Spec) IsNone() bool { return s.Mode == ModeNone }

// Client caches the dep's current fault spec.
type Client struct {
	baseURL string
	hc      *http.Client

	mu        sync.RWMutex
	cur       Spec
	cachedAt  time.Time
	cacheTTL  time.Duration

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

// Get returns the current fault spec for the dep. Refreshes from the
// injector if the cache is older than cacheTTL.
func (c *Client) Get(dep string) Spec {
	c.mu.RLock()
	if time.Since(c.cachedAt) < c.cacheTTL {
		spec := c.cur
		c.mu.RUnlock()
		return spec
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check after acquiring the write lock.
	if time.Since(c.cachedAt) < c.cacheTTL {
		return c.cur
	}
	spec, err := c.fetch(dep)
	atomic.AddUint64(&c.pollCount, 1)
	if err != nil {
		// Stay with whatever we had. The injector being down should
		// NOT make every dep look faulted - that would amplify a
		// configuration outage into a product outage.
		return c.cur
	}
	c.cur = spec
	c.cachedAt = time.Now()
	return spec
}

func (c *Client) fetch(dep string) (Spec, error) {
	url := fmt.Sprintf("%s/faults/%s", c.baseURL, dep)
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
