# HLSA2 lab 6-2 - CDN Strategy and Edge Delivery
#
# One-line reproducibility from a clean checkout:
#   make up && make seed && make env-fingerprint && make bench-baseline RUNS=3 DURATION=5m
#
# Every target maps to a step in the topic-254 step-by-step guide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

.DEFAULT_GOAL := help

# Knobs the topic guide references on the command line. Override via
#   make bench-baseline RUNS=3 DURATION=5m LABEL=baseline
RUNS      ?= 3
DURATION  ?= 3m
LABEL     ?= baseline
KEY       ?= stripped
CANDIDATE ?=
BEFORE    ?=
AFTER     ?=
USERS     ?= 20
REQUESTS  ?= 200

.PHONY: help up down stop clean logs ps seed env-fingerprint edge-status \
        bench-baseline analyze-baseline \
        bench-cachekey compare-cachekey apply-fix \
        probe-cross-user verify-cacheability compare-leak \
        expire-popular-object analyze-fanin compare-fanin \
        inject-origin-outage inject-setcookie-on-static analyze-cache-status \
        check-submission

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-26s\033[0m %s\n", $$1, $$2}'

# --- Stack lifecycle (step 1) ----------------------------------------

up: ## Bring up the full edge stack (origin, shield, pop-1..3, loadgen, observability)
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

seed: ## Warm an initial set of popular objects on every PoP + the shield
	bash scripts/seed.sh

# --- Environment fingerprint (step 2) --------------------------------

env-fingerprint: ## Capture host + edge fingerprint to perf/results/env.txt and meta.json
	bash perf/env.sh

edge-status: ## Show each PoP + the shield: config, cache entries, health
	bash scripts/edge-status.sh

# --- Step 3: baseline edge cache ratio (by request AND by bytes) -----

bench-baseline: ## RUNS=3 DURATION=5m LABEL=baseline - drive the representative mix
	RUNS=$(RUNS) DURATION=$(DURATION) LABEL=$(LABEL) bash scripts/run-bench-baseline.sh

analyze-baseline: ## Hit ratio by request AND by bytes, offload, cache-status, with sigma
	python3 scripts/analyze-baseline.py perf/results/$(LABEL)

# --- Step 4: cache-key fragmentation + fix ---------------------------

bench-cachekey: ## KEY=full-querystring|stripped DURATION=3m LABEL=... - measure under a key policy
	KEY=$(KEY) DURATION=$(DURATION) LABEL=$(LABEL) bash scripts/run-bench-cachekey.sh

compare-cachekey: ## BEFORE=cachekey-full AFTER=cachekey-stripped - hit ratio + cardinality delta
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare-cachekey.sh

apply-fix: ## CANDIDATE=strip-tracking-params|broad-key-personalized|private-personalized|shield-off|shield-on|stale-if-error
	CANDIDATE=$(CANDIDATE) bash scripts/apply-fix.sh

# --- Step 5: cross-user leak (security) ------------------------------

probe-cross-user: ## LABEL=leak-before - probe the personalized route as many users, count leaks
	LABEL=$(LABEL) USERS=$(USERS) REQUESTS=$(REQUESTS) bash scripts/probe-cross-user.sh

verify-cacheability: ## Confirm genuinely-shared content is still cached after the private fix
	bash scripts/verify-cacheability.sh

compare-leak: ## BEFORE=leak-before AFTER=leak-after - leak count delta
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare-leak.sh

# --- Step 6: thundering herd, shielding off vs on, stale-if-error ----

expire-popular-object: ## LABEL=shield-off - expire the hot object across PoPs, measure origin fan-in
	LABEL=$(LABEL) bash scripts/expire-popular-object.sh

analyze-fanin: ## LABEL=shield-off - origin fan-in for the popular object on its expiry
	python3 scripts/analyze-fanin.py perf/results/$(LABEL)

compare-fanin: ## BEFORE=shield-off AFTER=shield-on - origin fan-in delta
	BEFORE=$(BEFORE) AFTER=$(AFTER) bash scripts/compare-fanin.sh

inject-origin-outage: ## LABEL=stale-if-error - take the origin down, show the edge serves stale
	LABEL=$(LABEL) bash scripts/inject-origin-outage.sh

# --- Step 7: the 'caching nothing' silent failure --------------------

inject-setcookie-on-static: ## LABEL=bypass - glue a Set-Cookie onto static, forcing BYPASS
	LABEL=$(LABEL) bash scripts/inject-setcookie-on-static.sh

analyze-cache-status: ## LABEL=bypass - cache-status distribution (the BYPASS spike)
	python3 scripts/analyze-cache-status.py perf/results/$(LABEL)

# --- Step 9: submission checker --------------------------------------

check-submission: ## Verify every filename cited in docs/review.md actually exists
	bash scripts/check-submission.sh
