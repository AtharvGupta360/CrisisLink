// Package responder owns the rescue-unit registry (CRUD, status enum,
// specialization) and live presence/position via Redis heartbeats + GEO
// (P10, P23–P24). Sole owner of the `units` table; Redis holds ephemeral
// position, Postgres holds the durable source of truth. Built out from P10.
package responder
