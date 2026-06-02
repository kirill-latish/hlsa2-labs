# HLSA2 lab 4-2 — Replication, Consistency, and Distributed Transactions
#
# One-line reproducibility from a clean checkout:
#   make up && make seed && make topics && make env-fingerprint
#
# Every target maps to a step in the topic-248 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

LAB_ROOT := $(dir $(realpath $(firstword $(MAKEFILE_LIST))))
.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via
#   make bench-lag RUNS=5 DURATION=10m
RUNS              ?= 3
LABEL             ?= baseline
DURATION          ?= 5m
WRITE_RATE        ?= 500
RAW_RATE          ?= 200
SAGA_RATE         ?= 50
WARMUP_S          ?= 10
MODE              ?= naive
CONSUMER_MODE     ?= idempotent
WINDOW            ?= 24h
SERVICE           ?= inventory
P99_MS            ?= 2000
ERROR_RATE        ?= 0.0
CANDIDATE         ?= lsn-wait-on-raw
BEFORE            ?=
AFTER             ?=

.PHONY: help up down stop logs ps clean seed topics \
        env-fingerprint \
        bench-lag analyze-lag \
        bench-raw compare-raw \
        bench-2pc bench-saga compare-2pc-saga \
        inject-fault clear-fault \
        seed-events replay assert-idempotent \
        regression analyze \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (primary + 2 replicas + 3 service DBs + Redpanda + services + observability)
	docker compose up -d --build
	@echo
	@echo "Waiting for healthchecks (replicas need ~20s for pg_basebackup)..."
	@sleep 20
	@docker compose ps

down: ## Stop and remove containers (preserves named volumes)
	docker compose down

stop: down ## Alias for `make down`

clean: ## Stop AND remove named volumes - destroys Postgres + Prometheus state
	docker compose down -v

logs: ## Tail logs from all services
	docker compose logs -f --tail=50

ps: ## Show service status
	docker compose ps

seed: ## Seed primary + per-service DBs with a baseline row count
	bash scripts/seed.sh

topics: ## Create Redpanda topics (events, dlq) - idempotent
	bash scripts/topics.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture host + container fingerprint to perf/results/env.txt and meta.json
	bash perf/env.sh

# --- Step 3: replication-lag distribution under load -----------------

bench-lag: ## RUNS=3 DURATION=5m. Drives writes and samples lag every 100ms
	RUNS=$(RUNS) DURATION=$(DURATION) WRITE_RATE=$(WRITE_RATE) WARMUP_S=$(WARMUP_S) \
	  bash scripts/run-bench-lag.sh

analyze-lag: ## p50/p95/p99/p99.9 across runs and run-to-run sigma
	python3 scripts/analyze-lag.py perf/results/lag

# --- Step 4: read-after-write -----------------------------------------

bench-raw: ## MODE=naive|session-pin|lsn-wait|primary-read
	MODE=$(MODE) RATE=$(RAW_RATE) DURATION=$(DURATION) WARMUP_S=$(WARMUP_S) \
	  bash scripts/run-bench-raw.sh

compare-raw: ## BEFORE=<mode> AFTER=<mode> - side-by-side violation rate
	BEFORE=$(BEFORE) AFTER=$(AFTER) python3 scripts/compare-raw.py

# --- Step 5: 2PC vs saga under fault ---------------------------------

bench-2pc: ## RUNS=3 LABEL=healthy|faulted - place-order via 2PC
	RUNS=$(RUNS) LABEL=$(LABEL) RATE=$(SAGA_RATE) DURATION=$(DURATION) WARMUP_S=$(WARMUP_S) \
	  MODE=2pc bash scripts/run-bench-2pc.sh

bench-saga: ## RUNS=3 LABEL=healthy|faulted - place-order via saga + outbox
	RUNS=$(RUNS) LABEL=$(LABEL) RATE=$(SAGA_RATE) DURATION=$(DURATION) WARMUP_S=$(WARMUP_S) \
	  MODE=saga bash scripts/run-bench-saga.sh

inject-fault: ## SERVICE=payment|inventory|shipping MODE=latency|fail [P99_MS=2000 ERROR_RATE=0]
	SERVICE=$(SERVICE) MODE=$(MODE) P99_MS=$(P99_MS) ERROR_RATE=$(ERROR_RATE) \
	  bash scripts/inject-fault.sh

clear-fault: ## SERVICE=payment|inventory|shipping
	SERVICE=$(SERVICE) bash scripts/clear-fault.sh

compare-2pc-saga: ## Side-by-side success rate, p99, lock-hold for healthy vs faulted
	python3 scripts/compare-2pc-saga.py

# --- Step 6: saga idempotency proof ----------------------------------

seed-events: ## WINDOW=24h - synthetic event window, partitioned by order_id
	WINDOW=$(WINDOW) bash scripts/seed-events.sh

replay: ## WINDOW=24h CONSUMER_MODE=idempotent|naive - reset offsets and re-process
	WINDOW=$(WINDOW) CONSUMER_MODE=$(CONSUMER_MODE) bash scripts/replay.sh

assert-idempotent: ## CONSUMER_MODE=idempotent|naive - replay twice, hash final state
	CONSUMER_MODE=$(CONSUMER_MODE) python3 scripts/assert-idempotent.py

# --- Step 7: targeted improvement + 2-sigma decision -----------------

regression: ## CANDIDATE=lsn-wait-on-raw|outbox-cdc|replace-2pc-with-saga RUNS=3
	CANDIDATE=$(CANDIDATE) RUNS=$(RUNS) DURATION=$(DURATION) bash scripts/regression.sh

analyze: ## CANDIDATE=<name> - median/sigma/delta + 2-sigma decision
	python3 scripts/analyze-regression.py perf/results/regression $(CANDIDATE)

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
