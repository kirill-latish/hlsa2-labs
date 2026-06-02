// seed-events writes a deterministic stream of synthetic saga events
// directly to Redpanda. The window is partitioned by order_id so the
// per-order ordering invariant the consumer relies on holds.
//
// The event stream is intentionally repeatable: same seed -> same
// stream. That's what makes assert-idempotent meaningful: the second
// replay sees byte-identical input, so any state hash drift is
// purely from the consumer's own non-idempotency.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
)

func main() {
	brokers := flag.String("brokers", "redpanda:9092", "comma-separated broker list")
	topic := flag.String("topic", "events", "topic to produce to")
	window := flag.Duration("window", 24*time.Hour, "logical window size (controls event count)")
	rate := flag.Int("rate", 50, "synthetic events-per-second of the simulated window")
	orders := flag.Int("orders", 200, "number of distinct order_ids to spread events across")
	seed := flag.Int64("seed", 1, "deterministic RNG seed - keep stable across replays")
	users := flag.Int("users", 50, "number of distinct user_ids to spread balances across")
	flag.Parse()

	rng := rand.New(rand.NewSource(*seed))

	cli, err := kgo.NewClient(
		kgo.SeedBrokers(*brokers),
		kgo.ProducerLinger(20*time.Millisecond),
	)
	if err != nil {
		log.Fatalf("kafka client: %v", err)
	}
	defer cli.Close()
	for i := 0; i < 60; i++ {
		if err := cli.Ping(context.Background()); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	totalEvents := int(window.Seconds()) * (*rate)
	if totalEvents <= 0 {
		log.Fatalf("seed-events: nothing to do (window=%s rate=%d)", *window, *rate)
	}
	log.Printf("seed-events: producing %d events to %s topic=%s seed=%d", totalEvents, *brokers, *topic, *seed)

	// Pre-generate order_ids deterministically from the seed.
	orderIDs := make([]string, *orders)
	for i := range orderIDs {
		orderIDs[i] = fmt.Sprintf("seed-order-%05d", i)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var produced int64
	for i := 0; i < totalEvents; i++ {
		oid := orderIDs[rng.Intn(*orders)]
		uid := fmt.Sprintf("user-%d", (rng.Intn(*users))+1)
		amount := int64(1 + rng.Intn(100))
		evID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("%s|%d", oid, i))).String()

		body := map[string]any{
			"order_id":    oid,
			"user_id":     uid,
			"amount":      amount,
			"occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		}
		raw, _ := json.Marshal(body)
		rec := &kgo.Record{
			Topic: *topic,
			Key:   []byte(oid),
			Value: raw,
			Headers: []kgo.RecordHeader{
				{Key: "event_id", Value: []byte(evID)},
				{Key: "event_type", Value: []byte("payment.charged")},
				{Key: "source", Value: []byte("seed")},
			},
		}
		cli.Produce(ctx, rec, func(_ *kgo.Record, err error) {
			if err == nil {
				atomic.AddInt64(&produced, 1)
			}
		})
	}

	if err := cli.Flush(ctx); err != nil {
		log.Fatalf("flush: %v", err)
	}
	log.Printf("seed-events: produced=%d", atomic.LoadInt64(&produced))
}
