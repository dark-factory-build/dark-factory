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
	Number         uint64              `json:"number"`
	Title          string              `json:"title"`
	URL            string              `json:"url"`
	Head           string              `json:"head"`
	HeadRepository string              `json:"head_repository,omitempty"`
	Branch         string              `json:"branch"`
	Base           string              `json:"base"`
	State          string              `json:"state"`
	Merge          string              `json:"merge,omitempty"`
	MergeQueue     string              `json:"merge_queue,omitempty"`
	MergedAt       string              `json:"merged_at,omitempty"`
	Review         ProductionReview    `json:"review"`
	NextAction     string              `json:"next_action,omitempty"`
	Body           string              `json:"body,omitempty"`
	Publication    *PublicationReceipt `json:"publication,omitempty"`
}

// PublicationReceipt is the daemon's durable handoff record. Head is the
// remote branch head; SourceHead is the retained worker head that produced it.
type PublicationReceipt struct {
	SourceHead             string `json:"source_head"`
	PublishedHead          string `json:"published_head"`
	Delta                  int64  `json:"delta"`
	DeltaSet               bool   `json:"-"`
	PublishOperation       string `json:"publish_operation"`
	BodyOperation          string `json:"body_operation,omitempty"`
	ReviewRequestOperation string `json:"review_request_operation,omitempty"`
	ReviewOperation        string `json:"review_operation,omitempty"`
	// Failure is the last publication attempt's cause; a later success clears it.
	Failure string `json:"failure,omitempty"`
}

// ChangePublicationFact joins durable Change settlement with the last
// recorded pull-request observation. It is a read-only event projection; the
// daemon owns the remote transition.
type ChangePublicationFact struct {
	ProjectID              string
	ChangeID               string
	TaskID                 string
	Repository             string
	PullNumber             uint64
	Branch                 string
	Base                   string
	Title                  string
	Body                   string
	BaseCommit             string
	SettledHead            string
	PublishedHead          string
	PublishedSourceHead    string
	Delta                  int64
	PublishOperation       string
	BodyOperation          string
	ReviewRequestOperation string
	ReviewOperation        string
	ReviewHead             string
	ReviewState            string
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
	UpdatedAt    int64    `json:"updated_at,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Overflow     int      `json:"overflow,omitempty"`
}
