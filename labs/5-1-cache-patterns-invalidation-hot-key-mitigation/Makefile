# HLSA2 lab 5-1 - Cache Patterns, Invalidation, and Hot-Key Mitigation
#
# One-line reproducibility from a clean checkout:
#   make up && make seed && make env-fingerprint && make bench-baseline
#
# Every target maps to a step in the topic-250 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

LAB_ROOT := $(dir $(realpath $(firstword $(MAKEFILE_LIST))))
.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via
#   make bench-baseline DIST=zipf RUNS=3 DURATION=5m LABEL=baseline
DIST        ?= zipf
RUNS        ?= 3
DURATION    ?= 5m
LABEL       ?= baseline
RATE        ?= 2000

# Stampede knobs.
TTL         ?= 30s
HOT_RATE    ?= 5000

# apply-fix knobs.
CANDIDATE   ?=
JITTER      ?= 20pct
LOCAL_SIZE  ?= 1000
LOCAL_TTL   ?= 5s

# Hot-key knobs.
KEY         ?= celebrity-1
WEIGHT      ?= 0.4

# Staleness knobs.
STRATEGY    ?= ttl-only

# compare knobs.
BEFORE      ?=
AFTER       ?=

.PHONY: help up down stop clean logs ps seed env-fingerprint cluster-status \
        bench-baseline analyze-baseline \
        inject-stampede analyze-stampede clear-stampede apply-fix compare-stampede \
        inject-hot-key bench-hot-key compare-hot-key clear-hot-key \
        bench-staleness analyze-staleness \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (app, redis-1/2/3, postgres, loadgen, observability)
	docker compose up -d --build
	@echo
	@echo "Waiting for healthchecks..."
	@sleep 15
	@docker compose ps

down: ## Stop and remove containers (preserves named volumes)
	docker compose down

stop: down ## Alias for `make down`

clean: ## Stop AND remove named volumes - destroys Postgres + Prometheus + Grafana state
	docker compose down -v

logs: ## Tail logs from all services
	docker compose logs -f --tail=50

ps: ## Show service status
	docker compose ps

seed: ## Create the SoR table, insert rows, and warm an initial key set into Redis
	bash scripts/seed.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture host + container fingerprint to perf/results/env.txt and meta.json
	bash perf/env.sh

cluster-status: ## Ping all 3 Redis nodes and print per-node key counts
	bash scripts/cluster-status.sh

# --- Step 3: baseline under realistic load ---------------------------

bench-baseline: ## DIST=zipf RUNS=3 DURATION=5m LABEL=baseline
	DIST=$(DIST) RUNS=$(RUNS) DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) \
	  bash scripts/run-bench-baseline.sh

analyze-baseline: ## Hit ratio + p50/p99 + source-fetch rate + evictions with run-to-run sigma
	python3 scripts/analyze-baseline.py perf/results/$(LABEL)

# --- Step 4-5: cache stampede ----------------------------------------

inject-stampede: ## TTL=30s HOT_RATE=5000 DURATION=3m LABEL=stampede-baseline
	TTL=$(TTL) HOT_RATE=$(HOT_RATE) DURATION=$(DURATION) LABEL=$(LABEL) \
	  bash scripts/run-inject-stampede.sh

analyze-stampede: ## LABEL=stampede-baseline - fan-in ratio at expiration
	python3 scripts/analyze-stampede.py perf/results/$(LABEL)

clear-stampede: ## Stop the load, flush the cache, and reset config to baseline
	bash scripts/clear-stampede.sh

apply-fix: ## CANDIDATE=jitter|singleflight|xfetch|swr|local-lru [JITTER=20pct LOCAL_SIZE=1000 LOCAL_TTL=5s]
	CANDIDATE=$(CANDIDATE) JITTER=$(JITTER) LOCAL_SIZE=$(LOCAL_SIZE) LOCAL_TTL=$(LOCAL_TTL) \
	  bash scripts/apply-fix.sh

compare-stampede: ## BEFORE=stampede-baseline AFTER=stampede-after - fan-in before/after
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare-stampede.sh

# --- Step 6: hot key on the sharded cache ----------------------------

inject-hot-key: ## KEY=celebrity-1 WEIGHT=0.4 - send a fraction of traffic to one key
	KEY=$(KEY) WEIGHT=$(WEIGHT) bash scripts/inject-hot-key.sh

bench-hot-key: ## DURATION=3m LABEL=hot-baseline - drive load with the active hot key
	DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) DIST=$(DIST) \
	  bash scripts/run-bench-hot-key.sh

compare-hot-key: ## BEFORE=hot-baseline AFTER=hot-after - per-shard imbalance before/after
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare-hot-key.sh

clear-hot-key: ## Stop the load, clear the hot-key injection, and disable local LRU
	bash scripts/clear-hot-key.sh

# --- Step 7: invalidation staleness ----------------------------------

bench-staleness: ## STRATEGY=ttl-only|explicit-invalidate TTL=60s DURATION=5m LABEL=...
	STRATEGY=$(STRATEGY) TTL=$(TTL) DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) \
	  bash scripts/run-bench-staleness.sh

analyze-staleness: ## fraction_stale + max_staleness_seconds for both strategies
	python3 scripts/analyze-staleness.py perf/results/staleness

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
