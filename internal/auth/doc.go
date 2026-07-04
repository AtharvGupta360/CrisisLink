// Package auth owns user identity: password hashing and JWT issue/verify (P4),
// plus RBAC role enforcement (P6). It is the sole owner of the `users` table —
// no other package reads or writes it directly. Built out from P4 onward.
package auth
