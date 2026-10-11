// Package session is the process-wide session state shared by modules; it is read-only.
package session

import "uuid"

var id = uuid.NewV7()

// ID is this process's session: a UUIDv7, so IDs sort by start time.
func ID() uuid.UUID { return id }
