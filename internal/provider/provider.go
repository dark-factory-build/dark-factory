package provider

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	shellPath  = "/bin/sh"
	claudeTool = "claude"
	// maintainerBridge is the Maintainer App's MCP bridge. An orchestrator
	// launch names it to Claude, which is how an overseer publishes: the
	// daemon itself exposes no repository or publication operation.
	maintainerBridge = "dark-factory-maintainer-mcp-bridge"
	// GitIdentityName and GitIdentityEmail author a worker's local commits.
	GitIdentityName      = gitauthor.AutomationName
	GitIdentityEmail     = gitauthor.AutomationEmail
	codexTool            = "codex"
	maxPathBytes         = 4096
	claudeConfigDir      = ".claude"
	codexConfigDir       = ".codex"
	codexBootstrapPrompt = `Use the factory_attempt.factory tool with argv ["attempt","task"] before doing anything else. The returned JSON task field is the exact task: complete only that task. Use this tool for every factoryctl attempt or overseer command, passing argv without the executable; shell commands cannot access the attempt API. Peer collaboration is asynchronous: use argv ["attempt","peer","status"] to read or answer task-linked questions, but it grants no task or terminal control. For a stale paged peer status, restart from the first page. Before exiting, report the durable outcome with attempt succeed, block, or fail through this tool.` + " " + runner.DiscoveryInstructions
)

var (
	ErrInvalid     = errors.New("provider: invalid launch contract")
	ErrUnavailable = errors.New("provider: unavailable")
)

// Installation binds one provider kind to one exact executable commitment.
type Installation struct {
	provider   kernel.Provider
	executable runner.ExecutableCommitment
}

// ResolveInstallation selects one executable from the daemon's fixed tool
// path. Native tool locators may be symlinks, but the returned commitment is
// always to the resolved direct Mach-O target; no symlink is trusted again at
// exec. An invalid existing candidate fails closed instead of falling through
// to a different executable with the same name.
func ResolveInstallation(kind kernel.Provider, toolPath string) (Installation, error) {
	if !validToolPath(toolPath) {
		return Installation{}, ErrInvalid
	}
	if kind == kernel.ProviderShell {
		executable, err := runner.CommitExecutableLocator(shellPath)
		if err != nil {
			return Installation{}, unavailable(kind)
		}
		return Installation{provider: kind, executable: executable}, nil
	}
	var tool string
	switch kind {
	case kernel.ProviderClaudeCode:
		tool = claudeTool
	case kernel.ProviderCodex:
		tool = codexTool
	default:
		return Installation{}, ErrInvalid
	}
	executable, err := resolveTool(toolPath, tool)
	if err != nil {
		return Installation{}, unavailable(kind)
	}
	return Installation{provider: kind, executable: executable}, nil
}

// walkToolPath resolves the first tool of that name on the fixed tool path:
// the search is ordered, and an existing candidate that cannot be resolved
// fails closed rather than falling through to another.
func walkToolPath(toolPath, tool string) (string, error) {
	for _, directory := range filepath.SplitList(toolPath) {
		candidate := filepath.Join(directory, tool)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", ErrUnavailable
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !validAbsolute(resolved, maxPathBytes) {
			return "", ErrUnavailable
		}
		return resolved, nil
	}
	return "", ErrUnavailable
}

// resolveTool commits the first tool of that name on the fixed tool path.
func resolveTool(toolPath, tool string) (runner.ExecutableCommitment, error) {
	resolved, err := walkToolPath(toolPath, tool)
	if err != nil {
		return runner.ExecutableCommitment{}, err
	}
	executable, err := runner.CommitExecutableLocator(resolved)
	if err != nil {
		return runner.ExecutableCommitment{}, ErrUnavailable
	}
	return executable, nil
}

// errBridgeUnfit names a bridge that is on the path but not a regular file
// executable by its owner and writable by nobody else; the commitment a CLI
// gets is not asked of it, since Claude spawns the bridge itself much later
// and it may be a script.
var errBridgeUnfit = errors.New("provider: MCP bridge is not a regular owner-only executable")

// resolveBridge finds an operator-installed MCP bridge on the fixed tool path.
func resolveBridge(toolPath string, tool string) (string, error) {
	resolved, err := walkToolPath(toolPath, tool)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 || info.Mode().Perm()&0o022 != 0 {
		return "", errors.Join(ErrUnavailable, errBridgeUnfit)
	}
	return resolved, nil
}

// ConfigDirName is the directory a provider CLI keeps its login and
// configuration in, under an account home. Shell keeps none, so it answers
// empty. This is the one definition of those names: the launch environment
// below and the daemon's account discovery and default reader all derive
// their paths from it, so a launch and a reading of it cannot disagree.
func ConfigDirName(kind kernel.Provider) string {
	switch kind {
	case kernel.ProviderClaudeCode:
		return claudeConfigDir
	case kernel.ProviderCodex:
		return codexConfigDir
	}
	return ""
}

// ConfigHome is the directory a provider CLI reads its configuration from
// under one account home. It is the one place that join is spelled: the launch
// environment, the daemon's default reader and its account discovery all ask
// here, so none of them can name a different directory than a run uses.
func ConfigHome(kind kernel.Provider, accountHome string) string {
	name := ConfigDirName(kind)
	if accountHome == "" || name == "" {
		return ""
	}
	return filepath.Join(accountHome, name)
}

// codexConfigHome is the exact CODEX_HOME a Codex launch reads, and the one
// place that rule is spelled: the launch environment and Codex session
// discovery both ask here, so discovery cannot look in a different account
// than the one a launch actually uses.
func codexConfigHome(runtime RuntimePaths) string {
	if runtime.accountConfig != "" {
		return runtime.accountConfig
	}
	return ConfigHome(kernel.ProviderCodex, runtime.accountHome)
}

// claudeConfigHome is the effective directory a Claude Code launch's own
// configuration and native session transcripts live under: a linked
// account's own directory when one differs from the default, else the
// account home's own .claude directory (what the CLI reaches by default
// through HOME). This is the one place that rule is spelled: the launch
// environment (CLAUDE_CONFIG_DIR) and Claude session discovery both ask
// here, so a linked launch's discovery can never disagree with the
// directory the CLI itself was actually told to use.
func claudeConfigHome(runtime RuntimePaths) string {
	if configDir := filepath.Dir(ClaudeConfigFile(runtime.accountHome, runtime.accountConfig)); configDir != runtime.accountHome {
		return configDir
	}
	return ConfigHome(kernel.ProviderClaudeCode, runtime.accountHome)
}

func (Installation) String() string   { return "provider installation (private)" }
func (Installation) GoString() string { return "provider.Installation{private}" }

// RuntimePaths is the complete set of external strings permitted to enter the
// provider environment. Construction is lexical only; the daemon-owned Change
// worker must retain and revalidate the exact filesystem/executable
// capabilities that make these paths true immediately around Build and exec.
// This value is never authority by itself.
type RuntimePaths struct {
	gitAuthor                                                                gitauthor.Identity
	customerMaintainer                                                       bool
	localCILeaseDir                                                          string
	home, temp, socket, token, factoryctl, gitCeiling, toolPath, accountHome string
	// accountConfig is one linked provider login's own configuration
	// directory. Empty means the provider's default, which is what every
	// launch used before accounts existed.
	accountConfig      string
	toolchainReadRoots string
	// gitCommonDir is the project repository's Git directory, where the
	// Change worktree's index, refs and objects live. A worker's local
	// commands write it; an orchestrator's read it for review and
	// publication. It is metadata access, not a credential: the provider
	// environment still has no Git credential helper, SSH or prompt.
	gitCommonDir         string
	gitCommonDirWritable bool
	sourceReviewPaths    []retainedSourcePath
}

type retainedSourcePath struct{ source, git string }

// WithLocalCILeaseDirectory carries daemon-resolved lease storage below the
// Git directory into a worker.
func (runtime RuntimePaths) WithLocalCILeaseDirectory(path string) (RuntimePaths, error) {
	if path != "" && (!validAbsolute(path, maxPathBytes) || filepath.Base(path) != "dark-factory-local-ci") {
		return RuntimePaths{}, ErrInvalid
	}
	runtime.localCILeaseDir = path
	return runtime, nil
}

// WithGitCommonDirectory grants the project repository's Git directory to
// local commands: writable for a worker committing on its Change branch,
// read-only for an orchestrator reading a settled Change's commits.
func (runtime RuntimePaths) WithGitCommonDirectory(path string, writable bool) (RuntimePaths, error) {
	if !validAbsolute(path, maxPathBytes) || filepath.Base(path) != ".git" || path == runtime.home || path == runtime.temp {
		return RuntimePaths{}, ErrInvalid
	}
	runtime.gitCommonDir, runtime.gitCommonDirWritable = path, writable
	return runtime, nil
}

// WithRetainedSourceReview grants one daemon-resolved retained Change to a
// reviewer. Both paths come from an authenticated, revision-checked source
// receipt; local commands may read them but never write them. Repeated calls
// are used by an orchestrator that may inspect several handoffs.
func (runtime RuntimePaths) WithRetainedSourceReview(sourcePath, gitDirectory string) (RuntimePaths, error) {
	if !validAbsolute(sourcePath, maxPathBytes) || !validAbsolute(gitDirectory, maxPathBytes) || filepath.Base(gitDirectory) != ".git" || sourcePath == runtime.home || sourcePath == runtime.temp {
		return RuntimePaths{}, ErrInvalid
	}
	runtime.sourceReviewPaths = append(runtime.sourceReviewPaths, retainedSourcePath{sourcePath, gitDirectory})
	return runtime, nil
}

func (runtime RuntimePaths) WithGitAuthor(author gitauthor.Identity) (RuntimePaths, error) {
	if !author.Valid() {
		return RuntimePaths{}, ErrInvalid
	}
	runtime.gitAuthor = author
	return runtime, nil
}

// WithCustomerMaintainer uses the installed attempt bridge for opted-in homes.
func (runtime RuntimePaths) WithCustomerMaintainer(enabled bool) RuntimePaths {
	runtime.customerMaintainer = enabled
	return runtime
}

func NewRuntimePaths(home, temp, socket, token, factoryctl, gitCeiling, toolPath, accountHome, accountConfig, toolchainReadRoots string) (RuntimePaths, error) {
	runtime := RuntimePaths{
		home: home, temp: temp, socket: socket, token: token,
		factoryctl: factoryctl, gitCeiling: gitCeiling, toolPath: toolPath, accountHome: accountHome,
		accountConfig: accountConfig, toolchainReadRoots: toolchainReadRoots,
	}
	if !runtime.valid() {
		return RuntimePaths{}, ErrInvalid
	}
	return runtime, nil
}

func (RuntimePaths) String() string   { return "provider runtime paths (private)" }
func (RuntimePaths) GoString() string { return "provider.RuntimePaths{private}" }

type Request struct {
	provider          kernel.Provider
	installation      Installation
	model             string
	reasoningEffort   string
	runtime           RuntimePaths
	workingDirectory  string
	role              kernel.AgentRole
	agentID           string
	taskIncarnationID string
	// previousWorkingDirectory is set only for an orchestrator, from the
	// same agent's most recent terminal run (see
	// kernel.Store.LatestTerminalRuntimeRoot). See WithPreviousWorkingDirectory.
	previousWorkingDirectory string
}

// WithPreviousWorkingDirectory names the working directory a previous run of
// the same orchestrator agent launched its provider in. An orchestrator's own
// working directory is a fresh runtime root every run (unlike a worker's
// stable Change directory), so it could never itself be found again; Build
// uses this instead to look for that previous run's own Codex session. Empty
// means no known previous run, and is the value every non-orchestrator
// request keeps by leaving this unset.
func (request Request) WithPreviousWorkingDirectory(path string) (Request, error) {
	if path != "" && !validAbsolute(path, maxPathBytes) {
		return Request{}, ErrInvalid
	}
	request.previousWorkingDirectory = path
	return request, nil
}

// agentID and taskIncarnationID name the exact agent and task incarnation this
// attempt belongs to. Build uses them only for Claude Code worker launches, to
// derive a deterministic native-session key (see claudeSessionSelection); every
// caller still supplies them so one validation rule covers every request.
func NewRequest(kind kernel.Provider, installation Installation, model, reasoningEffort string, runtime RuntimePaths, workingDirectory string, role kernel.AgentRole, agentID, taskIncarnationID string) (Request, error) {
	if kernel.ValidateProviderLaunchControls(kind, model, reasoningEffort) != nil || installation.provider != kind || installation.executable.Path() == "" || !runtime.valid() || role.String() == "" ||
		!validValue(agentID, maxSessionKeyPartBytes) || !validValue(taskIncarnationID, maxSessionKeyPartBytes) ||
		kind == kernel.ProviderCodex && (!validAbsolute(workingDirectory, maxPathBytes) || len(codexUntrustedProjectConfig(workingDirectory)) > runner.MaxArgumentBytes) {
		return Request{}, ErrInvalid
	}
	return Request{
		provider: kind, installation: installation,
		model: model, reasoningEffort: reasoningEffort, runtime: runtime, workingDirectory: workingDirectory, role: role,
		agentID: agentID, taskIncarnationID: taskIncarnationID,
	}, nil
}

func (Request) String() string   { return "provider build request (private)" }
func (Request) GoString() string { return "provider.Request{private}" }

// Launch is the entire provider-owned result. It deliberately has no cwd,
// task input, descriptors, callbacks, process controls, or output decoder.
type Launch struct {
	executable   runner.ExecutableCommitment
	argv         []string
	environment  []string
	taskDelivery TaskDelivery
}

func (launch Launch) Executable() runner.ExecutableCommitment { return launch.executable }
func (launch Launch) Argv() []string                          { return append([]string(nil), launch.argv...) }
func (launch Launch) Environment() []string                   { return append([]string(nil), launch.environment...) }
func (launch Launch) TaskDelivery() TaskDelivery              { return launch.taskDelivery }

func (Launch) String() string   { return "provider launch (private)" }
func (Launch) GoString() string { return "provider.Launch{private}" }

// TaskDelivery is the one task-input channel selected with a provider launch.
// Shell reads its program from the inherited sealed descriptor. Claude receives
// its prompt once through the PTY. Codex starts from a fixed positional prompt
// and reads the exact task through its attempt-scoped local API capability.
type TaskDelivery uint8

const (
	TaskDeliveryFD11 TaskDelivery = iota + 1
	TaskDeliveryStartupTerminal
	TaskDeliveryAttemptAPI
)

const (
	// nativeSessionRotateBytes bounds one native provider transcript before
	// Build starts a fresh session instead of resuming it, for both Claude
	// Code and Codex.
	// ponytail: a single size ceiling is the simplest measurable growth bound
	// across an unbounded run of send-back retries on one task incarnation;
	// revisit if 32 MiB proves too eager or too late for real transcripts.
	nativeSessionRotateBytes = 32 << 20
	// maxClaudeSessionGenerations bounds the rotation search below. Reaching
	// it would mean thousands of rotations on one incarnation; ponytail:
	// defensive ceiling only, never expected to bind in practice.
	maxClaudeSessionGenerations = 1000
	maxSessionKeyPartBytes      = 128
	// maxCodexScanDays bounds how far back codexSessionSelection looks for a
	// matching rollout: a session worth resuming was active recently, and
	// this keeps discovery a bounded directory walk, not an unbounded one
	// growing with the account's whole history.
	maxCodexScanDays = 30
	// maxCodexRolloutHeaderBytes bounds the read of a rollout's first JSON
	// line (its session_meta record); Codex's own recorded metadata is small,
	// so a candidate whose first line does not fit is skipped, not trusted.
	maxCodexRolloutHeaderBytes = 64 << 10
)

// claudeSessionNamespace is a fixed, arbitrary namespace for the UUID v5 IDs
// claudeSessionSelection derives; it need not be registered, only stable.
var claudeSessionNamespace = sha256.Sum256([]byte("dark-factory.claude-code.session"))

// claudeSessionSelection derives the native Claude Code session this worker
// launch should use and decides fresh vs resume by whether that session's
// transcript already exists on disk. Claude Code keys a conversation's
// transcript by the exact launch cwd under its effective config directory
// (claudeConfigHome, matching the launch's own CLAUDE_CONFIG_DIR:
// <config-home>/projects/<escaped-cwd>/<uuid>.jsonl, see docs/providers.md),
// and a worker's cwd is its task incarnation's Change directory, which a
// send-back retry reuses (internal/kernel/change.go: one Change row per
// project+task+incarnation). Deriving the id from provider+agent+incarnation
// means no extra state is needed to remember which session belongs to which
// task: the same retry always recomputes the same id.
func claudeSessionSelection(runtime RuntimePaths, cwd, agentID, taskIncarnationID string) (id string, resume bool, err error) {
	projectDir := filepath.Join(claudeConfigHome(runtime), "projects", escapeClaudeProjectPath(cwd))
	seed := "claude-code\x00" + agentID + "\x00" + taskIncarnationID
	for generation := 0; generation < maxClaudeSessionGenerations; generation++ {
		candidate := formatUUID(uuidV5(claudeSessionNamespace, []byte(fmt.Sprintf("%s\x00%d", seed, generation))))
		info, statErr := os.Stat(filepath.Join(projectDir, candidate+".jsonl"))
		if statErr == nil && info.Size() < nativeSessionRotateBytes {
			return candidate, true, nil
		}
		if statErr != nil {
			// Missing, or some other stat failure: nothing provably resumable
			// exists at this generation, so start fresh with this exact id
			// rather than fail a launch over an absent or unreadable file.
			return candidate, false, nil
		}
	}
	return "", false, ErrInvalid
}

// escapeClaudeProjectPath is the CLI's own cwd-to-directory-name mapping:
// every character outside [A-Za-z0-9] becomes '-' (observed: a Change under
// `.dark-factory-recovered/changes/` is recorded under
// `-dark-factory-recovered-changes-`). It must match exactly, because a miss
// does not merely start fresh: the retry relaunches `--session-id` with the
// same derived id, which the CLI refuses as already in use and exits 1 before
// any attempt outcome.
func escapeClaudeProjectPath(cwd string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, cwd)
}

// uuidV5 and formatUUID implement RFC 4122 UUID version 5 (SHA-1 name-based)
// generation; the standard library has no UUID package.
func uuidV5(namespace [sha256.Size]byte, name []byte) [16]byte {
	hash := sha1.New()
	hash.Write(namespace[:16])
	hash.Write(name)
	sum := hash.Sum(nil)
	var id [16]byte
	copy(id[:], sum)
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

func formatUUID(id [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

// codexRolloutMeta is the first JSON line of a Codex rollout file, the exact
// fields codexSessionSelection needs: the launch cwd, to match a candidate
// against a working directory, and the resumable session/thread id (its
// filename's own trailing UUID for an ordinary, non-subagent session).
type codexRolloutMeta struct {
	Payload struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

// canonicalUUID reports whether value is a lowercase 8-4-4-4-12 hyphenated
// hex UUID: the exact text shape formatUUID produces and codex resume's own
// SESSION_ID argument expects. A rollout's recorded payload.id is untrusted
// file content; anything of another shape (oversized, containing NULs,
// uppercase, or simply not a UUID) is never placed in argv, where the
// runner's own argument-size guard would otherwise turn a malformed
// recorded id into a failed launch instead of codexSessionSelection's
// intended fallback to a fresh session.
func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if c := value[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// codexSessionSelection looks for the newest Codex rollout recorded for the
// exact cwd under this launch's CODEX_HOME, newest calendar day first within
// maxCodexScanDays, and resumes it while it is under the shared rotation
// ceiling. Unlike Claude Code, Codex assigns its own session id at creation
// (see docs/providers.md: no CLI flag or config key chooses or names one), so
// there is nothing to derive; this only discovers an existing rollout already
// on disk. It never fails a launch: an unreadable or malformed candidate is
// skipped, and an unresolvable case answers fresh, the same as no session
// existing at all.
func codexSessionSelection(runtime RuntimePaths, cwd string) (id string, resume bool) {
	if cwd == "" {
		return "", false
	}
	sessionsRoot := filepath.Join(codexConfigHome(runtime), "sessions")
	scanned := 0
	for _, year := range sortedNumericEntriesDescending(sessionsRoot) {
		for _, month := range sortedNumericEntriesDescending(filepath.Join(sessionsRoot, year)) {
			for _, day := range sortedNumericEntriesDescending(filepath.Join(sessionsRoot, year, month)) {
				if scanned >= maxCodexScanDays {
					return "", false
				}
				scanned++
				rollouts, err := filepath.Glob(filepath.Join(sessionsRoot, year, month, day, "rollout-*.jsonl"))
				if err != nil {
					continue
				}
				slices.Sort(rollouts)
				slices.Reverse(rollouts)
				for _, rollout := range rollouts {
					meta, size, err := readCodexRolloutMeta(rollout)
					if err != nil || meta.Payload.Cwd != cwd || !canonicalUUID(meta.Payload.ID) {
						continue
					}
					if size < nativeSessionRotateBytes {
						return meta.Payload.ID, true
					}
					// The newest matching rollout is over the ceiling: rotate
					// to fresh rather than resume an older, smaller one and
					// jump the conversation backward.
					return "", false
				}
			}
		}
	}
	return "", false
}

// sortedNumericEntriesDescending lists a Codex sessions tree's YYYY/MM/DD
// child directories, newest first; their zero-padded names sort correctly as
// plain strings. A missing or unreadable directory answers no entries rather
// than an error: an account with no session history yet is not a fault.
func sortedNumericEntriesDescending(directory string) []string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && allDigits(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	return names
}

func allDigits(value string) bool {
	return value != "" && !strings.ContainsFunc(value, func(r rune) bool { return r < '0' || r > '9' })
}

// readCodexRolloutMeta reads and decodes only a rollout's first line, bounded
// to maxCodexRolloutHeaderBytes, and returns the file's exact size alongside
// it for the rotation check; codexSessionSelection never needs more.
func readCodexRolloutMeta(path string) (codexRolloutMeta, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return codexRolloutMeta{}, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return codexRolloutMeta{}, 0, err
	}
	defer file.Close()
	header, err := io.ReadAll(io.LimitReader(file, maxCodexRolloutHeaderBytes+1))
	if err != nil {
		return codexRolloutMeta{}, 0, err
	}
	line := header
	if index := bytes.IndexByte(header, '\n'); index >= 0 {
		line = header[:index]
	} else if int64(len(header)) > maxCodexRolloutHeaderBytes {
		return codexRolloutMeta{}, 0, ErrInvalid
	}
	var meta codexRolloutMeta
	if err := json.Unmarshal(line, &meta); err != nil {
		return codexRolloutMeta{}, 0, err
	}
	return meta, info.Size(), nil
}

// Build is the one closed provider-selection switch.
func Build(request Request) (Launch, error) {
	if err := request.installation.executable.Verify(); err != nil {
		return Launch{}, errors.Join(ErrUnavailable, err)
	}
	path := request.installation.executable.Path()
	browser := ""
	if request.provider != kernel.ProviderShell {
		for _, directory := range filepath.SplitList(request.runtime.toolPath) {
			if _, err := os.Lstat(filepath.Join(directory, "dark-factory-browser-mcp")); os.IsNotExist(err) {
				continue
			}
			var err error
			browser, err = resolveBridge(request.runtime.toolPath, "dark-factory-browser-mcp")
			if err != nil {
				return Launch{}, err
			}
			break
		}
	}
	browserArgs := []string{"--runtime-dir", request.runtime.temp}
	switch request.provider {
	case kernel.ProviderShell:
		if request.model != "" || request.reasoningEffort != "" || path != shellPath {
			return Launch{}, ErrInvalid
		}
		return Launch{
			executable:   request.installation.executable,
			argv:         []string{shellPath, runner.ProviderTaskPath},
			environment:  request.runtime.environment(request.provider),
			taskDelivery: TaskDeliveryFD11,
		}, nil
	case kernel.ProviderClaudeCode:
		// No user, project or local settings are read: a checkout's own
		// .claude/settings.json could otherwise merge rules that widen the
		// boundary below. The account login lives outside those files.
		argv := []string{path, "--permission-mode", "dontAsk", "--setting-sources", ""}
		if request.role == kernel.RoleWorker {
			id, resume, err := claudeSessionSelection(request.runtime, request.workingDirectory, request.agentID, request.taskIncarnationID)
			if err != nil {
				return Launch{}, err
			}
			if resume {
				argv = append(argv, "--resume", id)
			} else {
				argv = append(argv, "--session-id", id)
			}
		}
		if request.model != "" {
			argv = append(argv, "--model", request.model)
		}
		if request.reasoningEffort != "" {
			argv = append(argv, "--effort", request.reasoningEffort)
		}
		// Only the installed attempt, browser and orchestrator Maintainer servers
		// are allowed; account configuration and Change-local .mcp.json cannot
		// add servers.
		argv = append(argv, "--strict-mcp-config")
		servers := map[string]any{
			"factory_attempt": map[string]any{
				"command": request.runtime.factoryctl,
				"args":    []string{"attempt", "mcp"},
			},
		}
		environment := request.runtime.environment(request.provider)
		if browser != "" {
			servers["factory_browser"] = map[string]any{"command": browser, "args": browserArgs}
		}
		if request.role == kernel.RoleOrchestrator && request.runtime.customerMaintainer {
			servers["maintainer"] = map[string]any{"command": request.runtime.factoryctl, "args": []string{"attempt", "maintainer-mcp"}}
		} else if request.role == kernel.RoleOrchestrator {
			bridge, err := resolveBridge(request.runtime.toolPath, maintainerBridge)
			if err != nil {
				return Launch{}, errors.Join(err, fmt.Errorf("%s on %s", maintainerBridge, request.runtime.toolPath))
			}
			servers["maintainer"] = map[string]string{"command": bridge}
			environment = append(environment, "DARK_FACTORY_MAINTAINER_BRIDGE="+bridge)
		}
		settings, err := claudeSettings(request, slices.Sorted(maps.Keys(servers)))
		if err != nil {
			return Launch{}, err
		}
		argv = append(argv, "--settings", settings)
		config, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil || len(config) > runner.MaxArgumentBytes {
			return Launch{}, ErrInvalid
		}
		argv = append(argv, "--mcp-config", string(config))
		return Launch{
			executable: request.installation.executable, argv: argv,
			environment: environment, taskDelivery: TaskDeliveryStartupTerminal,
		}, nil
	case kernel.ProviderCodex:
		permissions, err := codexPermissions(request)
		if err != nil {
			return Launch{}, err
		}
		// A worker's launch cwd is its task incarnation's retained Change
		// directory, which a send-back retry reuses, so its own prior rollout
		// is found there directly. An orchestrator's launch cwd is a fresh
		// runtime root every run and could never itself be found again;
		// previousWorkingDirectory instead names the same agent's most recent
		// terminal run's cwd, so its standing tasks share one continuing
		// session. Neither ever changes argv beyond an optional leading
		// "resume <id>": Codex assigns its own session id, there is nothing
		// to derive.
		discoveryCwd := request.workingDirectory
		if request.role == kernel.RoleOrchestrator {
			discoveryCwd = request.previousWorkingDirectory
		}
		argv := []string{path}
		if id, resume := codexSessionSelection(request.runtime, discoveryCwd); resume {
			argv = append(argv, "resume", id)
		}
		// Codex's explicit resume can otherwise open its interactive CWD
		// chooser when the recorded session belongs to an earlier attempt
		// runtime. The runner's committed cwd is request.workingDirectory, so
		// select that authorized current directory through Codex's native
		// resume configuration before any prompt can be shown.
		notify := "notify=[" + tomlBasicString(request.runtime.factoryctl) + ", \"attempt\", \"turn-complete\"]"
		argv = append(argv, "-c", notify, "--strict-config", "--no-alt-screen", "-c", "tui.resume_cwd=\"current\"", "-c", "check_for_update_on_startup=false", "-c", "tool_output_token_limit=32768", "-c", codexUntrustedProjectConfig(request.workingDirectory), "-c", "default_permissions="+tomlBasicString(codexPermissionName(request.runtime)), "-c", `approval_policy="never"`, "-c", permissions, "--disable", "computer_use", "--disable", "browser_use", "--disable", "plugins")
		attemptServer := codexAttemptServerName(request.runtime)
		argv = append(argv, "-c", "mcp_servers."+attemptServer+"={command="+tomlBasicString(request.runtime.factoryctl)+`,args=["attempt","mcp"],env_vars=["DARK_FACTORY_SOCKET","DARK_FACTORY_ATTEMPT_TOKEN_FILE"],enabled=true,required=true,tools={factory={approval_mode="approve"}}}`)
		if browser != "" {
			args := make([]string, len(browserArgs))
			for i, arg := range browserArgs {
				args[i] = tomlBasicString(arg)
			}
			config := "mcp_servers.factory_browser={command=" + tomlBasicString(browser) + ",args=[" + strings.Join(args, ",") + "],env_vars=[\"DARK_FACTORY_FACTORYCTL\",\"DARK_FACTORY_SOCKET\",\"DARK_FACTORY_ATTEMPT_TOKEN_FILE\"],enabled=true,required=true,default_tools_approval_mode=\"approve\"}"
			if len(config) > runner.MaxArgumentBytes {
				return Launch{}, ErrInvalid
			}
			argv = append(argv, "-c", config)
		}
		if request.model != "" {
			argv = append(argv, "--model", request.model)
		}
		if request.reasoningEffort != "" {
			argv = append(argv, "-c", fmt.Sprintf("model_reasoning_effort=%q", request.reasoningEffort))
		}
		environment := request.runtime.environment(request.provider)
		if request.role == kernel.RoleOrchestrator && request.runtime.customerMaintainer {
			argv = append(argv, "-c", "mcp_servers.dark_factory_maintainer={command="+tomlBasicString(request.runtime.factoryctl)+`,args=["attempt","maintainer-mcp"],env_vars=["DARK_FACTORY_SOCKET","DARK_FACTORY_ATTEMPT_TOKEN_FILE"],enabled=true,required=true,default_tools_approval_mode="approve"}`)
		} else if request.role == kernel.RoleOrchestrator {
			bridge, err := resolveBridge(request.runtime.toolPath, maintainerBridge)
			if err != nil {
				return Launch{}, err
			}
			argv = append(argv, "-c", "mcp_servers.dark_factory_maintainer={command="+tomlBasicString(bridge)+`,enabled=true,required=true,default_tools_approval_mode="approve"}`)
			environment = append(environment, "DARK_FACTORY_MAINTAINER_BRIDGE="+bridge)
		}
		prompt := codexBootstrapPromptFor(request.runtime)
		if request.role == kernel.RoleOrchestrator {
			prompt += " You are the project overseer. If no causal context is supplied, perform full reconciliation. On a causal wake, first read its prior overseer task result and affected tasks using overseer status --task without a head fence, then use the returned current head for subsequent pages; reconcile every fixed-head page only at startup, recovery, stale/uncertain cursors, omissions, or an event that cannot be resolved narrowly. For a settled worker Change, request attempt source --task TASK_ID and verify its exact task/work/Change receipt; its branch and head_commit are the work, read from git_directory with git, and source_path is that branch's worktree; never reconstruct private paths. Follow next_offset with --offset and --head; use --task and next_text_offset for complete text. Delegate with overseer task add; supervise with task update, agent pause/resume, worker message, worker interrupt, worker stop, worker replace and human reply. Keep enduring acceptance criteria, prerequisites and owner authority in the complete base instruction using overseer task update --body while the task is queued; preserve the original acceptance criteria. Send-back replaces previous feedback, so use it only for current findings or pointers, not durable requirements. Use the factory tool description for exact flags. Routine supported task routing needs no checkout. Use the registered repository and its configured base for edits and checks; inspect the exact retained Change and resolve review findings before publishing through your Maintainer App. For accepted intake, the attempt task body is the accepted snapshot: preserve its acceptance criteria and destination when delegating, and never replace it with newer GitHub title or body text. Revised issue content requires operator acceptance. For Linear intake, carry its source_url into GitHub publication as external_source_url with issue_number 0 and close_on_merge false; do not look up or create a corresponding GitHub issue. For GitHub intake, carry the fully qualified source repository and issue number into publication; reference cross-repository sources without closing them, and never publish private source details into a public result. A successful Maintainer response's structuredContent is its result: do not repeat the identical read or write after its content acknowledgement; observe an ambiguous write instead. Respect direct operator interventions. Do not retry a known capability refusal until role, capability, or runtime state changes; correct malformed paging once and restart stale paging at page one. Continue actionable supervision and delivery in this session; when none remains, report a durable checkpoint and exit without idle polling. Events remain pending for the next supervision task. Use attempt request-human only for operator decisions; non-shell overseers yield and release their lane, while shell overseers remain live for the answer."
		}
		argv = append(argv, prompt)
		return Launch{
			executable: request.installation.executable, argv: argv,
			environment: environment, taskDelivery: TaskDeliveryAttemptAPI,
		}, nil
	default:
		return Launch{}, ErrInvalid
	}
}

// The provider keeps its account/model configuration, but local commands get
// only the Change, disposable runtime paths and the attempt API inputs. Codex's
// minimal platform profile still includes its documented system/temp exceptions.
// grant is one path a native provider's local commands may reach.
type grant struct {
	path  string
	write bool
}

// sandboxGrants is the single filesystem grant both native providers receive:
// the Change, disposable runtime paths, the attempt API inputs and the frozen
// toolchain. Each provider renders it in its own sandbox dialect.
func sandboxGrants(request Request) []grant {
	var grants []grant
	writePaths := []string{request.workingDirectory, request.runtime.home, request.runtime.temp}
	for i, path := range writePaths {
		if !slices.Contains(writePaths[:i], path) {
			grants = append(grants, grant{path, true})
		}
	}
	for _, path := range []string{request.installation.executable.Path(), request.runtime.factoryctl, request.runtime.token, request.runtime.socket} {
		grants = append(grants, grant{path, false})
	}
	// Node/Corepack reads the system OpenSSL configuration before dispatch.
	// Permit this file, not the surrounding directory or operator configuration.
	grants = append(grants, grant{"/System/Library/OpenSSL/openssl.cnf", false})
	for _, root := range filepath.SplitList(request.runtime.toolchainReadRoots) {
		grants = append(grants, grant{root, false})
	}
	if request.runtime.localCILeaseDir != "" {
		// The lease wrapper uses macOS Ruby's built-in setsid for its owned
		// process group. Grant only the interpreter, not a standard-library tree.
		grants = append(grants, grant{request.runtime.localCILeaseDir, true}, grant{"/usr/bin/ruby", false})
	}
	if request.runtime.gitCommonDir != "" {
		writable := request.runtime.gitCommonDirWritable
		for _, review := range request.runtime.sourceReviewPaths {
			if request.runtime.gitCommonDir == review.git {
				writable = false
				break
			}
		}
		grants = append(grants, grant{request.runtime.gitCommonDir, writable})
	}
	for _, review := range request.runtime.sourceReviewPaths {
		for _, path := range []string{review.source, review.git} {
			if path != request.workingDirectory && path != request.runtime.gitCommonDir && !slices.ContainsFunc(grants, func(given grant) bool { return given.path == path }) {
				grants = append(grants, grant{path, false})
			}
		}
	}
	return grants
}

func codexPermissions(request Request) (string, error) {
	entries := []string{`":root"="deny"`, `":minimal"="read"`}
	for _, grant := range sandboxGrants(request) {
		access := "read"
		if grant.write {
			access = "write"
		}
		entries = append(entries, tomlBasicString(grant.path)+`="`+access+`"`)
	}
	// Codex merges profile tables. Use the existing private runtime identity
	// rather than a shared name that could inherit an account profile.
	value := "permissions." + codexPermissionName(request.runtime) + `={filesystem={` + strings.Join(entries, ",") + `},network={enabled=true,unix_sockets={` + tomlBasicString(request.runtime.socket) + `="allow"}}}`
	if len(value) > runner.MaxArgumentBytes {
		return "", ErrInvalid
	}
	return value, nil
}

// claudeSettings confines a Claude Code run to the same grants Codex gets.
// dontAsk refuses every tool call no rule allows, which covers the file
// tools; the OS sandbox covers Bash, where user data is unreadable except for
// the granted paths. Network stays open, as it is for Codex; no Unix socket is.
// Denying every read crashes ordinary tools on macOS, so the denied regions
// are where user data lives, with the grants and the CLI's own per-user
// scratch directory (where it captures Bash output) re-allowed. System
// locations stay readable, as they are under Codex's minimal profile.
// ponytail: a home or volume mounted elsewhere needs its own entry.
var claudeDeniedReads = []string{"//Users", "//Volumes", "//Network", "//private/tmp", "//private/var/folders", "//private/var/root"}

func claudeSettings(request Request, servers []string) (string, error) {
	allow := []string{"Bash", "WebFetch", "WebSearch"}
	for _, server := range servers {
		allow = append(allow, "mcp__"+server)
	}
	read, write := []string{}, []string{}
	// The attempt socket and its token are withheld: outcomes go through the
	// factory_attempt tool, a child of the CLI outside this sandbox, so Bash
	// and the file tools get no route to the attempt API at all.
	grants := slices.DeleteFunc(sandboxGrants(request), func(given grant) bool {
		return given.path == request.runtime.token || given.path == request.runtime.socket
	})
	// Claude names files by their resolved path, and a Change is reached
	// through a link, so each grant is allowed under both spellings.
	for _, given := range slices.Clone(grants) {
		if resolved, err := filepath.EvalSymlinks(given.path); err == nil && resolved != given.path {
			grants = append(grants, grant{resolved, given.write})
		}
	}
	for _, grant := range grants {
		rule := "/" + grant.path // a leading "//" is Claude's absolute path
		read = append(read, rule)
		allow = append(allow, "Read("+rule+")", "Read("+rule+"/**)")
		if grant.write {
			write = append(write, rule)
			allow = append(allow, "Edit("+rule+"/**)")
		}
	}
	settings, err := json.Marshal(map[string]any{
		"permissions": map[string]any{"allow": allow},
		"sandbox": map[string]any{
			"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false, "autoAllowBashIfSandboxed": true,
			"filesystem": map[string]any{"allowWrite": write, "denyRead": claudeDeniedReads, "allowRead": append(read, fmt.Sprintf("//private/tmp/claude-%d", os.Getuid()))},
			"network":    map[string]any{"allowedDomains": []string{"*"}, "allowLocalBinding": true},
		},
	})
	if err != nil || len(settings) > runner.MaxArgumentBytes {
		return "", ErrInvalid
	}
	return string(settings), nil
}

func codexPermissionName(runtime RuntimePaths) string {
	return fmt.Sprintf("dark-factory-%x", sha256.Sum256([]byte(runtime.home)))
}

func codexAttemptServerName(runtime RuntimePaths) string {
	digest := sha256.Sum256([]byte(runtime.home))
	return fmt.Sprintf("factory_attempt_%x", digest[:8])
}

func codexBootstrapPromptFor(runtime RuntimePaths) string {
	return strings.Replace(codexBootstrapPrompt, "factory_attempt.factory", codexAttemptServerName(runtime)+".factory", 1)
}

func codexUntrustedProjectConfig(path string) string {
	return "projects={" + tomlBasicString(path) + "={trust_level=\"untrusted\"}}"
}

func tomlBasicString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// Strings cannot make json.Marshal fail; the path has already passed
		// validValue, which also excludes invalid UTF-8.
		panic("provider: failed to encode valid TOML path")
	}
	// TOML basic strings exclude C0 controls, DEL, and C1 controls. JSON
	// escapes C0 but permits the latter two ranges literally.
	encodedString := string(encoded)
	for character := rune(0x7f); character <= 0x9f; character++ {
		encodedString = strings.ReplaceAll(encodedString, string(character), fmt.Sprintf(`\u%04x`, character))
	}
	return encodedString
}

// PrepareTask is also available before executable selection so the daemon can
// freeze descriptor or terminal input where required. Codex task bytes remain
// in the daemon and are retrieved through the attempt-scoped API. Build returns
// the same closed delivery value, which the Change worker must compare before
// exec.
func PrepareTask(kind kernel.Provider, task []byte) (TaskDelivery, []byte, error) {
	if len(task) == 0 || len(task) > runner.MaxProviderTaskBytes || !utf8.Valid(task) || bytes.IndexByte(task, 0) >= 0 {
		return 0, nil, ErrInvalid
	}
	switch kind {
	case kernel.ProviderShell:
		return TaskDeliveryFD11, bytes.Clone(task), nil
	case kernel.ProviderClaudeCode:
		encoded, err := runner.PrepareClaudeTask(task)
		if err != nil {
			return 0, nil, ErrInvalid
		}
		return TaskDeliveryStartupTerminal, encoded, nil
	case kernel.ProviderCodex:
		// Codex reads this value through a shell-tool result. Keep the exact task
		// comfortably below the model-visible result bound even after JSON turns
		// every DEL/C1 code point into a six-byte escape.
		if len(task) > runner.MaxCodexTaskBytes {
			return 0, nil, ErrInvalid
		}
		return TaskDeliveryAttemptAPI, nil, nil
	default:
		return 0, nil, ErrInvalid
	}
}

// ClaudeConfigFile is the .claude.json a Claude Code launch reads, and the one
// place that rule is spelled: the launch environment names CLAUDE_CONFIG_DIR
// only when this file is not the account home's own, and the daemon's account
// discovery reads a login's identity from the same file. The default
// directory's file is beside it in the home, because that directory holds
// only local flags; a directory made with CLAUDE_CONFIG_DIR set carries its
// own.
func ClaudeConfigFile(accountHome, accountConfig string) string {
	if accountConfig != "" && accountConfig != ConfigHome(kernel.ProviderClaudeCode, accountHome) {
		return filepath.Join(accountConfig, ".claude.json")
	}
	return filepath.Join(accountHome, ".claude.json")
}

// errClaudeConfiguration names a failure around the account's own file
// without carrying its path into a run's durable failure detail.
var errClaudeConfiguration = errors.New("provider: claude configuration")

const maxClaudeConfigBytes = 16 << 20

// TrustClaudeDirectory records cwd as trusted in the account's Claude Code
// configuration, which is what answering the CLI's folder-trust dialog does.
// Every Change is a path the CLI has never seen, so without this record the
// interactive session stops at that dialog and the startup task is typed into
// it. Only this one key is added; every other value in the file is kept, with
// numbers as their own digits and strings unescaped, and a file whose shape
// is not the CLI's is refused rather than rewritten.
// ponytail: a read-modify-write like the CLI's own sessions do on the same
// file, published by rename so a reader never sees a torn file; a concurrent
// writer's key can still be lost between read and rename. Take a lock file
// if a lost update is ever observed. Every Change adds one entry that nothing
// removes; past maxClaudeConfigBytes every Claude launch on the account is
// refused. Prune entries whose directory is gone if that ceiling nears.
func TrustClaudeDirectory(runtime RuntimePaths, cwd string) error {
	if !runtime.valid() || !validAbsolute(cwd, maxPathBytes) {
		return ErrInvalid
	}
	path := ClaudeConfigFile(runtime.accountHome, runtime.accountConfig)
	config := map[string]any{}
	if raw, err := readClaudeConfig(path); err != nil {
		return err
	} else if len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&config); err != nil || config == nil {
			return errClaudeConfiguration
		}
	}
	projects, ok := config["projects"].(map[string]any)
	if !ok {
		if _, present := config["projects"]; present {
			return errClaudeConfiguration
		}
		projects = map[string]any{}
	}
	project, ok := projects[cwd].(map[string]any)
	if !ok {
		if _, present := projects[cwd]; present {
			return errClaudeConfiguration
		}
		project = map[string]any{}
	}
	if project["hasTrustDialogAccepted"] == true {
		return nil
	}
	project["hasTrustDialogAccepted"] = true
	projects[cwd] = project
	config["projects"] = projects
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(config); err != nil {
		return errClaudeConfiguration
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.*")
	if err != nil {
		return errClaudeConfiguration
	}
	_, writeErr := temp.Write(encoded.Bytes())
	if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil || os.Rename(temp.Name(), path) != nil {
		_ = os.Remove(temp.Name())
		return errClaudeConfiguration
	}
	return nil
}

// readClaudeConfig returns the file's bytes, none when there is no file yet
// or an empty one, and refuses one past the bound rather than decoding it.
func readClaudeConfig(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errClaudeConfiguration
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxClaudeConfigBytes+1))
	if err != nil || len(raw) > maxClaudeConfigBytes {
		return nil, errClaudeConfiguration
	}
	return raw, nil
}

func unavailable(kind kernel.Provider) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, kind.String())
}

func (runtime RuntimePaths) valid() bool {
	if runtime.localCILeaseDir != "" && (!validAbsolute(runtime.localCILeaseDir, maxPathBytes) || filepath.Base(runtime.localCILeaseDir) != "dark-factory-local-ci") {
		return false
	}
	paths := []string{runtime.home, runtime.temp, runtime.socket, runtime.token, runtime.factoryctl}
	for _, path := range paths {
		if !validAbsolute(path, maxPathBytes) {
			return false
		}
	}
	return install.ToolchainReadRootsAllowed(runtime.toolchainReadRoots, runtime.accountHome, runtime.accountConfig, runtime.gitCeiling, runtime.home, runtime.temp) && len(runtime.socket) <= install.MaxSocketPathBytes && runtime.home != runtime.temp &&
		validGitCeiling(runtime.gitCeiling) && validToolPath(runtime.toolPath) &&
		validAbsolute(runtime.accountHome, maxPathBytes-len("/"+codexConfigDir)) &&
		runtime.accountHome != runtime.home && runtime.accountHome != runtime.temp &&
		(runtime.accountConfig == "" || validAbsolute(runtime.accountConfig, maxPathBytes))
}

func (runtime RuntimePaths) environment(kind kernel.Provider) []string {
	home := runtime.home
	if kind == kernel.ProviderClaudeCode {
		home = runtime.accountHome
	}
	environment := []string{
		"DARK_FACTORY_TASK_ATTACHMENTS=" + filepath.Join(runtime.home, "task-attachments"),
		"DARK_FACTORY_SOCKET=" + runtime.socket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + runtime.token,
		"DARK_FACTORY_FACTORYCTL=" + runtime.factoryctl,
		"HOME=" + home,
		"TMPDIR=" + runtime.temp,
		"PATH=" + runtime.toolPath,
	}
	// A run whose agent selects an account points that CLI at the account's
	// own configuration directory. No account leaves the environment exactly
	// as it was.
	// Both native providers confine local commands to the grants above, so
	// build caches live in the private runtime home rather than the account's.
	if kind != kernel.ProviderShell {
		environment = append(environment,
			"GOCACHE="+filepath.Join(runtime.home, ".cache", "go-build"),
			"GOPATH="+filepath.Join(runtime.home, "go"),
			"GOMODCACHE="+filepath.Join(runtime.home, "go", "pkg", "mod"),
			"CARGO_HOME="+filepath.Join(runtime.home, ".cargo"),
			"RUSTUP_HOME="+filepath.Join(runtime.accountHome, ".rustup"),
			"COREPACK_HOME="+filepath.Join(runtime.home, ".cache", "corepack"),
			"npm_config_cache="+filepath.Join(runtime.home, ".cache", "npm"),
			"XDG_CACHE_HOME="+filepath.Join(runtime.home, ".cache"))
	}
	switch kind {
	case kernel.ProviderCodex:
		environment = append(environment, "CODEX_HOME="+codexConfigHome(runtime))
	case kernel.ProviderClaudeCode:
		// Only a directory beside the default one is named. The default is
		// what the CLI already reaches through HOME, and its OAuth account
		// lives in $HOME/.claude.json rather than inside it, so naming it
		// would point the CLI at the flags-only file it does contain and
		// launch the run with no login at all.
		if configDir := claudeConfigHome(runtime); configDir != ConfigHome(kernel.ProviderClaudeCode, runtime.accountHome) {
			environment = append(environment, "CLAUDE_CONFIG_DIR="+configDir)
		}
	}
	if runtime.localCILeaseDir != "" {
		environment = append(environment, "DARK_FACTORY_LOCAL_CI_DIRECTORY="+runtime.localCILeaseDir)
	}
	// Attribution comes from the daemon-verified operator, never host Git config.
	// Publication still uses the Maintainer App and its operation receipts.
	return append(environment,
		"LANG=C",
		"LC_ALL=C",
		"TERM=xterm-256color",
		"SHELL=/bin/sh",
		"GIT_AUTHOR_NAME="+runtime.gitAuthor.Name(),
		"GIT_AUTHOR_EMAIL="+runtime.gitAuthor.Email(),
		"GIT_COMMITTER_NAME="+runtime.gitAuthor.Name(),
		"GIT_COMMITTER_EMAIL="+runtime.gitAuthor.Email(),
		"GIT_CEILING_DIRECTORIES="+runtime.gitCeiling,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"GIT_SSH_COMMAND=/usr/bin/false",
		"GH_CONFIG_DIR=/dev/null",
	)
}

func validAbsolute(value string, limit int) bool {
	return validValue(value, limit) && filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator)
}

func validToolPath(value string) bool { return install.ValidToolPath(value) }

// ValidateToolPath lets the daemon freeze one bounded startup-owned PATH and
// lets the worker codec reject drift without reimplementing its grammar.
func ValidateToolPath(value string) error {
	if !validToolPath(value) {
		return ErrInvalid
	}
	return nil
}

func validGitCeiling(value string) bool {
	return validAbsolute(value, maxPathBytes) && !strings.ContainsRune(value, rune(filepath.ListSeparator))
}

func validValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}
