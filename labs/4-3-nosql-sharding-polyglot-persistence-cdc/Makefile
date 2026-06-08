# HLSA2 lab 4-3 — NoSQL, Sharding, Polyglot Persistence, and CDC
#
# One-line reproducibility from a clean checkout:
#   make up && make seed && make env-fingerprint
#
# Every target maps to a step in the topic-249 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

LAB_ROOT := $(dir $(realpath $(firstword $(MAKEFILE_LIST))))
.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line.
RUNS              ?= 3
LABEL             ?= baseline
DURATION          ?= 5m
RATE              ?= 1x
WRITE_RATE        ?= 200
WARMUP_S          ?= 5
SHARD_KEY         ?= candidate
ENTITY            ?= tenant-A
WEIGHT            ?= 0.35
CANDIDATE         ?= composite-key
FRESHNESS         ?= read-from-sor
LSN_WAIT_MAX_MS   ?= 1500
BEFORE            ?=
AFTER             ?=

.PHONY: help up down stop logs ps clean seed debezium-status \
        env-fingerprint \
        bench-skew analyze-skew compare-skew \
        inject-hot clear-hot apply-fix \
        bench-cdc-lag analyze-cdc-lag \
        bench-polyglot \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (postgres + sharded mongo + redpanda + debezium + es + go svcs + prom + grafana)
	docker compose up -d --build
	@echo
	@echo "Waiting for healthchecks (mongo cluster bootstrap takes ~30s)..."
	@sleep 25
	@docker compose ps

down: ## Stop and remove containers (preserves named volumes)
	docker compose down

stop: down ## Alias for `make down`

clean: ## Stop AND remove named volumes - destroys all state
	docker compose down -v

logs: ## Tail logs from all services
	docker compose logs -f --tail=50

ps: ## Show service status
	docker compose ps

seed: ## Bootstrap mongo shards, register Debezium connector, seed Postgres rows, wait for ES products index
	bash scripts/seed.sh

debezium-status: ## Show connector + tasks state
	bash scripts/debezium-status.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture host + container fingerprint to perf/results/env.txt and meta.json
	bash perf/env.sh

# --- Step 3-5: shard-key skew + hot-partition + ladder fix -----------

bench-skew: ## SHARD_KEY=candidate|hash-suffix|composite-key|resharded|fixed RUNS=3 DURATION=5m LABEL=...
	SHARD_KEY=$(SHARD_KEY) RUNS=$(RUNS) DURATION=$(DURATION) WRITE_RATE=$(WRITE_RATE) \
	  LABEL=$(LABEL) WARMUP_S=$(WARMUP_S) \
	  bash scripts/run-bench-skew.sh

analyze-skew: ## LABEL=<name> - max/mean ratio across runs + sigma
	LABEL=$(LABEL) python3 scripts/analyze-skew.py perf/results/skew

compare-skew: ## BEFORE=<label> AFTER=<label> - 2-sigma decision rule
	BEFORE=$(BEFORE) AFTER=$(AFTER) python3 scripts/compare-skew.py perf/results/skew

inject-hot: ## ENTITY=tenant-A WEIGHT=0.35 - skews the load toward one tenant
	ENTITY=$(ENTITY) WEIGHT=$(WEIGHT) bash scripts/inject-hot.sh

clear-hot: ## Clear all injected hot entities
	bash scripts/clear-hot.sh

apply-fix: ## CANDIDATE=composite-key|hash-suffix|resharded - persist the chosen shard-key for `make bench-skew SHARD_KEY=fixed`
	CANDIDATE=$(CANDIDATE) bash scripts/apply-fix.sh

# --- Step 6: CDC lag distribution ------------------------------------

bench-cdc-lag: ## RUNS=3 DURATION=5m RATE=1x|2x LABEL=base|2x
	RUNS=$(RUNS) DURATION=$(DURATION) RATE=$(RATE) LABEL=$(LABEL) \
	  WARMUP_S=$(WARMUP_S) bash scripts/run-bench-cdc-lag.sh

analyze-cdc-lag: ## p50/p95/p99/p99.9 across all labels
	python3 scripts/analyze-cdc-lag.py perf/results/cdc-lag

# --- Step 7: polyglot read with explicit freshness policy ------------

bench-polyglot: ## RUNS=3 FRESHNESS=read-from-sor|read-from-derived|lsn-wait
	RUNS=$(RUNS) DURATION=$(DURATION) FRESHNESS=$(FRESHNESS) \
	  LSN_WAIT_MAX_MS=$(LSN_WAIT_MAX_MS) WARMUP_S=$(WARMUP_S) \
	  bash scripts/run-bench-polyglot.sh

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every path cited in docs/review.md actually exists
	bash scripts/check-submission.sh
