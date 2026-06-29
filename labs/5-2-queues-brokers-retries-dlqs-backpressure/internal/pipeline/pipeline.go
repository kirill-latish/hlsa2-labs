// Package pipeline holds the shared async-pipeline vocabulary: the
// message envelope, the RabbitMQ topology names, and an idempotent
// topology-declare helper. The producer, consumer fleet, and the seed
// script all agree on these names so a message published by the
// producer lands in the same queue the consumer reads, and so a poison
// message dead-letters into the same DLQ everyone inspects.
package pipeline

import (
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQ topology. The work queue dead-letters to lab52.dlx and is
// bounded by x-max-length + x-overflow=reject-publish so the broker can
// exert backpressure on the producer under sustained overload.
const (
	WorkExchange   = "lab52.work"
	WorkQueue      = "lab52.work"
	WorkRoutingKey = "work"

	DLXExchange = "lab52.dlx"
	DLQQueue    = "lab52.dlq"

	// RedpandaTopic exists only for broker-family comparison; nothing
	// consumes it in this lab (the consumer fleet reads RabbitMQ).
	RedpandaTopic = "lab52.events"

	// HeaderRetryCount tracks how many times a message has been retried.
	HeaderRetryCount = "x-retry-count"
	// HeaderType mirrors Message.Type into an AMQP header so a consumer
	// can classify without unmarshalling the whole body.
	HeaderType = "x-msg-type"
)

// MessageType drives how the consumer treats a message. The producer
// stamps the type; the consumer simulates the corresponding downstream
// outcome. This is how fault injection is expressed end-to-end.
type MessageType string

const (
	// TypeNormal succeeds after a simulated downstream write.
	TypeNormal MessageType = "normal"
	// TypePoison has an unprocessable payload: it always fails and is
	// classified permanent. The canonical poison message.
	TypePoison MessageType = "poison"
	// TypeTransient fails the first few attempts, then succeeds -
	// models a flaky downstream (503 / timeout / lock conflict).
	TypeTransient MessageType = "transient"
	// TypePermanent always fails and is classified permanent (400 /
	// validation error / dangling reference).
	TypePermanent MessageType = "permanent"
)

// Message is the JSON envelope carried in the AMQP body.
type Message struct {
	ID         string      `json:"id"`
	Seq        int64       `json:"seq"`
	Type       MessageType `json:"type"`
	Label      string      `json:"label"`
	EnqueuedAt time.Time   `json:"enqueued_at"`
	Payload    string      `json:"payload"`
}

// Connect dials RabbitMQ with a bounded retry loop so services that
// start before the broker is ready don't crash-loop.
func Connect(url string, attempts int) (*amqp.Connection, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		conn, err := amqp.Dial(url)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return nil, lastErr
}

// DeclareTopology idempotently declares the work exchange/queue, the
// dead-letter exchange/queue, and the binding. Both the producer and
// the consumer call this on startup, and the seed script asserts the
// same shape via the management API; all three must pass identical
// arguments or RabbitMQ returns PRECONDITION_FAILED.
func DeclareTopology(ch *amqp.Channel, maxLen int) error {
	if err := ch.ExchangeDeclare(DLXExchange, "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(DLQQueue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(DLQQueue, "", DLXExchange, false, nil); err != nil {
		return err
	}

	if err := ch.ExchangeDeclare(WorkExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	args := amqp.Table{
		"x-dead-letter-exchange": DLXExchange,
		"x-max-length":           int32(maxLen),
		"x-overflow":             "reject-publish",
	}
	if _, err := ch.QueueDeclare(WorkQueue, true, false, false, false, args); err != nil {
		return err
	}
	return ch.QueueBind(WorkQueue, WorkRoutingKey, WorkExchange, false, nil)
}

// HeaderInt reads an integer-valued AMQP header, tolerating the several
// numeric types the wire format may use.
func HeaderInt(h amqp.Table, key string) int {
	if h == nil {
		return 0
	}
	switch v := h[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
