# Highload Software Architecture 2 — Labs

Hands-on labs for the **Highload Software Architecture 2** course.
Each lab is a self-contained project you run locally — either a Python
simulator (labs 1-x) or a Docker Compose stack with a real service
under test, load generator, and observability tooling (labs 2-x) — that
you benchmark, improve, and document.

## Labs

| #   | Directory | Topic |
|-----|-----------|-------|
| 1-1 | [labs/1-1-latency-throughput-scaling-laws](labs/1-1-latency-throughput-scaling-laws/) | Latency, Throughput, and Scaling Laws |
| 1-2 | [labs/1-2-workload-characterization-bottleneck-analysis](labs/1-2-workload-characterization-bottleneck-analysis/) | Workload Characterization and Bottleneck Analysis |
| 1-3 | [labs/1-3-capacity-estimation-back-of-envelope](labs/1-3-capacity-estimation-back-of-envelope/) | Capacity Estimation (Back of Envelope) |
| 2-2 | [labs/2-2-red-use-sli-slo-alert-quality](labs/2-2-red-use-sli-slo-alert-quality/) | RED, USE, SLIs, SLOs, and Alert Quality |
| 2-3 | [labs/2-3-load-testing-stress-testing-benchmark-methodology](labs/2-3-load-testing-stress-testing-benchmark-methodology/) | Load Testing, Stress Testing, and Benchmark Methodology |
| 3-2 | [labs/3-2-sync-vs-async-rest-grpc-events](labs/3-2-sync-vs-async-rest-grpc-events/) | Synchronous vs Asynchronous Communication (REST, gRPC, Events) |
| 3-3 | [labs/3-3-failure-domains-blast-radius-graceful-degradation](labs/3-3-failure-domains-blast-radius-graceful-degradation/) | Failure Domains, Blast Radius, and Graceful Degradation |
| 4-2 | [labs/4-2-replication-consistency-distributed-transactions](labs/4-2-replication-consistency-distributed-transactions/) | Replication, Consistency, and Distributed Transactions |
| 4-3 | [labs/4-3-nosql-sharding-polyglot-persistence-cdc](labs/4-3-nosql-sharding-polyglot-persistence-cdc/) | NoSQL, Sharding, Polyglot Persistence, and CDC |

## Getting Started

1. **Fork** this repository on GitHub so you have your own copy.
2. **Clone** your fork locally:

```bash
gh repo fork kirill-latish/hlsa2-labs --clone
cd hlsa2-labs
```

3. Navigate to the lab directory and follow the lab's `README.md`.

## Requirements

- Python 3.12+ (labs 1-1, 1-2, 1-3; also used by the analyzers in 3-2)
- Docker 24+ and Docker Compose v2 (labs 2-2, 2-3, 3-2)
- [k6](https://k6.io/docs/getting-started/installation/) 0.50+ on PATH (labs 2-3, 3-2)
- Git

Each lab's `README.md` lists the exact tooling and version it expects.

## Repository Structure

```
hlsa2-labs/
  README.md          ← you are here
  labs/
    README.md        ← labs index
    1-1-latency-throughput-scaling-laws/
      README.md      ← lab setup, how to run, config reference
      simulator/     ← Python package (the code you benchmark and improve)
      scripts/       ← benchmark runner
      tests/         ← automated tests
      results/       ← your benchmark output (committed by you)
    1-2-workload-characterization-bottleneck-analysis/
      README.md      ← lab setup, how to run, config reference
      simulator/     ← Python package (arrival patterns, R/W workloads)
      scripts/       ← benchmark runner
      tests/         ← automated tests
      results/       ← your benchmark output (committed by you)
    1-3-capacity-estimation-back-of-envelope/
      README.md      ← lab setup, how to run, config reference
      simulator/     ← Python package (workload + storage + network + capacity + cost)
      scripts/       ← benchmark runner
      tests/         ← automated tests
      results/       ← your benchmark output (committed by you)
    2-2-red-use-sli-slo-alert-quality/
      README.md      ← lab setup, stack overview, experiment workflow
      docker-compose.yml ← checkout + payments + loadgen + Prometheus + Alertmanager + Grafana
      checkout/      ← FastAPI service under SLO
      payments/      ← downstream stub with fault-injection admin API
      loadgen/       ← scripted offered-load + fault profiles
      prometheus/    ← scrape config + your recording/alerting rules
      alertmanager/  ← routing for severity: page / ticket
      grafana/       ← provisioned RED + Burn Rate dashboard
      runbooks/      ← incident runbook(s) you fill in
      docs/          ← your SLO design and architecture review (committed by you)
      artifacts/     ← experiment evidence (committed by you)
      scripts/       ← helper scripts
    2-3-load-testing-stress-testing-benchmark-methodology/
      README.md      ← lab setup, stack overview, experiment workflow
      docker-compose.yml ← sut (Go) + downstream + Postgres + Prometheus + Grafana + exporters
      Makefile       ← `make run-sut`, `make perf-baseline`, etc.
      sut/           ← Go HTTP service under test
      downstream/    ← Go downstream stub
      postgres/      ← Postgres init + tuning
      prometheus/    ← scrape config and rules
      grafana/       ← provisioned dashboards
      perf/          ← k6 scripts, workload model, results (committed by you)
      runbooks/      ← rollback runbook(s) you fill in
      docs/          ← coordinated-omission analysis + architecture review (committed by you)
      scripts/       ← regression / coordinated-omission analyzers
    3-2-sync-vs-async-rest-grpc-events/
      README.md      ← lab setup, stack overview, experiment workflow
      docker-compose.yml ← lookup (REST+gRPC) + auth/pricing/inventory + gateway + producer + consumer + Redpanda + Postgres + Prometheus + Grafana
      Makefile       ← `make up`, `make bench-protocols`, `make bench-sync-chain`, `make bench-async-overload`, `make replay`, `make regression`, etc.
      proto/         ← lookup.proto + committed Go bindings (no protoc needed)
      cmd/           ← Go binaries: lookup-svc, chain-svc, gateway, producer, consumer
      internal/      ← shared Go packages (metrics, inject, payload, kafka, breaker)
      postgres/      ← events_audit + events_audit_naive bootstrap
      prometheus/    ← scrape config and Sync/Async recording rules
      grafana/       ← provisioned Sync/Async Overview dashboard
      perf/          ← k6 scripts (REST/gRPC/sync-chain/async), workload model, results (committed by you)
      runbooks/      ← sync-chain incident + async backpressure runbooks
      docs/          ← review template + dashboard screenshots (committed by you)
      scripts/       ← experiment runners + analyze-* Python scripts
    3-3-failure-domains-blast-radius-graceful-degradation/
      README.md      ← lab setup, stack overview, experiment workflow
      docker-compose.yml ← gateway + 5 deps (price, cart, recommendations, reviews, recently-viewed) + fault-injector + loadgen + Prometheus + Grafana
      Makefile       ← `make up`, `make bench-baseline`, `make inject-fault`, `make analyze-blast-radius`, `make compare`, `make bench-overload`, `make analyze-overload`, `make check-submission`
      cmd/           ← Go binaries: gateway, dep-svc, fault-injector, loadgen
      internal/      ← shared Go packages (metrics, breaker, bulkhead, retry, fallback, fault)
      prometheus/    ← scrape config + resilience recording rules & alerts
      grafana/       ← provisioned Resilience Overview dashboard
      perf/          ← workload profiles + results (committed by you)
      runbooks/      ← blast-radius incident + retry-storm runbooks
      docs/          ← review + failure-domains templates, screenshots (committed by you)
      scripts/       ← bench drivers + analyze-* Python scripts
    4-2-replication-consistency-distributed-transactions/
      README.md      ← lab setup, stack overview, experiment workflow
      docker-compose.yml ← Postgres primary + 2 replicas + 3 service DBs + Redpanda + payment/inventory/shipping svcs + orchestrator + outbox-relay + consumer + lag-sampler + fault-injector + Prometheus + Grafana
      Makefile       ← `make up`, `make bench-lag`, `make bench-raw`, `make bench-2pc`, `make bench-saga`, `make assert-idempotent`, `make regression`, `make analyze`, `make check-submission`
      cmd/           ← Go binaries: payment-svc, inventory-svc, shipping-svc, orchestrator, outbox-relay, consumer, lag-sampler, raw-bench, loadgen-saga, seed-events, fault-injector
      internal/      ← shared Go packages (lsn, outbox, saga, twopc, consumer, readpolicy, metrics, fault, payloads, svchelp)
      postgres/      ← primary postgresql.conf + pg_hba.conf, replica setup-replica.sh, per-service init.sql (events_outbox + processed_events + twopc_log)
      prometheus/    ← scrape config + consistency recording rules & alerts
      grafana/       ← provisioned Consistency Overview dashboard
      perf/          ← workload model + results (committed by you)
      runbooks/      ← replica-lag incident + saga-stuck incident
      docs/          ← review template + architecture diagram + screenshots (committed by you)
      scripts/       ← bench drivers + analyze-* Python scripts
    4-3-nosql-sharding-polyglot-persistence-cdc/
      README.md      ← lab setup, stack overview, experiment workflow
      docker-compose.yml ← Postgres + 3 mongo-config + 3 mongo-shard + 2 mongos + Redpanda + Debezium + Elasticsearch + loadgen + es-consumer + partition-stats + fault-injector + Prometheus + Grafana
      Makefile       ← `make up`, `make seed`, `make bench-skew`, `make inject-hot`, `make apply-fix`, `make compare-skew`, `make bench-cdc-lag`, `make analyze-cdc-lag`, `make bench-polyglot`, `make check-submission`
      cmd/           ← Go binaries: loadgen, bench-skew, bench-cdc-lag, bench-polyglot, es-consumer, fault-injector, partition-stats
      internal/      ← shared Go packages (shardkey, cdc, freshness, mongoutil, metrics, fault, payloads, svchelp)
      postgres/      ← postgresql.conf (wal_level=logical), pg_hba.conf, init.sql (users/products/orders + lab43_pub publication)
      mongo/         ← config-init.js + shards-init.js (4 pre-sharded collections + chunk pre-split)
      debezium/      ← pgoutput connector config + idempotent register.sh
      elasticsearch/ ← per-index mappings (products, orders, users)
      prometheus/    ← scrape config + max/mean recording rule + cdc lag percentiles
      grafana/       ← provisioned Polyglot Overview dashboard
      perf/          ← workload model + results (committed by you)
      runbooks/      ← hot-partition incident + CDC-stuck incident
      docs/          ← review template + architecture diagram + freshness-policy template + screenshots (committed by you)
      scripts/       ← bench drivers + analyze-* / compare-* Python scripts
```

## License

Educational use only. All rights reserved.
