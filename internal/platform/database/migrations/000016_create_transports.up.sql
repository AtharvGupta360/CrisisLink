-- transports: geolocated evacuation vehicles (bus / boat / helicopter / truck)
-- that move survivors away from an incident. This is the THIRD resource shape in
-- the system, and the distinction is worth stating explicitly:
--
--   unit      BOOLEAN counter  -- reserved or not          -> SELECT ... FOR UPDATE
--   shelter   COUNTER, +1      -- occupancy < capacity     -> guarded conditional UPDATE
--   transport COUNTER, +N      -- seats_taken + n <= cap   -> guarded conditional, QUANTITY
--
-- The quantity is the new part. Booking five seats for one family is not five
-- independent bookings: it is all-or-nothing, so the test and the increment must be
-- a single atomic statement. There must be no state where three seats were taken
-- and the fourth failed.
--
--   capacity    total seats (> 0).
--   seats_taken currently booked; the booking flow adds N under the capacity guard.
--   status      available | in_service | out_of_service.
--   version     bumped on every mutation, so an optimistic (CAS) path stays possible.
--
-- CHECK (seats_taken <= capacity) is the STRUCTURAL invariant — defense in depth,
-- exactly like shelters' occupancy check and units' partial unique index. Even a
-- buggy application path physically cannot oversubscribe a vehicle.
CREATE TABLE transports (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    call_sign   TEXT        NOT NULL UNIQUE,
    kind        TEXT        NOT NULL CHECK (kind IN ('bus', 'boat', 'helicopter', 'truck')),
    capacity    INTEGER     NOT NULL CHECK (capacity > 0),
    seats_taken INTEGER     NOT NULL DEFAULT 0
                            CHECK (seats_taken >= 0 AND seats_taken <= capacity),
    status      TEXT        NOT NULL DEFAULT 'available'
                            CHECK (status IN ('available', 'in_service', 'out_of_service')),
    location    geometry(Point, 4326) NOT NULL,
    version     INTEGER     NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transports_status ON transports (status);

-- PARTIAL GiST index: the nearest-transport search only ever looks at available
-- vehicles, so the index only carries those rows. Mirrors idx_units_available_gix
-- and idx_shelters_open_gix.
CREATE INDEX idx_transports_available_gix
    ON transports USING GIST ((location::geography))
    WHERE status = 'available';

-- transport_bookings: the audit record of who claimed which seats and why.
--
-- The seat count lives on BOTH the booking (how many this claim took) and the
-- transport (how many are taken in total). That is deliberate denormalisation: the
-- running total on transports is what the capacity guard can check atomically in a
-- single UPDATE. Deriving it with SUM() over bookings would require a lock or a
-- serializable transaction to be race-free.
CREATE TABLE transport_bookings (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    transport_id UUID        NOT NULL REFERENCES transports (id),
    incident_id  UUID        NOT NULL REFERENCES incidents (id),
    seats        INTEGER     NOT NULL CHECK (seats > 0),
    status       TEXT        NOT NULL DEFAULT 'booked'
                             CHECK (status IN ('booked', 'completed', 'cancelled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transport_bookings_transport ON transport_bookings (transport_id);
CREATE INDEX idx_transport_bookings_incident ON transport_bookings (incident_id);
