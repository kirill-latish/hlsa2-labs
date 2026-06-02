// Package payloads is the canonical wire format for every saga + 2PC
// hop. Keeping it in one place stops the orchestrator and the three
// participants from drifting apart.
package payloads

// PlaceOrderRequest is the public API of the orchestrator.
type PlaceOrderRequest struct {
	OrderID  string `json:"order_id"`
	UserID   string `json:"user_id"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Amount   int64  `json:"amount"`
	Address  string `json:"address"`
}

// PlaceOrderResponse is what the orchestrator returns to the caller.
// Mode echoes which path served the request so the loadgen can label
// stats correctly.
type PlaceOrderResponse struct {
	OrderID    string `json:"order_id"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	Latency    int64  `json:"latency_ms"`
	FailedAt   string `json:"failed_at,omitempty"`
	Compensated bool   `json:"compensated,omitempty"`
}

// ChargeRequest is sent to payment-svc step 1.
type ChargeRequest struct {
	OrderID string `json:"order_id"`
	UserID  string `json:"user_id"`
	Amount  int64  `json:"amount"`
}

// ReserveRequest is sent to inventory-svc step 2.
type ReserveRequest struct {
	OrderID  string `json:"order_id"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// ScheduleShippingRequest is sent to shipping-svc step 3.
type ScheduleShippingRequest struct {
	OrderID string `json:"order_id"`
	Address string `json:"address"`
}

// CompensationRequest is the universal compensation payload (refund,
// release-stock, cancel-shipping). All three accept it under their own
// /compensate endpoint.
type CompensationRequest struct {
	OrderID string `json:"order_id"`
	Reason  string `json:"reason,omitempty"`
}

// XAPrepareRequest carries the GID a participant must associate with
// the prepared transaction.
type XAPrepareRequest struct {
	OrderID string `json:"order_id"`
	GID     string `json:"gid"`
	// Same business payload field as the saga step. The participant
	// applies the change inside a transaction and PREPAREs that tx.
	UserID   string `json:"user_id,omitempty"`
	Amount   int64  `json:"amount,omitempty"`
	SKU      string `json:"sku,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Address  string `json:"address,omitempty"`
}

// XACommitRequest tells a participant to COMMIT PREPARED its gid.
type XACommitRequest struct {
	GID string `json:"gid"`
}

// XAAbortRequest tells a participant to ROLLBACK PREPARED its gid.
type XAAbortRequest struct {
	GID string `json:"gid"`
}

// XAResponse is shared by prepare/commit/abort.
type XAResponse struct {
	OK    bool   `json:"ok"`
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// SagaEvent is the on-wire shape every outbox row maps to. The
// consumer keys idempotency on EventID.
type SagaEvent struct {
	EventID     string                 `json:"event_id"`
	OrderID     string                 `json:"order_id"`
	EventType   string                 `json:"event_type"`
	OccurredAt  string                 `json:"occurred_at"`
	Payload     map[string]any         `json:"payload"`
}
