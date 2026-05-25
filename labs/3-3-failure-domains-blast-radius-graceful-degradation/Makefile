# HLSA2 lab 3-3 - Failure Domains, Blast Radius, and Graceful Degradation
#
# One-line reproducibility from a clean checkout:
#   make up && make env-fingerprint && make bench-baseline RUNS=3
#
# Every target maps to a step in the topic-246 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

LAB_ROOT := $(dir $(realpath $(firstword $(MAKEFILE_LIST))))
.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via
#   make bench-baseline RUNS=5 LABEL=faulted-after FALLBACK=on
RUNS              ?= 3
LABEL             ?= baseline
RATE              ?= 200
DURATION_S        ?= 180
WARMUP_S          ?= 30
PROFILE           ?= baseline
BEFORE            ?=
AFTER             ?=
DEP               ?=
MODE              ?= down
P99_MS            ?= 400
ERROR_RATE        ?= 0.1
BULKHEAD          ?= off
CIRCUIT_BREAKER   ?= off
FALLBACK          ?= off
RETRY_BUDGET      ?= off
LOAD_SHED         ?= off

.PHONY: help up down stop logs ps env-fingerprint show-topology \
        bench-baseline analyze-baseline \
        inject-fault clear-fault analyze-blast-radius compare \
        bench-overload analyze-overload \
        check-submission clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (gateway, 5 deps, loadgen, fault-injector, observability)
	docker compose up -d --build
	@echo
	@echo "Waiting for healthchecks..."
	@sleep 15
	@docker compose ps

down: ## Stop and remove containers (preserves named volumes)
	docker compose down

stop: down ## Alias for `make down`

clean: ## Stop AND remove named volumes - destroys Prometheus + Grafana state
	docker compose down -v

logs: ## Tail logs from all services
	docker compose logs -f --tail=50

ps: ## Show service status
	docker compose ps

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture host + container fingerprint to perf/results/env.txt and meta.json
	bash perf/env.sh

# --- Topology map (step 3) -------------------------------------------

show-topology: ## Print the gateway -> deps topology with critical/non-critical labels
	bash scripts/show-topology.sh

# --- Step 4: healthy baseline ----------------------------------------

bench-baseline: ## RUNS=3 LABEL=baseline. Knobs: BULKHEAD=on CIRCUIT_BREAKER=on FALLBACK=on RETRY_BUDGET=on LOAD_SHED=on
	RUNS=$(RUNS) LABEL=$(LABEL) RATE=$(RATE) DURATION_S=$(DURATION_S) WARMUP_S=$(WARMUP_S) \
	  BULKHEAD=$(BULKHEAD) CIRCUIT_BREAKER=$(CIRCUIT_BREAKER) FALLBACK=$(FALLBACK) \
	  RETRY_BUDGET=$(RETRY_BUDGET) LOAD_SHED=$(LOAD_SHED) \
	  bash scripts/run-bench-baseline.sh

analyze-baseline: ## Per-run success rate + p50/p95/p99 with sigma across runs
	python3 scripts/analyze-baseline.py perf/results/$(LABEL)

# --- Step 5: blast-radius injection ----------------------------------

inject-fault: ## DEP=recommendations MODE=down|latency|errors [P99_MS=400 ERROR_RATE=0.1]
	DEP=$(DEP) MODE=$(MODE) P99_MS=$(P99_MS) ERROR_RATE=$(ERROR_RATE) bash scripts/inject-fault.sh

clear-fault: ## DEP=recommendations
	DEP=$(DEP) bash scripts/clear-fault.sh

analyze-blast-radius: ## LABEL=faulted-before|faulted-after - fraction of critical journey broken
	python3 scripts/analyze-blast-radius.py perf/results/$(LABEL)

# --- Step 6: identical-fault before/after ----------------------------

compare: ## BEFORE=faulted-before AFTER=faulted-after - side-by-side with 2-sigma decision
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare.sh

# --- Step 7: load-dependent failure + mitigation ---------------------

bench-overload: ## RETRY_BUDGET=on LOAD_SHED=on LABEL=storm|tamed - drives toward saturation
	LABEL=$(LABEL) RATE=$(RATE) DURATION_S=$(DURATION_S) WARMUP_S=$(WARMUP_S) \
	  RETRY_BUDGET=$(RETRY_BUDGET) LOAD_SHED=$(LOAD_SHED) BULKHEAD=$(BULKHEAD) \
	  CIRCUIT_BREAKER=$(CIRCUIT_BREAKER) FALLBACK=$(FALLBACK) \
	  bash scripts/run-bench-overload.sh

analyze-overload: ## Storm vs tamed: offered/served rate, retry amplification, success rate
	python3 scripts/analyze-overload.py perf/results/overload

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
