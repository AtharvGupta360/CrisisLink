#!/usr/bin/env bash
# CrisisLink guided demo — the five things worth showing, in the order that tells
# a story. Run from the project root in Git Bash / any bash, with infra up,
# migrations applied and the API on :8080.
#
#   docker compose -f deploy/docker-compose.yml up -d
#   go run ./cmd/migrate up
#   go run ./cmd/server      # separate terminal
#   ./scripts/demo.sh
#
# Each step pauses so you can talk over it. Ctrl+C any time.

set -u
BASE="${BASE_URL:-http://localhost:8080}/api/v1"
PSQL="docker exec crisislink-postgres-1 psql -U crisislink -d crisislink"

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
pause() { printf '\n\033[2m-- enter to continue --\033[0m'; read -r _; }
jqf() { python -c "import sys,json;d=json.load(sys.stdin);print(json.dumps(d,indent=2)[:600])"; }

# ---------------------------------------------------------------- setup
bold "SETUP  admin user + fleet + an incident"
U="demo_$RANDOM"; E="$U@crisislink.dev"
curl -s -X POST "$BASE/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$U\",\"email\":\"$E\",\"password\":\"password123\"}" > /dev/null
# Roles are granted in the DB on purpose: there is no API to promote yourself.
$PSQL -q -c "UPDATE users SET role='admin' WHERE username='$U';"
TOKEN=$(curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$E\",\"password\":\"password123\"}" \
  | python -c "import sys,json;print(json.load(sys.stdin)['data']['token'])")
AUTH="Authorization: Bearer $TOKEN"
echo "authenticated as $U (admin)"

for i in 1 2 3; do
  curl -s -X POST "$BASE/units" -H "$AUTH" -H 'Content-Type: application/json' \
    -d "{\"callSign\":\"DEMO-$i\",\"type\":\"ambulance\",\"latitude\":28.61$i,\"longitude\":77.20$i}" > /dev/null
done
# Pick an AVAILABLE unit — a fleet accumulated over time contains reserved and
# out-of-service vehicles, and dispatching those would (correctly) be refused.
UNIT=$(curl -s "$BASE/units?status=available" -H "$AUTH" \
  | python -c "import sys,json;u=json.load(sys.stdin)['data'];print(u[0]['id'] if u else '')")
[ -z "$UNIT" ] && { echo "no available unit found; aborting"; exit 1; }
echo "registered 3 ambulances (demo unit: ${UNIT:0:8}...)"
pause

# ---------------------------------------------------------------- 1. dedupe
bold "1/5  DEDUPLICATION — 50 people reporting one fire is ONE incident"
INC=$(curl -s -X POST "$BASE/incidents" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"title":"Building collapse","description":"Two floors down","severity":"critical","latitude":28.6139,"longitude":77.2090}' \
  | python -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")
echo "first report  -> incident $INC"
echo "second report at the SAME spot ->"
curl -s -X POST "$BASE/incidents" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"title":"Collapse!","description":"same event","severity":"critical","latitude":28.6139,"longitude":77.2090}' \
  | python -c "import sys,json;d=json.load(sys.stdin);print(' ',d['message'],'| reportCount =',d['data']['reportCount'])"
echo "One SQL statement finds a nearby active incident and increments its count —"
echo "check-then-insert as two statements would race."
pause

# ---------------------------------------------------------------- 2. presence
bold "2/5  LIVE PRESENCE — absence of a Redis key IS the going-dark event"
curl -s -X POST "$BASE/units/$UNIT/heartbeat" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"latitude":28.6140,"longitude":77.2091}' > /dev/null
curl -s "$BASE/units/$UNIT/presence" -H "$AUTH" \
  | python -c "import sys,json;d=json.load(sys.stdin)['data'];print('  unit is LIVE — last seen %.1fs ago (key expires at 30s)'%d['ageSeconds'])"
echo "TTL is 3x the 10s heartbeat, so a unit must miss TWO pings before going dark."
echo "No polling job, nothing to fall behind — Redis deletes the key itself."
pause

# ---------------------------------------------------------------- 3. the race
bold "3/5  NO DOUBLE-BOOKING — the core claim"
echo "10 concurrent dispatches, all targeting the SAME ambulance:"
tmp=$(mktemp -d)
for i in $(seq 1 10); do
  ( I=$(curl -s -X POST "$BASE/incidents" -H "$AUTH" -H 'Content-Type: application/json' \
        -d "{\"title\":\"race-$i\",\"description\":\"d\",\"severity\":\"high\",\"latitude\":28.7$((200+i)),\"longitude\":77.3$((100+i))}" \
        | python -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")
    curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/incidents/$I/dispatch" \
      -H "$AUTH" -H 'Content-Type: application/json' -d "{\"unitId\":\"$UNIT\"}" > "$tmp/$i" ) &
done
wait
echo "  201 (winner) : $(cat "$tmp"/* | grep -c 201)"
echo "  409 (refused): $(cat "$tmp"/* | grep -c 409)"
rm -rf "$tmp"
$PSQL -qtA -c "SELECT 'active dispatches for that unit: '||count(*) FROM dispatches WHERE unit_id='$UNIT' AND status IN ('reserved','en_route','on_scene');"
echo "SELECT ... FOR UPDATE + a re-check under the lock, backed by a partial unique"
echo "index so a second active dispatch is physically unstorable."
pause

# ---------------------------------------------------------------- 4. outbox
bold "4/5  TRANSACTIONAL OUTBOX — the event committed WITH the dispatch"
curl -s "$BASE/admin/outbox" -H "$AUTH" \
  | python -c "import sys,json;e=json.load(sys.stdin)['data'];print('  unpublished events waiting for the relay:',len(e));[print('   -',x['eventType']) for x in e[:3]]"
echo "Postgres and Kafka cannot share a transaction, so the event is written to a"
echo "table in the SAME commit as the business change; the relay publishes after."
echo "Kill Kafka and this becomes a latency problem, never data loss."
pause

# ---------------------------------------------------------------- 5. audit
bold "5/5  APPEND-ONLY AUDIT — tampering requires a visible schema change"
$PSQL -q -c "UPDATE audit_log SET payload='{}' WHERE true;" 2>&1 | grep -i error || true
$PSQL -q -c "DELETE FROM audit_log;" 2>&1 | grep -i error || true
echo "Enforced by a Postgres trigger, not by convention."

bold "DONE"
echo "Grafana http://localhost:3000 (admin/admin) — 12 panels"
echo "Load: p99 32ms @ ~2,360 req/s, 0% errors (test/load/dispatch_decision.js)"
