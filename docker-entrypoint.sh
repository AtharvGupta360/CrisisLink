#!/bin/sh
# Container entrypoint: bring the schema up to date, then serve.
#
# WHY MIGRATIONS RUN HERE: on a managed host there is no shell to run them from,
# and pre-deploy hooks are a paid feature on most free tiers. Running them at boot
# keeps deployment to a single artifact with no manual step to forget.
#
# This is safe because golang-migrate is IDEMPOTENT — it records the applied
# version in schema_migrations and a second run is a no-op. That matters on a free
# tier, where the instance sleeps and restarts constantly.
#
# THE HONEST CAVEAT: with multiple replicas, two instances could migrate at the
# same time. golang-migrate takes an advisory lock so one waits rather than
# corrupting the schema, but the correct production answer is a separate migration
# job that runs once before the new version rolls out. Documented rather than
# pretended away.
set -e

echo "[entrypoint] applying migrations…"
/app/migrate up

echo "[entrypoint] starting API…"
exec /app/server
