// Package presence tracks which rescue units are ALIVE and where they are right
// now, using self-expiring Redis keys.
//
// The problem it solves: a unit's row in Postgres says status='available', but that
// says nothing about whether the unit is REACHABLE. A dead phone, a crashed app or a
// tunnel leaves the row untouched, so dispatch will happily reserve a unit that
// isn't there. Postgres answers "what is this unit"; presence answers "is it
// responding, and where is it".
//
// Why not a last_seen_at column on units? Two reasons, and the second is the
// interesting one:
//
//  1. WRITE AMPLIFICATION. 500 units pinging every 10s is 50 writes/second into the
//     durable table — WAL, dead tuples, vacuum pressure — to store data that is
//     worthless 30 seconds later. Correct, but the wrong storage for the access
//     pattern.
//
//  2. DETECTION MECHANICS. With a column, "offline" is COMPUTED ON READ: nothing
//     happens at the moment a unit dies, so you must poll (or run a reaper job) to
//     notice, and detection lags your poll interval. With a TTL, the expiry IS the
//     transition — Redis performs it for us, with no scheduler and no lag. Absence
//     of the key is the going-dark event.
//
// Losing this data is acceptable BY DESIGN: presence is ephemeral, so a Redis
// restart costs us positions, never units, and every live unit repopulates its key
// within one heartbeat interval.
package presence

import "time"

// Presence is a unit's most recent self-report: where it was and how long ago.
//
// AgeSeconds is derived on read rather than stored — a stored age would start
// lying the instant it was written. The caller usually cares about "how stale is
// this" more than the absolute timestamp.
type Presence struct {
	UnitID     string    `json:"unitId"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	LastSeen   time.Time `json:"lastSeen"`
	AgeSeconds float64   `json:"ageSeconds"`
}

// stored is the on-the-wire shape in Redis. It is deliberately smaller than
// Presence: UnitID is already in the key, and AgeSeconds is derived. Keeping the
// payload minimal matters when it is rewritten every few seconds per unit.
type stored struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
	TS  int64   `json:"ts"` // unix seconds of the heartbeat
}
