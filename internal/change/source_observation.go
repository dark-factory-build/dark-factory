package change

// SourcePath is an observed Git change, never an inference from task text.
type SourcePath struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
}

type SourceObservation struct {
	Base        string       `json:"base"`
	Target      string       `json:"target,omitempty"`
	Head        string       `json:"head"`
	Kind        string       `json:"kind"`
	Fingerprint string       `json:"fingerprint,omitempty"`
	Paths       []SourcePath `json:"paths"`
	Omitted     int          `json:"omitted"`
	Reason      string       `json:"reason,omitempty"`
	ObservedAt  int64        `json:"observed_at"`
}
