package action

import "fmt"

// MaxActionIDBytes: maximum size of action ID in config files
const MaxActionIDBytes = 1024

func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("action ID must not be empty")
	}
	if len(id) > MaxActionIDBytes {
		return fmt.Errorf("action ID is %d bytes; maximum is %d", len(id), MaxActionIDBytes)
	}
	return nil
}

// Notification is the complete daemon-to-client protocol. We only send
// actions IDs, not raw keyboard input events.
type Notification struct {
	Action string `json:"action"`
}
