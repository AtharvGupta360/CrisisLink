# CrisisLink

**Disaster response & relief coordination backend** — a Go service that sits between
citizens reporting geolocated emergencies and the scarce rescue resources that
respond, making dispatch decisions that are **fast, explainable, and impossible to
corrupt under concurrency.**

The hard part isn't CRUD. A rescue unit is a *physical, exclusive* resource: one
ambulance cannot be sent to two emergencies. Everything here is built around making
that impossible, and around never losing an event when a downstream system fails.

```
10 concurrent dispatch requests for the same ambulance
  -> 1 x 201 Created   (the winner)
  -> 9 x 409 Conflict  (correctly refused)
  -> exactly 1 active dispatch row in Postgres
```

| | |
|---|---|
| **Throughput** | 2,360 req/s sustained · 50 VUs · 0% errors |
| **Latency** | p95 25.8ms · **p99 32.5ms** (dispatch-decision path: PostGIS KNN + Redis + scoring) |
| **Size** | ~8.6k lines Go · 30 packages · 18 migrations · 5 binaries |

Measured over 130k requests with
[`test/load/dispatch_decision.js`](test/load/dispatch_decision.js) — reproduce it
yourself, see [Load testing](#load-testing).

---

## Stack

Go · Gin · PostgreSQL + PostGIS (pgx) · Redis · Kafka · Prometheus + Grafana ·
Docker · k6 · JWT + bcrypt · zap · viper · golang-migrate

## Architecture

Modular monolith with microservices-ready seams: every table is written **only** by
its owning module, and cross-module calls go through interfaces the *consumer*
declares — each taking the caller's `pgx.Tx`, so several modules commit inside one
transaction.

```
   citizen        operator       responder
      └───────────────┼───────────────┘
                      ▼
            ┌───────────────────┐
            │  GATEWAY  :8000   │  round-robin · edge rate limit
            │  reverse proxy    │  request-id · coarse authN
            └─────────┬─────────┘
                ┌─────┴─────┐            (authZ/RBAC stays in the app —
                ▼           ▼             it needs domain context)
          ┌──────────┐ ┌──────────┐
          │ API :8080│ │ API :8081│  stateless replicas
          └────┬─────┘ └────┬─────┘
      ┌────────┼────────────┼────────┐
      ▼        ▼            ▼        ▼
 ┌────────┐ ┌──────────────────┐ ┌──────────┐
 │ REDIS  │ │ POSTGRES+POSTGIS │ │ /metrics │
 │presence│ │  ┌────────────┐  │ └────┬─────┘
 │GEO·rate│ │  │   outbox   │  │      ▼
 └────────┘ │  └─────┬──────┘  │  Prometheus → Grafana
            └────────┼─────────┘
                     │ FOR UPDATE SKIP LOCKED
                     ▼
              ┌────────────┐
              │   RELAY    │  retry 2/4/8/16s → dead-letter at 5
              └──────┬─────┘
                     ▼
                 ┌───────┐
                 │ KAFKA │
                 └───┬───┘
           ┌─────────┴─────────┐
           ▼                   ▼
     ┌──────────┐        ┌──────────┐   independent consumer groups:
     │ notifier │        │ auditor  │   separate offsets, own dedup
     └────┬─────┘        └────┬─────┘
          └── inbox dedup ────┘  (consumer, event_id) written in the
                                  SAME tx as the side effect
```

### The three invariants

Three *different* shapes of "never over-allocate", each needing a different
mechanism. This is the core of the project:

| Resource | Shape | Mechanism |
|---|---|---|
| **Rescue unit** | boolean (free / taken) | `SELECT … FOR UPDATE` + **re-check under the lock**, plus a partial unique index making a second active dispatch physically unstorable |
| **Shelter bed** | counter +1 | `UPDATE … WHERE occupancy < capacity` — test and increment in **one statement**, so no explicit lock is needed |
| **Transport seats** | counter +N | `UPDATE … WHERE seats_taken + $n <= capacity` — the quantified guard is what makes a booking **all-or-nothing** |

### Layout

```
cmd/     server · gateway · relay · consumer · migrate      (5 entrypoints, 1 codebase)
internal/
  platform/   config common database dbx middleware authz cache geo metrics
  dispatch/   THE CORE — reservation, rerouting, severity preemption
  unit/       fleet; owns row locking + optimistic CAS
  incident/   reporting, geo-deduplication, status lifecycle
  presence/   Redis TTL heartbeats + GEO index
  outbox/     event write · inbox dedup · lag monitor
  relay/      publish loop, exponential backoff, dead-lettering
  consumer/   generic Kafka consumer; side effect injected
  scoring/    pure ranking functions — no DB, no clock, no I/O
  shelter/ victim/ transport/ auth/ notification/ audit/ server/
```

---

## Quick start

**Requires:** Go 1.26+, Docker Desktop running.

```bash
# 1. backing services (Postgres+PostGIS, Redis, Kafka, Prometheus, Grafana)
docker compose -f deploy/docker-compose.yml up -d

# 2. config (once)
cp config.yaml.example config.yaml

# 3. schema — an explicit step, never auto-applied on boot
go run ./cmd/migrate up

# 4. the services (separate terminals)
go run ./cmd/server      # API      :8080
go run ./cmd/relay       # outbox → Kafka
go run ./cmd/consumer    # notifier + auditor groups

curl localhost:8080/health
```

| Port | Service |
|---|---|
| 8080 | API |
| 8000 | Gateway (optional: `go run ./cmd/gateway`) |
| 15432 | Postgres + PostGIS |
| 6379 | Redis |
| 9092 | Kafka (KRaft — no ZooKeeper) |
| 9090 / 3000 | Prometheus / Grafana (`admin` / `admin`) |

Config precedence: defaults < `config.yaml` < environment variables
(e.g. `DATABASE_PASSWORD`, `SERVER_PORT`). In release mode the app **refuses to
boot** on a default JWT secret.

### Guided demo

```bash
./scripts/demo.sh
```

Five steps, pausing between each: geo-deduplication → live presence → **the
concurrency race** → transactional outbox → append-only audit log.

### Running behind the gateway

```bash
REPLICA_ID=api-A SERVER_PORT=8080 go run ./cmd/server
REPLICA_ID=api-B SERVER_PORT=8081 go run ./cmd/server
go run ./cmd/gateway                       # :8000

curl -D - localhost:8000/ready | grep X-Served-By   # alternates api-A / api-B
```

### Docker

```bash
docker build -t crisislink .    # multi-stage build, alpine runtime, non-root user
```

One image (186MB) contains all five static binaries and the entrypoint selects which
one runs — convenient for compose, where every service shares an image. The Go
toolchain stays in the build stage; the runtime is alpine + `ca-certificates`. A
per-service image would be ~40MB, which is the right trade once these deploy
independently.

### Load testing

```bash
docker run --rm -i --add-host=host.docker.internal:host-gateway \
  -e BASE_URL=http://host.docker.internal:8080 \
  -e TOKEN=<jwt> -e INCIDENT_ID=<uuid> \
  grafana/k6 run - < test/load/dispatch_decision.js
```

Thresholds are pass/fail (`p95<200ms`, `p99<500ms`, `errors<1%`), so this gates CI
rather than just printing numbers.

---

## Design decisions worth knowing

**Why a monolith?** The core operation touches four modules and must be atomic. As a
monolith that's one transaction; as microservices it becomes a saga, and *"sorry,
un-dispatch that ambulance"* is not a real compensating action. The service
boundaries are drawn anyway, so extraction stays possible.

**Transactional outbox.** Postgres and Kafka can't share a transaction. Publish first
and the DB write fails → you announced something that never happened. Write first and
the publish fails → the event is silently lost. So the event is written to a table in
the *same commit* as the business change, and a relay publishes it afterwards. Kafka
going down becomes a latency problem, not data loss.

**Idempotent consumers.** Kafka delivers at-least-once. Each consumer inserts
`(consumer, event_id)` into an inbox table **in the same transaction as its side
effect** — separate transactions would either lose the effect or duplicate it. Keyed
per-consumer, so the notifier and auditor each keep their own dedup ledger. The
result is exactly-once *effects*, which is the achievable guarantee.

**Presence via TTL.** A `last_seen_at` column needs something to poll before anyone
notices a unit went quiet. With a 30-second TTL key (3× the heartbeat interval, so
two pings must be missed), **the expiry is the event** — Redis deletes the key,
absence means dark, and no cleanup job exists to fall behind.

**Redis is an optimisation, not a dependency.** Lose it and dispatch degrades to
registered positions instead of live ones: worse decisions, still a working system.

**Rate limiting at the edge.** A Redis Lua token bucket in the gateway, so abuse is
shed *once* before it fans out to every replica. Redis-backed rather than in-process
because the gateway balances across stateless replicas — an in-memory bucket would
give N replicas N× the intended limit.

**Deadlock avoidance.** Rerouting locks *two* unit rows, so it sorts the ids and
locks in ascending order — a total order every caller computes identically without
coordinating. Locking "old then new" is precisely the swap case that deadlocks.

**Explainable dispatch.** Ranking is a pure function returning a component breakdown
(ETA, specialisation) weighted by severity, so an operator can reconstruct *why* one
unit outranked another. Severity is a **modulator, not a component** — it's constant
across candidates, so adding it as a weighted term would change the ranking by
exactly nothing.

---

## Known limitations

Stated plainly rather than left to be discovered:

- **Travel model is geometric, not routed.** Straight-line distance × 1.3 detour
  factor ÷ per-type average speed. No road graph, no live traffic. Isolated in two
  functions so a real routing engine (OSRM/Valhalla) can drop in.
- **Test coverage is thin.** Only the pure `scoring` package is unit-tested; the
  transactional paths are verified by `scripts/demo.sh` and by hand. Integration
  tests against a throwaway Postgres are the right next step.
- **Single Postgres** — the one real SPOF; no replication or failover configured.
- **The relay polls** on a fixed interval. `LISTEN/NOTIFY` or logical decoding would
  make it event-driven.
- **24h JWTs with no revocation.** Should be short-lived access tokens plus refresh.
- **Rate limit keyed by IP**, so clients behind one NAT share a bucket.

## Docs

Full design narrative and per-phase reasoning:
[`docs/LEARNING.md`](docs/LEARNING.md)
