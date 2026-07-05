-- dispatches: the assignment of a rescue unit to an incident. This row is created
-- by the P13 reservation transaction, which is the concurrency core of the system.
--
-- No double-booking is guaranteed TWO ways (defense in depth):
--   1. Application logic — the reservation runs in one transaction that does
--      SELECT ... FOR UPDATE on the unit row, re-checks it is still 'available'
--      under the lock, then flips it to 'reserved'. A concurrent dispatcher for
--      the same unit blocks on the lock, then loses the re-check.
--   2. Storage invariant — the partial UNIQUE index below makes a second ACTIVE
--      dispatch for the same unit physically impossible (raises 23505), even if
--      the application logic were wrong. The rule lives in the schema, not in code.
CREATE TABLE dispatches (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id UUID        NOT NULL REFERENCES incidents(id),
    unit_id     UUID        NOT NULL REFERENCES units(id),
    status      TEXT        NOT NULL DEFAULT 'reserved'
                            CHECK (status IN ('reserved','en_route','on_scene','completed','cancelled')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A unit may hold at most ONE active dispatch. Partial: completed/cancelled
-- dispatches don't count, so a freed unit can be dispatched again.
CREATE UNIQUE INDEX idx_dispatches_one_active_per_unit
    ON dispatches (unit_id)
    WHERE status IN ('reserved','en_route','on_scene');

-- Fast lookup of all dispatches for an incident.
CREATE INDEX idx_dispatches_incident ON dispatches (incident_id);
