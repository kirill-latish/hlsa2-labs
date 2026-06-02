// payment-svc is the saga + 2PC participant for the "charge user
// account" step. It owns its own Postgres (payment-pg).
//
// Saga endpoints (commit-locally + emit outbox event):
//
//	POST /charge      forward step
//	POST /refund      compensation
//
// 2PC endpoints (XA-style, hold the lock until orchestrator decides):
//
//	POST /xa/prepare
//	POST /xa/commit
//	POST /xa/abort
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

	"github.com/hlsa2-labs/lab4-2/internal/fault"
	"github.com/hlsa2-labs/lab4-2/internal/metrics"
	"github.com/hlsa2-labs/lab4-2/internal/outbox"
	"github.com/hlsa2-labs/lab4-2/internal/payloads"
	"github.com/hlsa2-labs/lab4-2/internal/svchelp"

	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "8081")
	dsn := svchelp.EnvOrDefault("DATABASE_URL", "postgres://payment:payment@payment-pg:5432/payment?sslmode=disable")
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
	mx := newSagaMetrics("payment")
	twopcMx := newTwopcMetrics("payment")

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	r.Post("/charge", chargeHandler(pool, fc, mx))
	r.Post("/refund", refundHandler(pool, fc, mx))

	r.Post("/xa/prepare", xaPrepareHandler(pool, fc, twopcMx))
	r.Post("/xa/commit", xaCommitHandler(pool, twopcMx))
	r.Post("/xa/abort", xaAbortHandler(pool, twopcMx))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Background: emit lock-hold gauge for in-doubt 2PC rows.
	go reportInDoubt(context.Background(), pool, twopcMx)

	go func() {
		log.Printf("payment-svc listening on :%s db=%s", port, dsn)
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

// ----------------------------------------------------------------
// Saga handlers
// ----------------------------------------------------------------

func chargeHandler(pool *pgxpool.Pool, fc *fault.Client, mx *sagaMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.ChargeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := svchelp.ApplyFault(fc, "payment"); err != nil {
			mx.failed.WithLabelValues("charge").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			mx.failed.WithLabelValues("charge").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Idempotent: if order already paid, return success.
		var existing string
		err = tx.QueryRow(ctx,
			`SELECT status FROM payments WHERE order_id = $1`, req.OrderID,
		).Scan(&existing)
		if err == nil && existing == "charged" {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "charged (idempotent)")
			return
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO payments (order_id, amount, status)
			 VALUES ($1, $2, 'charged')
			 ON CONFLICT (order_id) DO UPDATE SET status = 'charged', updated_at = now(), amount = EXCLUDED.amount`,
			req.OrderID, req.Amount,
		); err != nil {
			mx.failed.WithLabelValues("charge").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(ctx,
			`UPDATE accounts SET balance = balance - $1 WHERE user_id = $2`,
			req.Amount, req.UserID,
		); err != nil {
			mx.failed.WithLabelValues("charge").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := outbox.Insert(ctx, tx, outbox.Event{
			AggregateID: req.OrderID,
			Type:        "payment.charged",
			Payload: map[string]any{
				"order_id": req.OrderID,
				"user_id":  req.UserID,
				"amount":   req.Amount,
			},
		}); err != nil {
			mx.failed.WithLabelValues("charge").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			mx.failed.WithLabelValues("charge").Inc()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		mx.applied.WithLabelValues("charge").Inc()
		svchelp.WriteOK(w, "charged")
	}
}

func refundHandler(pool *pgxpool.Pool, _ *fault.Client, mx *sagaMetrics) http.HandlerFunc {
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

		// Idempotent: if already refunded, no-op.
		var status string
		var amount int64
		var userID string
		err = tx.QueryRow(ctx,
			`SELECT p.status, p.amount, COALESCE(e.payload->>'user_id', '')
			 FROM payments p
			 LEFT JOIN events_outbox e ON e.aggregate_id = p.order_id AND e.event_type = 'payment.charged'
			 WHERE p.order_id = $1
			 LIMIT 1`,
			req.OrderID,
		).Scan(&status, &amount, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "refunded (idempotent)")
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if status == "refunded" {
			_ = tx.Commit(ctx)
			svchelp.WriteOK(w, "refunded (idempotent)")
			return
		}

		if _, err := tx.Exec(ctx,
			`UPDATE payments SET status = 'refunded', updated_at = now() WHERE order_id = $1`,
			req.OrderID,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if userID != "" && amount > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE accounts SET balance = balance + $1 WHERE user_id = $2`,
				amount, userID,
			); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if _, err := outbox.Insert(ctx, tx, outbox.Event{
			AggregateID: req.OrderID,
			Type:        "payment.refunded",
			Payload: map[string]any{
				"order_id": req.OrderID,
				"reason":   req.Reason,
			},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mx.compensated.WithLabelValues("refund").Inc()
		svchelp.WriteOK(w, "refunded")
	}
}

// ----------------------------------------------------------------
// 2PC handlers
// ----------------------------------------------------------------

func xaPrepareHandler(pool *pgxpool.Pool, fc *fault.Client, mx *twopcMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.XAPrepareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			svchelp.WriteXA(w, http.StatusBadRequest, false, "", err.Error())
			return
		}
		if err := svchelp.ApplyFault(fc, "payment"); err != nil {
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "fault", err.Error())
			return
		}

		ctx := r.Context()
		// Each prepared tx needs its own connection because pgx can't
		// hold a tx open across PREPARE + return. Acquire one.
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
			`INSERT INTO payments (order_id, amount, status) VALUES ($1, $2, 'prepared')
			 ON CONFLICT (order_id) DO UPDATE SET status = 'prepared', amount = EXCLUDED.amount, updated_at = now()`,
			req.OrderID, req.Amount,
		); err != nil {
			_, _ = conn.Exec(ctx, "ROLLBACK")
			svchelp.WriteXA(w, http.StatusInternalServerError, false, "", err.Error())
			return
		}
		if _, err := conn.Exec(ctx,
			`UPDATE accounts SET balance = balance - $1 WHERE user_id = $2`,
			req.Amount, req.UserID,
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
		if _, err := pool.Exec(ctx,
			`INSERT INTO twopc_log (gid, order_id, state) VALUES ($1, $2, 'prepared')
			 ON CONFLICT (gid) DO UPDATE SET state = 'prepared', prepared_at = now()`,
			req.GID, req.OrderID,
		); err != nil {
			log.Printf("payment xa/prepare: twopc_log insert: %v", err)
		}
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
			`UPDATE twopc_log SET state = 'committed', finished_at = now() WHERE gid = $1`,
			req.GID)
		_, _ = pool.Exec(ctx,
			`UPDATE payments SET status = 'charged', updated_at = now() WHERE order_id IN
			   (SELECT order_id FROM twopc_log WHERE gid = $1)`,
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
			// Possibly already aborted, log but report success.
			log.Printf("payment xa/abort gid=%s: %v", req.GID, err)
		}
		_, _ = pool.Exec(ctx,
			`UPDATE twopc_log SET state = 'aborted', finished_at = now() WHERE gid = $1`,
			req.GID)
		mx.aborted.Inc()
		svchelp.WriteXA(w, http.StatusOK, true, "aborted", "")
	}
}

// ----------------------------------------------------------------
// Helpers / metrics
// ----------------------------------------------------------------

type sagaMetrics struct {
	applied     *prometheus.CounterVec
	compensated *prometheus.CounterVec
	failed      *prometheus.CounterVec
}

func newSagaMetrics(svc string) *sagaMetrics {
	m := &sagaMetrics{
		applied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "saga_step_applied_total",
			Help:        "Saga forward steps applied successfully.",
			ConstLabels: prometheus.Labels{"service": svc},
		}, []string{"step"}),
		compensated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "saga_step_compensated_total",
			Help:        "Saga compensation steps applied successfully.",
			ConstLabels: prometheus.Labels{"service": svc},
		}, []string{"step"}),
		failed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "saga_step_failed_total",
			Help:        "Saga steps that failed to apply.",
			ConstLabels: prometheus.Labels{"service": svc},
		}, []string{"step"}),
	}
	metrics.MustRegister(m.applied, m.compensated, m.failed)
	return m
}

type twopcMetrics struct {
	prepared  prometheus.Counter
	committed prometheus.Counter
	aborted   prometheus.Counter
	inDoubt   prometheus.Gauge
}

func newTwopcMetrics(svc string) *twopcMetrics {
	common := prometheus.Labels{"service": svc}
	m := &twopcMetrics{
		prepared: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "twopc_prepared_total", Help: "PREPARE TRANSACTION calls.", ConstLabels: common,
		}),
		committed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "twopc_committed_total", Help: "COMMIT PREPARED calls.", ConstLabels: common,
		}),
		aborted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "twopc_aborted_total", Help: "ROLLBACK PREPARED calls.", ConstLabels: common,
		}),
		inDoubt: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "twopc_in_doubt_count", Help: "Currently prepared but uncommitted XA transactions.", ConstLabels: common,
		}),
	}
	metrics.MustRegister(m.prepared, m.committed, m.aborted, m.inDoubt)
	return m
}

// reportInDoubt every 1s reads pg_prepared_xacts and updates the gauge.
func reportInDoubt(ctx context.Context, pool *pgxpool.Pool, mx *twopcMetrics) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var n int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_prepared_xacts").Scan(&n)
			if err == nil {
				mx.inDoubt.Set(float64(n))
			}
		}
	}
}

