DROP INDEX IF EXISTS idx_outbox_dead;
DROP INDEX IF EXISTS idx_outbox_pending;

ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS dead_at,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS attempts;

-- Restore the original partial index so the schema round-trips exactly.
CREATE INDEX idx_outbox_unpublished ON outbox_events (id) WHERE published_at IS NULL;
