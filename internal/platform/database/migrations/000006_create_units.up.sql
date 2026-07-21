-- units: the fleet of rescue resources. The heart of the dispatch subsystem.
--
--   status   is the CONTENDED field — the reservation transaction (P13) locks a
--            unit's row and flips available -> reserved; two dispatchers must not
--            both take the same unit. CHECK constrains the allowed values.
--   type     specialization, used by the scoring function (P12) to prefer the
--            right kind of unit for an incident.
--   location feeds the KNN nearest-units query (P11).
--
-- NOTE: the GiST spatial index on location is added in P11 (with the KNN query);
-- the status index is here because we constantly filter by "available".
CREATE TABLE units (
    id         UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    call_sign  TEXT          NOT NULL UNIQUE,               -- human id, e.g. "AMB-07"
    type       TEXT          NOT NULL CHECK (type IN ('ambulance','fire','rescue','police')),
    status     TEXT          NOT NULL DEFAULT 'available'
                             CHECK (status IN ('available','reserved','en_route','on_scene','out_of_service')),
    location   geometry(Point, 4326) NOT NULL,
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_units_status ON units (status);
