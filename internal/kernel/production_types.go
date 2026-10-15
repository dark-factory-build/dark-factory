package kernel

// ProductionObservation is a projection of existing external authorities, not
// a workflow. Shared checks and deliveries have one identity and name their PRs.
type ProductionObservation struct {
	Repository   string                  `json:"repository"`
	ObservedAt   int64                   `json:"observed_at"`
	PullRequests []ProductionPullRequest `json:"pull_requests"`
	Checks       []ProductionCheck       `json:"checks"`
	Reviewers    []ProductionReviewer    `json:"reviewers"`
	Deliveries   []ProductionDelivery    `json:"deliveries"`
	Unavailable  string                  `json:"unavailable,omitempty"`
	Overflow     int                     `json:"overflow,omitempty"`
	// DeployedAt is the newest successful production deployment GitHub
	// records for the repository, in Unix ms; 0 when it records production
	// deployments but none succeeded, nil when this refresh did not read them.
	DeployedAt *int64 `json:"deployed_at,omitempty"`
}

type ProductionPullRequest struct {
	Number         uint64           `json:"number"`
	Title          string           `json:"title"`
	URL            string           `json:"url"`
	Head           string           `json:"head"`
	HeadRepository string           `json:"head_repository,omitempty"`
	Branch         string           `json:"branch"`
	Base           string           `json:"base"`
	BaseSHA        string           `json:"base_sha,omitempty"`
	State          string           `json:"state"`
	Merge          string           `json:"merge,omitempty"`
	MergeQueue     string           `json:"merge_queue,omitempty"`
	Mergeable      *bool            `json:"mergeable,omitempty"`
	MergeState     string           `json:"merge_state,omitempty"`
	MergedAt       string           `json:"merged_at,omitempty"`
	Review         ProductionReview `json:"review"`
	NextAction     string           `json:"next_action,omitempty"`
}

type ProductionReview struct {
	Head        string `json:"head"`
	State       string `json:"state"`
	URL         string `json:"url,omitempty"`
	Findings    string `json:"findings,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
}

type ProductionCheck struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Revision     string          `json:"revision"`
	Scope        string          `json:"scope"`
	State        string          `json:"state"`
	Conclusion   string          `json:"conclusion"`
	URL          string          `json:"url"`
	Required     bool            `json:"required"` // the merge gate counts it (review.Pull); always written, so a record without it is from before and is read again
	PullRequests []uint64        `json:"pull_requests"`
	Jobs         []ProductionJob `json:"jobs"`
	Overflow     int             `json:"overflow,omitempty"`
}

type ProductionJob struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	State       string   `json:"state"`
	Conclusion  string   `json:"conclusion"`
	URL         string   `json:"url"`
	Annotations []string `json:"annotations,omitempty"`
}

type ProductionReviewer struct {
	ID       string `json:"id"`
	Number   uint64 `json:"number"`
	Head     string `json:"head"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Findings string `json:"findings,omitempty"`
}

type ProductionDelivery struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Destination  string   `json:"destination"`
	Revision     string   `json:"revision"`
	State        string   `json:"state"`
	URL          string   `json:"url,omitempty"`
	PullRequests []uint64 `json:"pull_requests"`
	VerifiedAt   int64    `json:"verified_at,omitempty"`
	UpdatedAt    int64    `json:"updated_at,omitempty"`
	// A release restart's local API outage: when the staging generation
	// withdrew its socket and when the generation that settles it bound one.
	WithdrawnAt int64  `json:"withdrawn_at,omitempty"`
	ServedAt    int64  `json:"served_at,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Overflow    int    `json:"overflow,omitempty"`
	// Group is the process group of a release's running build or deploy
	// command, which the next factoryd stops if this one dies first.
	Group int `json:"group,omitempty"`
	// RecordDelivery sets a release's Cause (phase and reason up to its
	// first colon) and FailedAt (when it first failed so), carried over
	// consecutive releases that fail the same way.
	Cause    string `json:"cause,omitempty"`
	FailedAt int64  `json:"failed_at,omitempty"`
}
