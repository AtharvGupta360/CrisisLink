// Package audit is an idempotent Kafka consumer that records an immutable audit
// log of every published event, deduping on event id to survive at-least-once
// redelivery (P22). Sole owner of the `audit_log` table. Built out from P22.
package audit
