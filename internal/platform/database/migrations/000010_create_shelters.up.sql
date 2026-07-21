-- shelters: geolocated refuges with FINITE CAPACITY. This is the shelters/victims
-- subsystem's core resource, and it differs from a unit in one important way: a
-- unit is binary (available or not), a shelter is a COUNTER — occupancy climbs
-- from 0 toward capacity as victims are assigned (P18).
--
--   capacity   max occupants (> 0).
--   occupancy  current occupants; the P18 assignment increments it under a lock.
--   status     open | closed (admin can close a shelter to new intake).
--   location   geometry(Point,4326), feeds the nearest-shelter search (P17).
--
-- The CHECK (occupancy <= capacity) is the STRUCTURAL no-overflow invariant: the
-- database physically refuses to let occupancy exceed capacity, the shelter analog
-- of the units' partial unique index. P18's assignment logic leans on it as a
-- backstop the same way the reservation leaned on the unique index.
CREATE TABLE shelters (
    id         UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT          NOT NULL UNIQUE,               -- human name, e.g. "Red Cross Hall"
    capacity   INTEGER       NOT NULL CHECK (capacity > 0),
    occupancy  INTEGER       NOT NULL DEFAULT 0
                             CHECK (occupancy >= 0 AND occupancy <= capacity),
    status     TEXT          NOT NULL DEFAULT 'open'
                             CHECK (status IN ('open','closed')),
    location   geometry(Point, 4326) NOT NULL,
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_shelters_status ON shelters (status);
