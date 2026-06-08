// es-consumer tails the Debezium CDC topics from Redpanda and writes
// the per-row state into Elasticsearch. Indexes used:
//
//	products  <- cdc.public.products
//	orders    <- cdc.public.orders
//	users     <- cdc.public.users
//
// On boot it ensures each index exists with a known mapping (loaded
// from elasticsearch/mappings/*.json mounted into /mappings).
//
// The consumer is the source of truth for the polyglot bench's
// LSN-wait policy: every batch records the highest committed LSN and
// exposes it on `lab43_es_indexed_lsn` as a gauge plus on
// `/indexed-lsn` as a tiny HTTP endpoint.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/hlsa2-labs/lab4-3/internal/cdc"
	"github.com/hlsa2-labs/lab4-3/internal/metrics"
	"github.com/hlsa2-labs/lab4-3/internal/svchelp"
)

type indexState struct {
	mu       sync.RWMutex
	lsn      string
	count    uint64
	failures uint64
}

func main() {
	port := svchelp.EnvOrDefault("PORT", "9000")
	brokers := svchelp.EnvOrDefault("REDPANDA_BROKERS", "redpanda:9092")
	group := svchelp.EnvOrDefault("CONSUMER_GROUP", "lab43-es-consumer")
	topicsCSV := svchelp.EnvOrDefault("CDC_TOPICS", "cdc.public.products,cdc.public.orders,cdc.public.users")
	esURL := svchelp.EnvOrDefault("ES_URL", "http://elasticsearch:9200")

	state := &indexState{}

	cdcLag := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "lab43_cdc_lag_ms",
		Help:    "End-to-end CDC lag (postgres commit -> es index) in ms.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 18),
	}, []string{"index"})
	indexedLSN := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "lab43_es_indexed_lsn_byte",
		Help: "Latest LSN byte position the consumer has indexed.",
	})
	indexedTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "lab43_es_indexed_total",
		Help: "Documents indexed by the es-consumer, partitioned by index and outcome.",
	}, []string{"index", "outcome"})
	metrics.MustRegister(cdcLag, indexedLSN, indexedTotal)

	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{esURL},
	})
	if err != nil {
		log.Fatalf("es-consumer: es client: %v", err)
	}
	if err := waitForElasticsearch(es, 90*time.Second); err != nil {
		log.Fatalf("es-consumer: %v", err)
	}
	if err := ensureIndices(es); err != nil {
		log.Fatalf("es-consumer: ensureIndices: %v", err)
	}

	ctx, cancel := svchelp.SignalContext()
	defer cancel()

	topics := strings.Split(topicsCSV, ",")
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.AutoCommitMarks(),
		kgo.AutoCommitInterval(2*time.Second),
		kgo.SessionTimeout(15*time.Second),
	)
	if err != nil {
		log.Fatalf("es-consumer: kafka: %v", err)
	}
	defer cl.Close()

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/indexed-lsn", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.RLock()
		defer state.mu.RUnlock()
		svchelp.WriteOK(w, map[string]any{
			"lsn":      state.lsn,
			"count":    state.count,
			"failures": state.failures,
		})
	})
	r.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("es-consumer listening on :%s, brokers=%s topics=%v", port, brokers, topics)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("es-consumer: listen: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shutdownCtx)
			c()
			return
		default:
		}
		fetches := cl.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if !errors.Is(e.Err, context.Canceled) {
					log.Printf("es-consumer: fetch err topic=%s partition=%d: %v", e.Topic, e.Partition, e.Err)
				}
			}
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			outcome := "ok"
			indexName := indexFromTopic(rec.Topic)
			env, err := cdc.Decode(rec.Value)
			if err != nil {
				outcome = "decode_err"
				state.mu.Lock()
				state.failures++
				state.mu.Unlock()
				indexedTotal.With(prometheus.Labels{"index": indexName, "outcome": outcome}).Inc()
				cl.MarkCommitRecords(rec)
				return
			}
			doc, err := envelopeToDocument(env)
			if err != nil {
				outcome = "shape_err"
				state.mu.Lock()
				state.failures++
				state.mu.Unlock()
				indexedTotal.With(prometheus.Labels{"index": indexName, "outcome": outcome}).Inc()
				cl.MarkCommitRecords(rec)
				return
			}
			id, err := env.IDInt64()
			if err != nil {
				outcome = "id_err"
				state.mu.Lock()
				state.failures++
				state.mu.Unlock()
				indexedTotal.With(prometheus.Labels{"index": indexName, "outcome": outcome}).Inc()
				cl.MarkCommitRecords(rec)
				return
			}
			doc["_indexed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
			doc["_lsn"] = env.LSNString()
			body, _ := json.Marshal(doc)
			req := bytes.NewReader(body)
			res, err := es.Index(indexName, req,
				es.Index.WithDocumentID(fmt.Sprintf("%d", id)),
				es.Index.WithRefresh("false"),
			)
			if err != nil || res.IsError() {
				outcome = "es_err"
				state.mu.Lock()
				state.failures++
				state.mu.Unlock()
				if res != nil {
					b, _ := io.ReadAll(res.Body)
					_ = res.Body.Close()
					log.Printf("es-consumer: index=%s id=%d err=%v body=%s", indexName, id, err, string(b))
				} else {
					log.Printf("es-consumer: index=%s id=%d err=%v", indexName, id, err)
				}
				indexedTotal.With(prometheus.Labels{"index": indexName, "outcome": outcome}).Inc()
				return
			}
			_ = res.Body.Close()

			ts := env.SourceTime()
			if !ts.IsZero() {
				cdcLag.With(prometheus.Labels{"index": indexName}).Observe(float64(time.Since(ts).Milliseconds()))
			}
			state.mu.Lock()
			state.count++
			if env.LSNString() != "" {
				state.lsn = env.LSNString()
			}
			state.mu.Unlock()
			indexedTotal.With(prometheus.Labels{"index": indexName, "outcome": outcome}).Inc()
			if env.LSN != nil {
				if v, ok := env.LSN.(float64); ok {
					indexedLSN.Set(v)
				}
			}
			cl.MarkCommitRecords(rec)
		})
	}
}

func indexFromTopic(topic string) string {
	// cdc.public.products -> products
	idx := strings.LastIndexByte(topic, '.')
	if idx < 0 {
		return topic
	}
	return topic[idx+1:]
}

// envelopeToDocument preserves only the fields each index's mapping
// allows (mappings are dynamic:strict).
func envelopeToDocument(e cdc.Envelope) (map[string]any, error) {
	id, err := e.IDInt64()
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"id":           id,
		"tenant_id":    e.TenantID,
		"committed_at": orRFC3339(e.CommittedAt),
		"updated_at":   orRFC3339(e.UpdatedAt),
	}
	switch {
	case e.SKU != "" || e.Title != "" || e.PriceCents > 0:
		doc["sku"] = e.SKU
		doc["title"] = e.Title
		doc["description"] = e.Description
		doc["price_cents"] = e.PriceCents
		doc["stock"] = e.Stock
		facets := []string{}
		switch v := e.SearchFacets.(type) {
		case []any:
			for _, f := range v {
				if s, ok := f.(string); ok {
					facets = append(facets, s)
				}
			}
		case string:
			if v != "" {
				facets = strings.Split(v, ",")
			}
		}
		doc["search_facets"] = facets
	case e.UserID != nil || e.ProductID != nil:
		uid, _ := coerceInt64(e.UserID)
		pid, _ := coerceInt64(e.ProductID)
		doc["user_id"] = uid
		doc["product_id"] = pid
		doc["quantity"] = e.Quantity
		doc["total_cents"] = e.TotalCents
		doc["status"] = e.Status
	default:
		doc["email"] = e.Email
		doc["display_name"] = e.DisplayName
	}
	return doc, nil
}

func coerceInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		return int64(t), true
	case int64:
		return t, true
	}
	return 0, false
}

func orRFC3339(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return time.UnixMilli(int64(t)).UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func ensureIndices(es *elasticsearch.Client) error {
	dir := svchelp.EnvOrDefault("ES_MAPPINGS_DIR", "/mappings")
	files := map[string]string{
		"products": dir + "/products.json",
		"orders":   dir + "/orders.json",
		"users":    dir + "/users.json",
	}
	for index, path := range files {
		exists, err := indexExists(es, index)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		mapping, err := os.ReadFile(path)
		if err != nil {
			// Mapping file isn't mounted - fall back to dynamic.
			log.Printf("es-consumer: mapping for %s missing at %s, creating dynamic index", index, path)
			res, err := es.Indices.Create(index)
			if err != nil {
				return err
			}
			_ = res.Body.Close()
			continue
		}
		res, err := es.Indices.Create(index, es.Indices.Create.WithBody(bytes.NewReader(mapping)))
		if err != nil {
			return err
		}
		if res.IsError() {
			b, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			return fmt.Errorf("create index %s: %s", index, string(b))
		}
		_ = res.Body.Close()
		log.Printf("es-consumer: created index %s with mapping %s", index, path)
	}
	return nil
}

func indexExists(es *elasticsearch.Client, name string) (bool, error) {
	res, err := es.Indices.Exists([]string{name})
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	return res.StatusCode == 200, nil
}

func waitForElasticsearch(es *elasticsearch.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := es.Cluster.Health(es.Cluster.Health.WithWaitForStatus("yellow"), es.Cluster.Health.WithTimeout(2*time.Second))
		if err == nil && !res.IsError() {
			_ = res.Body.Close()
			return nil
		}
		if res != nil {
			_ = res.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("elasticsearch not ready within %s", timeout)
}
