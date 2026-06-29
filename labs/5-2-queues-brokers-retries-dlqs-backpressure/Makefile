# HLSA2 lab 5-2 - Queues, Brokers, Retries, DLQs, and Backpressure
#
# One-line reproducibility from a clean checkout:
#   make up && make seed && make env-fingerprint && make bench-baseline
#
# Every target maps to a step in the topic-251 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via
#   make bench-baseline RUNS=3 DURATION=5m LABEL=baseline
RUNS            ?= 3
LABEL           ?= baseline
DURATION        ?= 5m
RATE            ?= 240
COUNT           ?= 1
TRANSIENT_RATE  ?= 10pct
PERMANENT_RATE  ?= 2pct
CANDIDATE       ?=
MAX_RETRIES     ?= 5
BEFORE          ?=
AFTER           ?=

.PHONY: help up down stop clean logs ps seed env-fingerprint \
        brokers-status consumer-status \
        bench-baseline analyze-baseline \
        inject-poison analyze-poison clear-poison \
        apply-fix compare-poison \
        bench-faults analyze-faults \
        bench-backpressure analyze-backpressure \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (producer, brokers, 3 consumers, postgres, loadgen, observability)
	docker compose up -d --build
	@echo
	@echo "Waiting for healthchecks..."
	@sleep 20
	@docker compose ps

down: ## Stop and remove containers (preserves named volumes)
	docker compose down

stop: down ## Alias for `make down`

clean: ## Stop AND remove named volumes - destroys broker/Prometheus/Grafana state
	docker compose down -v

logs: ## Tail logs from all services
	docker compose logs -f --tail=50

ps: ## Show service status
	docker compose ps

seed: ## Declare broker topology (queues/exchanges/DLX + Redpanda topic) and create the Postgres table
	bash scripts/seed.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture broker versions, consumer count, limits to perf/results/env.txt + meta.json
	bash perf/env.sh

# --- Health (step 1) -------------------------------------------------

brokers-status: ## Check RabbitMQ (mgmt API) and Redpanda (rpk) health
	bash scripts/brokers-status.sh

consumer-status: ## Show the 3 consumer health + state endpoints
	bash scripts/consumer-status.sh

# --- Step 3: healthy baseline ----------------------------------------

bench-baseline: ## RUNS=3 DURATION=5m LABEL=baseline - moderate sustained load
	RUNS=$(RUNS) LABEL=$(LABEL) DURATION=$(DURATION) RATE=$(RATE) bash scripts/run-bench-baseline.sh

analyze-baseline: ## Lag (count+time), throughput, p50/p99, DLQ with run-to-run sigma
	python3 scripts/analyze-baseline.py perf/results/$(LABEL)

# --- Step 4-5: poison message + bounded-retry fix --------------------

inject-poison: ## COUNT=1 LABEL=poison DURATION=3m - inject poison message(s) and capture the run
	COUNT=$(COUNT) LABEL=$(LABEL) DURATION=$(DURATION) RATE=$(RATE) bash scripts/inject-poison.sh

analyze-poison: ## LABEL=poison-baseline|poison-after - retry rate, throughput collapse/recovery, DLQ
	python3 scripts/analyze-poison.py perf/results/$(LABEL)

clear-poison: ## Purge work + dead-letter queues and reset producer poison knob
	bash scripts/clear-poison.sh

apply-fix: ## CANDIDATE=bounded-retry|classify-failures|backpressure-signal MAX_RETRIES=5 - flip consumer semantics live
	CANDIDATE=$(CANDIDATE) MAX_RETRIES=$(MAX_RETRIES) bash scripts/apply-fix.sh

compare-poison: ## BEFORE=poison-baseline AFTER=poison-after - side-by-side before/after the fix
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare-poison.sh

# --- Step 6: retry/DLQ under fault injection -------------------------

bench-faults: ## TRANSIENT_RATE=10pct PERMANENT_RATE=2pct DURATION=5m LABEL=faults
	TRANSIENT_RATE=$(TRANSIENT_RATE) PERMANENT_RATE=$(PERMANENT_RATE) DURATION=$(DURATION) \
	  LABEL=$(LABEL) RATE=$(RATE) bash scripts/run-bench-faults.sh

analyze-faults: ## LABEL=faults - retry rate vs transient, DLQ rate vs permanent, p99
	python3 scripts/analyze-faults.py perf/results/$(LABEL)

# --- Step 7: backpressure under sustained overload -------------------

bench-backpressure: ## RATE=2x DURATION=10m LABEL=backpressure - sustained overload
	RATE=$(RATE) DURATION=$(DURATION) LABEL=$(LABEL) bash scripts/run-bench-backpressure.sh

analyze-backpressure: ## LABEL=backpressure - lag stabilized vs unbounded growth, producer error rate
	python3 scripts/analyze-backpressure.py perf/results/$(LABEL)

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
