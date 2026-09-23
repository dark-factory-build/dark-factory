package changeworker

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	AttemptTokenName           = "attempt.token"
	HomeName                   = "home"
	TempName                   = "tmp"
	ResultLimit                = 32 << 10
	ConfigLimit                = 256 << 10
	maximumLocatorBytes        = 4096
	maximumRevisionBytes       = 4096
	maximumSessionKeyPartBytes = 128
)

var ErrInvalidContract = errors.New("Change worker: invalid private contract")

type Config struct {
	GitAuthor          gitauthor.Identity
	CustomerMaintainer bool
	Provider           kernel.Provider
	// Role decides whether the run works in a Change. A worker's Change is
	// prepared or reopened below; an orchestrator has none and works in its
	// private runtime home, so its FinalName and Retained are
	// empty.
	Role            kernel.AgentRole
	Model           string
	ReasoningEffort string
	// AgentID and TaskIncarnationID name the exact agent and task incarnation
	// this attempt belongs to. The provider boundary uses them only to derive
	// a deterministic native Claude Code worker session key (see
	// provider.claudeSessionSelection); nothing here persists them further.
	AgentID           string
	TaskIncarnationID string
	// PreviousWorkingDirectory is the same orchestrator agent's most recent
	// terminal run's own working directory (empty for a worker, or an
	// orchestrator with no prior terminal run). The provider boundary uses it
	// only to find that run's own Codex session, since an orchestrator's
	// current working directory is a fresh runtime root every run and could
	// never itself be found again; see provider.codexSessionSelection.
	PreviousWorkingDirectory string
	RuntimePath              string
	RuntimeIdentity          runner.FileIdentity
	GitExecutable            string
	FactoryctlExecutable     string
	ToolPath                 string
	ToolchainReadRoots       string
	LocalCILeaseDir          string
	AccountHome              string
	// AccountConfigDir is the linked provider login this run launches with.
	// Empty means the provider's own default configuration directory.
	AccountConfigDir       string
	RepositoryRoot         string
	RepositoryIdentity     change.RepositoryIdentity
	RepositoryGitIdentity  change.RepositoryIdentity
	RepositoryOriginDigest [32]byte
	// GitCommonDir is the project repository's Git directory, which the
	// worktree's commits and refs live in and a provider's local commands
	// are granted: written by a worker, read by an orchestrator.
	GitCommonDir  string
	Revision      string
	ChangeParent  string
	FinalName     string
	AttemptSocket string
	// Retained is the Change to reopen instead of making a fresh worktree.
	Retained *Result
	// RetainedSourceReview is a daemon-authenticated exact source receipt for
	// an independent reviewer. RetainedSourceReviews carries the exact set of
	// receipts an orchestrator may inspect during supervision.
	RetainedSourceReview  *SourceReview
	RetainedSourceReviews []SourceReview
	// ProviderTask selects and verifies the provider's closed delivery path.
	// Shell seals it on fd 11 and Claude receives a terminal-safe prompt. It is
	// empty for Codex, whose task remains in the daemon behind the attempt API.
	ProviderTask []byte
}

type SourceReview struct {
	TaskID, ChangeID                                 string
	TaskWorkRevision, ChangeRevision                 uint64
	BaseCommit, HeadCommit, SourcePath, GitDirectory string
}

func (Config) String() string   { return "Change worker config (private)" }
func (Config) GoString() string { return "changeworker.Config{private}" }

// Result is the selected base of one Change and, for a retained Change,
// the branch head the daemon last recorded. Head is absent while a retained
// Change is still a Git-free tree from before managed worktrees. Repository
// identity is absent because the daemon owns and independently verifies it.
type Result struct {
	Format change.ObjectFormat
	Base   change.ObjectID
	Head   *change.ObjectID
}

func (Result) String() string   { return "Change worker result (private)" }
func (Result) GoString() string { return "changeworker.Result{private}" }

type identityWire struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type resultWire struct {
	Format string `json:"format"`
	Base   string `json:"base"`
	Head   string `json:"head,omitempty"`
}

type configWire struct {
	GitAuthor                gitauthor.Identity `json:"git_author"`
	CustomerMaintainer       bool               `json:"customer_maintainer"`
	Provider                 string             `json:"provider"`
	Role                     string             `json:"role"`
	Model                    string             `json:"model"`
	ReasoningEffort          string             `json:"reasoning_effort"`
	AgentID                  string             `json:"agent_id"`
	TaskIncarnationID        string             `json:"task_incarnation_id"`
	PreviousWorkingDirectory string             `json:"previous_working_directory,omitempty"`
	RuntimePath              string             `json:"runtime_path"`
	RuntimeIdentity          identityWire       `json:"runtime_identity"`
	GitExecutable            string             `json:"git_executable"`
	FactoryctlExecutable     string             `json:"factoryctl_executable"`
	ToolPath                 string             `json:"tool_path"`
	ToolchainReadRoots       string             `json:"toolchain_read_roots,omitempty"`
	LocalCILeaseDir          string             `json:"local_ci_lease_directory,omitempty"`
	AccountHome              string             `json:"account_home"`
	AccountConfigDir         string             `json:"account_config_dir"`
	RepositoryRoot           string             `json:"repository_root"`
	RepositoryIdentity       identityWire       `json:"repository_identity"`
	RepositoryGitIdentity    identityWire       `json:"repository_git_identity"`
	RepositoryOriginDigest   [32]byte           `json:"repository_origin_digest"`
	GitCommonDir             string             `json:"git_common_dir"`
	Revision                 string             `json:"revision"`
	ChangeParent             string             `json:"change_parent"`
	FinalName                string             `json:"final_name"`
	AttemptSocket            string             `json:"attempt_socket"`
	Retained                 *resultWire        `json:"retained,omitempty"`
	RetainedSourceReview     *sourceReviewWire  `json:"retained_source_review,omitempty"`
	RetainedSourceReviews    []sourceReviewWire `json:"retained_source_reviews,omitempty"`
	ProviderTask             []byte             `json:"provider_task"`
}

type sourceReviewWire struct {
	TaskID           string `json:"task_id"`
	ChangeID         string `json:"change_id"`
	TaskWorkRevision uint64 `json:"task_work_revision"`
	ChangeRevision   uint64 `json:"change_revision"`
	BaseCommit       string `json:"base_commit"`
	HeadCommit       string `json:"head_commit"`
	SourcePath       string `json:"source_path"`
	GitDirectory     string `json:"git_directory"`
}

func EncodeConfig(config Config) ([]byte, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	wire := configWire{GitAuthor: config.GitAuthor, CustomerMaintainer: config.CustomerMaintainer,
		Provider: config.Provider.String(), Role: config.Role.String(), Model: config.Model, ReasoningEffort: config.ReasoningEffort,
		AgentID: config.AgentID, TaskIncarnationID: config.TaskIncarnationID, PreviousWorkingDirectory: config.PreviousWorkingDirectory,
		RuntimePath: config.RuntimePath, RuntimeIdentity: identityWire{Device: config.RuntimeIdentity.Device, Inode: config.RuntimeIdentity.Inode},
		GitExecutable: config.GitExecutable, FactoryctlExecutable: config.FactoryctlExecutable, ToolPath: config.ToolPath, ToolchainReadRoots: config.ToolchainReadRoots, LocalCILeaseDir: config.LocalCILeaseDir, AccountHome: config.AccountHome, AccountConfigDir: config.AccountConfigDir,
		RepositoryGitIdentity: identityWire{Device: config.RepositoryGitIdentity.Device(), Inode: config.RepositoryGitIdentity.Inode()}, RepositoryOriginDigest: config.RepositoryOriginDigest,
		RepositoryRoot: config.RepositoryRoot, RepositoryIdentity: identityWire{Device: config.RepositoryIdentity.Device(), Inode: config.RepositoryIdentity.Inode()}, GitCommonDir: config.GitCommonDir, Revision: config.Revision,
		ChangeParent: config.ChangeParent, FinalName: config.FinalName,
		AttemptSocket: config.AttemptSocket, ProviderTask: bytes.Clone(config.ProviderTask),
	}
	if config.Retained != nil {
		retained := resultToWire(*config.Retained)
		wire.Retained = &retained
	}
	if config.RetainedSourceReview != nil {
		r := config.RetainedSourceReview
		wire.RetainedSourceReview = &sourceReviewWire{r.TaskID, r.ChangeID, r.TaskWorkRevision, r.ChangeRevision, r.BaseCommit, r.HeadCommit, r.SourcePath, r.GitDirectory}
	}
	for _, r := range config.RetainedSourceReviews {
		wire.RetainedSourceReviews = append(wire.RetainedSourceReviews, sourceReviewWire{r.TaskID, r.ChangeID, r.TaskWorkRevision, r.ChangeRevision, r.BaseCommit, r.HeadCommit, r.SourcePath, r.GitDirectory})
	}
	return encodeJSON(wire, ConfigLimit)
}

func DecodeConfig(encoded []byte) (Config, error) {
	var wire configWire
	if err := decodeJSON(encoded, ConfigLimit, &wire); err != nil {
		return Config{}, err
	}
	providerKind, err := providerFromString(wire.Provider)
	if err != nil {
		return Config{}, err
	}
	role, err := roleFromString(wire.Role)
	if err != nil {
		return Config{}, err
	}
	repositoryGitIdentity, _ := change.NewRepositoryIdentity(wire.RepositoryGitIdentity.Device, wire.RepositoryGitIdentity.Inode)
	repositoryIdentity, err := change.NewRepositoryIdentity(wire.RepositoryIdentity.Device, wire.RepositoryIdentity.Inode)
	if err != nil {
		return Config{}, invalidContract(err)
	}
	var retained *Result
	if wire.Retained != nil {
		result, resultErr := resultFromWire(*wire.Retained)
		if resultErr != nil {
			return Config{}, resultErr
		}
		retained = &result
	}
	var sourceReview *SourceReview
	if wire.RetainedSourceReview != nil {
		r := wire.RetainedSourceReview
		sourceReview = &SourceReview{r.TaskID, r.ChangeID, r.TaskWorkRevision, r.ChangeRevision, r.BaseCommit, r.HeadCommit, r.SourcePath, r.GitDirectory}
	}
	var sourceReviews []SourceReview
	for _, r := range wire.RetainedSourceReviews {
		sourceReviews = append(sourceReviews, SourceReview{r.TaskID, r.ChangeID, r.TaskWorkRevision, r.ChangeRevision, r.BaseCommit, r.HeadCommit, r.SourcePath, r.GitDirectory})
	}
	config := Config{GitAuthor: wire.GitAuthor, CustomerMaintainer: wire.CustomerMaintainer,
		Provider: providerKind, Role: role, Model: wire.Model, ReasoningEffort: wire.ReasoningEffort,
		AgentID: wire.AgentID, TaskIncarnationID: wire.TaskIncarnationID, PreviousWorkingDirectory: wire.PreviousWorkingDirectory,
		RuntimePath: wire.RuntimePath, RuntimeIdentity: runner.FileIdentity{Device: wire.RuntimeIdentity.Device, Inode: wire.RuntimeIdentity.Inode},
		GitExecutable: wire.GitExecutable, FactoryctlExecutable: wire.FactoryctlExecutable, ToolPath: wire.ToolPath, ToolchainReadRoots: wire.ToolchainReadRoots, LocalCILeaseDir: wire.LocalCILeaseDir, AccountHome: wire.AccountHome, AccountConfigDir: wire.AccountConfigDir,
		RepositoryGitIdentity: repositoryGitIdentity, RepositoryOriginDigest: wire.RepositoryOriginDigest,
		RepositoryRoot: wire.RepositoryRoot, RepositoryIdentity: repositoryIdentity, GitCommonDir: wire.GitCommonDir, Revision: wire.Revision,
		ChangeParent: wire.ChangeParent, FinalName: wire.FinalName,
		AttemptSocket: wire.AttemptSocket, Retained: retained, ProviderTask: bytes.Clone(wire.ProviderTask),
		RetainedSourceReview: sourceReview, RetainedSourceReviews: sourceReviews,
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validateConfig(config Config) error {
	if !config.GitAuthor.Valid() {
		return ErrInvalidContract
	}
	paths := []string{config.RuntimePath, config.GitExecutable, config.FactoryctlExecutable, config.AccountHome, config.RepositoryRoot, config.ChangeParent, config.AttemptSocket, config.GitCommonDir}
	for _, path := range paths {
		if !validAbsolute(path, maximumLocatorBytes) {
			return invalidContract(nil)
		}
	}
	if filepath.Base(config.GitCommonDir) != ".git" || filepath.Dir(config.GitCommonDir) != config.RepositoryRoot {
		return invalidContract(nil)
	}
	if config.LocalCILeaseDir != "" && (!validAbsolute(config.LocalCILeaseDir, maximumLocatorBytes) || filepath.Base(config.LocalCILeaseDir) != "dark-factory-local-ci") {
		return ErrInvalidContract
	}
	if config.AccountConfigDir != "" && !validAbsolute(config.AccountConfigDir, maximumLocatorBytes) {
		return invalidContract(nil)
	}
	if config.PreviousWorkingDirectory != "" && (config.Role != kernel.RoleOrchestrator || !validAbsolute(config.PreviousWorkingDirectory, maximumLocatorBytes)) {
		return invalidContract(nil)
	}
	if len(config.AttemptSocket) > install.MaxSocketPathBytes || config.RuntimeIdentity.Device == 0 || config.RuntimeIdentity.Inode == 0 ||
		kernel.ValidateProviderLaunchControls(config.Provider, config.Model, config.ReasoningEffort) != nil || provider.ValidateToolPath(config.ToolPath) != nil || !install.ToolchainReadRootsAllowed(config.ToolchainReadRoots, config.AccountHome, config.AccountConfigDir, config.RuntimePath, config.RepositoryRoot, config.ChangeParent) ||
		!validText(config.Revision, maximumRevisionBytes) || config.Role.String() == "" ||
		!validText(config.AgentID, maximumSessionKeyPartBytes) || !validText(config.TaskIncarnationID, maximumSessionKeyPartBytes) {
		return invalidContract(nil)
	}
	if config.Role == kernel.RoleOrchestrator {
		if config.FinalName != "" || config.Retained != nil {
			return invalidContract(nil)
		}
	} else if !validChangeName(config.FinalName) {
		return invalidContract(nil)
	}
	if _, _, err := prepareProviderTask(config.Provider, config.ProviderTask); err != nil {
		return invalidContract(err)
	}
	if _, err := change.NewRepositoryIdentity(config.RepositoryIdentity.Device(), config.RepositoryIdentity.Inode()); err != nil {
		return invalidContract(err)
	}
	if config.Retained == nil && !(change.RepositorySourceIdentity{Root: config.RepositoryIdentity, Git: config.RepositoryGitIdentity, OriginDigest: config.RepositoryOriginDigest}).Valid() {
		return invalidContract(nil)
	}
	if config.Retained != nil {
		if validateResult(*config.Retained) != nil {
			return invalidContract(nil)
		}
	}
	if config.RetainedSourceReview != nil && validateSourceReview(*config.RetainedSourceReview) != nil {
		return invalidContract(nil)
	}
	for _, review := range config.RetainedSourceReviews {
		if validateSourceReview(review) != nil {
			return invalidContract(nil)
		}
	}
	return nil
}

func validateSourceReview(review SourceReview) error {
	if !validText(review.TaskID, maximumSessionKeyPartBytes) || !validText(review.ChangeID, maximumSessionKeyPartBytes) || review.TaskWorkRevision == 0 || review.ChangeRevision == 0 || !validText(review.BaseCommit, maximumRevisionBytes) || !validText(review.HeadCommit, maximumRevisionBytes) || !validAbsolute(review.SourcePath, maximumLocatorBytes) || !validAbsolute(review.GitDirectory, maximumLocatorBytes) || filepath.Base(review.GitDirectory) != ".git" {
		return ErrInvalidContract
	}
	return nil
}

func prepareProviderTask(kind kernel.Provider, task []byte) (provider.TaskDelivery, []byte, error) {
	if kind == kernel.ProviderCodex {
		if len(task) != 0 {
			return 0, nil, provider.ErrInvalid
		}
		return provider.TaskDeliveryAttemptAPI, nil, nil
	}
	return provider.PrepareTask(kind, task)
}

func roleFromString(value string) (kernel.AgentRole, error) {
	for _, candidate := range []kernel.AgentRole{kernel.RoleWorker, kernel.RoleOrchestrator} {
		if value == candidate.String() {
			return candidate, nil
		}
	}
	return 0, invalidContract(nil)
}

func providerFromString(value string) (kernel.Provider, error) {
	for _, candidate := range []kernel.Provider{kernel.ProviderShell, kernel.ProviderClaudeCode, kernel.ProviderCodex} {
		if value == candidate.String() {
			return candidate, nil
		}
	}
	return 0, invalidContract(nil)
}

func EncodeResult(result Result) ([]byte, error) {
	if err := validateResult(result); err != nil {
		return nil, err
	}
	return encodeJSON(resultToWire(result), ResultLimit)
}

func DecodeResult(encoded []byte) (Result, error) {
	var wire resultWire
	if err := decodeJSON(encoded, ResultLimit, &wire); err != nil {
		return Result{}, err
	}
	return resultFromWire(wire)
}

func validateResult(result Result) error {
	if result.Format.OIDLength() == 0 || result.Base.Format() != result.Format || len(result.Base.Bytes()) != result.Format.OIDLength() ||
		result.Head != nil && (result.Head.Format() != result.Format || len(result.Head.Bytes()) != result.Format.OIDLength()) {
		return invalidContract(nil)
	}
	return nil
}

func resultToWire(result Result) resultWire {
	wire := resultWire{Format: result.Format.Name(), Base: result.Base.Hex()}
	if result.Head != nil {
		wire.Head = result.Head.Hex()
	}
	return wire
}

func resultFromWire(wire resultWire) (Result, error) {
	format, err := change.NewObjectFormat(wire.Format)
	if err != nil {
		return Result{}, invalidContract(err)
	}
	base, err := decodeObjectID(format, wire.Base)
	if err != nil {
		return Result{}, err
	}
	result := Result{Format: format, Base: base}
	if wire.Head != "" {
		head, err := decodeObjectID(format, wire.Head)
		if err != nil {
			return Result{}, err
		}
		result.Head = &head
	}
	if err := validateResult(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func decodeObjectID(format change.ObjectFormat, encoded string) (change.ObjectID, error) {
	raw, err := hex.DecodeString(encoded)
	if err != nil || strings.ToLower(encoded) != encoded {
		return change.ObjectID{}, invalidContract(err)
	}
	id, err := change.NewObjectID(format, raw)
	if err != nil {
		return change.ObjectID{}, invalidContract(err)
	}
	return id, nil
}

func encodeJSON(value any, maximum int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximum {
		return nil, invalidContract(err)
	}
	return encoded, nil
}

func decodeJSON(encoded []byte, maximum int, value any) error {
	if len(encoded) == 0 || len(encoded) > maximum || value == nil {
		return invalidContract(nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return invalidContract(err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return invalidContract(err)
	}
	return nil
}

func validText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validAbsolute(value string, maximum int) bool {
	return validText(value, maximum) && filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator)
}

func validChangeName(value string) bool {
	return validText(value, 255) && filepath.Base(value) == value && value != "." && value != ".." && !strings.EqualFold(value, ".git")
}

func invalidContract(error) error { return ErrInvalidContract }
