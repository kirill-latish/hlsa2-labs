// Package fault is a tiny HTTP client for the fault-injector service.
// Lifted from lab 4-2 but keyed on `entity` (e.g. tenant-A) plus a
// `weight` (fraction of writes funnelled into that entity).
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

const (
	ModeNone = ""
	ModeHot  = "hot"
)

// Spec is the shape on the wire.
//
// hot mode: skew `Weight` fraction of writes to `Entity`.
type Spec struct {
	Mode   string  `json:"mode"`
	Entity string  `json:"entity,omitempty"`
	Weight float64 `json:"weight,omitempty"`
}

func (s Spec) IsNone() bool { return s.Mode == ModeNone }

// Client caches the current fault spec for ~200ms.
type Client struct {
	baseURL string
	hc      *http.Client

	mu       sync.RWMutex
	cur      Spec
	cachedAt time.Time
	cacheTTL time.Duration

	pollCount uint64
}

func New(baseURL string) *Client {
	return &Client{
		baseURL:  baseURL,
		hc:       &http.Client{Timeout: 200 * time.Millisecond},
		cacheTTL: 200 * time.Millisecond,
	}
}

// Get returns the current fault spec keyed on `slot` (the lab uses a
// single slot for hot-entity injection but the API stays generic).
func (c *Client) Get(slot string) Spec {
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
	spec, err := c.fetch(slot)
	atomic.AddUint64(&c.pollCount, 1)
	if err != nil {
		return c.cur
	}
	c.cur = spec
	c.cachedAt = time.Now()
	return spec
}

func (c *Client) fetch(slot string) (Spec, error) {
	url := fmt.Sprintf("%s/faults/%s", c.baseURL, slot)
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
