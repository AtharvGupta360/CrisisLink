-- Partial GiST index for the nearest-open-shelter KNN search (P17). Same pattern
-- as the units' idx_units_available_gix: index only the rows we search (open
-- shelters), so the `<->` operator returns them nearest-first via an index scan.
-- The query's WHERE must include status='open' to match this predicate; the extra
-- occupancy < capacity filter is applied on top.
CREATE INDEX idx_shelters_open_gix
    ON shelters USING GIST ((location::geography))
    WHERE status = 'open';
