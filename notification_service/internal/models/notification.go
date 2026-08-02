package models

import "time"

// Notification represents a dispatched notification record.
type Notification struct {
	ID        string     `json:"id" db:"id"`
	EventID   string     `json:"event_id" db:"event_id"`
	Channel   string     `json:"channel" db:"channel"` // email | webhook
	Recipient string     `json:"recipient" db:"recipient"`
	Message   string     `json:"message" db:"message"`
	Status    string     `json:"status" db:"status"` // pending | sent | failed
	SentAt    *time.Time `json:"sent_at,omitempty" db:"sent_at"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	Inserted  bool       `json:"-" db:"-"`
}
