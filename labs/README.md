# Labs Index

| Lab | Title | Topic |
|-----|-------|-------|
| [1-1](1-1-latency-throughput-scaling-laws/) | Latency, Throughput, and Scaling Laws | Foundations, Workloads, and Capacity |
| [1-2](1-2-workload-characterization-bottleneck-analysis/) | Workload Characterization and Bottleneck Analysis | Arrival Patterns, R/W Ratios, and Utilisation |
| [1-3](1-3-capacity-estimation-back-of-envelope/) | Capacity Estimation (Back of Envelope) | Capacity Modelling, Multipliers, and Cost |
| [2-2](2-2-red-use-sli-slo-alert-quality/) | RED, USE, SLIs, SLOs, and Alert Quality | Observability, SLOs, and Burn-Rate Alerts |
| [2-3](2-3-load-testing-stress-testing-benchmark-methodology/) | Load Testing, Stress Testing, and Benchmark Methodology | Open-Loop Load Testing, Coordinated Omission, Regression |
| [3-2](3-2-sync-vs-async-rest-grpc-events/) | Synchronous vs Asynchronous Communication (REST, gRPC, Events) | Communication Styles and Coupling |
| [3-3](3-3-failure-domains-blast-radius-graceful-degradation/) | Failure Domains, Blast Radius, and Graceful Degradation | Resilience and Failure Isolation |
| [4-2](4-2-replication-consistency-distributed-transactions/) | Replication, Consistency, and Distributed Transactions | Data: Replication and Consistency |
| [4-3](4-3-nosql-sharding-polyglot-persistence-cdc/) | NoSQL, Sharding, Polyglot Persistence, and CDC | Data: Sharding and Polyglot Persistence |
| [5-1](5-1-cache-patterns-invalidation-hot-key-mitigation/) | Cache Patterns, Invalidation, and Hot-Key Mitigation | Caching, Queues, and Event-Driven Architecture |
| [5-2](5-2-queues-brokers-retries-dlqs-backpressure/) | Queues, Brokers, Retries, DLQs, and Backpressure | Caching, Queues, and Event-Driven Architecture |
| [5-3](5-3-event-streaming-ordering-idempotency-outbox/) | Event Streaming: Ordering, Idempotency, and the Outbox Pattern | Caching, Queues, and Event-Driven Architecture |
| [6-1](6-1-load-balancing-reverse-proxies-api-gateways/) | Load Balancing, Reverse Proxies, and API Gateways | Edge: Load Balancing, Gateways, and CDN |
| [6-2](6-2-cdn-strategy-edge-delivery/) | CDN Strategy and Edge Delivery | Edge: Load Balancing, Gateways, and CDN |

Each lab folder contains its own `README.md` with full setup, run, and
deliverable instructions. The labs come in two flavours:

- **Python simulator labs (1-1, 1-2, 1-3).** A self-contained Python
  package, a benchmark script under `scripts/`, automated tests under
  `tests/`, and a `results/` directory where you commit your benchmark
  output and analysis.
- **Containerised stack labs (2-2, 2-3, 3-2, 3-3, 4-2, 4-3, 5-1, 5-2,
  5-3, 6-1, 6-2).** A Docker Compose stack (service under test, load
  generator, Prometheus, Grafana, and friends) plus `docs/`, `runbooks/`,
  and per-lab artefact directories where you commit your SLO/workload
  designs, experiment evidence, and architecture reviews.
