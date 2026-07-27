#!/usr/bin/env bash
# Seeds the four demo accounts the console's login screen offers, plus a fleet,
# shelters, victims and a few incidents so every view has something to show.
#
#   ./scripts/seed_demo.sh
#
# Safe to re-run: accounts are created once and roles are re-applied each time.

set -u
BASE="${BASE_URL:-http://localhost:8080}/api/v1"
PSQL="docker exec crisislink-postgres-1 psql -U crisislink -d crisislink"
PW="password123"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
jget() { python -c "import sys,json;d=json.load(sys.stdin)['data'];print(d$1)"; }

reg() { # username email
  curl -s -X POST "$BASE/auth/register" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"email\":\"$2\",\"password\":\"$PW\"}" > /dev/null
}
login() { # email
  curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$1\",\"password\":\"$PW\"}" | jget "['token']"
}

say "1. accounts"
reg demo_citizen   citizen@crisislink.dev
reg demo_responder responder@crisislink.dev
reg demo_shelter   shelter@crisislink.dev
reg demo_operator  operator@crisislink.dev
reg demo_admin     admin@crisislink.dev
$PSQL -q -c "UPDATE users SET role='citizen'  WHERE email='citizen@crisislink.dev';"
$PSQL -q -c "UPDATE users SET role='operator' WHERE email='operator@crisislink.dev';"
$PSQL -q -c "UPDATE users SET role='admin'    WHERE email='admin@crisislink.dev';"
echo "   created 5 accounts (password: $PW)"

ADMIN=$(login admin@crisislink.dev)
AUTH="Authorization: Bearer $ADMIN"

say "2. fleet"
# Call signs are unique, so a second run gets 409 on create. These demo entities
# need STABLE identities (the responder is bound to a specific unit), so the
# helper creates on first run and looks up the existing row afterwards — making
# the whole script safely re-runnable.
mkunit() { # callsign type lat lng
  local id
  id=$(curl -s -X POST "$BASE/units" -H "$AUTH" -H 'Content-Type: application/json' \
    -d "{\"callSign\":\"$1\",\"type\":\"$2\",\"latitude\":$3,\"longitude\":$4}" \
    | jget "['id']" 2>/dev/null)
  if [ -z "$id" ]; then
    id=$($PSQL -tA -c "SELECT id FROM units WHERE call_sign='$1' LIMIT 1;" | tr -d '\r' | tr -d ' ')
  fi
  echo "$id"
}
U1=$(mkunit DEMO-AMB-1 ambulance 28.6200 77.2100)
U2=$(mkunit DEMO-AMB-2 ambulance 28.6050 77.2200)
U3=$(mkunit DEMO-FIRE-1 fire     28.6300 77.1950)
U4=$(mkunit DEMO-RESC-1 rescue   28.6100 77.2300)
echo "   4 units registered"

say "3. bind the responder to a unit (the ownership claim in their JWT)"
$PSQL -q -c "UPDATE users SET role='responder', unit_id='$U1' WHERE email='responder@crisislink.dev';"
echo "   demo_responder -> DEMO-AMB-1"

say "4. shelters"
mkshelter() { # name capacity lat lng — create-or-find, same reasoning as mkunit
  local id
  id=$(curl -s -X POST "$BASE/shelters" -H "$AUTH" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"capacity\":$2,\"latitude\":$3,\"longitude\":$4}" \
    | jget "['id']" 2>/dev/null)
  if [ -z "$id" ]; then
    id=$($PSQL -tA -c "SELECT id FROM shelters WHERE name='$1' LIMIT 1;" | tr -d '\r' | tr -d ' ')
  fi
  echo "$id"
}
S1=$(mkshelter "Demo Community Hall" 20 28.6180 77.2150)
mkshelter "Demo School Gym" 40 28.6000 77.2250 > /dev/null
$PSQL -q -c "UPDATE users SET role='shelter_manager', shelter_id='$S1' WHERE email='shelter@crisislink.dev';"
echo "   2 shelters; demo_shelter -> Demo Community Hall"

say "5. people awaiting placement"
CIT=$(login citizen@crisislink.dev)
for n in "Asha Kumari" "Ravi Sharma" "Priya Nair" "Dev Menon"; do
  curl -s -X POST "$BASE/victims" -H "Authorization: Bearer $CIT" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$n\",\"latitude\":28.614,\"longitude\":77.209}" > /dev/null
done
echo "   4 victims registered"

say "6. incidents (spread apart so geo-dedupe doesn't merge them)"
mkinc() { # title severity lat lng
  curl -s -X POST "$BASE/incidents" -H "Authorization: Bearer $CIT" -H 'Content-Type: application/json' \
    -d "{\"title\":\"$1\",\"description\":\"demo incident\",\"severity\":\"$2\",\"latitude\":$3,\"longitude\":$4}" > /dev/null
}
mkinc "Building collapse"  critical 28.6190 77.2110
mkinc "Gas leak"           high     28.6060 77.2210
mkinc "Road flooding"      medium   28.6310 77.1960
mkinc "Fallen tree"        low      28.6110 77.2310
echo "   4 incidents reported"

say "DONE — sign in at http://localhost:5173"
cat <<EOF

  citizen@crisislink.dev     citizen          report + track
  responder@crisislink.dev   responder        assignment + broadcast (DEMO-AMB-1)
  shelter@crisislink.dev     shelter_manager  occupancy + admit
  operator@crisislink.dev    operator         the dispatch console
  admin@crisislink.dev       admin            console + admin endpoints

  password for all: $PW
EOF
