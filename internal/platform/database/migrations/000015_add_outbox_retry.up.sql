-- Retry + dead-letter state for the outbox relay.
--
-- The bug this fixes: the relay selected "WHERE published_at IS NULL ORDER BY id"
-- and aborted the batch on the first publish failure. A row that can NEVER publish
-- (oversized payload, revoked topic ACL, a serialization bug) was therefore
-- selected first every tick, failed every tick, and starved every event behind it.
-- One poison row blocked the entire outbox forever — the same head-of-line problem
-- the consumer's DLQ already solves on the other side of the pipe.
--
--   attempts        how many publish attempts this row has survived.
--   last_error      why the most recent attempt failed (operator visibility).
--   next_attempt_at "don't try again before this". Exponential backoff writes it,
--                   and because the relay filters on it, a failing row REMOVES
--                   ITSELF from the selection window instead of blocking the head.
--   dead_at         terminal state after the retry budget is exhausted. Excluded
--                   from selection forever, but kept (not deleted) so an operator
--                   can inspect and replay it.
ALTER TABLE outbox_events
    ADD COLUMN attempts        INT         NOT NULL DEFAULT 0,
    ADD COLUMN last_error      TEXT,
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN dead_at         TIMESTAMPTZ;

-- Replace the old partial index: the relay's hot query now also filters on
-- dead_at IS NULL and next_attempt_at <= now(). Including next_attempt_at in the
-- index lets Postgres skip backed-off rows without reading them.
DROP INDEX IF EXISTS idx_outbox_unpublished;

CREATE INDEX idx_outbox_pending ON outbox_events (next_attempt_at, id)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Dead rows are rare but queried by operators; a tiny partial index keeps that
-- lookup from scanning the whole (large) outbox table.
CREATE INDEX idx_outbox_dead ON outbox_events (id) WHERE dead_at IS NOT NULL;
