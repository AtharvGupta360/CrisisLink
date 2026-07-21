-- outbox_events: the transactional outbox. Domain changes write an event row here
-- IN THE SAME TRANSACTION as the change itself, so the event and the change commit
-- atomically (solving the dual-write problem: you can't atomically commit a DB
-- change AND publish to Kafka across two systems). A separate relay (P20) reads
-- unpublished rows and publishes them, then stamps published_at.
--
--   id           BIGSERIAL — monotonic, so events relay in creation order (a random
--                UUID would lose ordering).
--   aggregate_*  which domain entity the event is about (e.g. dispatch / <uuid>).
--   event_type   e.g. 'dispatch.created', 'victim.assigned'.
--   payload      JSONB event body.
--   published_at NULL until the relay publishes it (P20).
CREATE TABLE outbox_events (
    id             BIGSERIAL     PRIMARY KEY,
    aggregate_type TEXT          NOT NULL,
    aggregate_id   UUID          NOT NULL,
    event_type     TEXT          NOT NULL,
    payload        JSONB         NOT NULL,
    created_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ
);

-- The relay polls "WHERE published_at IS NULL ORDER BY id". This partial index
-- covers exactly that hot query and shrinks as events get published.
CREATE INDEX idx_outbox_unpublished ON outbox_events (id) WHERE published_at IS NULL;
