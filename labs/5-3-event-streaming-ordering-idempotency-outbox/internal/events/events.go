// Package events is the single wire contract shared by the producer,
// the outbox-relay, and the consumer. Keeping it in one place means
// the partition-key strategy, the per-entity sequence number, and the
// dedup key (event_id) never drift between binaries.
package events

import (
	"encoding/json"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Event is one business event about an order. Seq is the per-entity
// (per order_id) monotonic sequence number used by the consumer to
// detect ordering violations. PaymentMethod is the deliberately-wrong
// partition key the ordering experiment routes on.
type Event struct {
	EventID       string `json:"event_id"`
	OrderID       string `json:"order_id"`
	PaymentMethod string `json:"payment_method"`
	Seq           int64  `json:"seq"`
	Type          string `json:"type"`
	Amount        int64  `json:"amount"`
}

// Marshal encodes the event body. Errors are impossible for this flat
// struct, so callers can ignore them.
func (e Event) Marshal() []byte {
	b, _ := json.Marshal(e)
	return b
}

// Unmarshal decodes an event body.
func Unmarshal(b []byte) (Event, error) {
	var e Event
	err := json.Unmarshal(b, &e)
	return e, err
}

// PartitionKey returns the Kafka record key for the configured
// strategy. "entity" keys by order_id (all of an order's events share
// a partition -> in-order). "wrong" keys by payment_method, which
// varies per event, so the same order scatters across partitions.
func (e Event) PartitionKey(strategy string) string {
	if strategy == "wrong" {
		return e.PaymentMethod
	}
	return e.OrderID
}

// ToRecord builds a kgo.Record carrying the event in the value plus
// event_id / event_type / seq headers so the consumer can dedup and
// trace without re-parsing the body.
func (e Event) ToRecord(topic, keyStrategy string) *kgo.Record {
	return &kgo.Record{
		Topic: topic,
		Key:   []byte(e.PartitionKey(keyStrategy)),
		Value: e.Marshal(),
		Headers: []kgo.RecordHeader{
			{Key: "event_id", Value: []byte(e.EventID)},
			{Key: "event_type", Value: []byte(e.Type)},
		},
	}
}

// FromRecord reconstructs an Event from a consumed record, preferring
// the JSON body and falling back to the event_id header.
func FromRecord(rec *kgo.Record) (Event, error) {
	e, err := Unmarshal(rec.Value)
	if err != nil {
		return e, err
	}
	if e.EventID == "" {
		for _, h := range rec.Headers {
			if h.Key == "event_id" {
				e.EventID = string(h.Value)
			}
		}
	}
	return e, nil
}
