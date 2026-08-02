package models

import "time"

// Event represents an inbound domain event to be processed.
type Event struct {
	ID        string                 `json:"id" db:"id"`
	RequestID string                 `json:"request_id" db:"request_id"`
	Type      string                 `json:"type" db:"type"`
	Payload   map[string]interface{} `json:"payload" db:"payload"`
	Status    string                 `json:"status" db:"status"` // pending | processed | failed
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}
