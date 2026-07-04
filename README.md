# CrisisLink

**Disaster Response & Relief Coordination Platform** — a Go modular monolith that
sits between citizens reporting geolocated disasters and the scarce rescue
resources that respond, making dispatch decisions that are **fast, explainable,
and impossible to corrupt under concurrency.**

> Learning-first project. The full teaching narrative, architecture decisions,
> and per-phase deep-dives live in [`docs/LEARNING.md`](docs/LEARNING.md).

## Stack
Go · PostgreSQL + PostGIS · Redis · Kafka · Prometheus/Grafana · Docker · k6

## Three core subsystems
1. **Explainable dispatch** — PostGIS KNN candidate query + a pure scoring
   function that emits a visible score breakdown.
2. **No double-booking under concurrency** — `SELECT ... FOR UPDATE` reservation
   transactions.
3. **Transactional outbox → Kafka** — events written in the same DB transaction
   as the state change, relayed at-least-once to idempotent consumers.

## Layout
```
cmd/crisislink/      the only main(): startup, config, graceful shutdown
internal/            service-shaped modules, each owns its own tables
  auth/ incident/ dispatch/ shelter/ responder/ outbox/ audit/
pkg/                 shared helpers: httpx postgis kafkax redisx metrics
migrations/          SQL migrations (P3+)
deploy/              docker-compose, k8s (P2, P30)
docs/                LEARNING.md — the teaching artifact
```

## Run (P1)
```bash
go run ./cmd/crisislink
# in another terminal:
curl localhost:8080/healthz   # -> ok
# Ctrl+C triggers graceful shutdown (drains in-flight requests)
```

Config via env: `HTTP_ADDR` (default `:8080`), `SHUTDOWN_TIMEOUT` (default `15s`).
