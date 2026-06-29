# HLSA2 lab 6-1 - Load Balancing, Reverse Proxies, and API Gateways
#
# One-line reproducibility from a clean checkout:
#   make up && make env-fingerprint && make bench-baseline RUNS=3 DURATION=5m
#
# Every target maps to a step in the topic-253 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via
#   make bench-baseline RUNS=3 DURATION=5m LABEL=baseline
RUNS       ?= 3
DURATION   ?= 5m
LABEL      ?= baseline
RATE       ?= 200
SLOW_RATIO ?= 0.2
ALGO       ?= round-robin
BEFORE     ?=
AFTER      ?=
BACKEND    ?=
CANDIDATE  ?=
INTERVAL   ?= 2s
THRESHOLD  ?= 2

.PHONY: help up down stop clean logs ps seed env-fingerprint edge-status \
        bench-baseline analyze-baseline \
        bench-distribution compare-distribution \
        inject-backend-failure analyze-failover restore-backend apply-fix compare-failover \
        inject-dependency-hiccup analyze-healthcheck compare-healthcheck \
        inject-5xx-scenarios analyze-5xx \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full stack (edge-proxy, backend-1..4, postgres, loadgen, observability)
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

seed: ## Send a few fast+slow requests and show which backend served each
	bash scripts/seed.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture proxy/backend/host fingerprint to perf/results/env.txt and meta.json
	bash perf/env.sh

edge-status: ## Show the proxy's view of backend health (GET edge /admin/status)
	bash scripts/edge-status.sh

# --- Step 3: baseline edge overhead ----------------------------------

bench-baseline: ## RUNS=3 DURATION=5m LABEL=baseline - drive load, capture edge overhead SEPARATELY
	RUNS=$(RUNS) DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) SLOW_RATIO=$(SLOW_RATIO) \
	  bash scripts/run-bench-baseline.sh

analyze-baseline: ## Edge overhead p50/p99 SEPARATE from total latency, with run-to-run sigma
	python3 scripts/analyze-baseline.py perf/results/$(LABEL)

# --- Step 4: load distribution under two algorithms ------------------

bench-distribution: ## ALGO=round-robin|least-conn DURATION=3m LABEL=dist-rr - per-backend distribution
	ALGO=$(ALGO) DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) SLOW_RATIO=$(SLOW_RATIO) \
	  bash scripts/run-bench-distribution.sh

compare-distribution: ## BEFORE=dist-rr AFTER=dist-lc - per-backend counts + max-vs-mean skew
	python3 scripts/compare-distribution.py perf/results/distribution/$(BEFORE) perf/results/distribution/$(AFTER)

# --- Step 5: failover under induced backend failure ------------------

inject-backend-failure: ## BACKEND=backend-2 LABEL=failover-baseline - kill a backend mid-load
	BACKEND=$(BACKEND) LABEL=$(LABEL) RATE=$(RATE) bash scripts/inject-backend-failure.sh

analyze-failover: ## LABEL=failover-baseline - detection time, dropped requests, redistribution
	python3 scripts/analyze-failover.py perf/results/$(LABEL)

restore-backend: ## BACKEND=backend-2 - clear the injected fault so it rejoins rotation
	BACKEND=$(BACKEND) bash scripts/restore-backend.sh

apply-fix: ## CANDIDATE=fast-healthcheck|deep-healthcheck|shallow-healthcheck INTERVAL=2s THRESHOLD=2
	CANDIDATE=$(CANDIDATE) INTERVAL=$(INTERVAL) THRESHOLD=$(THRESHOLD) bash scripts/apply-fix.sh

compare-failover: ## BEFORE=failover-baseline AFTER=failover-after - detection + dropped side by side
	python3 scripts/compare-failover.py perf/results/$(BEFORE) perf/results/$(AFTER)

# --- Step 6: deep vs shallow health-check cascade --------------------

inject-dependency-hiccup: ## DURATION=5s LABEL=healthcheck-deep - briefly pause Postgres
	DURATION=$(DURATION) LABEL=$(LABEL) RATE=$(RATE) bash scripts/inject-dependency-hiccup.sh

analyze-healthcheck: ## LABEL=healthcheck-deep - min healthy backends + 503 outage signal
	python3 scripts/analyze-healthcheck.py perf/results/$(LABEL)

compare-healthcheck: ## Deep vs shallow under the same hiccup (defaults BEFORE=healthcheck-deep AFTER=healthcheck-shallow)
	python3 scripts/compare-healthcheck.py \
	  perf/results/$(if $(BEFORE),$(BEFORE),healthcheck-deep) \
	  perf/results/$(if $(AFTER),$(AFTER),healthcheck-shallow)

# --- Step 7: 502 / 503 / 504 as layer signals ------------------------

inject-5xx-scenarios: ## LABEL=5xx - drive 502/503/504 scenarios
	LABEL=$(LABEL) RATE=$(RATE) bash scripts/inject-5xx-scenarios.sh

analyze-5xx: ## LABEL=5xx - attribute each 5xx class to its layer
	python3 scripts/analyze-5xx.py perf/results/$(LABEL)

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
