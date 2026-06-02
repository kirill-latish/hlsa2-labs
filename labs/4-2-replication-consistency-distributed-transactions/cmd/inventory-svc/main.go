// inventory-svc is the saga + 2PC participant for "reserve stock".
// Saga: POST /reserve, POST /release. 2PC: /xa/prepare|commit|abort.
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
	port := svchelp.EnvOrDefault("PORT", "8082")
	dsn := svchelp.EnvOrDefault("DATABASE_URL", "postgres://inventory:inventory@inventory-pg:5432/inventory?sslmode=disable")
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

	r.Post("/reserve", reserveHandler(pool, fc, mx))
	r.Post("/release", releaseHandler(pool, mx))
	r.Post("/xa/prepare", xaPrepareHandler(pool, fc, tm))
	r.Post("/xa/commit", xaCommitHandler(pool, tm))
	r.Post("/xa/abort", xaAbortHandler(pool, tm))

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go reportInDoubt(context.Background(), pool, tm)

	go func() {
		log.Printf("inventory-svc listening on :%s db=%s", port, dsn)
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

func reserveHandler(pool *pgxpool.Pool, fc *fault.Client, mx *sagaMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.ReserveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := svchelp.ApplyFault(fc, "inventory"); err != nil {
			mx.failed.WithLabelValues("reserve").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ctx := r.Context()

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			mx.failed.WithLabelValues("reserve").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Idempotent: if reservation already exists, do nothing.
		var existing string
		err = tx.QueryRow(ctx, `SELECT status FROM reservations WHERE order_id = $1`, req.OrderID).Scan(&existing)
		if err == nil && existing == "reserved" {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "reserved (idempotent)")
			return
		}

		if _, err := tx.Exec(ctx,
			`UPDATE stock SET available = available - $1, reserved = reserved + $1 WHERE sku = $2`,
			req.Quantity, req.SKU,
		); err != nil {
			mx.failed.WithLabelValues("reserve").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO reservations (order_id, sku, quantity, status) VALUES ($1, $2, $3, 'reserved')
			 ON CONFLICT (order_id) DO UPDATE SET sku = EXCLUDED.sku, quantity = EXCLUDED.quantity, status = 'reserved'`,
			req.OrderID, req.SKU, req.Quantity,
		); err != nil {
			mx.failed.WithLabelValues("reserve").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := outbox.Insert(ctx, tx, outbox.Event{
			AggregateID: req.OrderID,
			Type:        "inventory.reserved",
			Payload: map[string]any{"order_id": req.OrderID, "sku": req.SKU, "quantity": req.Quantity},
		}); err != nil {
			mx.failed.WithLabelValues("reserve").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			mx.failed.WithLabelValues("reserve").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mx.applied.WithLabelValues("reserve").Inc()
		svchelp.WriteOK(w, "reserved")
	}
}

func releaseHandler(pool *pgxpool.Pool, mx *sagaMetrics) http.HandlerFunc {
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

		var status, sku string
		var qty int
		err = tx.QueryRow(ctx,
			`SELECT status, sku, quantity FROM reservations WHERE order_id = $1`, req.OrderID,
		).Scan(&status, &sku, &qty)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "released (idempotent)")
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if status == "released" {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "released (idempotent)")
			return
		}

		if _, err := tx.Exec(ctx,
			`UPDATE reservations SET status = 'released' WHERE order_id = $1`, req.OrderID,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(ctx,
			`UPDATE stock SET available = available + $1, reserved = reserved - $1 WHERE sku = $2`,
			qty, sku,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := outbox.Insert(ctx, tx, outbox.Event{
			AggregateID: req.OrderID,
			Type:        "inventory.released",
			Payload: map[string]any{"order_id": req.OrderID, "sku": sku, "quantity": qty, "reason": req.Reason},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mx.compensated.WithLabelValues("release").Inc()
		svchelp.WriteOK(w, "released")
	}
}

// ----- 2PC handlers -----

func xaPrepareHandler(pool *pgxpool.Pool, fc *fault.Client, mx *twopcMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.XAPrepareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			svchelp.WriteXA(w, http.StatusBadRequest, false, "", err.Error())
			return
		}
		if err := svchelp.ApplyFault(fc, "inventory"); err != nil {
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
			`UPDATE stock SET available = available - $1, reserved = reserved + $1 WHERE sku = $2`,
			req.Quantity, req.SKU,
		); err != nil {
			_, _ = conn.Exec(ctx, "ROLLBACK")
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO reservations (order_id, sku, quantity, status) VALUES ($1, $2, $3, 'prepared')
			 ON CONFLICT (order_id) DO UPDATE SET sku = EXCLUDED.sku, quantity = EXCLUDED.quantity, status = 'prepared'`,
			req.OrderID, req.SKU, req.Quantity,
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
			`UPDATE reservations SET status = 'reserved' WHERE order_id IN (SELECT order_id FROM twopc_log WHERE gid = $1)`,
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
			log.Printf("inventory xa/abort gid=%s: %v", req.GID, err)
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
	common := prometheus.Labels{"service": "inventory"}
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
	common := prometheus.Labels{"service": "inventory"}
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
