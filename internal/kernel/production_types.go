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
}

type ProductionPullRequest struct {
	Number     uint64           `json:"number"`
	Title      string           `json:"title"`
	URL        string           `json:"url"`
	Head       string           `json:"head"`
	Branch     string           `json:"branch"`
	Base       string           `json:"base"`
	State      string           `json:"state"`
	Merge      string           `json:"merge,omitempty"`
	MergeQueue string           `json:"merge_queue,omitempty"`
	MergedAt   string           `json:"merged_at,omitempty"`
	Review     ProductionReview `json:"review"`
	NextAction string           `json:"next_action,omitempty"`
}

type ProductionReview struct {
	Head     string `json:"head"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Findings string `json:"findings,omitempty"`
}

type ProductionCheck struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Revision     string          `json:"revision"`
	Scope        string          `json:"scope"`
	State        string          `json:"state"`
	Conclusion   string          `json:"conclusion"`
	URL          string          `json:"url"`
	PullRequests []uint64        `json:"pull_requests"`
	Jobs         []ProductionJob `json:"jobs"`
	Overflow     int             `json:"overflow,omitempty"`
}

type ProductionJob struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
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
	Overflow     int      `json:"overflow,omitempty"`
}
