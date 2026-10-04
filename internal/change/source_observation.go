package change

// SourcePath is an observed Git change, never an inference from task text.
type SourcePath struct {
	Status   string `json:"status"`
	Resource string `json:"resource,omitempty"`
	Path     string `json:"path"`
	OldPath  string `json:"old_path,omitempty"`
}

type SourceRelationship struct {
	Status   string `json:"status"`
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
	Weight   uint32 `json:"weight"`
}

type SourceObservation struct {
	Base                     string               `json:"base"`
	Target                   string               `json:"target,omitempty"`
	Head                     string               `json:"head"`
	Kind                     string               `json:"kind"`
	Fingerprint              string               `json:"fingerprint,omitempty"`
	Paths                    []SourcePath         `json:"paths"`
	Omitted                  int                  `json:"omitted"`
	Reason                   string               `json:"reason,omitempty"`
	ObservedAt               int64                `json:"observed_at"`
	Relationships            []SourceRelationship `json:"relationships"`
	RelationshipsOmitted     int                  `json:"relationships_omitted"`
	RelationshipsUnavailable string               `json:"relationships_unavailable,omitempty"`
}
