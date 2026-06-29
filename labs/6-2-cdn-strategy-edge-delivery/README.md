# HLSA2 Lab 6-2 — CDN Strategy and Edge Delivery

Companion lab for topic 254. You will:

1. Stand up a small CDN-shaped edge: a load driver, three edge-cache
   **PoPs**, a single **origin shield** tier, and an **origin** with
   configurable size/latency/failure injection, plus a pre-provisioned
   Grafana + Prometheus pair with an **Edge Delivery** dashboard.
2. Measure the baseline edge cache ratio **by request AND by bytes**
   (the pair teams never separate) and the origin offload.
3. Reproduce the hit-ratio collapse caused by a **fragmenting cache key**
   (tracking params in the key) and recover it by stripping them.
4. Reproduce the **cross-user content leak** a too-broad key causes on
   personalized content (the security failure) and eliminate it.
5. Reproduce the **origin thundering herd** on a popular-object expiry,
   with origin shielding off vs on, plus **stale-if-error** during an
   origin outage.
6. Expose the **"caching nothing"** silent failure via a BYPASS spike.
7. Write the architecture review and runbooks.

> The mandate is the same as every lab in this course: ship CDN changes
> with evidence, not with the assumption that "the CDN handles it."

## Design decision: one instrumented Go caching proxy

The PoPs and the origin shield are **one purpose-built Go binary**
(`cmd/cache-proxy`), not Varnish or NGINX. The role is chosen at runtime
by the `ROLE` env var (`pop` | `shield`); the upstream wiring differs but
the code is identical.

We made this choice deliberately so the lab has **total control** over
the things the topic is actually about:

- **Cache-key control** — the key is computed in code, so you can switch
  between full-querystring keying and a stripped allowlist at runtime and
  watch cardinality move.
- **A precise cache status per response** — every reply is labelled
  `HIT` / `MISS` / `EXPIRED` / `STALE` / `BYPASS`, emitted both as a
  Prometheus metric (`lab62_cache_responses_total`) and an
  `X-Cache-Status` response header.
- **Request collapsing** (singleflight within a PoP) and **origin
  shielding** (a shared mid-tier across PoPs) as independent runtime
  toggles, so you can show how they compose to bound origin fan-in.
- **stale-if-error**, **personalized-content key policy**, **TTL**, and
  **Vary** all as `POST /admin/config` knobs the `make` targets flip
  between runs — no restarts, no rebuilds.

**Varnish (VCL) and NGINX (`proxy_cache_*`) are perfectly valid in
production**, and you would normally reach for them before writing your
own cache. We use a Go binary here only because a teaching lab needs to
emit precise, comparable metrics and flip behaviour deterministically
between labelled benchmark runs — which is far easier when the cache is
instrumented from the inside.

## Prerequisites

- Docker + Docker Compose (Docker Desktop or equivalent).
- `make`, `bash`, `jq`, `python3` (3.10+), `curl` on the host.
- ~3 GB of free disk for images + Prometheus TSDB.
- Ports `3000`, `8081-8083`, `8086`, `8088`, `8090`, `9090` free on the
  host (override via `.env` from `.env.example` if any clash).

## Stack overview

| Service      | Host port | Role                                                       |
| ------------ | --------- | ---------------------------------------------------------- |
| `origin`     | 8088      | Backend: sized objects, latency, outage + Set-Cookie injection |
| `shield`     | 8086      | Origin-shield cache (cache-proxy, `ROLE=shield`)          |
| `pop-1`      | 8081      | Edge PoP cache (cache-proxy, `ROLE=pop`)                  |
| `pop-2`      | 8082      | Edge PoP cache                                            |
| `pop-3`      | 8083      | Edge PoP cache                                            |
| `loadgen`    | 8090      | In-cluster Go load driver + cross-user leak probe         |
| `prometheus` | 9090      | Scrapes every node every 5s                               |
| `grafana`    | 3000      | Provisioned **Edge Delivery** dashboard (anonymous Admin) |

All Go services use one `go.mod` at the lab root and share helpers under
[`internal/`](internal/) (`metrics`, `cache`).

## One-line bring-up

```bash
cp .env.example .env
make up
make seed
make env-fingerprint
make edge-status
```

You should see all containers healthy, every PoP + the shield reporting
their config, and Grafana reachable at <http://localhost:3000> with the
**Edge Delivery** dashboard.

## Steps from the topic guide

| Step | Command(s) |
| ---- | ---------- |
| 1 | `make up`, `make ps`, `make seed`, `make edge-status` |
| 2 | `make env-fingerprint` |
| 3 | `make bench-baseline RUNS=3 DURATION=5m`, `make analyze-baseline` |
| 4 | `make bench-cachekey KEY=full-querystring DURATION=3m LABEL=cachekey-full`, `make apply-fix CANDIDATE=strip-tracking-params`, `make bench-cachekey KEY=stripped DURATION=3m LABEL=cachekey-stripped`, `make compare-cachekey BEFORE=cachekey-full AFTER=cachekey-stripped` |
| 5 | `make apply-fix CANDIDATE=broad-key-personalized`, `make probe-cross-user LABEL=leak-before`, `make apply-fix CANDIDATE=private-personalized`, `make probe-cross-user LABEL=leak-after`, `make verify-cacheability`, `make compare-leak BEFORE=leak-before AFTER=leak-after` |
| 6 | `make apply-fix CANDIDATE=shield-off`, `make expire-popular-object LABEL=shield-off`, `make analyze-fanin LABEL=shield-off`, `make apply-fix CANDIDATE=shield-on`, `make expire-popular-object LABEL=shield-on`, `make analyze-fanin LABEL=shield-on`, `make compare-fanin BEFORE=shield-off AFTER=shield-on`, `make apply-fix CANDIDATE=stale-if-error`, `make inject-origin-outage LABEL=stale-if-error` |
| 7 | `make inject-setcookie-on-static LABEL=bypass`, `make analyze-cache-status LABEL=bypass` |
| 8 | Fill in `docs/review.md` from `docs/review.template.md` |
| 9 | `make check-submission` |

Each `make analyze-*` writes a Markdown report (and a JSON sibling) under
`perf/results/<label>/` that the review template tells you how to cite.

## Runtime cache config (the knobs)

Every PoP and the shield expose `GET/POST /admin/config`. The bench and
`apply-fix` scripts POST partial updates between runs, so a single
`make up` is enough — you never restart a proxy between conditions.

| Knob | Values | What it controls |
| ---- | ------ | ---------------- |
| `cache_key_mode` | `full-querystring` \| `stripped-allowlist` | Whether tracking params fragment the cache |
| `allowlist` | list of params | Params kept in the key under stripped mode (those that DO change content) |
| `ttl_seconds` | int | Freshness lifetime |
| `vary` | list of headers | Request headers folded into the key |
| `request_collapsing` | bool | Singleflight concurrent misses for one key |
| `stale_if_error` | bool | Serve last-known-good on upstream error |
| `personalized_mode` | `broad-key-ignores-auth` \| `private-no-store` \| `per-user-key` | How personalized responses are (mis)cached |
| `shield_routing` | bool (PoP only) | Route misses through the shield vs straight to origin |

`make edge-status` prints each node's live config and cache-entry count.
`make apply-fix CANDIDATE=…` maps each named remediation to the right
POST.

## How the experiments work (mechanism)

- **Baseline by request vs by bytes** — popular static objects are 2 KiB
  and the rare long-tail `big*` objects are 256 KiB, so the origin can
  serve most BYTES even at a high by-request hit ratio. Origin cost is
  driven by bytes, so the lab reports both.
- **Cache-key fragmentation** — the loadgen injects `utm_source` /
  `fbclid` / `gclid` onto ~30% of static+page traffic. Under
  full-querystring keying each distinct param is a separate, guaranteed-
  miss entry for identical content; `lab62_cache_entries` explodes and
  the hit ratio collapses. Stripping the tracking params recovers both.
- **Cross-user leak** — under `broad-key-ignores-auth` the PoP caches a
  `/account` response under a key that ignores the `uid` cookie, so the
  next user gets the first user's data. The probe sends requests as many
  users and counts responses personalized for the *wrong* user
  (`lab62_cross_user_leak_total`). The fix (`private-no-store` or
  `per-user-key`) drives that to zero.
- **Thundering herd** — `expire-popular-object` warms the hot object on
  every PoP, purges it everywhere at once, then fires a concurrent burst.
  With `shield-off` each PoP fetches the origin (fan-in ~ PoP count);
  with `shield-on` the shield collapses them to ~one origin fetch.
- **stale-if-error** — with a short TTL and the origin in outage, the PoP
  serves the expired last-known-good copy (`STALE`) instead of erroring.
- **Caching nothing** — `inject-setcookie-on-static` makes the origin
  glue a `Set-Cookie` onto static responses; the edge is forced to
  `BYPASS`. Latency and uptime look normal — only the cache-status
  distribution reveals the collapse.

## Observability

- Prometheus UI: <http://localhost:9090>
- Grafana: <http://localhost:3000> (anonymous Admin)
- Provisioned dashboard: **Edge Delivery** — hit ratio by request AND by
  bytes, offload, cache-status distribution, origin request rate, cache-
  entry cardinality, cross-user leak count, and offered-vs-served.

Key metrics (all prefixed `lab62_`): `lab62_cache_responses_total`
(`status` ∈ HIT/MISS/EXPIRED/STALE/BYPASS, by `node`),
`lab62_bytes_served_total` (`source` ∈ edge/origin),
`lab62_origin_requests_total`, `lab62_origin_object_requests_total`,
`lab62_cache_entries`, `lab62_cross_user_leak_total`,
`lab62_http_request_duration_seconds`, `lab62_loadgen_offered_total` /
`lab62_loadgen_served_total`.

## What to submit

- `docs/review.md` (filled from `docs/review.template.md`, ~1,500-2,000 words).
- `perf/results/env.txt`, `perf/results/meta.json`.
- Every `perf/results/<label>/` run directory referenced in the review.
- Grafana screenshots in `docs/img/` cited from the review.
- The two runbooks under `runbooks/`.

`make check-submission` parses `docs/review.md`, asserts every cited
filename exists, and warns on remaining `TODO` markers.

## Troubleshooting

- Port collisions: copy `.env.example` to `.env` and override the
  `LAB_*_PORT` values that conflict.
- `docker compose ps` stuck in `health: starting`: the PoPs wait on the
  shield and the shield on the origin; give it ~15s.
- "No data points" in Grafana: the dashboard uses recording rules; let it
  run for ~2 minutes after a bench starts.
- Hit ratio looks low on the very first run: run `make seed` first so the
  catalog is warm, and remember each bench flushes the edge to start cold.
