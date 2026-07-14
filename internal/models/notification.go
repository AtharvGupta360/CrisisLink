package models

import "time"

// Notification is the side effect of consuming an event — the thing a duplicate
// delivery would visibly double up if the consumer weren't idempotent.
type Notification struct {
	ID        int64     `json:"id"`
	EventID   int64     `json:"eventId"` // the outbox event that caused this
	EventType string    `json:"eventType"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}
