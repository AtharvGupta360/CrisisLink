-- Partial GiST index for KNN dispatch search. Dispatch only ever searches
-- AVAILABLE units, so we index just those — smaller index, and the KNN scan
-- (ORDER BY location::geography <-> point) walks only available units nearest
-- first. The query's WHERE status='available' MUST match this predicate for the
-- planner to use the index.
CREATE INDEX idx_units_available_gix ON units USING GIST ((location::geography))
    WHERE status = 'available';
