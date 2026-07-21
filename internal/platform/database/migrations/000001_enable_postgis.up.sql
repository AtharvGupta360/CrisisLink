-- Enable PostGIS: geometry/geography types + GiST spatial-index support. Must
-- exist before any spatial column/index (P7+). IF NOT EXISTS keeps it safe on a
-- DB where the extension already exists.
CREATE EXTENSION IF NOT EXISTS postgis;
