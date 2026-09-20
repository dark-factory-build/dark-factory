package browserprotocol

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// AttachmentRetention reads the setting when Enabled is absent, or saves it.
type AttachmentRetention struct {
	Enabled *Bool `json:"enabled,omitempty"`
}
type AttachmentRetentionResult struct {
	Enabled Bool `json:"enabled"`
}

func EncodeAttachmentRetentionResult(id string, value AttachmentRetentionResult) ([]byte, error) {
	return encodeControl(TypeAttachmentRetentionResult, id, value)
}

// AgentUpdate is the console's bounded agent-configuration edit. Model,
// ReasoningEffort and Paused are each optional: an absent member leaves the
// durable value alone, so one console screen can edit one control at a time.
type AgentUpdate struct {
	AgentID          string            `json:"agent_id"`
	ExpectedRevision Decimal           `json:"expected_revision"`
	Appearance       *SpriteAppearance `json:"appearance,omitempty"`
	Model            *string           `json:"model,omitempty"`
	ReasoningEffort  *string           `json:"reasoning_effort,omitempty"`
	// AccountID selects a linked provider login; an empty string clears the
	// selection back to that provider's default configuration directory.
	AccountID *string `json:"account_id,omitempty"`
	Paused    *Bool   `json:"paused,omitempty"`
	Archived  *Bool   `json:"archived,omitempty"`
	// The idle rule. A new budget starts the used count again.
	IdlePolicy       *string `json:"idle_policy,omitempty"`
	IdleAfterSeconds *uint32 `json:"idle_after_seconds,omitempty"`
	IdleInstruction  *string `json:"idle_instruction,omitempty"`
	IdleRunBudget    *uint32 `json:"idle_run_budget,omitempty"`
}

type AgentUpdateResult struct {
	AgentID  string  `json:"agent_id"`
	Revision Decimal `json:"revision"`
}

// ProjectLimits replaces the future run allowance and per-run ceiling. A zero
// allowance or duration explicitly means unlimited.
type ProjectLimits struct {
	ProjectID        string  `json:"project_id"`
	ExpectedRevision Decimal `json:"expected_revision"`
	RunBudget        Decimal `json:"run_budget"`
	MaxRunSeconds    uint32  `json:"max_run_seconds"`
}

type ProjectLimitsResult struct {
	ProjectID string  `json:"project_id"`
	Revision  Decimal `json:"revision"`
}

// ProjectCreate is an administration-only private bootstrap operation. Root
// never appears in its result or in STATE; creation makes the legacy root the
// initial default repository inside the Store transaction.
type ProjectCreate struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Root      string `json:"root"`
}
type ProjectCreateResult struct {
	ProjectID string  `json:"project_id"`
	Revision  Decimal `json:"revision"`
}

// RepositoriesGet and RepositoryMutate are administration-only private
// settings controls. They never appear in STATE snapshots.
type RepositoriesGet struct {
	ProjectID string `json:"project_id"`
}
type Repository struct {
	ID                 string   `json:"id"`
	ProjectID          string   `json:"project_id"`
	Name               string   `json:"name"`
	Root               string   `json:"root"`
	BaseRef            string   `json:"base_ref"`
	Enabled            Bool     `json:"enabled"`
	Default            Bool     `json:"default"`
	Revision           Decimal  `json:"revision"`
	GitHubRepositoryID *Decimal `json:"github_repository_id,omitempty"`
	FetchState         string   `json:"fetch_state,omitempty"`
	PublicationState   string   `json:"publication_state,omitempty"`
	ReadinessMessage   string   `json:"readiness_message,omitempty"`
}
type Repositories struct {
	ProjectID string       `json:"project_id"`
	Items     []Repository `json:"items"`
}
type RepositoryMutate struct {
	Action           string  `json:"action"`
	ID               string  `json:"id,omitempty"`
	ProjectID        string  `json:"project_id,omitempty"`
	Name             string  `json:"name,omitempty"`
	Root             string  `json:"root,omitempty"`
	BaseRef          string  `json:"base_ref,omitempty"`
	ExpectedRevision Decimal `json:"expected_revision,omitempty"`
	Enabled          *Bool   `json:"enabled,omitempty"`
}
type RepositoryMutateResult struct {
	Repository *Repository `json:"repository,omitempty"`
}

// Intake is private administration traffic for configured GitHub issue
// sources. Candidate bytes only arrive in the result after an explicit
// preview; acceptance carries the reviewed hash, never caller-authored text.
type IntakeConfiguration struct {
	LinearTeamID       string           `json:"linear_team_id,omitempty"`
	PriorityDefault    int64            `json:"priority_default,omitempty"`
	PriorityByLabel    map[string]int64 `json:"priority_by_label,omitempty"`
	Repository         string           `json:"repository"`
	TargetRepositoryID string           `json:"target_repository_id"`
	OverseerAgentID    string           `json:"overseer_agent_id"`
	Label              string           `json:"label"`
	Policy             string           `json:"policy"`
	TrustedAuthors     []string         `json:"trusted_authors"`
	PollSeconds        uint32           `json:"poll_seconds"`
	AdmissionLimit     uint16           `json:"admission_limit"`
}
type Intake struct {
	APIKey           string               `json:"api_key,omitempty"`
	Action           string               `json:"action"`
	SourceID         string               `json:"source_id,omitempty"`
	ProjectID        string               `json:"project_id,omitempty"`
	Configuration    *IntakeConfiguration `json:"configuration,omitempty"`
	ExpectedRevision Decimal              `json:"expected_revision,omitempty"`
	ReviewedRevision Decimal              `json:"reviewed_revision,omitempty"`
	Page             uint32               `json:"page,omitempty"`
	IssueNumber      Decimal              `json:"issue_number,omitempty"`
	ContentHash      string               `json:"content_hash,omitempty"`
	AcceptanceID     string               `json:"acceptance_id,omitempty"`
}
type IntakeSync struct {
	LastAttemptAt Decimal `json:"last_attempt_at"`
	LastSuccessAt Decimal `json:"last_success_at"`
	ImportedTasks uint16  `json:"imported_tasks"`
	State         string  `json:"state"`
	Error         string  `json:"error"`
}
type IntakeSource struct {
	LinearTeamID string      `json:"linear_team_id,omitempty"`
	Sync         *IntakeSync `json:"sync,omitempty"`

	PriorityDefault    int64            `json:"priority_default,omitempty"`
	PriorityByLabel    map[string]int64 `json:"priority_by_label,omitempty"`
	Repository         string           `json:"repository"`
	TargetRepositoryID string           `json:"target_repository_id"`
	OverseerAgentID    string           `json:"overseer_agent_id"`
	Label              string           `json:"label"`
	Policy             string           `json:"policy"`
	TrustedAuthors     []string         `json:"trusted_authors"`
	PollSeconds        uint32           `json:"poll_seconds"`
	AdmissionLimit     uint16           `json:"admission_limit"`
	ID                 string           `json:"id"`
	ProjectID          string           `json:"project_id"`
	GitHubRepositoryID Decimal          `json:"github_repository_id"`
	Enabled            Bool             `json:"enabled"`
	Revision           Decimal          `json:"revision"`
}
type IntakeCandidate struct {
	Number       Decimal  `json:"number"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Author       string   `json:"author"`
	Labels       []string `json:"labels"`
	ContentHash  string   `json:"content_hash"`
	Reason       string   `json:"reason"`
	AcceptanceID string   `json:"acceptance_id,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	Truncated    Bool     `json:"truncated,omitempty"`
}
type IntakeTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}
type IntakeResult struct {
	SourceID         string            `json:"source_id,omitempty"`
	LinearTeams      []IntakeTeam      `json:"linear_teams,omitempty"`
	State            string            `json:"state"`
	ImportedTasks    []string          `json:"imported_tasks,omitempty"`
	Sources          []IntakeSource    `json:"sources,omitempty"`
	Candidates       []IntakeCandidate `json:"candidates,omitempty"`
	NextPage         *uint32           `json:"next_page,omitempty"`
	ReviewedRevision Decimal           `json:"reviewed_revision,omitempty"`
	AcceptanceID     string            `json:"acceptance_id,omitempty"`
	TaskID           string            `json:"task_id,omitempty"`
}

func validateRepository(value Repository) error {
	if validateDynamicID(value.ID) != nil || validateDynamicID(value.ProjectID) != nil || validateBoundedText(value.Name, 1, 128) != nil || validateBoundedText(value.Root, 1, 4096) != nil || validateBoundedText(value.BaseRef, 1, 4096) != nil || value.Revision == 0 {
		return fmt.Errorf("%w: repository", ErrMalformed)
	}
	if value.GitHubRepositoryID != nil && *value.GitHubRepositoryID == 0 {
		return fmt.Errorf("%w: github repository id", ErrMalformed)
	}
	if value.FetchState != "" && value.FetchState != "unchecked" && value.FetchState != "ready" && value.FetchState != "setup_required" ||
		value.PublicationState != "" && value.PublicationState != "unchecked" && value.PublicationState != "ready" && value.PublicationState != "unbound" && value.PublicationState != "setup_required" ||
		validateBoundedText(value.ReadinessMessage, 0, 512) != nil || strings.ContainsAny(value.ReadinessMessage, "\x00\r\n") {
		return fmt.Errorf("%w: repository readiness", ErrMalformed)
	}
	return nil
}

// TaskUpdate edits one still-queued task. Status is the only member that is
// not free: "cancelled" retires the task, "queued" retries a blocked or failed
// one, and it says nothing else.
type TaskUpdate struct {
	TaskID           string  `json:"task_id"`
	ExpectedRevision Decimal `json:"expected_revision"`
	Title            *string `json:"title,omitempty"`
	Body             *string `json:"body,omitempty"`
	Priority         *int64  `json:"priority,omitempty"`
	AssignedAgentID  *string `json:"assigned_agent_id,omitempty"`
	Status           *string `json:"status,omitempty"`
}

type TaskUpdateResult struct {
	TaskID   string  `json:"task_id"`
	Revision Decimal `json:"revision"`
}

type TopologyGet struct {
	ProjectID string `json:"project_id"`
}

// Topology is the regenerable project structure, computed on demand. It is not
// durable state, so it carries no head and no revision; the digest is the only
// identity a client needs to tell one computation from another. Containment is
// implied by ParentID. Optional dependencies are bounded analyser observations.
type Topology struct {
	ProjectID        string                `json:"project_id"`
	Digest           string                `json:"digest"`
	SourceRevision   string                `json:"source_revision"`
	Nodes            []TopologyNode        `json:"nodes"`
	Dependencies     *TopologyDependencies `json:"dependencies,omitempty"`
	InventoryOmitted *uint32               `json:"inventory_omitted,omitempty"`
}

const MaxTopologyEdges = 256

// Coverage is deliberately partial: only resolved project-local Go imports and
// package-manifest dependencies are analysed. Absent support means unknown.
type TopologyDependencies struct {
	Source  string         `json:"source"`
	Edges   []TopologyEdge `json:"edges"`
	Omitted uint32         `json:"omitted"`
}

type TopologyEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Weight uint32 `json:"weight"`
}

type RunPathsGet struct {
	AgentID string `json:"agent_id"`
}

// RunPaths places one live worker in the part of the tree it is changing. It
// is a regenerable observation of that run's working directory, not durable
// state, so it carries no revision: the run identity is all a client needs to
// tell one worker's rooms from another's. An agent with no live run answers
// with an empty run identity and no paths.
type RunPaths struct {
	AgentID string   `json:"agent_id"`
	RunID   string   `json:"run_id"`
	Paths   []string `json:"paths"`
}

// AccountsDiscover asks what provider logins exist on this machine. It is an
// observation of the operator's own home directory, not durable state, so it
// carries no revision. Offset continues a bounded observation.
type AccountsDiscover struct {
	Offset uint32 `json:"offset,omitempty"`
}

// DiscoveredAccount is one CLI login the daemon found. Identity comes from the
// login's own files; the tokens that prove it never leave the daemon and have
// no field here. LinkedID is empty until the operator links it.
type DiscoveredAccount struct {
	Provider               string `json:"provider"`
	Home                   string `json:"home"`
	Label                  string `json:"label"`
	Email                  string `json:"email"`
	Organization           string `json:"organization"`
	DefaultModel           string `json:"default_model"`
	DefaultReasoningEffort string `json:"default_reasoning_effort"`
	LinkedID               string `json:"linked_id"`
	// UnavailableReason is optional so older daemons can still describe a
	// login without claiming why it cannot serve a particular selection.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type Accounts struct {
	Accounts   []DiscoveredAccount `json:"accounts"`
	NextOffset *uint32             `json:"next_offset,omitempty"`
}

// PageAccounts returns one complete, byte-bounded account page. The offset is
// cumulative; it is never a per-page entity number.
func PageAccounts(all []DiscoveredAccount, offset uint32) (Accounts, error) {
	if uint64(offset) > uint64(len(all)) {
		return Accounts{}, ErrMalformed
	}
	// Account entries are independent JSON values. Track their exact encoded
	// bytes instead of repeatedly encoding the growing page (which is
	// quadratic for a large observation). The maximum cursor allowance covers
	// every uint32 cursor; the finished page is still encoded and validated.
	maximumCursor := uint32(^uint32(0))
	withCursor, err := EncodeAccounts("accounts", Accounts{Accounts: []DiscoveredAccount{}, NextOffset: &maximumCursor})
	if err != nil {
		return Accounts{}, err
	}
	baseBytes := len(withCursor)
	page := Accounts{Accounts: make([]DiscoveredAccount, 0, MaxSnapshotEntities)}
	entryBytes := 0
	for index := int(offset); index < len(all) && len(page.Accounts) < MaxSnapshotEntities; index++ {
		if err := ValidDiscoveredAccount(all[index]); err != nil {
			return Accounts{}, err
		}
		encoded, err := json.Marshal(all[index])
		if err != nil {
			return Accounts{}, fmt.Errorf("%w: account: %v", ErrMalformed, err)
		}
		separator := 0
		if len(page.Accounts) != 0 {
			separator = 1
		}
		// Leave room for the local API response envelope too. The browser
		// frame and the API frame must both carry the same complete page.
		if baseBytes+entryBytes+separator+len(encoded)+1024 > MaxSnapshotBytes {
			if len(page.Accounts) == 0 {
				return Accounts{}, ErrOversized
			}
			break
		}
		page.Accounts = append(page.Accounts, all[index])
		entryBytes += separator + len(encoded)
	}
	end := int(offset) + len(page.Accounts)
	if end < len(all) {
		next := uint32(end)
		page.NextOffset = &next
	}
	encoded, err := EncodeAccounts("accounts", page)
	if err != nil {
		return Accounts{}, err
	}
	if len(encoded)+1024 > MaxSnapshotBytes {
		return Accounts{}, ErrOversized
	}
	return page, nil
}

// AccountLink registers one login that already exists. Starting a new CLI
// login flow is not part of this message.
type AccountLink struct {
	Provider string `json:"provider"`
	Home     string `json:"home"`
	Label    string `json:"label"`
}

type AccountLinkResult struct {
	AccountID string  `json:"account_id"`
	Revision  Decimal `json:"revision"`
}

// AccountUpdate edits one linked login or removes it when no agent or run uses it.
type AccountUpdate struct {
	AccountID        string  `json:"account_id"`
	ExpectedRevision Decimal `json:"expected_revision"`
	Label            *string `json:"label,omitempty"`
	Remove           *Bool   `json:"remove,omitempty"`
}

type AccountUpdateResult struct {
	AccountID string  `json:"account_id"`
	Revision  Decimal `json:"revision"`
}

// BrowserClientsGet asks for the browser identities this factory has granted
// and not revoked. Administration only: the list names every device that can
// reach the factory.
type BrowserClientsGet struct{}

// BrowserClientItem is one granted identity: enough to recognise and revoke
// it, never its key or fingerprint.
type BrowserClientItem struct {
	ClientID     string       `json:"client_id"`
	Capabilities Capabilities `json:"capabilities"`
	Revision     Decimal      `json:"revision"`
	CreatedAtMS  Decimal      `json:"created_at_ms"`
}

// BrowserClients lists the newest identities first; More says the bound cut
// the list short.
type BrowserClients struct {
	Clients []BrowserClientItem `json:"clients"`
	More    Bool                `json:"more"`
}

// BrowserClientRevoke withdraws one identity at an exact revision.
type BrowserClientRevoke struct {
	ClientID         string  `json:"client_id"`
	ExpectedRevision Decimal `json:"expected_revision"`
}

type BrowserClientRevokeResult struct {
	ClientID string  `json:"client_id"`
	Revision Decimal `json:"revision"`
}

type TopologyNode struct {
	ID         string             `json:"id"`
	ParentID   string             `json:"parent_id"`
	Kind       string             `json:"kind"`
	Path       string             `json:"path"`
	Label      string             `json:"label"`
	Language   string             `json:"language"`
	SizeBucket string             `json:"size_bucket"`
	Inventory  *TopologyInventory `json:"inventory,omitempty"`
}

func EncodeAgentUpdateResult(id string, value AgentUpdateResult) ([]byte, error) {
	return encodeControl(TypeAgentUpdateResult, id, value)
}

func EncodeProjectLimitsResult(id string, value ProjectLimitsResult) ([]byte, error) {
	return encodeControl(TypeProjectLimitsResult, id, value)
}
func EncodeProjectCreateResult(id string, value ProjectCreateResult) ([]byte, error) {
	return encodeControl(TypeProjectCreateResult, id, value)
}
func EncodeRepositories(id string, value Repositories) ([]byte, error) {
	return encodeControl(TypeRepositories, id, value)
}
func EncodeRepositoryMutateResult(id string, value RepositoryMutateResult) ([]byte, error) {
	return encodeControl(TypeRepositoryMutateResult, id, value)
}
func EncodeIntakeResult(id string, value IntakeResult) ([]byte, error) {
	for i := range value.Sources {
		if value.Sources[i].TrustedAuthors == nil {
			value.Sources[i].TrustedAuthors = []string{}
		}
	}
	for i := range value.Candidates {
		if value.Candidates[i].Labels == nil {
			value.Candidates[i].Labels = []string{}
		}
	}
	return encodeControl(TypeIntakeResult, id, value)
}

func EncodeTaskUpdateResult(id string, value TaskUpdateResult) ([]byte, error) {
	return encodeControl(TypeTaskUpdateResult, id, value)
}

func EncodeTopology(id string, value Topology) ([]byte, error) {
	return encodeControl(TypeTopology, id, value)
}

// EncodeRunPaths normalizes an absent path list to an empty one: JSON null is
// not an array on either side of the boundary.
func EncodeRunPaths(id string, value RunPaths) ([]byte, error) {
	if value.Paths == nil {
		value.Paths = []string{}
	}
	return encodeControl(TypeRunPaths, id, value)
}

// EncodeAccounts normalizes an absent list the same way EncodeRunPaths does.
func EncodeAccounts(id string, value Accounts) ([]byte, error) {
	if value.Accounts == nil {
		value.Accounts = []DiscoveredAccount{}
	}
	return encodeControl(TypeAccounts, id, value)
}

func EncodeAccountLinkResult(id string, value AccountLinkResult) ([]byte, error) {
	return encodeControl(TypeAccountLinkResult, id, value)
}

func EncodeAccountUpdateResult(id string, value AccountUpdateResult) ([]byte, error) {
	return encodeControl(TypeAccountUpdateResult, id, value)
}

func EncodeBrowserClientsGet(id string, value BrowserClientsGet) ([]byte, error) {
	return encodeControl(TypeBrowserClientsGet, id, value)
}

func EncodeBrowserClients(id string, value BrowserClients) ([]byte, error) {
	return encodeControl(TypeBrowserClients, id, value)
}

func EncodeBrowserClientRevoke(id string, value BrowserClientRevoke) ([]byte, error) {
	return encodeControl(TypeBrowserClientRevoke, id, value)
}

func EncodeBrowserClientRevokeResult(id string, value BrowserClientRevokeResult) ([]byte, error) {
	return encodeControl(TypeBrowserClientRevokeResult, id, value)
}

func validConsoleControl(kind MessageType, body any) error {
	body = indirect(body)
	bad := func() error { return fmt.Errorf("%w: invalid %s", ErrMalformed, kind) }
	switch value := body.(type) {
	case AgentUpdate:
		if validateDynamicID(value.AgentID) != nil || value.ExpectedRevision == 0 ||
			value.Appearance != nil && validateSpriteAppearance(*value.Appearance) != nil ||
			value.Model != nil && validateBoundedText(*value.Model, 0, MaxAgentModelBytes) != nil ||
			value.ReasoningEffort != nil && validateBoundedText(*value.ReasoningEffort, 0, MaxAgentModelBytes) != nil ||
			value.AccountID != nil && *value.AccountID != "" && validateDynamicID(*value.AccountID) != nil ||
			value.IdlePolicy != nil && !validIdlePolicy(*value.IdlePolicy) ||
			value.IdleAfterSeconds != nil && *value.IdleAfterSeconds > MaxIdleAfterSeconds ||
			value.IdleInstruction != nil && validateBoundedText(*value.IdleInstruction, 0, MaxTaskInstructionBytes) != nil ||
			value.IdleRunBudget != nil && *value.IdleRunBudget > MaxIdleRunBudget {
			return bad()
		}
	case AgentUpdateResult:
		if validateDynamicID(value.AgentID) != nil || value.Revision == 0 {
			return bad()
		}
	case AttachmentRetention, AttachmentRetentionResult:
		return nil
	case ProjectLimits:
		if validateDynamicID(value.ProjectID) != nil || value.ExpectedRevision == 0 || uint64(value.RunBudget) > MaxSQLiteInteger || value.MaxRunSeconds > 86400 {
			return bad()
		}
	case ProjectLimitsResult:
		if validateDynamicID(value.ProjectID) != nil || value.Revision == 0 {
			return bad()
		}
	case ProjectCreate:
		if validateDynamicID(value.ProjectID) != nil || validateBoundedText(value.Name, 1, 128) != nil || validateBoundedText(value.Root, 1, 4096) != nil {
			return bad()
		}
	case ProjectCreateResult:
		if validateDynamicID(value.ProjectID) != nil || value.Revision == 0 {
			return bad()
		}
	case RepositoriesGet:
		if validateDynamicID(value.ProjectID) != nil {
			return bad()
		}
	case Repositories:
		if validateDynamicID(value.ProjectID) != nil || value.Items == nil || len(value.Items) > MaxSnapshotEntities {
			return bad()
		}
		for _, item := range value.Items {
			if validateRepository(item) != nil || item.ProjectID != value.ProjectID {
				return bad()
			}
		}
	case RepositoryMutate:
		if value.Action != "add" && value.Action != "name" && value.Action != "base" && value.Action != "default" && value.Action != "enabled" && value.Action != "remove" && value.Action != "github" && value.Action != "fetch" {
			return bad()
		}
		if value.Action == "add" {
			if validateDynamicID(value.ID) != nil || validateDynamicID(value.ProjectID) != nil || validateBoundedText(value.Name, 1, 128) != nil || validateBoundedText(value.Root, 1, 4096) != nil || validateBoundedText(value.BaseRef, 1, 4096) != nil {
				return bad()
			}
		} else if value.Action == "github" || value.Action == "fetch" {
			if validateDynamicID(value.ID) != nil || value.ExpectedRevision != 0 || value.ProjectID != "" || value.Name != "" || value.Root != "" || value.BaseRef != "" || value.Enabled != nil {
				return bad()
			}
		} else if validateDynamicID(value.ID) != nil || value.ExpectedRevision == 0 || value.Action == "base" && validateBoundedText(value.BaseRef, 1, 4096) != nil || value.Action == "enabled" && value.Enabled == nil {
			return bad()
		}
	case RepositoryMutateResult:
		if value.Repository != nil {
			item := *value.Repository
			if validateRepository(item) != nil {
				return bad()
			}
		}
	case Intake:
		if !validIntake(value) {
			return bad()
		}
	case IntakeResult:
		if len(value.LinearTeams) > 100 {
			return bad()
		}
		for _, team := range value.LinearTeams {
			if validateBoundedText(team.ID, 36, 36) != nil || validateBoundedText(team.Name, 1, 140) != nil || validateBoundedText(team.Key, 1, 32) != nil {
				return bad()
			}
		}
		if !validIntakeResult(value) {
			return bad()
		}
	case TaskUpdate:
		if validateDynamicID(value.TaskID) != nil || value.ExpectedRevision == 0 ||
			value.Title != nil && validateBoundedText(*value.Title, 1, MaxTaskTitleBytes) != nil ||
			value.Body != nil && validateBoundedText(*value.Body, 0, MaxTaskInstructionBytes) != nil ||
			value.Priority != nil && (*value.Priority < -MaxTaskPriority || *value.Priority > MaxTaskPriority) ||
			value.AssignedAgentID != nil && validateDynamicID(*value.AssignedAgentID) != nil ||
			value.Status != nil && *value.Status != "cancelled" && *value.Status != "queued" ||
			// A retry re-queues the task as it stands; an edit beside it would be dropped.
			value.Status != nil && *value.Status == "queued" && (value.Title != nil || value.Body != nil || value.Priority != nil) {
			return bad()
		}
	case TaskUpdateResult:
		if validateDynamicID(value.TaskID) != nil || value.Revision == 0 {
			return bad()
		}
	case TopologyGet:
		if validateDynamicID(value.ProjectID) != nil {
			return bad()
		}
	case Topology:
		if validateDynamicID(value.ProjectID) != nil || !validTopologyDigest(value.Digest) ||
			!validTopologySource(value.SourceRevision) || len(value.Nodes) > MaxSnapshotEntities {
			return bad()
		}
		if value.InventoryOmitted != nil && *value.InventoryOmitted > uint32(len(value.Nodes)) {
			return bad()
		}
		ids := make(map[string]bool, len(value.Nodes))
		for _, node := range value.Nodes {
			if !validTopologyNode(node) || ids[node.ID] {
				return bad()
			}
			ids[node.ID] = true
		}
		if d := value.Dependencies; d != nil {
			if d.Source != "go-imports-package-manifests" || d.Edges == nil || len(d.Edges) > MaxTopologyEdges {
				return bad()
			}
			seen := make(map[[2]string]bool, len(d.Edges))
			for _, edge := range d.Edges {
				pair := [2]string{edge.From, edge.To}
				if !ids[edge.From] || !ids[edge.To] || edge.From == edge.To || edge.Weight == 0 || seen[pair] {
					return bad()
				}
				seen[pair] = true
			}
		}
	case RunPathsGet:
		if validateDynamicID(value.AgentID) != nil {
			return bad()
		}
	case RunPaths:
		// No live run means no rooms, so an empty run identity may not carry
		// paths. The array itself keeps the generic control item bound.
		if validateDynamicID(value.AgentID) != nil || value.Paths == nil || len(value.Paths) > MaxJSONArray ||
			value.RunID == "" && len(value.Paths) != 0 ||
			value.RunID != "" && validateDynamicID(value.RunID) != nil {
			return bad()
		}
		for _, path := range value.Paths {
			if validateBoundedText(path, 1, MaxTaskTitleBytes) != nil {
				return bad()
			}
		}
	case AccountsDiscover:
	case Accounts:
		if value.Accounts == nil || len(value.Accounts) > MaxSnapshotEntities || value.NextOffset != nil && *value.NextOffset == 0 {
			return bad()
		}
		for _, account := range value.Accounts {
			if ValidDiscoveredAccount(account) != nil {
				return bad()
			}
		}
	case AccountLink:
		if !validProviderAccount(value.Provider) || validAccountHome(value.Home) != nil ||
			validateBoundedText(value.Label, 1, MaxAgentNameBytes) != nil {
			return bad()
		}
	case AccountLinkResult:
		if validateDynamicID(value.AccountID) != nil || value.Revision == 0 {
			return bad()
		}
	case AccountUpdate:
		if validateDynamicID(value.AccountID) != nil || value.ExpectedRevision == 0 ||
			(value.Label == nil) == (value.Remove == nil) ||
			value.Label != nil && validateBoundedText(*value.Label, 1, MaxAgentNameBytes) != nil ||
			value.Remove != nil && !bool(*value.Remove) {
			return bad()
		}
	case AccountUpdateResult:
		if validateDynamicID(value.AccountID) != nil || value.Revision == 0 {
			return bad()
		}
	case BrowserClientsGet:
	case BrowserClients:
		if value.Clients == nil || len(value.Clients) > MaxJSONArray {
			return bad()
		}
		for _, client := range value.Clients {
			if validateDynamicID(client.ClientID) != nil || validateCapabilities(client.Capabilities) != nil || client.Revision == 0 {
				return bad()
			}
		}
	case BrowserClientRevoke:
		if validateDynamicID(value.ClientID) != nil || value.ExpectedRevision == 0 {
			return bad()
		}
	case BrowserClientRevokeResult:
		if validateDynamicID(value.ClientID) != nil || value.Revision == 0 {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

// ValidDiscoveredAccount is the single rule for one discovered login, shared
// by the wire and by the daemon's discovery, so a login the wire would refuse
// is dropped where it is found instead of poisoning the whole answer.
func ValidDiscoveredAccount(value DiscoveredAccount) error {
	if !validProviderAccount(value.Provider) || validAccountHome(value.Home) != nil ||
		validateBoundedText(value.Label, 1, MaxAgentNameBytes) != nil ||
		validateBoundedText(value.Email, 0, MaxAgentNameBytes) != nil ||
		validateBoundedText(value.Organization, 0, MaxAgentNameBytes) != nil ||
		validateBoundedText(value.DefaultModel, 0, MaxAgentModelBytes) != nil ||
		validateBoundedText(value.DefaultReasoningEffort, 0, MaxAgentModelBytes) != nil ||
		validateBoundedText(value.UnavailableReason, 0, MaxAgentNameBytes) != nil ||
		value.LinkedID != "" && validateDynamicID(value.LinkedID) != nil {
		return fmt.Errorf("%w: discovered account", ErrMalformed)
	}
	return nil
}

// validProviderAccount closes the account provider set: shell has no logins.
func validProviderAccount(value string) bool {
	return value == "claude_code" || value == "codex"
}

// validAccountHome bounds one absolute configuration directory. It is the same
// 1024-byte bound the durable accounts table enforces.
func validAccountHome(value string) error {
	if validateBoundedText(value, 1, MaxTaskTitleBytes) != nil || value[0] != '/' || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%w: account home", ErrMalformed)
	}
	return nil
}

func validTopologyDigest(value string) bool {
	_, err := fixedHex("digest", value, 32)
	return err == nil
}

// validTopologySource accepts an empty revision or one canonical Git object
// name, in either the SHA-1 or the SHA-256 length Git itself uses.
func validTopologySource(value string) bool {
	if value == "" {
		return true
	}
	if _, err := fixedHex("source_revision", value, 20); err == nil {
		return true
	}
	_, err := fixedHex("source_revision", value, 32)
	return err == nil
}

func validTopologyNode(node TopologyNode) bool {
	if _, err := fixedHex("node id", node.ID, 32); err != nil {
		return false
	}
	if node.ParentID != "" {
		if _, err := fixedHex("node parent id", node.ParentID, 32); err != nil {
			return false
		}
	}
	switch node.Kind {
	case "repository", "module", "package", "directory":
	default:
		return false
	}
	switch node.SizeBucket {
	case "empty", "tiny", "small", "medium", "large":
	default:
		return false
	}
	return (node.Inventory == nil || validTopologyInventory(*node.Inventory)) && validateBoundedText(node.Path, 1, MaxTaskTitleBytes) == nil &&
		validateBoundedText(node.Label, 1, MaxAgentNameBytes) == nil &&
		validateBoundedText(node.Language, 0, MaxAgentNameBytes) == nil
}

// The idle rule bounds; the kernel enforces the same numbers durably.
const (
	MaxIdleAfterSeconds = 604800
	MaxIdleRunBudget    = 1000000
)

func validIdlePolicy(value string) bool {
	return value == "wait" || value == "standing_instruction"
}

// Inventory is optional scanned-file evidence, not coverage or execution.
type TopologyInventory struct {
	Direct         TopologyInventoryCounts `json:"direct"`
	Total          TopologyInventoryCounts `json:"total"`
	Samples        []string                `json:"samples"`
	SamplesOmitted uint32                  `json:"samples_omitted"`
}
type TopologyInventoryCounts struct {
	Source        uint32 `json:"source"`
	Tests         uint32 `json:"tests"`
	Documentation uint32 `json:"documentation"`
	Configuration uint32 `json:"configuration"`
	Assets        uint32 `json:"assets"`
	Unclassified  uint32 `json:"unclassified"`
}

func validTopologyInventory(value TopologyInventory) bool {
	direct := [...]uint32{value.Direct.Source, value.Direct.Tests, value.Direct.Documentation, value.Direct.Configuration, value.Direct.Assets, value.Direct.Unclassified}
	total := [...]uint32{value.Total.Source, value.Total.Tests, value.Total.Documentation, value.Total.Configuration, value.Total.Assets, value.Total.Unclassified}
	var directCount, totalCount uint64
	for i, count := range direct {
		if count > total[i] {
			return false
		}
		directCount += uint64(count)
		totalCount += uint64(total[i])
	}
	if totalCount > 50000 || value.Samples == nil || len(value.Samples) > 3 || uint64(len(value.Samples))+uint64(value.SamplesOmitted) != directCount {
		return false
	}
	seen := make(map[string]bool, len(value.Samples))
	for _, name := range value.Samples {
		if validateBoundedText(name, 1, 128) != nil || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func validIntakeConfiguration(value IntakeConfiguration) bool {
	if value.PriorityDefault < -MaxTaskPriority || value.PriorityDefault > MaxTaskPriority || len(value.PriorityByLabel) > 25 {
		return false
	}
	for label, priority := range value.PriorityByLabel {
		if validateBoundedText(label, 1, 100) != nil || priority < -MaxTaskPriority || priority > MaxTaskPriority {
			return false
		}
	}
	priorities, err := json.Marshal(value.PriorityByLabel)
	if err != nil || len(priorities) > 2048 {
		return false
	}
	if (value.LinearTeamID == "" && (validateBoundedText(value.Repository, 3, 140) != nil || strings.Count(value.Repository, "/") != 1) || value.LinearTeamID != "" && (validateBoundedText(value.LinearTeamID, 36, 36) != nil || validateBoundedText(value.Repository, 1, 140) != nil || value.Policy != "manual" || len(value.TrustedAuthors) != 0)) || validateDynamicID(value.TargetRepositoryID) != nil || value.OverseerAgentID != "" && validateDynamicID(value.OverseerAgentID) != nil || validateBoundedText(value.Label, 0, 100) != nil || (value.Policy != "manual" && value.Policy != "trusted_authors") || value.PollSeconds < 5 || value.PollSeconds > 86400 || value.AdmissionLimit < 1 || value.AdmissionLimit > 200 || len(value.TrustedAuthors) > 25 {
		return false
	}
	seen := map[string]bool{}
	for _, author := range value.TrustedAuthors {
		if validateBoundedText(author, 1, 39) != nil || seen[strings.ToLower(author)] {
			return false
		}
		seen[strings.ToLower(author)] = true
	}
	return value.Policy != "trusted_authors" || len(value.TrustedAuthors) > 0
}

func validIntake(value Intake) bool {
	if strings.HasPrefix(value.Action, "linear_") {
		key := value.APIKey
		value.APIKey = ""
		action := value.Action
		value.Action = ""
		return value == (Intake{}) && (action == "linear_connect" && validateBoundedText(key, 10, 512) == nil || (action == "linear_disconnect" || action == "linear_teams") && key == "")
	}
	if value.APIKey != "" {
		return false
	}
	if value.Page > 1000 || value.IssueNumber > Decimal(MaxSQLiteInteger) {
		return false
	}
	validHash := func(value string) bool {
		decoded, err := hex.DecodeString(value)
		return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
	}
	switch value.Action {
	case "list":
		return value.SourceID == "" && value.Configuration == nil && value.ExpectedRevision == 0 && value.ReviewedRevision == 0 && value.Page == 0 && value.IssueNumber == 0 && value.ContentHash == "" && value.AcceptanceID == "" && (value.ProjectID == "" || validateDynamicID(value.ProjectID) == nil)
	case "create":
		return validateDynamicID(value.SourceID) == nil && validateDynamicID(value.ProjectID) == nil && value.Configuration != nil && validIntakeConfiguration(*value.Configuration) && value.ExpectedRevision == 0 && value.ReviewedRevision == 0 && value.Page == 0 && value.IssueNumber == 0 && value.ContentHash == "" && value.AcceptanceID == ""
	case "update":
		return validateDynamicID(value.SourceID) == nil && validateDynamicID(value.ProjectID) == nil && value.Configuration != nil && validIntakeConfiguration(*value.Configuration) && value.ExpectedRevision > 0 && value.ReviewedRevision == 0 && value.Page == 0 && value.IssueNumber == 0 && value.ContentHash == "" && value.AcceptanceID == ""
	case "preview", "refresh", "tick":
		return validateDynamicID(value.SourceID) == nil && value.ProjectID == "" && value.Configuration == nil && value.ExpectedRevision == 0 && value.ReviewedRevision == 0 && value.Page > 0 && value.IssueNumber == 0 && value.ContentHash == "" && value.AcceptanceID == ""
	case "enable":
		return validateDynamicID(value.SourceID) == nil && value.ProjectID == "" && value.Configuration == nil && value.ExpectedRevision > 0 && value.ReviewedRevision == value.ExpectedRevision && value.Page == 0 && value.IssueNumber == 0 && value.ContentHash == "" && value.AcceptanceID == ""
	case "pause":
		return validateDynamicID(value.SourceID) == nil && value.ProjectID == "" && value.Configuration == nil && value.ExpectedRevision > 0 && value.ReviewedRevision == 0 && value.Page == 0 && value.IssueNumber == 0 && value.ContentHash == "" && value.AcceptanceID == ""
	case "accept":
		return validateDynamicID(value.SourceID) == nil && value.ProjectID == "" && value.Configuration == nil && value.ExpectedRevision > 0 && value.ReviewedRevision == 0 && value.Page == 0 && value.IssueNumber > 0 && validHash(value.ContentHash) && value.AcceptanceID == ""
	case "withdraw", "import":
		return value.SourceID == "" && value.ProjectID == "" && value.Configuration == nil && value.ExpectedRevision == 0 && value.ReviewedRevision == 0 && value.Page == 0 && value.IssueNumber == 0 && value.ContentHash == "" && validateDynamicID(value.AcceptanceID) == nil
	}
	return false
}

func validIntakeResult(value IntakeResult) bool {
	if value.SourceID != "" && validateDynamicID(value.SourceID) != nil || len(value.LinearTeams) > 100 {
		return false
	}
	for _, team := range value.LinearTeams {
		if validateBoundedText(team.ID, 36, 36) != nil || validateBoundedText(team.Name, 1, 140) != nil || validateBoundedText(team.Key, 1, 32) != nil {
			return false
		}
	}
	if validateBoundedText(value.State, 1, 128) != nil || len(value.ImportedTasks) > MaxSnapshotEntities || len(value.Sources) > MaxSnapshotEntities || len(value.Candidates) > MaxSnapshotEntities || value.NextPage != nil && (*value.NextPage == 0 || *value.NextPage > 1000) || value.ReviewedRevision > Decimal(MaxSQLiteInteger) || value.AcceptanceID != "" && validateDynamicID(value.AcceptanceID) != nil || value.TaskID != "" && validateDynamicID(value.TaskID) != nil {
		return false
	}
	for _, task := range value.ImportedTasks {
		if validateDynamicID(task) != nil {
			return false
		}
	}
	for _, source := range value.Sources {
		if validateDynamicID(source.ID) != nil || validateDynamicID(source.ProjectID) != nil || source.GitHubRepositoryID == 0 && source.LinearTeamID == "" || source.GitHubRepositoryID != 0 && source.LinearTeamID != "" || source.GitHubRepositoryID > Decimal(MaxSQLiteInteger) || source.Revision == 0 || !validIntakeConfiguration(IntakeConfiguration{LinearTeamID: source.LinearTeamID, PriorityDefault: source.PriorityDefault, PriorityByLabel: source.PriorityByLabel, Repository: source.Repository, TargetRepositoryID: source.TargetRepositoryID, OverseerAgentID: source.OverseerAgentID, Label: source.Label, Policy: source.Policy, TrustedAuthors: source.TrustedAuthors, PollSeconds: source.PollSeconds, AdmissionLimit: source.AdmissionLimit}) {
			return false
		}
		if source.Sync != nil && (source.Sync.LastAttemptAt > Decimal(MaxSQLiteInteger) || source.Sync.LastSuccessAt > Decimal(MaxSQLiteInteger) || source.Sync.ImportedTasks > 200 || (source.Sync.State != "ok" && source.Sync.State != "paused" && source.Sync.State != "error") || validateBoundedText(source.Sync.Error, 0, 128) != nil) {
			return false
		}
	}
	for _, candidate := range value.Candidates {
		if candidate.Number == 0 || candidate.Number > Decimal(MaxSQLiteInteger) || validateBoundedText(candidate.URL, 0, 4096) != nil || validateBoundedText(candidate.Title, 0, 900) != nil || validateBoundedText(candidate.Body, 0, 5000) != nil || validateBoundedText(candidate.Author, 0, 44) != nil || len(candidate.Labels) > MaxSnapshotEntities || len(candidate.ContentHash) != 64 || validateBoundedText(candidate.Reason, 1, 128) != nil || candidate.AcceptanceID != "" && validateDynamicID(candidate.AcceptanceID) != nil || candidate.TaskID != "" && validateDynamicID(candidate.TaskID) != nil {
			return false
		}
		for _, label := range candidate.Labels {
			if validateBoundedText(label, 1, 100) != nil {
				return false
			}
		}
	}
	return true
}
