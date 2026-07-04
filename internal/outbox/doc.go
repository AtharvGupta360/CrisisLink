// Package outbox implements the transactional outbox: state changes write an
// event row in the SAME DB transaction as the change (P19), and a relay loop
// polls pending rows with FOR UPDATE SKIP LOCKED and publishes them to Kafka
// at-least-once with retry/backoff/dead-letter (P20–P21). Sole owner of the
// `outbox` table. Built out from P19 onward.
package outbox
