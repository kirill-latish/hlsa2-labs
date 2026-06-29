# HLSA2 lab 5-3 - Event Streaming: Ordering, Idempotency, Outbox, Replay
#
# One-line reproducibility from a clean checkout:
#   cp .env.example .env && make up && make seed && make env-fingerprint
#   make bench-baseline RUNS=3 DURATION=5m
#
# Every target maps to a step in the topic-252 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via e.g.
#   make bench-baseline RUNS=3 DURATION=5m LABEL=baseline
#   make bench-ordering KEY=wrong DURATION=3m LABEL=ordering-wrong
#   make apply-fix CANDIDATE=idempotent-consumer
RUNS       ?= 3
DURATION   ?= 5m
LABEL      ?= baseline
KEY        ?= wrong
RATE       ?=
CANDIDATE  ?=
BEFORE     ?=
AFTER      ?=
FROM       ?= earliest
MODE       ?= rebuild-only

.PHONY: help up down stop clean logs ps seed env-fingerprint topic-status relay-status \
        bench-baseline analyze-baseline \
        apply-fix inject-duplicates crash-consumer-midbatch verify-exactly-once compare-idempotency \
        bench-ordering compare-ordering \
        inject-crash-between-writes analyze-consistency compare-consistency \
        corrupt-projection replay-rebuild analyze-replay verify-no-side-effects \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-26s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (producer, redpanda, consumer-1..3, outbox-relay, observability)
	docker compose up -d --build
	@echo
	@echo "Waiting for healthchecks..."
	@sleep 15
	@docker compose ps

down: ## Stop and remove containers (preserves named volumes)
	docker compose down

stop: down ## Alias for `make down`

clean: ## Stop AND remove named volumes - destroys Postgres + Redpanda + Grafana state
	docker compose down -v

logs: ## Tail logs from all services
	docker compose logs -f --tail=50

ps: ## Show service status
	docker compose ps

seed: ## Create the topic (with partition count) and verify the DB schema
	bash scripts/seed.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture broker/partition/consumer/postgres/RTT fingerprint to perf/results/
	bash perf/env.sh

topic-status: ## Show the topic, partitions, and consumer-group offsets/lag via rpk
	bash scripts/topic-status.sh

relay-status: ## Health + backlog of the Go outbox-relay (this lab's Debezium stand-in)
	bash scripts/relay-status.sh

# --- Step 3: baseline pipeline + duplicate rate ----------------------

bench-baseline: ## RUNS=3 DURATION=5m LABEL=baseline - throughput, lag count+time, duplicate rate
	RUNS=$(RUNS) DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) bash scripts/run-bench-baseline.sh

analyze-baseline: ## Aggregate baseline runs: median + sigma of throughput / lag / duplicate rate
	python3 scripts/analyze-baseline.py perf/results/$(LABEL)

# --- Step 4: idempotency under duplicates + crash --------------------

apply-fix: ## CANDIDATE=naive-consumer|idempotent-consumer|naive-dualwrite|outbox - flip a runtime mode
	CANDIDATE=$(CANDIDATE) bash scripts/apply-fix.sh

inject-duplicates: ## RATE=20pct - set duplicate injection and drive a burst
	RATE=$(RATE) bash scripts/inject-duplicates.sh

crash-consumer-midbatch: ## Crash consumers mid-batch (uncommitted tail is redelivered)
	bash scripts/crash-consumer-midbatch.sh

verify-exactly-once: ## LABEL=<...> - side effects vs unique events -> result.json
	LABEL=$(LABEL) bash scripts/verify-exactly-once.sh

compare-idempotency: ## BEFORE=idempotency-naive AFTER=idempotency-after
	python3 scripts/compare-idempotency.py $(BEFORE) $(AFTER)

# --- Step 5: ordering under wrong vs correct partition key -----------

bench-ordering: ## KEY=wrong|entity DURATION=3m LABEL=<...> - measure ordering-violation rate
	KEY=$(KEY) DURATION=$(DURATION) LABEL=$(LABEL) bash scripts/bench-ordering.sh

compare-ordering: ## BEFORE=ordering-wrong AFTER=ordering-entity
	python3 scripts/compare-ordering.py $(BEFORE) $(AFTER)

# --- Step 6: dual-write inconsistency + outbox fix -------------------

inject-crash-between-writes: ## LABEL=<...> - crash after DB commit, before publish
	LABEL=$(LABEL) bash scripts/inject-crash-between-writes.sh

analyze-consistency: ## LABEL=<...> - count orphaned state changes -> result.json
	LABEL=$(LABEL) bash scripts/analyze-consistency.sh

compare-consistency: ## BEFORE=dualwrite-naive AFTER=dualwrite-outbox
	python3 scripts/compare-consistency.py $(BEFORE) $(AFTER)

# --- Step 7: replay to rebuild without re-firing side effects --------

corrupt-projection: ## Snapshot the projection baseline, then corrupt the read model
	bash scripts/corrupt-projection.sh

replay-rebuild: ## FROM=earliest MODE=rebuild-only|reprocess LABEL=replay - rewind + rebuild
	FROM=$(FROM) MODE=$(MODE) LABEL=$(LABEL) bash scripts/replay-rebuild.sh

analyze-replay: ## LABEL=replay - confirm projection rebuilt + report recovery time
	LABEL=$(LABEL) bash scripts/analyze-replay.sh

verify-no-side-effects: ## LABEL=replay - prove rebuild-only did NOT re-fire side effects
	LABEL=$(LABEL) bash scripts/verify-no-side-effects.sh

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
