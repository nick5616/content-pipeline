package sanitize

// Finding is one thing that survived sanitizing and shouldn't have.
type Finding struct {
	Tag    string `json:"tag"`             // exiftool "Group:Tag", or a structural description
	Value  string `json:"value,omitempty"` // truncated
	Reason string `json:"reason"`
}
