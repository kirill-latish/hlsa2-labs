// shipping-svc is the saga + 2PC participant for "schedule shipping".
// Saga: POST /schedule, POST /cancel. 2PC: /xa/prepare|commit|abort.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hlsa2-labs/lab4-2/internal/fault"
	"github.com/hlsa2-labs/lab4-2/internal/metrics"
	"github.com/hlsa2-labs/lab4-2/internal/outbox"
	"github.com/hlsa2-labs/lab4-2/internal/payloads"
	"github.com/hlsa2-labs/lab4-2/internal/svchelp"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "8083")
	dsn := svchelp.EnvOrDefault("DATABASE_URL", "postgres://shipping:shipping@shipping-pg:5432/shipping?sslmode=disable")
	injector := svchelp.EnvOrDefault("FAULT_INJECTOR_URL", "http://fault-injector:9000")

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("dsn: %v", err)
	}
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	for i := 0; i < 60; i++ {
		if err := pool.Ping(context.Background()); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	fc := fault.New(injector)
	mx := newSagaMetrics()
	tm := newTwopcMetrics()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	r.Post("/schedule", scheduleHandler(pool, fc, mx))
	r.Post("/cancel", cancelHandler(pool, mx))
	r.Post("/xa/prepare", xaPrepareHandler(pool, fc, tm))
	r.Post("/xa/commit", xaCommitHandler(pool, tm))
	r.Post("/xa/abort", xaAbortHandler(pool, tm))

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go reportInDoubt(context.Background(), pool, tm)

	go func() {
		log.Printf("shipping-svc listening on :%s db=%s", port, dsn)
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

func scheduleHandler(pool *pgxpool.Pool, fc *fault.Client, mx *sagaMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.ScheduleShippingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := svchelp.ApplyFault(fc, "shipping"); err != nil {
			mx.failed.WithLabelValues("schedule").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ctx := r.Context()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			mx.failed.WithLabelValues("schedule").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var existing string
		err = tx.QueryRow(ctx, `SELECT status FROM shipments WHERE order_id = $1`, req.OrderID).Scan(&existing)
		if err == nil && existing == "scheduled" {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "scheduled (idempotent)")
			return
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO shipments (order_id, address, status) VALUES ($1, $2, 'scheduled')
			 ON CONFLICT (order_id) DO UPDATE SET address = EXCLUDED.address, status = 'scheduled', cancelled_at = NULL`,
			req.OrderID, req.Address,
		); err != nil {
			mx.failed.WithLabelValues("schedule").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := outbox.Insert(ctx, tx, outbox.Event{
			AggregateID: req.OrderID,
			Type:        "shipping.scheduled",
			Payload:     map[string]any{"order_id": req.OrderID, "address": req.Address},
		}); err != nil {
			mx.failed.WithLabelValues("schedule").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			mx.failed.WithLabelValues("schedule").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mx.applied.WithLabelValues("schedule").Inc()
		svchelp.WriteOK(w, "scheduled")
	}
}

func cancelHandler(pool *pgxpool.Pool, mx *sagaMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.CompensationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var status string
		err = tx.QueryRow(ctx, `SELECT status FROM shipments WHERE order_id = $1`, req.OrderID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) || status == "cancelled" {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "cancelled (idempotent)")
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(ctx,
			`UPDATE shipments SET status = 'cancelled', cancelled_at = now() WHERE order_id = $1`, req.OrderID,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := outbox.Insert(ctx, tx, outbox.Event{
			AggregateID: req.OrderID,
			Type:        "shipping.cancelled",
			Payload:     map[string]any{"order_id": req.OrderID, "reason": req.Reason},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mx.compensated.WithLabelValues("cancel").Inc()
		svchelp.WriteOK(w, "cancelled")
	}
}

func xaPrepareHandler(pool *pgxpool.Pool, fc *fault.Client, mx *twopcMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.XAPrepareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			svchelp.WriteXA(w, http.StatusBadRequest, false, "", err.Error())
			return
		}
		if err := svchelp.ApplyFault(fc, "shipping"); err != nil {
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "fault", err.Error())
			return
		}
		ctx := r.Context()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		defer conn.Release()

		if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO shipments (order_id, address, status) VALUES ($1, $2, 'prepared')
			 ON CONFLICT (order_id) DO UPDATE SET address = EXCLUDED.address, status = 'prepared'`,
			req.OrderID, req.Address,
		); err != nil {
			_, _ = conn.Exec(ctx, "ROLLBACK")
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf("PREPARE TRANSACTION '%s'", req.GID)); err != nil {
			_, _ = conn.Exec(ctx, "ROLLBACK")
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		_, _ = pool.Exec(ctx,
			`INSERT INTO twopc_log (gid, order_id, state) VALUES ($1, $2, 'prepared')
			 ON CONFLICT (gid) DO UPDATE SET state = 'prepared', prepared_at = now()`,
			req.GID, req.OrderID)
		mx.prepared.Inc()
		svchelp.WriteXA(w, http.StatusOK, true, "prepared", "")
	}
}

func xaCommitHandler(pool *pgxpool.Pool, mx *twopcMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.XACommitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			svchelp.WriteXA(w, http.StatusBadRequest, false, "", err.Error())
			return
		}
		ctx := r.Context()
		if _, err := pool.Exec(ctx, fmt.Sprintf("COMMIT PREPARED '%s'", req.GID)); err != nil {
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		_, _ = pool.Exec(ctx,
			`UPDATE twopc_log SET state = 'committed', finished_at = now() WHERE gid = $1`, req.GID)
		_, _ = pool.Exec(ctx,
			`UPDATE shipments SET status = 'scheduled' WHERE order_id IN (SELECT order_id FROM twopc_log WHERE gid = $1)`,
			req.GID)
		mx.committed.Inc()
		svchelp.WriteXA(w, http.StatusOK, true, "committed", "")
	}
}

func xaAbortHandler(pool *pgxpool.Pool, mx *twopcMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.XAAbortRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			svchelp.WriteXA(w, http.StatusBadRequest, false, "", err.Error())
			return
		}
		ctx := r.Context()
		if _, err := pool.Exec(ctx, fmt.Sprintf("ROLLBACK PREPARED '%s'", req.GID)); err != nil {
			log.Printf("shipping xa/abort gid=%s: %v", req.GID, err)
		}
		_, _ = pool.Exec(ctx,
			`UPDATE twopc_log SET state = 'aborted', finished_at = now() WHERE gid = $1`, req.GID)
		mx.aborted.Inc()
		svchelp.WriteXA(w, http.StatusOK, true, "aborted", "")
	}
}

// ----- metrics scaffolding (per-service so labels are correct) -----

type sagaMetrics struct {
	applied     *prometheus.CounterVec
	compensated *prometheus.CounterVec
	failed      *prometheus.CounterVec
}

func newSagaMetrics() *sagaMetrics {
	common := prometheus.Labels{"service": "shipping"}
	m := &sagaMetrics{
		applied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "saga_step_applied_total", Help: "Saga forward steps applied successfully.", ConstLabels: common,
		}, []string{"step"}),
		compensated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "saga_step_compensated_total", Help: "Saga compensation steps applied successfully.", ConstLabels: common,
		}, []string{"step"}),
		failed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "saga_step_failed_total", Help: "Saga steps that failed to apply.", ConstLabels: common,
		}, []string{"step"}),
	}
	metrics.MustRegister(m.applied, m.compensated, m.failed)
	return m
}

type twopcMetrics struct {
	prepared, committed, aborted prometheus.Counter
	inDoubt                      prometheus.Gauge
}

func newTwopcMetrics() *twopcMetrics {
	common := prometheus.Labels{"service": "shipping"}
	m := &twopcMetrics{
		prepared:  prometheus.NewCounter(prometheus.CounterOpts{Name: "twopc_prepared_total", Help: "PREPARE TRANSACTION calls.", ConstLabels: common}),
		committed: prometheus.NewCounter(prometheus.CounterOpts{Name: "twopc_committed_total", Help: "COMMIT PREPARED calls.", ConstLabels: common}),
		aborted:   prometheus.NewCounter(prometheus.CounterOpts{Name: "twopc_aborted_total", Help: "ROLLBACK PREPARED calls.", ConstLabels: common}),
		inDoubt:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "twopc_in_doubt_count", Help: "Currently prepared but uncommitted XA transactions.", ConstLabels: common}),
	}
	metrics.MustRegister(m.prepared, m.committed, m.aborted, m.inDoubt)
	return m
}

func reportInDoubt(ctx context.Context, pool *pgxpool.Pool, mx *twopcMetrics) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var n int64
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_prepared_xacts").Scan(&n); err == nil {
				mx.inDoubt.Set(float64(n))
			}
		}
	}
}
