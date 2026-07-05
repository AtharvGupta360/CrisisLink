-- incidents: a citizen-reported, geolocated event (the domain's heart). Owned by
-- the incident repository/service.
--
-- location is geometry(Point, 4326): a point in WGS84 lat/lng (GPS coords). We
-- store geometry (compact, GiST-indexable) and cast to geography when we need
-- real-world meters (P8 radius search / dispatch).
--
-- severity and status carry CHECK constraints as a DB-level backstop; the
-- service layer is the primary guard (and owns the status state machine).
--
-- NOTE: the GiST spatial index on location is intentionally added in P8, so we
-- can compare the query plan (EXPLAIN ANALYZE) with vs without it.
CREATE TABLE incidents (
    id          UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    reporter_id UUID          NOT NULL REFERENCES users(id),
    title       TEXT          NOT NULL,
    description TEXT          NOT NULL DEFAULT '',
    severity    TEXT          NOT NULL CHECK (severity IN ('low','medium','high','critical')),
    status      TEXT          NOT NULL DEFAULT 'reported'
                              CHECK (status IN ('reported','verified','dispatched','resolved','cancelled')),
    location    geometry(Point, 4326) NOT NULL,
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);

-- Common access pattern: newest incidents first.
CREATE INDEX idx_incidents_created_at ON incidents (created_at DESC);
