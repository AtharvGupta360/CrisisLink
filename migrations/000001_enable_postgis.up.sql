-- Enable PostGIS: adds geometry/geography types and GiST spatial-index support.
-- Must exist before any spatial column or index in later phases (P7+). IF NOT
-- EXISTS keeps the migration safe to run against a DB where the extension already
-- exists (e.g. the postgis Docker image may pre-create it).
CREATE EXTENSION IF NOT EXISTS postgis;
