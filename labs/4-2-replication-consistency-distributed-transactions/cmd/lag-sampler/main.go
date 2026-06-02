// lag-sampler runs forever in the stack. Every 100ms it queries:
//
//	primary  -> pg_current_wal_lsn()
//	replica  -> pg_last_wal_replay_lsn(), pg_last_xact_replay_timestamp()
//
// and emits two histograms per replica: lag in bytes and lag in
// seconds. Recent samples are also written to a CSV file under
// $LAG_OUTPUT_DIR (defaults to /perf/lag/live) so the bench scripts
// can copy them per-run.
//
// HTTP API:
//
//	GET  /healthz       -> ok
//	GET  /metrics       -> Prometheus
//	POST /run/start?run=<name>     - rotate the CSV to perf/lag/<name>/samples.csv
//	POST /run/stop                 - flush + close the active CSV
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hlsa2-labs/lab4-2/internal/lsn"
	"github.com/hlsa2-labs/lab4-2/internal/metrics"
	"github.com/hlsa2-labs/lab4-2/internal/svchelp"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "9101")
	periodMS := envInt("SAMPLE_PERIOD_MS", 100)
	outDir := svchelp.EnvOrDefault("LAG_OUTPUT_DIR", "/perf/lag")

	primaryDSN := svchelp.EnvOrDefault("PRIMARY_DSN", "postgres://hlsa:hlsa@postgres-primary:5432/hlsa?sslmode=disable")
	replicaDSNs := map[string]string{
		"replica-1": svchelp.EnvOrDefault("REPLICA1_DSN", "postgres://hlsa:hlsa@postgres-replica-1:5432/hlsa?sslmode=disable"),
		"replica-2": svchelp.EnvOrDefault("REPLICA2_DSN", "postgres://hlsa:hlsa@postgres-replica-2:5432/hlsa?sslmode=disable"),
	}

	primary := mustPool(primaryDSN, "primary")
	defer primary.Close()
	replicas := make(map[string]*pgxpool.Pool, len(replicaDSNs))
	for name, dsn := range replicaDSNs {
		replicas[name] = mustPool(dsn, name)
		defer replicas[name].Close()
	}

	mx := newSamplerMetrics()
	ctrl := newCSVCtrl(outDir)
	go ctrl.runLive(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	go sampleLoop(ctx, primary, replicas, time.Duration(periodMS)*time.Millisecond, mx, ctrl)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", metrics.Handler())
	mux.HandleFunc("/run/start", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("run")
		if name == "" {
			http.Error(w, "missing run", http.StatusBadRequest)
			return
		}
		if err := ctrl.startRun(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("started:" + name))
	})
	mux.HandleFunc("/run/stop", func(w http.ResponseWriter, _ *http.Request) {
		if err := ctrl.stopRun(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("stopped"))
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("lag-sampler listening on :%s period=%dms outDir=%s", port, periodMS, outDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	cancel()
	_ = ctrl.stopRun()
	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShut()
	_ = srv.Shutdown(shutCtx)
}

func mustPool(dsn, label string) *pgxpool.Pool {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("parse dsn %s: %v", label, err)
	}
	cfg.MaxConns = 4
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("pool %s: %v", label, err)
	}
	for i := 0; i < 60; i++ {
		if err := p.Ping(context.Background()); err == nil {
			log.Printf("lag-sampler: connected to %s", label)
			return p
		}
		time.Sleep(time.Second)
	}
	log.Fatalf("lag-sampler: timed out connecting to %s", label)
	return nil
}

func sampleLoop(ctx context.Context, primary *pgxpool.Pool, replicas map[string]*pgxpool.Pool, period time.Duration, mx *samplerMetrics, ctrl *csvCtrl) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			pLSN, err := lsn.CurrentWAL(ctx, primary)
			if err != nil {
				continue
			}
			for name, p := range replicas {
				rLSN, err := lsn.LastReplay(ctx, p)
				if err != nil {
					continue
				}
				rTime, _ := lsn.LastReplayTimestamp(ctx, p)

				bytesBehind := lsn.Diff(pLSN, rLSN)
				secsBehind := 0.0
				if !rTime.IsZero() {
					secsBehind = now.UTC().Sub(rTime).Seconds()
					if secsBehind < 0 {
						secsBehind = 0
					}
				}
				mx.lagBytes.WithLabelValues(name).Observe(float64(bytesBehind))
				mx.lagSeconds.WithLabelValues(name).Observe(secsBehind)
				mx.lagBytesGauge.WithLabelValues(name).Set(float64(bytesBehind))
				mx.lagSecondsGauge.WithLabelValues(name).Set(secsBehind)

				ctrl.write(now, name, pLSN, rLSN, bytesBehind, secsBehind)
			}
		}
	}
}

// ----------------------------------------------------------------
// Per-run CSV writer
// ----------------------------------------------------------------

type csvCtrl struct {
	baseDir string

	mu     sync.Mutex
	w      *csv.Writer
	f      *os.File
	active string
}

func newCSVCtrl(baseDir string) *csvCtrl { return &csvCtrl{baseDir: baseDir} }

// runLive opens a "live" CSV that is always active; the bench scripts
// can copy out of it if they didn't call /run/start. Tolerates the
// directory not existing.
func (c *csvCtrl) runLive(_ context.Context) {
	_ = os.MkdirAll(c.baseDir, 0o755)
	_ = c.startRun("live")
}

func (c *csvCtrl) startRun(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.w != nil {
		c.w.Flush()
		_ = c.f.Close()
	}
	dir := filepath.Join(c.baseDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "samples.csv"))
	if err != nil {
		return err
	}
	c.f = f
	c.w = csv.NewWriter(f)
	_ = c.w.Write([]string{"ts", "replica", "primary_lsn", "replica_lsn", "bytes_behind", "seconds_behind"})
	c.active = name
	return nil
}

func (c *csvCtrl) stopRun() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.w == nil {
		return nil
	}
	c.w.Flush()
	if err := c.f.Close(); err != nil {
		return err
	}
	c.w = nil
	c.f = nil
	c.active = ""
	return nil
}

func (c *csvCtrl) write(ts time.Time, replica string, pLSN, rLSN lsn.LSN, bytesBehind uint64, secs float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.w == nil {
		return
	}
	_ = c.w.Write([]string{
		ts.UTC().Format(time.RFC3339Nano),
		replica,
		pLSN.String(),
		rLSN.String(),
		strconv.FormatUint(bytesBehind, 10),
		fmt.Sprintf("%.6f", secs),
	})
}

// ----------------------------------------------------------------
// metrics
// ----------------------------------------------------------------

type samplerMetrics struct {
	lagBytes        *prometheus.HistogramVec
	lagSeconds      *prometheus.HistogramVec
	lagBytesGauge   *prometheus.GaugeVec
	lagSecondsGauge *prometheus.GaugeVec
}

func newSamplerMetrics() *samplerMetrics {
	m := &samplerMetrics{
		lagBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "replica_lag_bytes",
			Help:    "Bytes the replica trails the primary by.",
			Buckets: prometheus.ExponentialBucketsRange(64, 64*1024*1024, 18),
		}, []string{"replica"}),
		lagSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "replica_lag_seconds",
			Help:    "Wallclock seconds the replica trails the primary by.",
			Buckets: prometheus.ExponentialBucketsRange(0.001, 60, 18),
		}, []string{"replica"}),
		lagBytesGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "replica_lag_bytes_current", Help: "Most recent lag sample in bytes.",
		}, []string{"replica"}),
		lagSecondsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "replica_lag_seconds_current", Help: "Most recent lag sample in seconds.",
		}, []string{"replica"}),
	}
	metrics.MustRegister(m.lagBytes, m.lagSeconds, m.lagBytesGauge, m.lagSecondsGauge)
	return m
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return def
	}
	return v
}
