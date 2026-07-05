-- report_count tracks how many times the same event has been reported. It starts
-- at 1 (the first report creates the incident); each deduplicated report bumps it.
-- Useful later as a corroboration/urgency signal for prioritization.
ALTER TABLE incidents ADD COLUMN report_count INT NOT NULL DEFAULT 1;
