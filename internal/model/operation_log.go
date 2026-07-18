package model

import "time"

// OperationLog records an auditable user or system operation.
type OperationLog struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id,omitempty"`
	Username   string    `json:"username,omitempty"`
	ActorType  string    `json:"actor_type"` // user or system
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	ResourceID string    `json:"resource_id,omitempty"`
	Status     string    `json:"status"` // success or failure
	Message    string    `json:"message,omitempty"`
	Metadata   string    `json:"metadata,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
