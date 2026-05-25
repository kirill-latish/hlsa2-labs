#!/usr/bin/env bash
# show-topology - print the gateway -> deps topology with critical vs
# non-critical labels. Reads docker-compose.yml-defined ports from the
# env (.env if it exists, otherwise defaults).
set -euo pipefail

LAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${LAB_ROOT}"

if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

render() {
  cat <<TOPO

Lab 3-3 topology
================

  loadgen :${LAB_LOADGEN_PORT:-8090}
      |
      v
  gateway :${LAB_GATEWAY_PORT:-8080}   --(critical, must succeed)-->  price        :${LAB_PRICE_PORT:-8091}
                                       --(critical, must succeed)-->  cart         :${LAB_CART_PORT:-8092}
                                       --(non-critical, can degrade)->  recommendations  :${LAB_RECOMMENDATIONS_PORT:-8093}
                                       --(non-critical, can degrade)->  reviews          :${LAB_REVIEWS_PORT:-8094}
                                       --(non-critical, can degrade)->  recently-viewed  :${LAB_RECENTLY_VIEWED_PORT:-8095}

  fault-injector :${LAB_FAULT_INJECTOR_PORT:-9000}   (deps poll it; gateway scrapes its metrics)
  prometheus      :${LAB_PROMETHEUS_PORT:-9090}
  grafana         :${LAB_GRAFANA_PORT:-3000}        Resilience Overview dashboard

Failure-domain notes
--------------------
  - 'critical' deps share fate with the inbound /checkout request. If price or
    cart is down or slow, the request must fail-fast rather than serve a lie.
  - 'non-critical' deps each live in their OWN failure domain. With FALLBACK=on
    the gateway returns last-known-good content from an in-process cache.
  - With BULKHEAD=off, all five deps share one HTTP transport (one pool). This
    is the "shared fate via shared pool" antipattern step 5 will exhibit.

TOPO
}

render

# Snapshot to perf/results so the review template can cite it.
mkdir -p perf/results
TS="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
OUT="perf/results/topology-${TS}.txt"
render >"${OUT}"
echo "[show-topology] wrote ${OUT}"
