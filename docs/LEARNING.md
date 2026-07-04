# CrisisLink — Learning & Design Log

> This is the single source of truth for **what we built, why, and how to defend
> it in an interview.** Every phase appends a section. Read it top to bottom and
> you should be able to explain the whole system without opening the chat.

## How to use this doc
- **TEACH → BUILD → CHECK.** Each phase first teaches the concept, then lists
  what was built, then gives you a self-check / interview drill.
- ⭐ marks the three core subsystems (dispatch reservation, outbox+Kafka,
  explainable scoring) — the parts worth the most interview depth.
- 🎯 marks an interview question and its model answer.
- ✅ marks an open checkpoint you owe an answer to before advancing.

## Table of contents
- [0. Project overview — what & why](#0-project-overview)
- [Architecture decision — modular monolith vs microservices](#architecture-decision)
- [P1 — Repo skeleton & config](#p1--repo-skeleton--config)
- [Open checkpoints](#open-checkpoints)

---

## 0. Project overview

**One-sentence pitch:** CrisisLink is the coordination brain between *citizens
reporting disasters* and *the limited rescue resources that respond*, making
dispatch decisions that are **fast, explainable, and impossible to corrupt under
concurrency.**

### The real-world problem
In a disaster (earthquake, flood, building collapse) three things are true at once:
1. **Reports flood in faster than humans can triage** — hundreds of geolocated
   incidents, many duplicates of the same event.
2. **Resources are scarce and contended** — 8 ambulances, 200 incidents. Send the
   *same* ambulance to two places and someone promised help gets nobody. That's
   not a bug, that's a death.
3. **Nobody trusts a black box in a crisis** — a commander must know *why* unit-7
   was chosen over unit-3, and be able to override it.

Every hard part of the project maps to one of those pressures:

| Real-world failure | System pressure | Subsystem (phase) |
|---|---|---|
| Two dispatchers send the same ambulance to two incidents | Concurrent contention on a scarce resource | ⭐ Reservation transaction `SELECT … FOR UPDATE` (P13) |
| Commander asks "why this unit?" and gets no answer | Trust / accountability | ⭐ Explainable dispatch: pure scoring fn + Breakdown (P12) |
| "Unit assigned" notification lost because the process crashed | Reliability across DB + broker | ⭐ Transactional outbox → Kafka (P19–P22) |
| 300 people report the same collapsed building → 300 dispatches | Signal vs noise | Report dedupe, space+time window (P9) |
| Responder's radio dies, we keep routing to them | Liveness / presence | Redis heartbeats + GEO (P23–P24) |
| Shelter marked "20 beds free" fills to 25 people | Same contention, different resource | Bed reservation `FOR UPDATE` (P17) |

That table **is** the project. The tech stack is just the tools each row demands.

---

## Architecture decision
### Why a modular monolith over microservices — the choice everything else inherits

**The essential difference** is not "one repo vs many" or "one server vs many."
It's **where module boundaries are enforced and how modules talk across them.**

- **Modular monolith:** modules separated *logically* (Go packages) but share one
  process, one binary, and one database *process* (though never each other's
  *tables*). A call from `dispatch` into `incident` is an in-process function
  call — it cannot be half-successful, there is no network in the middle.
- **Microservices:** modules separated *physically* — separate processes,
  deployables, and **databases**. Every cross-module call is a network call that
  can time out, retry, arrive twice, or silently vanish.

The moment a boundary is drawn with a network, you've opted into a **distributed
system** and its taxes:
- **Partial failure** — a network call can succeed on the server but lose the
  response; the caller can't tell if it happened. This one fact spawns retries,
  idempotency keys, and sagas.
- **No distributed transactions (in practice)** — "reserve the unit AND write the
  outbox event" is ONE Postgres transaction in our monolith. Split those into two
  services with two DBs and that single `BEGIN…COMMIT` becomes two-phase commit or
  a saga — the exact thing that sinks naive microservices designs.
- **Operational multiplier** — N services = N pipelines, N dashboards, service
  discovery, distributed tracing to answer "where did the request die."

### Why the monolith is *correct* here (not just easier)
1. **Our core invariant is a database transaction, and transactions don't cross
   service boundaries.** The heart of the project (P13, P17, P19) is *change state
   and record the event atomically.* Splitting services replaces our cleanest
   mechanism with the hardest problem in distributed systems — for no benefit.
2. **Our modules are coupled by the domain, not independent.**
   `incident → dispatch → responder → outbox` is one causal chain that fires in
   milliseconds during a single dispatch. Microservices pay off when modules scale
   and deploy *independently*; ours spike and change *together* (a disaster hits
   all of them at once). No independence to exploit.
3. **It's the honest maturity answer.** Shopify and others run large modular
   monoliths precisely because premature microservices is a top cause of failure.
   "Clean seams, extract later if a real reason appears" is the senior answer.

### The honest costs (no false confidence)
- **No independent scaling** — CPU-bound scoring would force scaling the whole
  binary.
- **Blast radius** — one package panicking can take the process down (mitigated by
  the `recover` middleware in P5, but a memory leak in `audit` *can* kill
  `dispatch`).
- **Discipline is manual** — "no package touches another's tables" is a convention
  the compiler only partly enforces (via `internal/`). It can rot if we're sloppy;
  that's why the P1 layout matters.

### Built for extraction from day one — what it takes to pull out `dispatch`
The point of a *modular* monolith (vs a big ball of mud) is that seams sit where a
network could later go. To extract `dispatch` into its own service:
1. **Sever the shared database.** `dispatch` needs its own DB; incident data it
   depends on must arrive via API calls or **events** — which is exactly why we
   publish incident state changes to Kafka (P19–P22). The extracted service would
   *consume* those events instead of joining the table.
2. **Replace in-process calls with a network contract** (gRPC/HTTP). We keep those
   call sites narrow and interface-typed now so the swap is mechanical.
3. **The killer: the reservation transaction can no longer span services.** Today
   "reserve unit + write outbox event" is one txn because both live in one DB.
   Split them and you need a **saga** (local transactions + compensating
   rollbacks) or eventual consistency. **Answer:** keep the unit-reservation txn
   *local* to the dispatch service (the atomic invariant stays inside one DB) and
   make the incident-side update eventually consistent via the outbox events it
   already emits. The boundary was designed so the transaction never needed to
   cross it.
4. **Idempotency becomes mandatory** — network retries mean a call can arrive
   twice; the idempotent-consumer work (P22) is what makes extraction safe.

The pattern: **the outbox + idempotency work we do anyway (good practice inside a
monolith) is precisely the machinery that makes future extraction possible.**

🎯 **"Why not microservices?"** →
> My core correctness guarantee — no double-booking a rescue unit — is a
> single-database transaction. Microservices would split that across databases and
> force sagas / two-phase commit, trading my simplest, most-defensible mechanism
> for the hardest problem in distributed systems, with no scaling or ownership
> benefit — my modules spike and change together. So I built a modular monolith:
> logical seams drawn where a network could later go, state changes already
> published as events. If a real reason to extract `dispatch` appeared, the events
> are already there; the one hard consequence is that the reservation transaction
> can't cross the new boundary, which is why I kept that transaction entirely
> inside what would become the dispatch service.

🎯 **"Okay, extract dispatch — what breaks?"** → walk the 4-point checklist above,
land on point 3 (transaction can't span the split → saga).

---

## P1 — Repo skeleton & config

### What this phase solves
Nothing "runs" yet, but P1 fixes two things expensive to change later:
1. **Where the module seams live** — the folder layout *is* the "modular" in
   modular monolith. Wrong here and the extract-later story dies.
2. **How the process starts and — the interesting part — how it *stops*.** A
   disaster backend killed mid-dispatch that loses an in-flight reservation is
   exactly what this project exists to prevent. Even the skeleton shuts down
   *gracefully.*

### Concept 1 — Package layout & the `internal/` enforcement
```
cmd/crisislink/main.go   ← the ONLY main(). Composition root: wires modules, owns lifecycle.
internal/                ← compiler FORBIDS import from outside this module
  auth/ incident/ dispatch/ shelter/ responder/ outbox/ audit/
pkg/                     ← shared helpers: httpx postgis kafkax redisx metrics
migrations/ deploy/ docs/
```
- **`internal/` is a real compiler rule, not a convention.** Go physically refuses
  imports of `internal/...` from outside the module — the first line of defense for
  module encapsulation. Modules import each other freely today (one binary), but
  nothing external can reach in, which matters the day we extract one.
- **`cmd/crisislink/` holds the only `main`.** Every module is a *library*; `main`
  is the *composition root* that constructs them and injects dependencies (DB pool,
  Kafka client). This keeps modules testable in isolation and swappable for a
  network client later. Modules must never grab global singletons — that's what
  makes a monolith un-extractable.
- **Alternative rejected:** flat layout (everything in root/one pkg). No
  compiler-enforced wall between modules → "no package touches another's tables"
  becomes hope. We pay the small `internal/` cost for enforceable seams.
- Each module carries a `doc.go` stating its single responsibility and **which
  table it solely owns** — the encapsulation rule written down.

### Concept 2 — Config loading (kept short)
Load config from **environment variables** into a typed `Config` struct at startup
and **fail fast** if a required one is missing/invalid.
- **Env vars, not a checked-in file:** twelve-factor — same binary across
  dev/CI/prod, secrets never touch git, Docker/k8s inject env naturally.
- **Fail fast at boot:** a missing `DATABASE_URL` should kill the process at
  startup, loudly — not surface as a nil-pointer panic on the first request during
  a real disaster. "Crash at boot" is a feature.
- **Alternative rejected:** the `viper` library — powerful but magic; you'd have to
  explain its precedence rules in an interview. We use the **standard library**
  (`os.LookupEnv` + a tiny typed loader). Everything visible, everything
  defensible.

### Concept 3 — Graceful shutdown ⭐ (the one systems idea in P1)
**Failure mode:** the server is mid-request (say committing a unit reservation)
when the orchestrator stops the instance. The OS sends **`SIGTERM`** ("please
stop, you have a few seconds"); ignore it and after a grace period comes
**`SIGKILL`** — immediate, unstoppable. If SIGKILL lands mid-transaction:
```
BEGIN
SELECT unit FOR UPDATE      -- lock held
UPDATE unit SET status='reserved'
                            -- 💥 SIGKILL: connection dies, txn ROLLS BACK
COMMIT                      -- never runs
```
The transaction rolls back cleanly (Postgres aborts on a dropped connection — the
DB protecting us), but the **client got no response and can't tell if it
happened**. Ambiguity in a dispatch system is the enemy.

**The fix — graceful shutdown:** on SIGTERM, instead of dying:
1. **Stop accepting new connections** (close the listener).
2. **Let in-flight requests finish** — bounded by a timeout (~15s). This is
   *draining*.
3. **Close resources in reverse order of creation** — HTTP server first, then
   Kafka, then the DB pool LAST (so draining handlers still have a working DB).
4. If draining exceeds the timeout, **force exit** — a hung request can't hold a
   deploy hostage forever.

Mechanism in Go: `signal.NotifyContext` → a `context.Context` that **cancels** on
SIGTERM, and `http.Server.Shutdown(ctx)` which does the stop-accepting + drain
dance. **Insight: shutdown is just context cancellation propagating outward** —
the same primitive that cancels a slow DB query cancels the whole server.

**Real-life mapping:** *finish the rescue you already dispatched before you go off
shift — don't drop the radio mid-call.*

**Alternative rejected:** no handler, die on SIGKILL. Zero code, but every
deploy/restart becomes a dice-roll on in-flight operations — unacceptable for a
system whose whole purpose is not dropping the ball. ~30 lines = cheapest
insurance in the project.

### What was built
- `go mod init github.com/AtharvGupta360/CrisisLink`, the folder tree, `git init`.
- `internal/config/config.go` — typed `Config`, `Load()` reads env, fails fast on a
  bad `SHUTDOWN_TIMEOUT`, `envOr` default helper.
- `cmd/crisislink/main.go` — `run()` pattern (single `os.Exit` site), `/healthz`,
  full `signal.NotifyContext` + `srv.Shutdown` graceful lifecycle, `slog` JSON logs.
- Per-module `doc.go` files declaring ownership; `.gitignore`; `README.md`.

### Run it
```bash
go run ./cmd/crisislink
curl localhost:8080/healthz        # -> ok
# Ctrl+C -> logs "draining" then "shutdown complete"
```

### 🎯 Interview drills for P1
- *"How does your service shut down cleanly on a deploy?"* → SIGTERM → stop
  accepting → drain in-flight with a timeout → close resources in reverse order.
  Bonus: reservation txns roll back safely if interrupted (Postgres aborts on a
  dropped connection).
- *"Why `internal/`?"* → compiler-enforced module boundary; encapsulation that
  survives to the extraction story.
- *"Config in git?"* → no; env vars, fail-fast at boot, secrets out of the repo.

### ✅ Self-check before P2 — rebuild `run()` from a blank file
Without looking, write `main.go`'s `run()`: config load → signal context → server
in a goroutine → `select` on serverErr/ctx.Done → `Shutdown` with a fresh timeout
context. Then diff against the real file. You should be able to say *why* each of:
the buffered channel, the fresh shutdown context, and the `errors.Is(err,
http.ErrServerClosed)` filter exists.

---

## Open checkpoints

You owe answers to these before we start **P2** (framing gate from the intro):

- ✅ **Q1.** Why does splitting a module into its own database turn our one clean
  `BEGIN…COMMIT` into a hard problem — and what's the name of the pattern you're
  forced into?
- ✅ **Q2.** Beyond reliability, what's the *second* payoff of building the outbox +
  idempotency inside the monolith?
- ✅ **P1 self-check** (above): rebuild `run()` from blank and justify the three
  non-obvious lines.
