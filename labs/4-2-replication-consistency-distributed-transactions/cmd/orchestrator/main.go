// orchestrator exposes POST /place-order?mode=saga|2pc and routes to
// the right strategy across payment-svc, inventory-svc, shipping-svc.
//
// In saga mode each participant commits locally (with an outbox event
// in the same transaction) and the orchestrator runs forward,
// compensating in reverse on failure. In 2pc mode the orchestrator
// coordinates PREPARE/COMMIT|ABORT across all three. Both report the
// same shape of summary so the bench scripts can compare them.
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
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hlsa2-labs/lab4-2/internal/metrics"
	"github.com/hlsa2-labs/lab4-2/internal/payloads"
	"github.com/hlsa2-labs/lab4-2/internal/saga"
	"github.com/hlsa2-labs/lab4-2/internal/svchelp"
	"github.com/hlsa2-labs/lab4-2/internal/twopc"
)

func main() {
	port := svchelp.EnvOrDefault("PORT", "8080")
	paymentURL := svchelp.EnvOrDefault("PAYMENT_URL", "http://payment-svc:8081")
	inventoryURL := svchelp.EnvOrDefault("INVENTORY_URL", "http://inventory-svc:8082")
	shippingURL := svchelp.EnvOrDefault("SHIPPING_URL", "http://shipping-svc:8083")

	hc := &http.Client{Timeout: 30 * time.Second}
	mx := newOrchestratorMetrics()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Handle("/metrics", metrics.Handler())

	r.Post("/place-order", placeOrderHandler(hc, paymentURL, inventoryURL, shippingURL, mx))

	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("orchestrator listening on :%s", port)
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

func placeOrderHandler(hc *http.Client, paymentURL, inventoryURL, shippingURL string, mx *orchestratorMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req payloads.PlaceOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.OrderID == "" {
			req.OrderID = uuid.NewString()
		}
		mode := strings.ToLower(r.URL.Query().Get("mode"))
		if mode == "" {
			mode = "saga"
		}

		ctx := r.Context()
		var resp payloads.PlaceOrderResponse
		resp.OrderID = req.OrderID
		resp.Mode = mode

		switch mode {
		case "saga":
			result := runSaga(ctx, hc, req, paymentURL, inventoryURL, shippingURL)
			resp.Latency = result.Latency.Milliseconds()
			if result.OK {
				resp.Status = "completed"
				mx.success.WithLabelValues(mode).Inc()
			} else {
				resp.Status = "failed"
				resp.FailedAt = result.FailedAt
				resp.Compensated = result.Compensated
				mx.failed.WithLabelValues(mode).Inc()
			}
			mx.latency.WithLabelValues(mode).Observe(result.Latency.Seconds())
		case "2pc":
			result := run2PC(ctx, hc, req, paymentURL, inventoryURL, shippingURL)
			resp.Latency = result.Latency.Milliseconds()
			if result.OK {
				resp.Status = "completed"
				mx.success.WithLabelValues(mode).Inc()
			} else {
				resp.Status = "failed"
				resp.FailedAt = result.FailedAt
				mx.failed.WithLabelValues(mode).Inc()
			}
			mx.latency.WithLabelValues(mode).Observe(result.Latency.Seconds())
		default:
			http.Error(w, "mode must be saga|2pc", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// ----------------------------------------------------------------
// saga path
// ----------------------------------------------------------------

func runSaga(ctx context.Context, hc *http.Client, req payloads.PlaceOrderRequest, paymentURL, inventoryURL, shippingURL string) saga.Result {
	steps := []saga.Step{
		{
			Name:    "payment.charge",
			Forward: func(ctx context.Context) error {
				return postExpectOK(ctx, hc, paymentURL+"/charge", payloads.ChargeRequest{
					OrderID: req.OrderID, UserID: req.UserID, Amount: req.Amount,
				})
			},
			Compensate: func(ctx context.Context) error {
				return postExpectOK(ctx, hc, paymentURL+"/refund", payloads.CompensationRequest{OrderID: req.OrderID, Reason: "saga compensation"})
			},
		},
		{
			Name:    "inventory.reserve",
			Forward: func(ctx context.Context) error {
				return postExpectOK(ctx, hc, inventoryURL+"/reserve", payloads.ReserveRequest{
					OrderID: req.OrderID, SKU: req.SKU, Quantity: req.Quantity,
				})
			},
			Compensate: func(ctx context.Context) error {
				return postExpectOK(ctx, hc, inventoryURL+"/release", payloads.CompensationRequest{OrderID: req.OrderID, Reason: "saga compensation"})
			},
		},
		{
			Name:    "shipping.schedule",
			Forward: func(ctx context.Context) error {
				return postExpectOK(ctx, hc, shippingURL+"/schedule", payloads.ScheduleShippingRequest{
					OrderID: req.OrderID, Address: req.Address,
				})
			},
			Compensate: func(ctx context.Context) error {
				return postExpectOK(ctx, hc, shippingURL+"/cancel", payloads.CompensationRequest{OrderID: req.OrderID, Reason: "saga compensation"})
			},
		},
	}
	return saga.Run(ctx, steps, 1, 5)
}

// ----------------------------------------------------------------
// 2PC path
// ----------------------------------------------------------------

func run2PC(ctx context.Context, hc *http.Client, req payloads.PlaceOrderRequest, paymentURL, inventoryURL, shippingURL string) twopc.Result {
	gid := "lab42-" + uuid.NewString()
	parts := []twopc.PerParticipant{
		{
			P:       twopc.Participant{Name: "payment", URL: paymentURL, Client: hc},
			Payload: twopc.PreparePayload{OrderID: req.OrderID, GID: gid, UserID: req.UserID, Amount: req.Amount},
		},
		{
			P:       twopc.Participant{Name: "inventory", URL: inventoryURL, Client: hc},
			Payload: twopc.PreparePayload{OrderID: req.OrderID, GID: gid, SKU: req.SKU, Quantity: req.Quantity},
		},
		{
			P:       twopc.Participant{Name: "shipping", URL: shippingURL, Client: hc},
			Payload: twopc.PreparePayload{OrderID: req.OrderID, GID: gid, Address: req.Address},
		},
	}
	// 10s prepare/commit timeouts: large enough that lock queueing
	// under healthy load doesn't time out, small enough that an
	// injected fault on a participant still surfaces as in-doubt.
	return twopc.Run(ctx, gid, parts, 10*time.Second, 10*time.Second)
}

// ----------------------------------------------------------------
// helpers
// ----------------------------------------------------------------

func postExpectOK(ctx context.Context, hc *http.Client, url string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post %s: %d: %s", url, resp.StatusCode, string(raw))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

type orchestratorMetrics struct {
	success *prometheus.CounterVec
	failed  *prometheus.CounterVec
	latency *prometheus.HistogramVec
}

func newOrchestratorMetrics() *orchestratorMetrics {
	m := &orchestratorMetrics{
		success: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "place_order_success_total", Help: "place-order requests that completed.",
		}, []string{"mode"}),
		failed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "place_order_failed_total", Help: "place-order requests that failed.",
		}, []string{"mode"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "place_order_latency_seconds",
			Help:    "place-order end-to-end latency.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
		}, []string{"mode"}),
	}
	metrics.MustRegister(m.success, m.failed, m.latency)
	return m
}
