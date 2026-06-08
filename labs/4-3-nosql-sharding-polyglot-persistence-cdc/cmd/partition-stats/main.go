// partition-stats periodically calls db.serverStatus() against each
// shard mongod and exposes the values that matter for the lab on
// `/metrics`:
//
//	lab43_shard_inserts_total{shard="..."}
//	lab43_shard_cpu_user_seconds_total{shard="..."}
//	lab43_shard_wt_cache_bytes_in_use{shard="..."}
//	lab43_collection_doc_count{collection="...",shard="..."}
//
// The Prometheus recording rule `lab43:max_to_mean_ratio` consumes
// `lab43_collection_doc_count` to drive the dashboard's max/mean panel.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/hlsa2-labs/lab4-3/internal/metrics"
	"github.com/hlsa2-labs/lab4-3/internal/mongoutil"
	"github.com/hlsa2-labs/lab4-3/internal/svchelp"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "9000")
	shardHosts := strings.Split(svchelp.EnvOrDefault("SHARD_HOSTS",
		"mongo-shard-1:27017,mongo-shard-2:27017,mongo-shard-3:27017"), ",")
	mongosHosts := svchelp.EnvOrDefault("MONGO_HOSTS", "mongos-1:27017,mongos-2:27017")
	mongoDB := svchelp.EnvOrDefault("MONGO_DB", "lab43")
	period := svchelp.ParseDuration(svchelp.EnvOrDefault("SAMPLE_PERIOD_MS", "1000ms"), time.Second)

	ctx, cancel := svchelp.SignalContext()
	defer cancel()

	inserts := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "lab43_shard_inserts_total",
		Help: "metrics.commands.insert.total per shard.",
	}, []string{"shard"})
	cpuUser := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "lab43_shard_cpu_user_seconds_total",
		Help: "extra_info.user_time_us / 1e6 per shard.",
	}, []string{"shard"})
	wtCache := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "lab43_shard_wt_cache_bytes_in_use",
		Help: "wiredTiger.cache.bytes currently in cache per shard.",
	}, []string{"shard"})
	docCount := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "lab43_collection_doc_count",
		Help: "Document count per shard per collection (sampled via mongos `collStats`).",
	}, []string{"shard", "collection"})
	metrics.MustRegister(inserts, cpuUser, wtCache, docCount)

	shards := make(map[string]*mongo.Client, len(shardHosts))
	for _, h := range shardHosts {
		cli, err := mongoutil.ConnectShard(ctx, h)
		if err != nil {
			log.Fatalf("partition-stats: shard %s: %v", h, err)
		}
		shards[h] = cli
	}
	router, err := mongoutil.ConnectMongos(ctx, mongosHosts)
	if err != nil {
		log.Fatalf("partition-stats: mongos: %v", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("partition-stats listening on :%s, shards=%v period=%s", port, shardHosts, period)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("partition-stats: listen: %v", err)
		}
	}()

	collections := []string{"events_candidate", "events_hash_suffix", "events_composite", "events_resharded"}
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shutdownCtx)
			c()
			return
		case <-tick.C:
		}
		for _, h := range shardHosts {
			ss, err := mongoutil.ServerStatus(ctx, shards[h])
			if err != nil {
				log.Printf("partition-stats: serverStatus(%s): %v", h, err)
				continue
			}
			if v, ok := pickInt(ss, "metrics", "commands", "insert", "total"); ok {
				inserts.With(prometheus.Labels{"shard": h}).Set(float64(v))
			}
			if v, ok := pickInt(ss, "extra_info", "user_time_us"); ok {
				cpuUser.With(prometheus.Labels{"shard": h}).Set(float64(v) / 1e6)
			}
			if v, ok := pickInt(ss, "wiredTiger", "cache", "bytes currently in the cache"); ok {
				wtCache.With(prometheus.Labels{"shard": h}).Set(float64(v))
			}
		}
		for _, coll := range collections {
			dist, err := mongoutil.ShardDistribution(ctx, router, mongoDB, coll)
			if err != nil {
				continue
			}
			for shard, count := range dist {
				docCount.With(prometheus.Labels{"shard": shard, "collection": coll}).Set(float64(count))
			}
		}
	}
}

func pickInt(m bson.M, path ...string) (int64, bool) {
	cur := any(m)
	for _, key := range path {
		switch t := cur.(type) {
		case bson.M:
			cur = t[key]
		case map[string]any:
			cur = t[key]
		default:
			return 0, false
		}
		if cur == nil {
			return 0, false
		}
	}
	switch v := cur.(type) {
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}
