-- victims: people needing shelter during a disaster. Intake (P17) registers them
-- with a location; assignment to a shelter (P18) sets shelter_id and status.
--
--   status     registered -> sheltered -> discharged (lifecycle).
--   shelter_id NULL until the victim is placed in a shelter (P18). "Not yet
--              assigned" is modeled as absence (NULL), not a sentinel value.
--   location   geometry(Point,4326): where the victim is, feeds the
--              nearest-open-shelter search.
CREATE TABLE victims (
    id         UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT          NOT NULL,
    status     TEXT          NOT NULL DEFAULT 'registered'
                             CHECK (status IN ('registered','sheltered','discharged')),
    notes      TEXT          NOT NULL DEFAULT '',
    shelter_id UUID          REFERENCES shelters(id),   -- NULL until assigned (P18)
    location   geometry(Point, 4326) NOT NULL,
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_victims_status ON victims (status);
