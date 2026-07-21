-- audit_log: an append-only record of every domain event, built by its own Kafka
-- consumer group.
--
-- WHY A SECOND CONSUMER GROUP rather than another write inside the existing one:
-- Kafka consumer groups are INDEPENDENT SUBSCRIPTIONS. Each group gets its own copy
-- of every message and tracks its own offsets, so the auditor can be added, removed,
-- or replayed from the beginning without touching the notifier. That is the whole
-- point of a log-structured broker: new consumers are additive, and a slow or broken
-- auditor cannot delay notifications.
--
--   event_id     the outbox event id — the idempotency key. Paired with the
--                consumer name in processed_events, it is what makes replay safe.
--   occurred_at  when the event HAPPENED (from the payload).
--   recorded_at  when the auditor SAW it. The gap between them is pipeline lag,
--                and keeping both is what lets an auditor distinguish "this took
--                two hours to happen" from "this took two hours to be recorded".
CREATE TABLE audit_log (
    id             BIGSERIAL   PRIMARY KEY,
    event_id       BIGINT      NOT NULL,
    event_type     TEXT        NOT NULL,
    aggregate_type TEXT        NOT NULL,
    aggregate_id   UUID        NOT NULL,
    payload        JSONB       NOT NULL,
    occurred_at    TIMESTAMPTZ NOT NULL,
    recorded_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "What happened to THIS incident/unit/dispatch?" — the question an auditor
-- actually asks. Ordered by id so the trail reads chronologically.
CREATE INDEX idx_audit_aggregate ON audit_log (aggregate_type, aggregate_id, id);
CREATE INDEX idx_audit_event_type ON audit_log (event_type, id);

-- One row per (consumer, event) is already guaranteed by processed_events, but this
-- makes the audit table's own invariant explicit and independent of that ledger.
CREATE UNIQUE INDEX idx_audit_event_id ON audit_log (event_id);

-- IMMUTABILITY, ENFORCED BY THE DATABASE.
--
-- An audit log that can be edited is not an audit log. Making it append-only by
-- convention means it holds right up until the moment someone needs it not to —
-- which is exactly when it matters. This trigger makes UPDATE and DELETE raise,
-- so tampering requires dropping the trigger, which is itself a visible schema
-- change rather than a quiet row edit.
CREATE OR REPLACE FUNCTION audit_log_is_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_update
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_is_append_only();
