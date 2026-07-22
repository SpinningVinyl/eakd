package action

// Notification is the complete daemon-to-client protocol. Action is an
// opaque identifier interpreted only by the user-side client.
type Notification struct {
	Action string `json:"action"`
}
