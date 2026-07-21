-- processed_events: the INBOX (dedup) table — the mirror image of the outbox.
-- Kafka delivers at-least-once, so a consumer can see the same event twice. Before
-- doing any work, a consumer inserts (consumer, event_id) here; a conflict means
-- "already handled" and the work is skipped.
--
-- The idempotency key is the OUTBOX EVENT ID: stable across redeliveries (a Kafka
-- offset is not — a republished event gets a new offset but the same event id).
--
-- Keyed by (consumer, event_id) so independent consumers each get their own dedup
-- ledger: two different consumers must both be allowed to process the same event.
--
-- CRITICAL: this marker is inserted in the SAME TRANSACTION as the side effect
-- below. Separate transactions would either lose the effect (marked, then crash) or
-- duplicate it (effect, then crash before marking).
CREATE TABLE processed_events (
    consumer     TEXT        NOT NULL,
    event_id     BIGINT      NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

-- notifications: the demonstrable SIDE EFFECT of consuming an event. Duplicates
-- here would be plainly visible (two alerts for one dispatch) — which is exactly
-- what the inbox prevents.
CREATE TABLE notifications (
    id         BIGSERIAL   PRIMARY KEY,
    event_id   BIGINT      NOT NULL,   -- the outbox event that caused this
    event_type TEXT        NOT NULL,
    message    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_event ON notifications (event_id);
