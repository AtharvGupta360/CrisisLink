-- GiST spatial index for radius search. It is built on the geography CAST of
-- location because our queries use ST_DWithin(location::geography, ...) for
-- meter-accurate distance — the index expression MUST match the query expression
-- or the planner won't use it.
--
-- This is the P8 payoff: with this index, the radius query goes from a full
-- sequential scan (compute distance for every row) to an index scan (descend only
-- into bounding boxes that can match).
CREATE INDEX idx_incidents_location_gix ON incidents USING GIST ((location::geography));
