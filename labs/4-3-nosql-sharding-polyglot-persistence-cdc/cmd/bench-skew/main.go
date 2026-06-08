// bench-skew is a one-shot driver. It:
//
//   - tells the loadgen to write at WRITE_RATE/sec for DURATION
//   - polls per-shard insert/CPU counters every 5s during the run
//   - at the end, scrapes `db.<coll>.collStats` for the per-shard
//     document count and writes partition_metrics.json into OUT_DIR
//
// OUT_DIR is supplied by scripts/run-bench-skew.sh
// (perf/results/skew/<label>/run-<n>).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hlsa2-labs/lab4-3/internal/mongoutil"
	"github.com/hlsa2-labs/lab4-3/internal/shardkey"
	"github.com/hlsa2-labs/lab4-3/internal/svchelp"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type sample struct {
	TS         time.Time          `json:"ts"`
	Inserts    map[string]int64   `json:"inserts"`
	CPUUserSec map[string]float64 `json:"cpu_user_sec"`
}

type result struct {
	StartedAt    time.Time        `json:"started_at"`
	StoppedAt    time.Time        `json:"stopped_at"`
	ShardKey     string           `json:"shard_key"`
	Collection   string           `json:"collection"`
	WriteRate    int              `json:"write_rate"`
	Duration     string           `json:"duration"`
	Label        string           `json:"label"`
	Samples      []sample         `json:"samples"`
	DocCount     map[string]int64 `json:"doc_count"`
	ClusterTotal int64            `json:"cluster_total"`
	MaxToMean    float64          `json:"max_to_mean"`
}

func main() {
	loadgenURL := svchelp.EnvOrDefault("LOADGEN_URL", "http://loadgen:9000")
	mongosHosts := svchelp.EnvOrDefault("MONGO_HOSTS", "mongos-1:27017,mongos-2:27017")
	shardHostsCSV := svchelp.EnvOrDefault("SHARD_HOSTS", "mongo-shard-1:27017,mongo-shard-2:27017,mongo-shard-3:27017")
	outDir := flag.String("out", svchelp.EnvOrDefault("OUT_DIR", "/perf"), "output directory")
	stratFlag := flag.String("shard-key", svchelp.EnvOrDefault("SHARD_KEY", "candidate"), "shard key strategy")
	rate := flag.Int("rate", svchelp.EnvIntOrDefault("WRITE_RATE", 200), "writes per second")
	durSec := flag.Int("duration-seconds", svchelp.EnvIntOrDefault("DURATION_SECONDS", 30), "duration in seconds")
	warmup := flag.Int("warmup-seconds", svchelp.EnvIntOrDefault("WARMUP_S", 5), "warmup seconds")
	label := flag.String("label", svchelp.EnvOrDefault("LABEL", "baseline"), "label written into partition_metrics.json")
	fixedFallback := flag.String("fixed-fallback", svchelp.EnvOrDefault("FIXED_FALLBACK", ""), "candidate to use when SHARD_KEY=fixed")
	flag.Parse()

	strat, err := shardkey.Parse(*stratFlag, *fixedFallback)
	if err != nil {
		log.Fatalf("bench-skew: %v", err)
	}
	collName, _ := shardkey.CollectionFor(strat)

	ctx, cancel := svchelp.SignalContext()
	defer cancel()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("bench-skew: mkdir %s: %v", *outDir, err)
	}

	router, err := mongoutil.ConnectMongos(ctx, mongosHosts)
	if err != nil {
		log.Fatalf("bench-skew: mongos: %v", err)
	}
	defer router.Disconnect(context.Background())

	shardHosts := strings.Split(shardHostsCSV, ",")
	shards := make(map[string]*mongo.Client, len(shardHosts))
	for _, h := range shardHosts {
		cli, err := mongoutil.ConnectShard(ctx, h)
		if err != nil {
			log.Fatalf("bench-skew: shard %s: %v", h, err)
		}
		shards[h] = cli
	}

	res := result{
		StartedAt:  time.Now().UTC(),
		ShardKey:   string(strat),
		Collection: collName,
		WriteRate:  *rate,
		Duration:   (time.Duration(*durSec) * time.Second).String(),
		Label:      *label,
	}

	startBody, _ := json.Marshal(map[string]any{
		"write_rate":       *rate,
		"duration_seconds": *durSec + *warmup + 5,
		"shard_key":        string(strat),
	})
	if err := postJSON(ctx, loadgenURL+"/run", startBody); err != nil {
		log.Fatalf("bench-skew: loadgen /run: %v", err)
	}

	if *warmup > 0 {
		log.Printf("bench-skew: warmup %ds", *warmup)
		time.Sleep(time.Duration(*warmup) * time.Second)
	}

	deadline := time.Now().Add(time.Duration(*durSec) * time.Second)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s := sample{TS: time.Now().UTC(), Inserts: map[string]int64{}, CPUUserSec: map[string]float64{}}
		for _, h := range shardHosts {
			ss, err := mongoutil.ServerStatus(ctx, shards[h])
			if err != nil {
				log.Printf("bench-skew: serverStatus(%s): %v", h, err)
				continue
			}
			if v, ok := pickInt(ss, "metrics", "commands", "insert", "total"); ok {
				s.Inserts[h] = v
			}
			if v, ok := pickInt(ss, "extra_info", "user_time_us"); ok {
				s.CPUUserSec[h] = float64(v) / 1e6
			}
		}
		res.Samples = append(res.Samples, s)
	}

	if err := postJSON(ctx, loadgenURL+"/stop", []byte("{}")); err != nil {
		log.Printf("bench-skew: loadgen /stop: %v", err)
	}
	res.StoppedAt = time.Now().UTC()

	dist, err := mongoutil.ShardDistribution(ctx, router, "lab43", collName)
	if err != nil {
		log.Printf("bench-skew: shardDistribution: %v", err)
	}
	// Use *all* known shards as the denominator so a single-shard
	// distribution (the broken candidate) shows max/mean = N rather
	// than 1.0. Map the shard rsName ("shard1rs") onto our N shards
	// list; merge/zero-fill anything not present.
	knownShardRS := []string{"shard1rs", "shard2rs", "shard3rs"}
	full := map[string]int64{}
	for _, s := range knownShardRS {
		full[s] = dist[s]
	}
	res.DocCount = full

	var total int64
	for _, v := range full {
		total += v
	}
	res.ClusterTotal = total
	if len(full) > 0 {
		var max int64
		for _, v := range full {
			if v > max {
				max = v
			}
		}
		mean := float64(total) / float64(len(full))
		if mean > 0 {
			res.MaxToMean = float64(max) / mean
		}
	}

	out := filepath.Join(*outDir, "partition_metrics.json")
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		log.Fatalf("bench-skew: write %s: %v", out, err)
	}
	log.Printf("bench-skew: wrote %s (max/mean=%.2f)", out, res.MaxToMean)
}

func postJSON(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
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
