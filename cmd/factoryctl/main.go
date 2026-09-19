package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	attemptRequestTimeout        = 5 * time.Second
	retainedSourceRequestTimeout = 10 * time.Minute
	serviceRequestTimeout        = 30 * time.Second
	exitUsage                    = 64
	exitFailure                  = 1
	maxHomeArgumentBytes         = 4096

	// pairListenAddress is factoryd's fixed loopback listener and pairPageURL
	// the first-party pair page it serves there. A successful install opens
	// that page, so pairing a browser never needs a terminal. launchd returns
	// from bootstrap before factoryd listens, hence the bounded wait.
	pairListenAddress  = "127.0.0.1:43123"
	pairPageURL        = "http://" + pairListenAddress + "/pair"
	pairListenPatience = 10 * time.Second

	usage = `usage:
  factoryctl github connect [--open] | confirm CODE | status | refresh | disconnect
  factoryctl github installations [--page N]
  factoryctl github repositories --installation ID [--page N]
  factoryctl github delegate --repositories FILE
	factoryctl intake list [--project ID]
	factoryctl intake config [--project ID]
	factoryctl intake create --project ID --repository OWNER/REPO --target-repository ID [--overseer ID] [--label LABEL] [--policy manual|trusted-authors] [--trusted-author LOGIN ...] [--poll-seconds N] [--admission-limit N] [--priority N] [--source ID]
	factoryctl intake update --source ID --project ID --repository OWNER/REPO --target-repository ID [--overseer ID] [--label LABEL] [--policy manual|trusted-authors] [--trusted-author LOGIN ...] [--poll-seconds N] [--admission-limit N] [--priority N] --revision N
	  New sources use manual approval, poll every 60 seconds, and admit up to 25 accepted issues. --configuration JSON remains available for automation.
	factoryctl intake preview|refresh --source ID --page N
	factoryctl intake enable|pause --source ID --revision N [--reviewed-revision N]
	factoryctl intake accept --source ID --revision N --issue N --hash HEX64
	factoryctl intake withdraw|import --acceptance ID
	factoryctl intake tick --source ID --page N [--acceptance-cursor ID]
  factoryctl github manage --installation ID [--open]
  factoryctl attempt task
  factoryctl attempt source --task ID
  factoryctl attempt succeed [--result TEXT]
  factoryctl attempt block --detail TEXT
  factoryctl attempt fail [--detail TEXT]
  factoryctl attempt request-human --idempotency-key HEX32 --question TEXT [--option TEXT ...]
  factoryctl attempt turn-complete NOTIFICATION_JSON
  factoryctl attempt peer status [--targets [--target-offset N]] [--offset N] [--head HEAD]
  factoryctl attempt peer ask --task ID --idempotency-key HEX32 --question TEXT
  factoryctl attempt peer answer --question ID --revision REVISION --idempotency-key HEX32 --answer TEXT
  factoryctl attempt terminal observe --project ID --task ID --run ID [--cursor N] [--max-bytes N]
  factoryctl terminal observe --project ID --task ID --run ID [--cursor N] [--max-bytes N] [--text]
  factoryctl attempt send-back --task ID --note TEXT
  factoryctl overseer status [--task ID] [--offset N --head HEAD] [--text-offset RUNES --head HEAD]
	factoryctl overseer task add --agent ID|any --title TEXT [--body TEXT] [--priority N] [--prerequisite TASK_ID:WORK_REVISION ...] [--conflict-path PATH ...] [--task-id ID --incarnation-id ID]
  factoryctl overseer task update --task ID --revision REVISION [--title TEXT] [--body TEXT] [--priority N] [--agent ID] [--cancel] [--retry]
  factoryctl overseer task send-back --task ID --note TEXT
  factoryctl overseer agent pause|resume|archive|restore --agent ID --revision REVISION
  factoryctl overseer worker stop --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION
  factoryctl overseer worker replace --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION --successor-task ID --successor-incarnation ID --instruction TEXT
  factoryctl overseer worker message --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION --message TEXT
  factoryctl overseer worker interrupt --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION
  factoryctl overseer human reply --operation-id ID --request ID --revision REVISION --reply TEXT
  factoryctl human list
  factoryctl human reply --operation-id ID --request ID --revision REVISION --reply TEXT
  factoryctl project create --name TEXT --root ABSOLUTE
  factoryctl project repository list --project ID
	factoryctl project repository add --project ID --name TEXT --root ABSOLUTE --base REF [--id HEX32]
  factoryctl project repository name --id ID --revision REVISION --name TEXT
  factoryctl project repository base --id ID --revision REVISION --base REF
  factoryctl project repository default|enable|disable|remove --id ID --revision REVISION
  factoryctl project repository fetch|github --id ID
  factoryctl project limits --project ID --revision REVISION --run-budget N --max-run-seconds N
  factoryctl agent create --project ID --name TEXT --provider shell|claude_code|codex --tool-budget N [--role worker|orchestrator] [--model TEXT] [--reasoning-effort low|medium|high|xhigh|max|ultra] [--account ID]
  factoryctl agent idle-policy --agent ID --revision REVISION --policy wait
  factoryctl agent idle-policy --agent ID --revision REVISION --policy standing_instruction --after-seconds N --instruction TEXT [--run-budget N]
  factoryctl agent pause|resume|archive|restore --agent ID --revision REVISION
  factoryctl worker stop --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION
  factoryctl worker replace --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION --successor-task ID --successor-incarnation ID --instruction TEXT
  factoryctl worker message --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION --message TEXT
  factoryctl worker interrupt --operation-id ID --task ID --task-revision REVISION --run ID --run-revision REVISION
  factoryctl worker operation --operation-id ID

  factoryctl account discover
  factoryctl account list
  factoryctl account link --provider claude_code|codex --home ABSOLUTE --label TEXT
  factoryctl agent select-account --agent ID --revision REVISION --account ID
	factoryctl agent select-model --agent ID --revision REVISION --model TEXT [--reasoning-effort low|medium|high|xhigh|max|ultra]
	factoryctl agent paths --agent ID
  factoryctl task add --project ID [--repository ID] --agent ID|any --title TEXT [--body TEXT] [--priority N] [--task-id ID --incarnation-id ID]
    --agent any queues the task for any eligible worker in the project; the first worker admitted keeps it.
  factoryctl status
  factoryctl task send-back --task ID --note TEXT
  factoryctl task update --task ID --revision REVISION [--title TEXT] [--body TEXT] [--priority N] [--agent ID] [--cancel] [--retry]
  factoryctl task recovery --task ID --incarnation ID
  factoryctl task read --task ID --revision REVISION [--offset N]
  factoryctl dispatch on|off [--revision REVISION]
  factoryctl capacity --workers N --revision REVISION
    Worker slots only; the separate overseer lane remains available.
  factoryctl web status
  factoryctl web list-clients [--after CLIENT_ID]
  factoryctl web revoke CLIENT_ID --revision REVISION
	factoryctl content create [--id ID] --project ID --kind KIND --title TEXT [--description TEXT] [--body TEXT|--body-file PATH|--commit OID --path PATH] [--source-references TEXT]
	factoryctl content revise --project ID --id ID --revision REVISION --kind KIND --title TEXT [--description TEXT] [--body TEXT|--body-file PATH|--commit OID --path PATH]
  factoryctl content deprecate --project ID --id ID --revision REVISION
  factoryctl content list --project ID [--kind KIND] [--offset N] [--limit N]
  factoryctl content read --id ID --revision REVISION
  factoryctl content body --id ID --revision REVISION --offset N --limit N
  factoryctl content evidence [--evidence-id ID] --project ID --id ID --revision REVISION --tested-source TEXT --result passed|failed|incomplete|not_run  [--environment TEXT] [--location TEXT] [--judgment TEXT]
  factoryctl content evidence-list --project ID --id ID --revision REVISION [--offset N] [--limit N]
  factoryctl content attach --project ID --task TASK_ID --id ID --revision REVISION
  factoryctl content attachments --project ID --task TASK_ID --revision TASK_WORK_REVISION
  factoryctl attempt content ... (same content commands, authenticated to the live attempt)
  factoryctl remote status
  factoryctl init --home ABSOLUTE
  factoryctl doctor --home ABSOLUTE
  factoryctl service status --home ABSOLUTE [--label LABEL] [--plist-dir ABSOLUTE]
  factoryctl service install --home ABSOLUTE [--label LABEL] [--plist-dir ABSOLUTE] [--relay-origin WSS_ORIGIN] [--development-browser-address LOOPBACK] [--tool-path PATH] [--toolchain-read-roots PATH_LIST]
  factoryctl service start --home ABSOLUTE [--label LABEL] [--plist-dir ABSOLUTE]
  factoryctl service stop --home ABSOLUTE [--label LABEL] [--plist-dir ABSOLUTE]
  factoryctl service uninstall --home ABSOLUTE [--label LABEL] [--plist-dir ABSOLUTE]
  factoryctl --version
  factoryctl feedback bug|feature [--open] [--agent-assisted] [--factory-name TEXT]
  factoryctl backlog [--open]
  factoryctl --build-identity
  factoryctl outcome write --id ID --project ID [--revision N] --document JSON|--document-file PATH
  factoryctl outcome read --project ID --id ID [--revision N]
  factoryctl outcome list --project ID [--offset N] [--limit N]
`
)

type commandKind uint8

const (
	commandSucceed commandKind = iota + 1
	commandBlock
	commandFail
	commandRequestHuman
	commandTurnComplete
	commandPeerStatus
	commandPeerAsk
	commandPeerAnswer
	commandSendBack
	commandAttemptTask
	commandTerminalObserve
	commandOperatorTerminalObserve
	commandAttemptSource
	commandWebStatus
	commandWebListClients
	commandWebRevoke
	commandRemoteStatus
	commandInit
	commandDoctor
	commandServiceStatus
	commandServiceInstall
	commandServiceStart
	commandServiceStop
	commandServiceUninstall
	commandProjectCreate
	commandProjectLimits
	commandProjectRepository
	commandIntake
	commandAgentCreate
	commandAgentIdlePolicy
	commandAccountsDiscover
	commandAccountsList
	commandAccountLink
	commandAgentSelectAccount
	commandAgentSelectModel
	commandAgentPaths
	commandTaskAdd
	commandTaskSendBack
	commandTaskRecovery
	commandTaskRead
	commandDispatch
	commandCapacity
	commandStatus
	commandOverseerStatus
	commandOverseerTaskAdd
	commandOverseerTaskUpdate
	commandOverseerTaskSendBack
	commandOverseerAgentUpdate
	commandOverseerStopWorker
	commandOverseerReplaceWorker
	commandOverseerMessageWorker
	commandOverseerInterruptWorker
	commandOverseerReplyHuman
	commandHumanList
	commandHumanReply
	commandWorkerOperation
	commandContentCreate
	commandContentRevise
	commandContentDeprecate
	commandContentList
	commandContentRead
	commandContentBody
	commandContentEvidence
	commandContentAttach
	commandContentEvidenceList
	commandContentAttachments
	commandOutcomeWrite
	commandOutcomeRead
	commandOutcomeList
)

type attemptCommand struct {
	toolPath, toolchainReadRoots string

	kind             commandKind
	operatorControl  bool
	home             string
	idempotencyKey   string
	text             string
	options          []string
	id               string
	after            string
	expectedRevision uint64

	label            string
	plistDir         string
	relayOrigin      string
	browserAddress   string
	name             string
	root             string
	project          string
	repository       string
	agent            string
	role             string
	provider         string
	model            string
	reasoningEffort  string
	account          string
	title            string
	body             string
	bodySet          bool
	toolBudget       uint64
	capacity         uint16
	maxBytes         uint32
	maxRunSeconds    uint32
	priority         int64
	prioritySet      bool
	offset           uint64
	head             uint64
	textOffset       uint64
	terminalText     bool
	includeTargets   bool
	enabled          bool
	operationID      string
	taskRevision     uint64
	runRevision      uint64
	run              string
	paused           bool
	archived         bool
	archiveSet       bool
	cancel           bool
	retry            bool
	bodyFile         string
	contentKind      string
	intake           api.IntakeInput
	contentID        string
	contentRevision  uint64
	description      string
	sourceReferences string
	sourceCommit     string
	sourcePath       string
	testedSource     string
	environment      string
	contentResult    string
	location         string
	judgment         string
	document         string
	documentFile     string
	prerequisites    []api.TaskPrerequisiteInput
	conflictPaths    []string
}

func main() {
	if len(os.Args) == 4 && os.Args[1] == "intake" && os.Args[2] == "review-mcp" {
		os.Exit(runIntakeReviewMCP(context.Background(), os.Args[3], os.Stdin, os.Stdout, os.Getenv))
	}
	if len(os.Args) == 3 && os.Args[1] == "attempt" && os.Args[2] == "maintainer-mcp" {
		os.Exit(runMaintainerMCP(context.Background(), os.Stdin, os.Stdout, os.Getenv))
	}
	if len(os.Args) == 3 && os.Args[1] == "attempt" && os.Args[2] == "mcp" {
		os.Exit(runAttemptMCP(context.Background(), os.Stdin, os.Stdout, os.Getenv))
	}
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return runWithOpener(ctx, args, getenv, stdout, stderr, openBrowser)
}

type browserOpener func(context.Context, string) error

func runWithOpener(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, opener browserOpener) int {
	if len(args) == 2 && args[0] == "intake" && (args[1] == "legacy_preview" || args[1] == "legacy_commit" || args[1] == "legacy_lineage" || args[1] == "review") {
		return runLegacyIntakeProtocol(ctx, args[1], os.Stdin, getenv, stdout)
	}
	if len(args) >= 2 && args[0] == "intake" && args[1] == "service" {
		return runIntakeService(ctx, args[2:], getenv, stdout, stderr)
	}
	return runWithDependencies(ctx, args, getenv, stdout, stderr, opener, install.InspectService)
}

type serviceInspector func(context.Context, string) (install.ServiceStatus, error)

func runWithDependencies(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, opener browserOpener, inspect serviceInspector) int {
	if len(args) > 0 && args[0] == "github" {
		return runGitHub(ctx, args[1:], getenv, stdout, stderr, opener)
	}
	if len(args) > 0 && args[0] == "backlog" {
		return runBacklog(ctx, args[1:], stdout, stderr, opener)
	}
	if len(args) > 0 && args[0] == "feedback" {
		return runFeedback(ctx, args[1:], stdout, stderr, opener)
	}
	if len(args) > 0 && args[0] == "--local-ci-process-identity" {
		if len(args) != 2 {
			return exitUsage
		}
		pid, err := strconv.Atoi(args[1])
		if err != nil || pid <= 1 {
			return exitUsage
		}
		identity, err := runner.ReadOwnedProcessIdentity(pid)
		if err != nil {
			return exitFailure
		}
		_, err = fmt.Fprintf(stdout, "%d:%06d %d\n", identity.Birth.Seconds, identity.Birth.Microseconds, identity.PGID)
		if err != nil {
			return exitFailure
		}
		return 0
	}
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintf(stdout, "factoryctl %s\n", buildinfo.Current().Version())
		return 0
	}
	if len(args) == 1 && args[0] == "--build-identity" {
		if err := buildinfo.Current().WriteJSON(stdout); err != nil {
			return exitFailure
		}
		return 0
	}
	command, help, ok := parse(args)
	if help {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	if !ok {
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}
	if command.kind == commandInit || command.kind == commandDoctor {
		return runHome(ctx, command, stdout, stderr)
	}
	if command.kind == commandServiceStatus || command.kind == commandServiceInstall || command.kind == commandServiceStart || command.kind == commandServiceStop || command.kind == commandServiceUninstall {
		return runService(ctx, command, getenv, stdout, stderr, inspect, opener, runIntakeService)
	}
	if command.kind == commandWebStatus || command.kind == commandWebListClients || command.kind == commandWebRevoke {
		return runWeb(ctx, command, getenv, stdout, stderr)
	}
	if command.kind == commandRemoteStatus {
		return runRemote(ctx, getenv, stdout, stderr)
	}
	if command.kind == commandAgentPaths || command.kind == commandOperatorTerminalObserve || command.kind == commandWorkerOperation || command.kind == commandProjectCreate || command.kind == commandProjectRepository || command.kind == commandIntake || command.kind == commandProjectLimits || command.kind == commandAgentCreate || command.kind == commandAgentIdlePolicy || command.kind == commandAccountsDiscover || command.kind == commandAccountsList || command.kind == commandAccountLink || command.kind == commandAgentSelectAccount || command.kind == commandAgentSelectModel || command.kind == commandTaskAdd || command.kind == commandTaskSendBack || command.kind == commandTaskRecovery || command.kind == commandTaskRead || command.kind == commandDispatch || command.kind == commandCapacity || command.kind == commandStatus || command.kind == commandHumanList || command.kind == commandHumanReply {
		return runOperator(ctx, command, getenv, stdout, stderr)
	}
	if command.kind >= commandContentCreate && command.kind <= commandContentAttachments && len(args) > 0 && args[0] == "content" {
		return runOperator(ctx, command, getenv, stdout, stderr)
	}
	if command.operatorControl && (command.kind == commandOverseerTaskUpdate || command.kind == commandOverseerAgentUpdate || command.kind == commandOverseerStopWorker || command.kind == commandOverseerReplaceWorker || command.kind == commandOverseerMessageWorker || command.kind == commandOverseerInterruptWorker) {
		return runOperator(ctx, command, getenv, stdout, stderr)
	}
	if command.kind >= commandOutcomeWrite && command.kind <= commandOutcomeList && len(args) > 0 && args[0] == "outcome" {
		return runOperator(ctx, command, getenv, stdout, stderr)
	}
	if command.kind >= commandOverseerStatus && command.kind <= commandOverseerReplyHuman {
		return runOverseer(ctx, command, getenv, stdout, stderr)
	}

	socket := getenv("DARK_FACTORY_SOCKET")
	if socket == "" {
		// A Codex session's shell does not receive the attempt variables; its
		// attempt-scoped factory tool does. Say so instead of sending the
		// provider to read the source for the cause.
		_, _ = io.WriteString(stderr, "factoryctl: attempt client configuration is invalid\nfactoryctl: DARK_FACTORY_SOCKET is not set in this shell; a Codex session runs attempt commands through its factory tool\n")
		return exitFailure
	}
	client, err := api.NewAttemptClientFromEnvironment(socket)
	if err != nil {
		if command.kind == commandTurnComplete {
			var remote *api.RemoteError
			if errors.As(err, &remote) && remote.Code() == api.RemoteConflict {
				return 0
			}
		}
		writeFailure(stderr, command.kind, err)
		return exitFailure
	}

	timeout := attemptRequestTimeout
	if command.kind == commandAttemptSource {
		timeout = retainedSourceRequestTimeout
	}
	if command.kind == commandPeerAsk || command.kind == commandPeerAnswer {
		timeout = serviceRequestTimeout
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if command.kind == commandAttemptTask {
		result, taskErr := client.Task(callContext)
		if taskErr != nil {
			writeFailure(stderr, command.kind, taskErr)
			return exitFailure
		}
		return writeJSON(stdout, result)
	}
	if command.kind == commandAttemptSource {
		result, sourceErr := client.Source(callContext, command.id)
		if sourceErr != nil {
			writeFailure(stderr, command.kind, sourceErr)
			return exitFailure
		}
		return writeJSON(stdout, result)
	}
	if command.kind == commandTerminalObserve {
		result, observeErr := client.TerminalObserve(callContext, api.TerminalObserveInput{ProjectID: command.project, TaskID: command.id, RunID: command.run, Cursor: command.offset, MaxBytes: command.maxBytes})
		if observeErr != nil {
			writeFailure(stderr, command.kind, observeErr)
			return exitFailure
		}
		encoded, err := result.MarshalDisplayJSON()
		if err != nil {
			writeFailure(stderr, command.kind, err)
			return exitFailure
		}
		return writeJSON(stdout, json.RawMessage(encoded))
	}
	if command.kind == commandPeerStatus {
		var result api.PeerStatus
		var statusErr error
		if command.includeTargets {
			result, statusErr = client.PeerStatusPage(callContext, command.offset, command.textOffset, command.head)
		} else {
			result, statusErr = client.PeerInboxPage(callContext, command.offset, command.head)
		}
		if statusErr != nil {
			writeFailure(stderr, command.kind, statusErr)
			return exitFailure
		}
		return writeJSON(stdout, result)
	}
	if command.kind >= commandContentCreate && command.kind <= commandContentAttachments {
		return runContent(callContext, client, command, stdout, stderr)
	}
	if command.kind >= commandOutcomeWrite && command.kind <= commandOutcomeList {
		return runOutcome(callContext, client, command, stdout, stderr)
	}
	var result api.MutationResult
	switch command.kind {
	case commandSucceed:
		result, err = client.Succeed(callContext, command.text)
	case commandBlock:
		result, err = client.Block(callContext, command.text)
	case commandFail:
		result, err = client.Fail(callContext, command.text)
	case commandRequestHuman:
		result, err = client.RequestHuman(callContext, api.HumanQuestionInput{IdempotencyKey: command.idempotencyKey, Question: command.text, Options: command.options})
	case commandTurnComplete:
		result, err = client.RequestHuman(callContext, api.HumanQuestionInput{IdempotencyKey: command.idempotencyKey, Question: command.text, ReuseExisting: true})
	case commandPeerAsk:
		result, err = client.PeerAsk(callContext, api.PeerQuestionInput{TargetTaskID: command.id, IdempotencyKey: command.idempotencyKey, Question: command.text})
	case commandPeerAnswer:
		result, err = client.PeerAnswer(callContext, api.PeerAnswerInput{QuestionID: command.id, ExpectedRevision: command.expectedRevision, IdempotencyKey: command.idempotencyKey, Answer: command.text})
	case commandSendBack:
		result, err = client.SendBack(callContext, api.SendBackInput{TaskID: command.id, Note: command.text})
	default:
		err = api.ErrInvalidInput
	}
	if err != nil {
		if command.kind == commandTurnComplete {
			var remote *api.RemoteError
			if errors.As(err, &remote) && remote.Code() == api.RemoteConflict {
				return 0
			}
		}
		writeFailure(stderr, command.kind, err)
		return exitFailure
	}
	if command.kind == commandTurnComplete {
		return 0
	} else if command.kind == commandRequestHuman {
		_, _ = fmt.Fprintf(stdout, "human request accepted: head=%d revision=%d\n", result.Head, result.Revision)
	} else if command.kind == commandSendBack {
		_, _ = fmt.Fprintf(stdout, "task sent back: head=%d revision=%d\n", result.Head, result.Revision)
	} else {
		_, _ = fmt.Fprintf(stdout, "attempt outcome request accepted: head=%d revision=%d\n", result.Head, result.Revision)
	}
	return 0
}

func contentBody(command attemptCommand) (string, error) {
	if command.bodyFile == "" {
		return command.body, nil
	}
	var reader io.Reader = os.Stdin
	if command.bodyFile != "-" {
		file, err := os.Open(command.bodyFile)
		if err != nil {
			return "", err
		}
		defer file.Close()
		reader = file
	}
	body, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !utf8.Valid(body) {
		return "", api.ErrInvalidInput
	}
	return string(body), nil
}

func contentInput(command attemptCommand, body string) api.ContentInput {
	return api.ContentInput{ID: command.contentID, ProjectID: command.project, Kind: command.contentKind, Title: command.title, Description: command.description, Body: body, SourceReferences: command.sourceReferences, Commit: command.sourceCommit, Path: command.sourcePath, ExpectedRevision: command.contentRevision}
}

type contentClient interface {
	ContentCreate(context.Context, api.ContentInput) (api.Content, error)
	ContentRevise(context.Context, api.ContentInput) (api.Content, error)
	ContentDeprecate(context.Context, api.ContentInput) (api.Content, error)
	ContentList(context.Context, api.ContentListInput) (api.ContentList, error)
	ContentRead(context.Context, api.ContentReadInput) (api.Content, error)
	ContentBody(context.Context, api.ContentBodyInput) (api.ContentBody, error)
	ContentEvidence(context.Context, api.ContentEvidenceInput) (api.ContentEvidence, error)
	ContentEvidenceList(context.Context, api.ContentEvidenceListInput) (api.ContentEvidenceList, error)
	ContentAttachments(context.Context, api.ContentAttachmentsInput) (api.ContentAttachments, error)
	ContentAttach(context.Context, api.ContentAttachInput) error
}

func runContent(ctx context.Context, client contentClient, command attemptCommand, stdout, stderr io.Writer) int {
	body, err := contentBody(command)
	if err != nil {
		return writeWebFailure(stderr, "content", err)
	}
	var value any
	switch command.kind {
	case commandContentCreate:
		id := command.contentID
		if id == "" {
			id, err = newOperatorID()
			if err != nil {
				return writeWebFailure(stderr, "content create", err)
			}
		}
		in := contentInput(command, body)
		in.ID = id
		value, err = client.ContentCreate(ctx, in)
	case commandContentRevise:
		value, err = client.ContentRevise(ctx, contentInput(command, body))
	case commandContentDeprecate:
		value, err = client.ContentDeprecate(ctx, contentInput(command, ""))
	case commandContentList:
		value, err = client.ContentList(ctx, api.ContentListInput{ProjectID: command.project, Kind: command.contentKind, Offset: command.offset, Limit: command.head})
	case commandContentRead:
		value, err = client.ContentRead(ctx, api.ContentReadInput{ID: command.contentID, Revision: command.contentRevision})
	case commandContentBody:
		value, err = client.ContentBody(ctx, api.ContentBodyInput{ID: command.contentID, Revision: command.contentRevision, Offset: command.offset, Limit: command.head})
	case commandContentEvidence:
		id := command.operationID
		if id == "" {
			id, err = newOperatorID()
			if err != nil {
				return writeWebFailure(stderr, "content evidence", err)
			}
		}
		value, err = client.ContentEvidence(ctx, api.ContentEvidenceInput{ID: id, ProjectID: command.project, ContentID: command.contentID, ContentRevision: command.contentRevision, TestedSource: command.testedSource, Environment: command.environment, Result: command.contentResult, Location: command.location, Judgment: command.judgment})
	case commandContentAttach:
		err = client.ContentAttach(ctx, api.ContentAttachInput{TaskID: command.id, ProjectID: command.project, ContentID: command.contentID, ContentRevision: command.contentRevision})
	case commandContentEvidenceList:
		value, err = client.ContentEvidenceList(ctx, api.ContentEvidenceListInput{ProjectID: command.project, ContentID: command.contentID, ContentRevision: command.contentRevision, Offset: command.offset, Limit: command.head})
	case commandContentAttachments:
		value, err = client.ContentAttachments(ctx, api.ContentAttachmentsInput{ProjectID: command.project, TaskID: command.id, TaskWorkRevision: command.contentRevision})
	}
	if err != nil {
		return writeWebFailure(stderr, "content", err)
	}
	if value != nil {
		return writeJSON(stdout, value)
	}
	return 0
}

func parse(args []string) (attemptCommand, bool, bool) {
	if len(args) == 1 && helpFlag(args[0]) {
		return attemptCommand{}, true, true
	}
	if len(args) == 3 && (args[0] == "init" || args[0] == "doctor") && args[1] == "--home" {
		if validHomeArg(args[2]) {
			kind := commandInit
			if args[0] == "doctor" {
				kind = commandDoctor
			}
			return attemptCommand{kind: kind, home: args[2]}, false, true
		}
	}
	if len(args) == 2 && (args[0] == "init" || args[0] == "doctor") && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) >= 1 && args[0] == "service" {
		return parseServiceCommand(args)
	}
	if len(args) >= 2 && (args[0] == "agent" && (args[1] == "pause" || args[1] == "resume" || args[1] == "archive" || args[1] == "restore") || args[0] == "task" && args[1] == "update") {
		command, help, ok := parseOverseer(append([]string{"overseer"}, args...))
		command.operatorControl = ok
		return command, help, ok
	}
	if len(args) >= 1 && (args[0] == "status" || args[0] == "content" || args[0] == "outcome" || args[0] == "project" || args[0] == "agent" || args[0] == "account" || args[0] == "task" || args[0] == "worker" || args[0] == "dispatch" || args[0] == "capacity" || args[0] == "intake") {
		return parseOperator(args)
	}
	if len(args) >= 1 && args[0] == "overseer" {
		if len(args) >= 2 && helpFlag(args[len(args)-1]) {
			return attemptCommand{}, true, true
		}
		return parseOverseer(args)
	}
	if len(args) >= 1 && args[0] == "human" {
		if len(args) == 2 && args[1] == "list" {
			return attemptCommand{kind: commandHumanList}, false, true
		}
		if len(args) == 10 && args[1] == "reply" && args[2] == "--operation-id" && validHumanRequestKey(args[3]) && args[4] == "--request" && validHumanRequestKey(args[5]) && args[6] == "--revision" && validRevision(args[7]) && args[8] == "--reply" && validQuestion(args[9]) {
			revision, _ := strconv.ParseUint(args[7], 10, 64)
			return attemptCommand{kind: commandHumanReply, operationID: args[3], id: args[5], expectedRevision: revision, text: args[9]}, false, true
		}
		return attemptCommand{}, false, false
	}
	if len(args) >= 1 && args[0] == "remote" {
		return parseRemote(args)
	}
	if len(args) >= 1 && args[0] == "terminal" {
		textMode := false
		filtered := make([]string, 0, len(args)+1)
		filtered = append(filtered, "attempt")
		for _, arg := range args {
			if arg == "--text" {
				if textMode {
					return attemptCommand{}, false, false
				}
				textMode = true
				continue
			}
			filtered = append(filtered, arg)
		}
		parsed, help, ok := parse(filtered)
		if ok && parsed.kind == commandTerminalObserve {
			parsed.kind = commandOperatorTerminalObserve
			parsed.terminalText = textMode
			ok = !textMode || parsed.offset == 0
		}
		return parsed, help, ok
	}
	if len(args) < 2 || args[0] != "attempt" && args[0] != "web" {
		return attemptCommand{}, false, false
	}
	if len(args) == 2 && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) == 3 && helpFlag(args[2]) {
		switch args[1] {
		case "task", "source", "succeed", "block", "fail", "request-human", "turn-complete", "send-back", "peer":
			return attemptCommand{}, true, true
		case "status", "list-clients", "revoke":
			if args[0] == "web" {
				return attemptCommand{}, true, true
			}
		default:
			return attemptCommand{}, false, false
		}
	}

	if args[0] == "web" {
		return parseWeb(args)
	}
	if args[1] == "content" {
		return parseContent(args)
	}
	if args[1] == "outcome" {
		return parseOutcome(args)
	}
	switch args[1] {
	case "task":
		if len(args) == 2 {
			return attemptCommand{kind: commandAttemptTask}, false, true
		}
	case "source":
		if len(args) == 4 && args[2] == "--task" && validHumanRequestKey(args[3]) {
			return attemptCommand{kind: commandAttemptSource, id: args[3]}, false, true
		}
	case "succeed":
		if len(args) == 2 {
			return attemptCommand{kind: commandSucceed}, false, true
		}
		if len(args) == 4 && args[2] == "--result" && strings.TrimSpace(args[3]) != "" {
			return attemptCommand{kind: commandSucceed, text: args[3]}, false, true
		}
	case "block":
		if len(args) == 4 && args[2] == "--detail" && args[3] != "" {
			return attemptCommand{kind: commandBlock, text: args[3]}, false, true
		}
	case "fail":
		if len(args) == 2 {
			return attemptCommand{kind: commandFail}, false, true
		}
		if len(args) == 4 && args[2] == "--detail" {
			return attemptCommand{kind: commandFail, text: args[3]}, false, true
		}
	case "request-human":
		if len(args) >= 6 && len(args)%2 == 0 && args[2] == "--idempotency-key" && validHumanRequestKey(args[3]) && args[4] == "--question" && validQuestion(args[5]) {
			var options []string
			for i := 6; i < len(args); i += 2 {
				if args[i] != "--option" {
					return attemptCommand{}, false, false
				}
				options = append(options, args[i+1])
			}
			if kernel.ValidateHumanOptions(options) != nil {
				return attemptCommand{}, false, false
			}
			return attemptCommand{kind: commandRequestHuman, idempotencyKey: args[3], text: args[5], options: options}, false, true
		}
	case "turn-complete":
		if len(args) == 3 {
			var notification struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread-id"`
				TurnID   string `json:"turn-id"`
				CWD      string `json:"cwd"`
			}
			if json.Unmarshal([]byte(args[2]), &notification) == nil && notification.Type == "agent-turn-complete" && validOperatorText(notification.ThreadID, 1, 256) && validOperatorText(notification.TurnID, 1, 256) && validOperatorText(notification.CWD, 1, 4096) {
				digest := sha256.Sum256([]byte(notification.ThreadID + "\x00" + notification.TurnID + "\x00" + notification.CWD))
				return attemptCommand{kind: commandTurnComplete, idempotencyKey: hex.EncodeToString(digest[:16]), text: "Codex turn completed without a durable attempt outcome; resume this session and record succeed, block, or fail."}, false, true
			}
		}
	case "peer":
		if len(args) >= 3 && args[2] == "status" {
			command := attemptCommand{kind: commandPeerStatus}
			seen := map[string]bool{}
			for index := 3; index < len(args); {
				if args[index] == "--targets" && !seen[args[index]] {
					seen[args[index]] = true
					command.includeTargets = true
					index++
					continue
				}
				if index+1 >= len(args) || seen[args[index]] {
					return attemptCommand{}, false, false
				}
				seen[args[index]] = true
				value, ok := parseRevision(args[index+1])
				if !ok {
					return attemptCommand{}, false, false
				}
				switch args[index] {
				case "--offset":
					command.offset = value
				case "--target-offset":
					command.textOffset = value
				case "--head":
					command.head = value
				default:
					return attemptCommand{}, false, false
				}
				index += 2
			}
			if command.head == 0 && (command.offset != 0 || command.textOffset != 0) || !command.includeTargets && command.textOffset != 0 {
				return attemptCommand{}, false, false
			}
			return command, false, true
		}
		if len(args) == 9 && args[2] == "ask" && args[3] == "--task" && validHumanRequestKey(args[4]) && args[5] == "--idempotency-key" && validHumanRequestKey(args[6]) && args[7] == "--question" && validPeerText(args[8]) {
			return attemptCommand{kind: commandPeerAsk, id: args[4], idempotencyKey: args[6], text: args[8]}, false, true
		}
		if len(args) == 11 && args[2] == "answer" && args[3] == "--question" && validHumanRequestKey(args[4]) && args[5] == "--revision" && validRevision(args[6]) && args[7] == "--idempotency-key" && validHumanRequestKey(args[8]) && args[9] == "--answer" && validPeerText(args[10]) {
			revision, _ := strconv.ParseUint(args[6], 10, 64)
			return attemptCommand{kind: commandPeerAnswer, id: args[4], expectedRevision: revision, idempotencyKey: args[8], text: args[10]}, false, true
		}
	case "terminal":
		if len(args) >= 3 && args[2] == "observe" && (len(args)-3)%2 == 0 {
			command := attemptCommand{kind: commandTerminalObserve, maxBytes: 8192}
			seen := map[string]bool{}
			for i := 3; i < len(args); i += 2 {
				name, value := args[i], args[i+1]
				if seen[name] {
					return attemptCommand{}, false, false
				}
				seen[name] = true
				switch name {
				case "--project", "--task", "--run":
					if !validHumanRequestKey(value) {
						return attemptCommand{}, false, false
					}
					if name == "--project" {
						command.project = value
					} else if name == "--task" {
						command.id = value
					} else {
						command.run = value
					}
				case "--cursor", "--max-bytes":
					parsed, err := strconv.ParseUint(value, 10, 64)
					if err != nil || name == "--max-bytes" && parsed > 65536 {
						return attemptCommand{}, false, false
					}
					if name == "--cursor" {
						command.offset = parsed
					} else if parsed > 0 {
						command.maxBytes = uint32(parsed)
					} else {
						return attemptCommand{}, false, false
					}
				default:
					return attemptCommand{}, false, false
				}
			}
			if command.project != "" && command.id != "" && command.run != "" {
				return command, false, true
			}
		}
	case "send-back":
		if len(args) == 6 && args[2] == "--task" && validHumanRequestKey(args[3]) && args[4] == "--note" && validQuestion(args[5]) {
			return attemptCommand{kind: commandSendBack, id: args[3], text: args[5]}, false, true
		}
	}
	return attemptCommand{}, false, false
}

func parseContent(args []string) (attemptCommand, bool, bool) {
	start := 0
	if args[0] == "attempt" {
		start = 1
	}
	if len(args) < start+2 || args[start] != "content" {
		return attemptCommand{}, false, false
	}
	if len(args) == start+2 && helpFlag(args[start+1]) {
		return attemptCommand{}, true, true
	}
	command := attemptCommand{}
	switch args[start+1] {
	case "create":
		command.kind = commandContentCreate
	case "revise":
		command.kind = commandContentRevise
	case "deprecate":
		command.kind = commandContentDeprecate
	case "list":
		command.kind = commandContentList
	case "read":
		command.kind = commandContentRead
	case "body":
		command.kind = commandContentBody
	case "evidence":
		command.kind = commandContentEvidence
	case "attach":
		command.kind = commandContentAttach
	case "evidence-list":
		command.kind = commandContentEvidenceList
	case "attachments":
		command.kind = commandContentAttachments
	default:
		return attemptCommand{}, false, false
	}
	if len(args) == start+3 && helpFlag(args[start+2]) {
		return attemptCommand{}, true, true
	}
	seen := map[string]bool{}
	for i := start + 2; i < len(args); i += 2 {
		if i+1 >= len(args) || seen[args[i]] {
			return attemptCommand{}, false, false
		}
		seen[args[i]] = true
		name, value := args[i], args[i+1]
		switch name {
		case "--project":
			if validHumanRequestKey(value) {
				command.project = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--kind":
			if validOperatorText(value, 1, 64) {
				command.contentKind = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--id":
			if validHumanRequestKey(value) {
				command.contentID = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--evidence-id":
			if !validHumanRequestKey(value) || command.kind != commandContentEvidence {
				return attemptCommand{}, false, false
			}
			command.operationID = value
		case "--task":
			if validHumanRequestKey(value) {
				command.id = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--revision":
			n, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.contentRevision = n
		case "--offset":
			n, ok := parseOffset(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.offset = n
		case "--limit":
			n, ok := parseRevision(value)
			max := uint64(64 * 1024)
			if command.kind == commandContentList || command.kind == commandContentEvidenceList {
				max = api.MaxContentPageItems
			}
			if !ok || n > max {
				return attemptCommand{}, false, false
			}
			command.head = n
		case "--title":
			if validOperatorText(value, 1, 1024) {
				command.title = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--description":
			if validOperatorText(value, 0, 4096) {
				command.description = value
			} else {
				return attemptCommand{}, false, false
			}

		case "--body":
			if validOperatorText(value, 0, 1<<20) {
				command.body, command.bodySet = value, true
			} else {
				return attemptCommand{}, false, false
			}
		case "--body-file":
			if validOperatorText(value, 1, 4096) {
				command.bodyFile = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--source-references":
			if validOperatorText(value, 0, 32768) {
				command.sourceReferences = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--commit":
			if len(value) >= 40 && len(value) <= 64 {
				command.sourceCommit = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--path":
			if validOperatorText(value, 1, 4096) {
				command.sourcePath = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--tested-source":
			if validOperatorText(value, 1, 4096) {
				command.testedSource = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--environment":
			if validOperatorText(value, 0, 4096) {
				command.environment = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--result":
			if value == "passed" || value == "failed" || value == "incomplete" || value == "not_run" {
				command.contentResult = value
			} else {
				return attemptCommand{}, false, false
			}
		case "--location":
			if validOperatorText(value, 0, 4096) {
				command.location = value
			} else {
				return attemptCommand{}, false, false
			}

		case "--judgment":
			if validOperatorText(value, 0, 8192) {
				command.judgment = value
			} else {
				return attemptCommand{}, false, false
			}
		default:
			return attemptCommand{}, false, false
		}
	}
	if command.bodySet && command.bodyFile != "" {
		return attemptCommand{}, false, false
	}
	if (command.sourceCommit == "") != (command.sourcePath == "") || command.sourceCommit != "" && (command.bodySet || command.bodyFile != "") {
		return attemptCommand{}, false, false
	}
	switch command.kind {
	case commandContentCreate:
		if command.project == "" || command.contentKind == "" || command.title == "" {
			return attemptCommand{}, false, false
		}
	case commandContentRevise:
		if command.project == "" || command.contentID == "" || command.contentRevision == 0 || command.contentKind == "" || command.title == "" {
			return attemptCommand{}, false, false
		}
	case commandContentDeprecate:
		if command.project == "" || command.contentID == "" || command.contentRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandContentList:
		if command.project == "" || command.offset > uint64(^uint64(0)>>1) {
			return attemptCommand{}, false, false
		}
	case commandContentRead:
		if command.contentID == "" || command.contentRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandContentBody:
		if command.contentID == "" || command.contentRevision == 0 || command.head == 0 || command.head > 64*1024 {
			return attemptCommand{}, false, false
		}
	case commandContentEvidence:
		if command.project == "" || command.contentID == "" || command.contentRevision == 0 || command.testedSource == "" || command.contentResult == "" {
			return attemptCommand{}, false, false
		}
	case commandContentAttach:
		if command.project == "" || command.id == "" || command.contentID == "" || command.contentRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandContentEvidenceList:
		if (start == 0 && command.project == "") || command.contentID == "" || command.contentRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandContentAttachments:
		if command.contentRevision == 0 || (start == 0 && (command.project == "" || command.id == "")) {
			return attemptCommand{}, false, false
		}
	}
	return command, false, true
}

func parseServiceCommand(args []string) (attemptCommand, bool, bool) {
	if len(args) == 2 && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) >= 3 && helpFlag(args[2]) {
		switch args[1] {
		case "status", "install", "start", "stop", "uninstall":
			return attemptCommand{}, true, true
		}
	}
	if len(args) < 2 {
		return attemptCommand{}, false, false
	}
	command := attemptCommand{}
	switch args[1] {
	case "status":
		command.kind = commandServiceStatus
	case "install":
		command.kind = commandServiceInstall
	case "start":
		command.kind = commandServiceStart
	case "stop":
		command.kind = commandServiceStop
	case "uninstall":
		command.kind = commandServiceUninstall
	default:
		return attemptCommand{}, false, false
	}
	seen := map[string]bool{}
	for index := 2; index < len(args); index += 2 {
		if index+1 >= len(args) {
			return attemptCommand{}, false, false
		}
		name, value := args[index], args[index+1]
		if seen[name] {
			return attemptCommand{}, false, false
		}
		seen[name] = true
		switch name {
		case "--home":
			if !validHomeArg(value) {
				return attemptCommand{}, false, false
			}
			command.home = value
		case "--label":
			if value == "" || len(value) > 127 {
				return attemptCommand{}, false, false
			}
			command.label = value
		case "--plist-dir":
			if !validHomeArg(value) {
				return attemptCommand{}, false, false
			}
			command.plistDir = value
		case "--relay-origin":
			// Only install renders a plist; every other verb recovers the
			// origin from the receipt this install writes.
			if command.kind != commandServiceInstall || !install.ValidRelayOrigin(value) {
				return attemptCommand{}, false, false
			}
			command.relayOrigin = value
		case "--tool-path":
			if command.kind != commandServiceInstall || !install.ValidToolPath(value) {
				return attemptCommand{}, false, false
			}
			command.toolPath = value
		case "--toolchain-read-roots":
			if command.kind != commandServiceInstall || value == "" || !install.ValidToolchainReadRoots(value) {
				return attemptCommand{}, false, false
			}
			command.toolchainReadRoots = value
		case "--development-browser-address":
			if command.kind != commandServiceInstall || !install.ValidDevelopmentBrowserAddress(value) || value == "" {
				return attemptCommand{}, false, false
			}
			command.browserAddress = value
		default:
			return attemptCommand{}, false, false
		}
	}
	if command.home == "" {
		return attemptCommand{}, false, false
	}
	return command, false, true
}

func serviceConfigFor(command attemptCommand) install.ServiceConfig {
	config := install.DefaultServiceConfig()
	if command.label != "" {
		config.Label = command.label
	}
	config.PlistDirectory = command.plistDir
	config.RelayOrigin = command.relayOrigin
	config.ToolPath = command.toolPath
	config.ToolchainReadRoots = command.toolchainReadRoots
	config.DevelopmentBrowserAddress = command.browserAddress
	return config
}

type intakeServiceRunner func(context.Context, []string, func(string) string, io.Writer, io.Writer) int

type intakeServiceStatus struct {
	State  string          `json:"state"`
	Detail string          `json:"detail,omitempty"`
	Sync   json.RawMessage `json:"sync,omitempty"`
}

type serviceStatusOutput struct {
	install.ServiceStatus
	Intake intakeServiceStatus `json:"intake"`
}

type serviceInstallOutput struct {
	serviceStatusOutput
	PairPage      string `json:"pair_page,omitempty"`
	BrowserOpened bool   `json:"browser_opened"`
}

func runService(ctx context.Context, command attemptCommand, getenv func(string) string, stdout, stderr io.Writer, inspect serviceInspector, opener browserOpener, intakeRunner intakeServiceRunner) int {
	callContext, cancel := context.WithTimeout(ctx, serviceRequestTimeout)
	defer cancel()
	config := serviceConfigFor(command)
	var status install.ServiceStatus
	var existing install.ServiceState
	var err error
	switch command.kind {
	case commandServiceStatus:
		if inspect == nil && command.label == "" && command.plistDir == "" {
			_, _ = io.WriteString(stderr, "factoryctl: service status configuration is invalid\n")
			return exitFailure
		}
		status, err = inspectService(callContext, command, config, inspect)
	case commandServiceInstall:
		var self string
		self, err = serviceSourceDirectory()
		if err == nil {
			// The service found before the install decides whether this
			// command started anything: repeating an install returns the
			// service it found, unchanged, and must open no browser.
			if found, inspectErr := inspectService(callContext, command, config, inspect); inspectErr == nil {
				existing = found.State
			}
			status, err = install.ServiceInstall(callContext, command.home, config, self)
		}
	case commandServiceStart:
		status, err = install.ServiceStart(callContext, command.home, config)
	case commandServiceStop:
		status, err = install.ServiceStop(callContext, command.home, config)
	case commandServiceUninstall:
		intake, ready := managedIntakeStatus(callContext, command.home, getenv, intakeRunner)
		if !ready {
			_, _ = io.WriteString(stderr, "factoryctl: intake service must be removed before factoryd; "+intake.Detail+"\n")
			return exitFailure
		}
		if intake.State == "scheduled" || intake.State == "stopped" {
			var output bytes.Buffer
			if intakeRunner == nil || intakeRunner(callContext, []string{"uninstall", "--home", command.home}, getenv, &output, stderr) != 0 || intakeServiceResult(output.Bytes(), "uninstall").State != "absent" {
				_, _ = io.WriteString(stderr, "factoryctl: intake service must be removed before factoryd; run factoryctl intake service uninstall --home ABSOLUTE\n")
				return exitFailure
			}
		}
		status, err = install.ServiceUninstall(callContext, command.home, config)
	}
	if err != nil {
		message := "factoryctl: the service operation is ambiguous; inspect the home and launchd state\n"
		switch {
		case errors.Is(err, context.Canceled):
			message = "factoryctl: service operation canceled\n"
		case errors.Is(err, context.DeadlineExceeded):
			message = "factoryctl: service operation timed out\n"
		case errors.Is(err, install.ErrUnsupported):
			message = "factoryctl: service operations are unsupported on this platform\n"
		case errors.Is(err, install.ErrInvalidHome):
			message = "factoryctl: service operations require an exact Go home\n"
		case errors.Is(err, install.ErrServiceRelayOrigin):
			// The only service error printed verbatim: it names the installed
			// origin and the way out, and the engine assembles it from its own
			// words alone.
			message = "factoryctl: " + err.Error() + "\n"
		case errors.Is(err, install.ErrServiceForeign):
			message = "factoryctl: a service artifact is not this installation's property; refusing\n"
		case errors.Is(err, install.ErrServiceResidue):
			message = "factoryctl: service residue found; run factoryctl service uninstall first\n"
		}
		_, _ = io.WriteString(stderr, message)
		return exitFailure
	}
	// The projection is exact or refused: states outside the finite set, or a
	// pid that disagrees with its state, never print as success.
	switch status.State {
	case install.ServiceAbsent, install.ServiceInstalled:
		if status.PID != 0 {
			_, _ = io.WriteString(stderr, "factoryctl: the service projection is ambiguous\n")
			return exitFailure
		}
	case install.ServiceRunning:
		if status.PID <= 0 {
			_, _ = io.WriteString(stderr, "factoryctl: the service projection is ambiguous\n")
			return exitFailure
		}
	default:
		_, _ = io.WriteString(stderr, "factoryctl: the service projection is ambiguous\n")
		return exitFailure
	}
	intake, _ := managedIntakeStatus(callContext, command.home, getenv, intakeRunner)
	packagedIntake := command.kind == commandServiceInstall && installedIntakeAssets()
	if packagedIntake {
		var diagnostic []byte
		var failed bool
		intake, diagnostic, failed = installManagedIntake(callContext, command.home, getenv, intakeRunner)
		if failed {
			_, _ = stderr.Write(diagnostic)
			_, _ = io.WriteString(stderr, "factoryctl: factoryd started, but intake setup failed; run factoryctl intake service install --home ABSOLUTE after resolving the reported problem\n")
			return exitFailure
		}
	}
	if command.kind == commandServiceInstall && !packagedIntake && intake.State == "absent" {
		intake = intakeServiceStatus{State: "unavailable", Detail: "Install a release or Homebrew factoryctl to add managed intake."}
	}
	if command.kind == commandServiceInstall && pairPageOpens(existing, status.State) {
		// The one command whose result is more than the projection. Every word
		// of it goes in the JSON on stdout: this output is parsed, and a stray
		// stderr line would be merged into it by any caller reading both.
		return writeJSON(stdout, serviceInstallOutput{serviceStatusOutput: serviceStatusOutput{ServiceStatus: status, Intake: intake}, PairPage: pairPageURL, BrowserOpened: openPairPage(ctx, pairListenAddress, pairPageURL, opener)})
	}
	return writeJSON(stdout, serviceStatusOutput{ServiceStatus: status, Intake: intake})
}

// managedIntakeStatus is deliberately separate from factoryd's service state:
// a source checkout can run factoryd without packaged controller assets, while
// a release gets one controller schedule through the existing private receipt.
func managedIntakeStatus(ctx context.Context, home string, getenv func(string) string, runner intakeServiceRunner) (intakeServiceStatus, bool) {
	return managedIntakeStatusWithAssets(ctx, home, getenv, runner, installedIntakeAssets())
}

func managedIntakeStatusWithAssets(ctx context.Context, home string, getenv func(string) string, runner intakeServiceRunner, assets bool) (intakeServiceStatus, bool) {
	present, err := managedIntakeReceipt(home)
	if err != nil {
		return intakeServiceStatus{State: "unavailable", Detail: "Intake receipt is unsafe; inspect it before retrying."}, false
	}
	if !present && !assets {
		if _, err := os.Lstat(home + ".intake"); errors.Is(err, os.ErrNotExist) {
			return intakeServiceStatus{State: "absent"}, true
		}
		return intakeServiceStatus{State: "unavailable", Detail: "Intake state needs a packaged release to inspect safely."}, false
	}
	var output, serviceErr bytes.Buffer
	if runner == nil || runner(ctx, []string{"status", "--home", home}, getenv, &output, &serviceErr) != 0 {
		return intakeServiceStatus{State: "unavailable", Detail: "Run factoryctl intake service status --home ABSOLUTE."}, false
	}
	result := intakeServiceResult(output.Bytes(), "status")
	return result, result.State != "unavailable"
}

func managedIntakeReceipt(home string) (bool, error) {
	_, err := os.Lstat(home + ".intake/service.json")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = privateControllerFile(home+".intake/service.json", 16384, true)
	return err == nil, err
}

func installedIntakeAssets() bool {
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return false
	}
	_, err = installedIntakeController(executable)
	return err == nil
}

func intakeServiceResult(output []byte, action string) intakeServiceStatus {
	var result struct {
		State string          `json:"state"`
		Sync  json.RawMessage `json:"sync"`
	}
	if len(output) > 64<<10 || json.Unmarshal(output, &result) != nil {
		return intakeServiceStatus{State: "unavailable", Detail: "Managed intake returned an invalid status; run factoryctl intake service " + action + " --home ABSOLUTE."}
	}
	switch result.State {
	case "scheduled", "stopped":
		return intakeServiceStatus{State: result.State, Sync: result.Sync}
	case "absent":
		return intakeServiceStatus{State: "absent"}
	default:
		return intakeServiceStatus{State: "unavailable", Detail: "Managed intake is not scheduled; run factoryctl intake service " + action + " --home ABSOLUTE."}
	}
}

func legacyIntakeSchedule(output string) bool {
	return strings.Contains(output, "legacy intake already schedules this factory")
}

func installManagedIntake(ctx context.Context, home string, getenv func(string) string, runner intakeServiceRunner) (intakeServiceStatus, []byte, bool) {
	var output, diagnostic bytes.Buffer
	if runner == nil || runner(ctx, []string{"install", "--home", home}, getenv, &output, &diagnostic) != 0 {
		if legacyIntakeSchedule(diagnostic.String()) {
			return intakeServiceStatus{State: "unavailable", Detail: "A legacy intake controller manages this factory; run its explicit migration before replacing it."}, nil, false
		}
		return intakeServiceStatus{State: "unavailable"}, diagnostic.Bytes(), true
	}
	result := intakeServiceResult(output.Bytes(), "install")
	return result, nil, result.State != "scheduled"
}

// inspectService is the read-only projection status and install share: the
// injected exact inspector for the default label and plist directory, and the
// explicit-config inspector otherwise.
func inspectService(ctx context.Context, command attemptCommand, config install.ServiceConfig, inspect serviceInspector) (install.ServiceStatus, error) {
	if inspect != nil && command.label == "" && command.plistDir == "" {
		return inspect(ctx, command.home)
	}
	return install.InspectServiceWithConfig(ctx, command.home, config)
}

// pairPageOpens is true only for an install that actually started the service.
func pairPageOpens(existing, resulting install.ServiceState) bool {
	return resulting == install.ServiceRunning && existing != install.ServiceInstalled && existing != install.ServiceRunning
}

// openPairPage waits, bounded, for factoryd to accept on its loopback listener
// and then opens the pair page exactly once, reporting whether it did. A
// listener that never appears or an opener that fails is not an install
// failure: the caller names the page in its own output and exits 0.
func openPairPage(ctx context.Context, address, page string, opener browserOpener) bool {
	return opener != nil && listenerAccepts(ctx, address) && opener(ctx, page) == nil
}

func listenerAccepts(ctx context.Context, address string) bool {
	deadline := time.Now().Add(pairListenPatience)
	for {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = connection.Close()
			return true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// serviceSourceDirectory is the invoking factoryctl's own resolved directory:
// the managed installation installs exactly the sibling binaries it shipped
// with, never a path the operator did not run.
func serviceSourceDirectory() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	return filepath.Dir(resolved), nil
}

func validHomeArg(value string) bool {
	return value != "" && len(value) <= maxHomeArgumentBytes && filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Base(value) != "." && filepath.Base(value) != string(filepath.Separator)
}

func runHome(ctx context.Context, command attemptCommand, stdout, stderr io.Writer) int {
	var result install.Result
	var err error
	if command.kind == commandInit {
		result, err = install.Init(ctx, command.home)
	} else {
		result, err = install.Doctor(ctx, command.home)
	}
	if err != nil {
		switch {
		case errors.Is(err, install.ErrUncertain):
			message := "factoryctl: home publication outcome is uncertain; inspect the explicit home path before retrying\n"
			if command.kind == commandDoctor {
				message = "factoryctl: home inspection outcome is uncertain; inspect the explicit home path again\n"
			}
			_, _ = io.WriteString(stderr, message)
		case errors.Is(err, install.ErrUnsupported):
			_, _ = io.WriteString(stderr, "factoryctl: Go home operations are unsupported on this platform\n")
		case errors.Is(err, install.ErrInvalidHome):
			_, _ = io.WriteString(stderr, "factoryctl: home is invalid or not an exact stopped Go home\n")
		default:
			_, _ = io.WriteString(stderr, "factoryctl: home operation failed\n")
		}
		return exitFailure
	}
	if result.State == install.Ready {
		_, _ = io.WriteString(stdout, "home ready\n")
	} else {
		_, _ = io.WriteString(stdout, "home initialized\n")
	}
	return 0
}

func parseWeb(args []string) (attemptCommand, bool, bool) {
	switch args[1] {
	case "status":
		if len(args) == 2 {
			return attemptCommand{kind: commandWebStatus}, false, true
		}
	case "list-clients":
		if len(args) == 2 {
			return attemptCommand{kind: commandWebListClients}, false, true
		}
		if len(args) == 4 && args[2] == "--after" && validBrowserClientID(args[3]) {
			return attemptCommand{kind: commandWebListClients, after: args[3]}, false, true
		}
	case "revoke":
		if len(args) == 5 && validBrowserClientID(args[2]) && args[3] == "--revision" {
			revision, ok := parseRevision(args[4])
			if ok {
				return attemptCommand{kind: commandWebRevoke, id: args[2], expectedRevision: revision}, false, true
			}
		}
	}
	return attemptCommand{}, false, false
}

func parseOperator(args []string) (attemptCommand, bool, bool) {
	if len(args) >= 2 && args[0] == "intake" {
		input := api.IntakeInput{Action: args[1]}
		if input.Action == "config" {
			input.Action = "list"
		}
		if (len(args)-2)%2 != 0 {
			return attemptCommand{}, false, false
		}
		configuration := api.IntakeConfiguration{Policy: "manual", PollSeconds: 60, AdmissionLimit: 25}
		configurationFlags := false
		configurationJSON := false
		seen := map[string]bool{}
		for i := 2; i < len(args); i += 2 {
			key, value := args[i], args[i+1]
			if key != "--trusted-author" && seen[key] {
				return attemptCommand{}, false, false
			}
			seen[key] = true
			switch key {
			case "--source":
				input.SourceID = value
			case "--project":
				input.ProjectID = value
			case "--revision":
				n, ok := parseRevision(value)
				if !ok {
					return attemptCommand{}, false, false
				}
				input.ExpectedRevision = n
			case "--reviewed-revision":
				n, ok := parseRevision(value)
				if !ok {
					return attemptCommand{}, false, false
				}
				input.ReviewedRevision = n
			case "--page":
				n, ok := parseRevision(value)
				if !ok || n > 1000 {
					return attemptCommand{}, false, false
				}
				input.Page = uint32(n)
			case "--issue":
				n, ok := parseRevision(value)
				if !ok {
					return attemptCommand{}, false, false
				}
				input.IssueNumber = n
			case "--hash":
				input.ContentHash = value
			case "--acceptance-cursor":
				input.AcceptanceCursor = value
			case "--acceptance":
				input.AcceptanceID = value
			case "--configuration":
				if configurationFlags || json.Unmarshal([]byte(value), &configuration) != nil {
					return attemptCommand{}, false, false
				}
				configurationJSON = true
			case "--repository":
				if configurationJSON || !validIntakeRepository(value) {
					return attemptCommand{}, false, false
				}
				configuration.Repository, configurationFlags = value, true
			case "--target-repository":
				if configurationJSON || !validHumanRequestKey(value) {
					return attemptCommand{}, false, false
				}
				configuration.TargetRepositoryID, configurationFlags = value, true
			case "--overseer":
				if configurationJSON || !validHumanRequestKey(value) {
					return attemptCommand{}, false, false
				}
				configuration.OverseerAgentID, configurationFlags = value, true
			case "--label":
				if configurationJSON || !validOperatorText(value, 1, 100) {
					return attemptCommand{}, false, false
				}
				configuration.Label, configurationFlags = value, true
			case "--policy":
				if configurationJSON || value != "manual" && value != "trusted-authors" {
					return attemptCommand{}, false, false
				}
				configuration.Policy, configurationFlags = strings.ReplaceAll(value, "-", "_"), true
			case "--trusted-author":
				if configurationJSON || !validOperatorText(value, 1, 100) {
					return attemptCommand{}, false, false
				}
				configuration.TrustedAuthors, configurationFlags = append(configuration.TrustedAuthors, value), true
			case "--poll-seconds":
				n, ok := parseRevision(value)
				if configurationJSON || !ok || n < 5 || n > 86400 {
					return attemptCommand{}, false, false
				}
				configuration.PollSeconds, configurationFlags = uint32(n), true
			case "--admission-limit":
				n, ok := parseRevision(value)
				if configurationJSON || !ok || n < 1 || n > 200 {
					return attemptCommand{}, false, false
				}
				configuration.AdmissionLimit, configurationFlags = uint16(n), true
			case "--priority":
				n, err := strconv.ParseInt(value, 10, 64)
				if configurationJSON || err != nil || value != strconv.FormatInt(n, 10) || n < -1_000_000 || n > 1_000_000 {
					return attemptCommand{}, false, false
				}
				configuration.PriorityDefault, configurationFlags = n, true
			default:
				return attemptCommand{}, false, false
			}
		}
		if configurationFlags || configurationJSON {
			input.Configuration = &configuration
		}
		if input.Action == "create" && input.SourceID == "" {
			id, err := newOperatorID()
			if err != nil {
				return attemptCommand{}, false, false
			}
			input.SourceID = id
		}
		if input.Action == "create" || input.Action == "update" {
			if input.Configuration == nil || configurationFlags && !validNamedIntakeConfiguration(*input.Configuration) {
				return attemptCommand{}, false, false
			}
		}
		if !api.ValidIntakeInput(input) {
			return attemptCommand{}, false, false
		}
		return attemptCommand{kind: commandIntake, intake: input}, false, true
	}
	if len(args) >= 3 && args[0] == "project" && args[1] == "repository" {
		action := args[2]
		if action != "list" && action != "add" && action != "name" && action != "base" && action != "default" && action != "enable" && action != "disable" && action != "remove" && action != "fetch" && action != "github" {
			return attemptCommand{}, false, false
		}
		command := attemptCommand{kind: commandProjectRepository, provider: action}
		if (len(args)-3)%2 != 0 {
			return attemptCommand{}, false, false
		}
		for i := 3; i < len(args); i += 2 {
			key, value := args[i], args[i+1]
			switch key {
			case "--project":
				if validHumanRequestKey(value) {
					command.project = value
				} else {
					return attemptCommand{}, false, false
				}
			case "--id":
				if validHumanRequestKey(value) {
					command.repository = value
				} else {
					return attemptCommand{}, false, false
				}
			case "--name":
				if validOperatorText(value, 1, 128) {
					command.name = value
				} else {
					return attemptCommand{}, false, false
				}
			case "--root":
				if validHomeArg(value) {
					command.root = value
				} else {
					return attemptCommand{}, false, false
				}
			case "--base":
				if validOperatorText(value, 1, 256) {
					command.body = value
				} else {
					return attemptCommand{}, false, false
				}
			case "--revision":
				n, ok := parseRevision(value)
				if !ok {
					return attemptCommand{}, false, false
				}
				command.expectedRevision = n
			default:
				return attemptCommand{}, false, false
			}
		}
		if action == "fetch" || action == "github" {
			return command, false, len(args) == 5 && args[3] == "--id" && command.repository != ""
		}
		if action == "list" {
			return command, false, command.project != ""
		}
		if action == "add" {
			return command, false, command.project != "" && command.name != "" && command.root != "" && command.body != ""
		}
		return command, false, command.repository != "" && command.expectedRevision != 0
	}
	if len(args) >= 1 && args[0] == "worker" {
		if len(args) == 4 && args[1] == "operation" && args[2] == "--operation-id" && validHumanRequestKey(args[3]) {
			return attemptCommand{kind: commandWorkerOperation, operationID: args[3]}, false, true
		}
		command, help, ok := parseOverseer(append([]string{"overseer"}, args...))
		command.operatorControl = true
		return command, help, ok
	}
	if len(args) >= 2 && args[0] == "content" {
		return parseContent(args)
	}
	if len(args) >= 2 && args[0] == "outcome" {
		return parseOutcome(args)
	}
	if len(args) == 1 && args[0] == "status" {
		return attemptCommand{kind: commandStatus}, false, true
	}
	if len(args) == 2 && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) >= 3 && helpFlag(args[2]) {
		switch args[0] + " " + args[1] {
		case "project create", "project limits", "agent create", "agent idle-policy", "agent select-account", "agent select-model", "agent paths", "account link", "task add", "task send-back", "task recovery":
			return attemptCommand{}, true, true
		}
	}
	if args[0] == "dispatch" {
		if len(args) == 2 && (args[1] == "on" || args[1] == "off") {
			return attemptCommand{kind: commandDispatch, enabled: args[1] == "on"}, false, true
		}
		if len(args) == 4 && (args[1] == "on" || args[1] == "off") && args[2] == "--revision" {
			revision, ok := parseRevision(args[3])
			if ok {
				return attemptCommand{kind: commandDispatch, enabled: args[1] == "on", expectedRevision: revision}, false, true
			}
		}
		return attemptCommand{}, false, false
	}
	if args[0] == "capacity" {
		if len(args) == 5 && args[1] == "--workers" && args[3] == "--revision" {
			workers, workersOK := parseRevision(args[2])
			revision, revisionOK := parseRevision(args[4])
			if workersOK && workers >= 1 && workers <= uint64(kernel.MaxFactoryCapacity) && revisionOK {
				return attemptCommand{kind: commandCapacity, capacity: uint16(workers), expectedRevision: revision}, false, true
			}
		}
		return attemptCommand{}, false, false
	}
	if len(args) == 2 && args[0] == "account" && args[1] == "discover" {
		return attemptCommand{kind: commandAccountsDiscover}, false, true
	}
	if len(args) == 2 && args[0] == "account" && args[1] == "list" {
		return attemptCommand{kind: commandAccountsList}, false, true
	}
	if len(args) < 2 {
		return attemptCommand{}, false, false
	}
	command := attemptCommand{role: "worker"}
	switch args[0] + " " + args[1] {
	case "project create":
		command.kind = commandProjectCreate
	case "project limits":
		command.kind = commandProjectLimits
	case "agent create":
		command.kind = commandAgentCreate
	case "agent idle-policy":
		command.kind = commandAgentIdlePolicy
	case "agent select-account":
		command.kind = commandAgentSelectAccount
	case "agent select-model":
		command.kind = commandAgentSelectModel
	case "agent paths":
		command.kind = commandAgentPaths
	case "account link":
		command.kind = commandAccountLink
	case "task add":
		command.kind = commandTaskAdd
	case "task send-back":
		command.kind = commandTaskSendBack
	case "task recovery":
		command.kind = commandTaskRecovery
	case "task read":
		command.kind = commandTaskRead
	default:
		return attemptCommand{}, false, false
	}
	seen := map[string]bool{}
	for index := 2; index < len(args); index += 2 {
		if index+1 >= len(args) {
			return attemptCommand{}, false, false
		}
		name, value := args[index], args[index+1]
		if seen[name] {
			return attemptCommand{}, false, false
		}
		seen[name] = true
		switch {
		case name == "--name" && (command.kind == commandProjectCreate || command.kind == commandAgentCreate) && validOperatorText(value, 1, 128):
			command.name = value
		case name == "--root" && command.kind == commandProjectCreate && validHomeArg(value) && validOperatorText(value, 1, 4096):
			command.root = value
		case name == "--project" && (command.kind == commandProjectLimits || command.kind == commandAgentCreate || command.kind == commandTaskAdd) && validHumanRequestKey(value):
			command.project = value
		case name == "--repository" && command.kind == commandTaskAdd && validHumanRequestKey(value):
			command.repository = value
		case name == "--revision" && (command.kind == commandProjectLimits || command.kind == commandAgentIdlePolicy):
			revision, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.expectedRevision = revision
		case name == "--run-budget" && command.kind == commandProjectLimits:
			budget, err := strconv.ParseUint(value, 10, 64)
			if err != nil || value != strconv.FormatUint(budget, 10) || budget > uint64(^uint64(0)>>1) {
				return attemptCommand{}, false, false
			}
			command.toolBudget = budget
		case name == "--max-run-seconds" && command.kind == commandProjectLimits:
			seconds, err := strconv.ParseUint(value, 10, 32)
			if err != nil || value != strconv.FormatUint(seconds, 10) || seconds > 86400 {
				return attemptCommand{}, false, false
			}
			command.maxRunSeconds = uint32(seconds)
		case name == "--agent" && (command.kind == commandTaskAdd || command.kind == commandAgentIdlePolicy || command.kind == commandAgentSelectAccount || command.kind == commandAgentSelectModel || command.kind == commandAgentPaths) && (validHumanRequestKey(value) || command.kind == commandTaskAdd && value == "any"):
			command.agent = value
		case name == "--revision" && (command.kind == commandAgentSelectAccount || command.kind == commandAgentSelectModel):
			revision, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.expectedRevision = revision
		case name == "--account" && command.kind == commandAgentSelectAccount && validHumanRequestKey(value):
			command.account = value
		case name == "--model" && command.kind == commandAgentSelectModel && validOperatorText(value, 1, 128):
			command.model = value
		case name == "--reasoning-effort" && command.kind == commandAgentSelectModel:
			command.reasoningEffort = value
		case name == "--provider" && command.kind == commandAccountLink && (value == "claude_code" || value == "codex"):
			command.provider = value
		case name == "--home" && command.kind == commandAccountLink && validHomeArg(value) && validOperatorText(value, 1, 1024):
			command.root = value
		case name == "--label" && command.kind == commandAccountLink && validOperatorText(value, 1, 128):
			command.label = value
		case name == "--role" && command.kind == commandAgentCreate && (value == "worker" || value == "orchestrator"):
			command.role = value
		case name == "--provider" && command.kind == commandAgentCreate:
			command.provider = value
		case name == "--model" && command.kind == commandAgentCreate && validOperatorText(value, 0, 128):
			command.model = value
		case name == "--reasoning-effort" && command.kind == commandAgentCreate:
			command.reasoningEffort = value
		case name == "--account" && command.kind == commandAgentCreate && validHumanRequestKey(value):
			command.account = value
		case name == "--tool-budget" && command.kind == commandAgentCreate:
			budget, err := strconv.ParseUint(value, 10, 64)
			if err != nil || value != strconv.FormatUint(budget, 10) || budget < 1 || budget > 1_000_000_000 {
				return attemptCommand{}, false, false
			}
			command.toolBudget = budget
		case name == "--policy" && command.kind == commandAgentIdlePolicy && (value == "wait" || value == "standing_instruction"):
			command.provider = value
		case name == "--after-seconds" && command.kind == commandAgentIdlePolicy:
			seconds, err := strconv.ParseUint(value, 10, 32)
			if err != nil || value != strconv.FormatUint(seconds, 10) || seconds > uint64(kernel.MaxIdleAfterSeconds) {
				return attemptCommand{}, false, false
			}
			command.maxRunSeconds = uint32(seconds)
		case name == "--instruction" && command.kind == commandAgentIdlePolicy && validOperatorText(value, 1, 32768):
			command.text = value
		case name == "--run-budget" && command.kind == commandAgentIdlePolicy:
			budget, err := strconv.ParseUint(value, 10, 32)
			if err != nil || value != strconv.FormatUint(budget, 10) || budget > uint64(kernel.MaxIdleRunBudget) {
				return attemptCommand{}, false, false
			}
			command.toolBudget = budget
		case name == "--title" && command.kind == commandTaskAdd && validOperatorText(value, 1, 1024):
			command.title = value
		case name == "--body" && command.kind == commandTaskAdd && validOperatorText(value, 0, 131072):
			command.body = value
		case name == "--task-id" && command.kind == commandTaskAdd && validHumanRequestKey(value):
			command.id = value
		case name == "--incarnation-id" && command.kind == commandTaskAdd && validHumanRequestKey(value):
			command.run = value
		case name == "--task" && command.kind == commandTaskSendBack && validHumanRequestKey(value):
			command.id = value
		case name == "--task" && command.kind == commandTaskRecovery && validHumanRequestKey(value):
			command.id = value
		case name == "--task" && command.kind == commandTaskRead && validHumanRequestKey(value):
			command.id = value
		case name == "--revision" && command.kind == commandTaskRead:
			revision, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.expectedRevision = revision
		case name == "--offset" && command.kind == commandTaskRead:
			offset, err := strconv.ParseUint(value, 10, 64)
			if err != nil || value != strconv.FormatUint(offset, 10) || offset > uint64(^uint64(0)>>1) {
				return attemptCommand{}, false, false
			}
			command.offset = offset
		case name == "--incarnation" && command.kind == commandTaskRecovery && validHumanRequestKey(value):
			command.run = value
		case name == "--note" && command.kind == commandTaskSendBack && validQuestion(value):
			command.text = value
		case name == "--priority" && command.kind == commandTaskAdd:
			priority, err := strconv.ParseInt(value, 10, 64)
			if err != nil || value != strconv.FormatInt(priority, 10) || priority < -1_000_000 || priority > 1_000_000 {
				return attemptCommand{}, false, false
			}
			command.priority = priority
			command.prioritySet = true
		default:
			return attemptCommand{}, false, false
		}
	}
	switch command.kind {
	case commandProjectCreate:
		if command.name == "" || command.root == "" {
			return attemptCommand{}, false, false
		}
	case commandProjectLimits:
		if command.project == "" || command.expectedRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandAgentCreate:
		provider, err := kernel.ParseProvider(command.provider)
		if command.project == "" || command.name == "" || command.toolBudget == 0 || err != nil || kernel.ValidateProviderLaunchControls(provider, command.model, command.reasoningEffort) != nil {
			return attemptCommand{}, false, false
		}
	case commandAgentIdlePolicy:
		if command.agent == "" || command.expectedRevision == 0 || command.provider == "" || (command.provider == "wait" && (command.maxRunSeconds != 0 || command.text != "" || command.toolBudget != 0)) || (command.provider == "standing_instruction" && (command.maxRunSeconds == 0 || command.text == "")) {
			return attemptCommand{}, false, false
		}
	case commandAgentSelectAccount:
		if command.agent == "" || command.account == "" || command.expectedRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandAgentSelectModel:
		if command.agent == "" || command.model == "" || command.expectedRevision == 0 || !validReasoningEffort(command.reasoningEffort) {
			return attemptCommand{}, false, false
		}
	case commandAgentPaths:
		if command.agent == "" {
			return attemptCommand{}, false, false
		}
	case commandAccountLink:
		if command.provider == "" || command.root == "" || command.label == "" {
			return attemptCommand{}, false, false
		}
	case commandTaskAdd:
		if command.project == "" || command.agent == "" || command.title == "" || (command.id == "") != (command.run == "") {
			return attemptCommand{}, false, false
		}
	case commandTaskSendBack:
		if command.id == "" || command.text == "" {
			return attemptCommand{}, false, false
		}
	case commandTaskRecovery:
		if command.id == "" || command.run == "" {
			return attemptCommand{}, false, false
		}
	case commandTaskRead:
		if command.id == "" || command.expectedRevision == 0 {
			return attemptCommand{}, false, false
		}
	}
	return command, false, true
}

// validNamedIntakeConfiguration keeps the friendly flags at the same trust
// boundary as the JSON form. The daemon still verifies repository access and
// persists the authoritative configuration.
func validNamedIntakeConfiguration(value api.IntakeConfiguration) bool {
	if !validIntakeRepository(value.Repository) || !validHumanRequestKey(value.TargetRepositoryID) || value.OverseerAgentID != "" && !validHumanRequestKey(value.OverseerAgentID) || !validOperatorText(value.Label, 0, 100) || value.Policy != "manual" && value.Policy != "trusted_authors" || value.PollSeconds < 5 || value.PollSeconds > 86400 || value.AdmissionLimit < 1 || value.AdmissionLimit > 200 || value.PriorityDefault < -1_000_000 || value.PriorityDefault > 1_000_000 || len(value.TrustedAuthors) > 25 || len(value.PriorityByLabel) > 25 {
		return false
	}
	seen := map[string]bool{}
	for _, author := range value.TrustedAuthors {
		if !validOperatorText(author, 1, 39) || seen[strings.ToLower(author)] {
			return false
		}
		seen[strings.ToLower(author)] = true
	}
	for label, priority := range value.PriorityByLabel {
		if !validOperatorText(label, 1, 100) || priority < -1_000_000 || priority > 1_000_000 {
			return false
		}
	}
	encoded, err := json.Marshal(value.PriorityByLabel)
	return err == nil && len(encoded) <= 2048 && (value.Policy != "trusted_authors" || len(value.TrustedAuthors) > 0)
}

func validIntakeRepository(value string) bool {
	return validOperatorText(value, 3, 140) && strings.Count(value, "/") == 1
}

// anyWorkerAgent maps the CLI's `--agent any` to the wire's empty assigned
// agent: the task is queued for any eligible worker in its project.
func anyWorkerAgent(agent string) string {
	if agent == "any" {
		return ""
	}
	return agent
}

func parseOverseer(args []string) (attemptCommand, bool, bool) {
	if len(args) >= 2 && args[1] == "status" {
		command := attemptCommand{kind: commandOverseerStatus}
		seen := map[string]bool{}
		for index := 2; index < len(args); index += 2 {
			if index+1 >= len(args) || seen[args[index]] {
				return attemptCommand{}, false, false
			}
			seen[args[index]] = true
			value := args[index+1]
			switch args[index] {
			case "--task":
				if !validHumanRequestKey(value) {
					return attemptCommand{}, false, false
				}
				command.id = value
			case "--offset":
				offset, ok := parseOverseerOffset(value, true)
				if !ok {
					return attemptCommand{}, false, false
				}
				command.offset = offset
			case "--head":
				head, ok := parseRevision(value)
				if !ok {
					return attemptCommand{}, false, false
				}
				command.head = head
			case "--text-offset":
				offset, ok := parseOverseerOffset(value, false)
				if !ok {
					return attemptCommand{}, false, false
				}
				command.textOffset = offset
			default:
				return attemptCommand{}, false, false
			}
		}
		if command.head == 0 && (command.offset != 0 || command.textOffset != 0) || command.textOffset != 0 && command.id == "" {
			return attemptCommand{}, false, false
		}
		return command, false, true
	}
	if len(args) < 3 {
		return attemptCommand{}, false, false
	}
	command := attemptCommand{}
	switch strings.Join(args[1:3], " ") {
	case "task add":
		command.kind = commandOverseerTaskAdd
	case "task update":
		command.kind = commandOverseerTaskUpdate
	case "task send-back":
		command.kind = commandOverseerTaskSendBack
	case "agent pause":
		command.kind, command.paused = commandOverseerAgentUpdate, true
	case "agent resume":
		command.kind = commandOverseerAgentUpdate
	case "agent archive":
		command.kind, command.archived, command.archiveSet = commandOverseerAgentUpdate, true, true
	case "agent restore":
		command.kind, command.archiveSet = commandOverseerAgentUpdate, true
	case "worker stop":
		command.kind = commandOverseerStopWorker
	case "worker replace":
		command.kind = commandOverseerReplaceWorker
	case "worker message":
		command.kind = commandOverseerMessageWorker
	case "worker interrupt":
		command.kind = commandOverseerInterruptWorker
	case "human reply":
		command.kind = commandOverseerReplyHuman
	default:
		return attemptCommand{}, false, false
	}
	seen := map[string]bool{}
	for index := 3; index < len(args); {
		name := args[index]
		if name == "--cancel" && command.kind == commandOverseerTaskUpdate {
			if seen[name] {
				return attemptCommand{}, false, false
			}
			seen[name], command.cancel = true, true
			index++
			continue
		}
		if name == "--retry" && command.kind == commandOverseerTaskUpdate {
			if seen[name] {
				return attemptCommand{}, false, false
			}
			seen[name], command.retry = true, true
			index++
			continue
		}
		if index+1 >= len(args) || seen[name] && name != "--prerequisite" && name != "--conflict-path" {
			return attemptCommand{}, false, false
		}
		seen[name] = true
		value := args[index+1]
		index += 2
		switch name {
		case "--agent":
			if !validHumanRequestKey(value) && !(command.kind == commandOverseerTaskAdd && value == "any") {
				return attemptCommand{}, false, false
			}
			command.agent = value
		case "--task":
			if !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.id = value
		case "--task-id":
			if command.kind != commandOverseerTaskAdd || !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.id = value
		case "--incarnation-id":
			if command.kind != commandOverseerTaskAdd || !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.project = value
		case "--run":
			if !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.run = value
		case "--request":
			if !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.id = value
		case "--successor-task":
			if !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.project = value
		case "--successor-incarnation":
			if !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.account = value
		case "--title":
			if command.kind != commandOverseerTaskAdd && command.kind != commandOverseerTaskUpdate || !validOperatorText(value, 1, 1024) {
				return attemptCommand{}, false, false
			}
			command.title = value
		case "--body":
			if command.kind != commandOverseerTaskAdd && command.kind != commandOverseerTaskUpdate || !validOperatorText(value, 0, 131072) {
				return attemptCommand{}, false, false
			}
			command.body, command.bodySet = value, true
		case "--priority":
			priority, err := strconv.ParseInt(value, 10, 64)
			if err != nil || value != strconv.FormatInt(priority, 10) || priority < -1_000_000 || priority > 1_000_000 || command.kind != commandOverseerTaskAdd && command.kind != commandOverseerTaskUpdate {
				return attemptCommand{}, false, false
			}
			command.priority = priority
			command.prioritySet = true
		case "--prerequisite":
			if command.kind != commandOverseerTaskAdd {
				return attemptCommand{}, false, false
			}
			parts := strings.Split(value, ":")
			if len(parts) != 2 || !validHumanRequestKey(parts[0]) {
				return attemptCommand{}, false, false
			}
			revision, ok := parseRevision(parts[1])
			if !ok {
				return attemptCommand{}, false, false
			}
			command.prerequisites = append(command.prerequisites, api.TaskPrerequisiteInput{TaskID: parts[0], WorkRevision: revision})
		case "--conflict-path":
			if command.kind != commandOverseerTaskAdd || !validOperatorText(value, 1, 4096) || strings.HasPrefix(value, "/") {
				return attemptCommand{}, false, false
			}
			command.conflictPaths = append(command.conflictPaths, value)
		case "--revision":
			revision, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.expectedRevision = revision
		case "--task-revision":
			revision, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.taskRevision = revision
		case "--run-revision":
			revision, ok := parseRevision(value)
			if !ok {
				return attemptCommand{}, false, false
			}
			command.runRevision = revision
		case "--note", "--detail", "--message", "--reply", "--instruction":
			if !validQuestion(value) {
				return attemptCommand{}, false, false
			}
			command.text = value
		case "--operation-id":
			if !validHumanRequestKey(value) {
				return attemptCommand{}, false, false
			}
			command.operationID = value
		default:
			return attemptCommand{}, false, false
		}
	}
	switch command.kind {
	case commandOverseerTaskAdd:
		if command.agent == "" || command.title == "" || command.id == "" != (command.project == "") {
			return attemptCommand{}, false, false
		}
	case commandOverseerTaskUpdate:
		if command.id == "" || command.expectedRevision == 0 || command.retry && (command.title != "" || command.bodySet || command.prioritySet || command.cancel) || !command.retry && !command.cancel && command.agent == "" && command.title == "" && !command.bodySet && !command.prioritySet {
			return attemptCommand{}, false, false
		}
	case commandOverseerTaskSendBack:
		if command.id == "" || command.text == "" {
			return attemptCommand{}, false, false
		}
	case commandOverseerAgentUpdate:
		if command.agent == "" || command.expectedRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandOverseerStopWorker:
		if command.operationID == "" || command.id == "" || command.run == "" || command.taskRevision == 0 || command.runRevision == 0 {
			return attemptCommand{}, false, false
		}
	case commandOverseerReplaceWorker:
		if command.operationID == "" || command.id == "" || command.run == "" || command.taskRevision == 0 || command.runRevision == 0 || command.project == "" || command.account == "" || command.text == "" {
			return attemptCommand{}, false, false
		}
	case commandOverseerMessageWorker, commandOverseerInterruptWorker:
		if command.operationID == "" || command.id == "" || command.run == "" || command.taskRevision == 0 || command.runRevision == 0 || command.kind == commandOverseerMessageWorker && command.text == "" {
			return attemptCommand{}, false, false
		}
	case commandOverseerReplyHuman:
		if command.operationID == "" || command.id == "" || command.expectedRevision == 0 || command.text == "" {
			return attemptCommand{}, false, false
		}
	}
	return command, false, true
}

func validOperatorText(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validBrowserClientID(value string) bool { return validHumanRequestKey(value) }

func parseRevision(value string) (uint64, bool) {
	if value == "" || value[0] == '0' || len(value) > 19 {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil && parsed > 0 && parsed <= uint64(^uint64(0)>>1)
}

func parseOffset(value string) (uint64, bool) {
	if value == "0" {
		return 0, true
	}
	return parseRevision(value)
}

func parseOverseerOffset(value string, allowZero bool) (uint64, bool) {
	if value == "" || len(value) > 19 || value[0] == '0' && (len(value) > 1 || !allowZero) {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil && parsed <= uint64(^uint64(0)>>1)
}

func helpFlag(value string) bool { return value == "-h" || value == "--help" }

func validHumanRequestKey(value string) bool {
	if len(value) != 32 {
		return false
	}
	nonzero := false
	for _, value := range []byte(value) {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
		nonzero = nonzero || value != '0'
	}
	return nonzero
}

func validQuestion(value string) bool {
	return len(value) >= 1 && len(value) <= 8192 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validPeerText(value string) bool {
	return len(value) >= 1 && len(value) <= 2048 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validRevision(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && parsed <= uint64(^uint64(0)>>1)
}

func writeFailure(stderr io.Writer, kind commandKind, err error) {
	subject, input := "outcome request", "attempt input"
	if kind == commandAttemptTask {
		subject = "task request"
	} else if kind == commandAttemptSource {
		subject, input = "source request", "source request input"
	} else if kind == commandTerminalObserve || kind == commandOperatorTerminalObserve {
		subject, input = "terminal observation", "terminal observation input"
	} else if kind == commandRequestHuman {
		subject, input = "human request", "human request input"
	} else if kind == commandPeerStatus || kind == commandPeerAsk || kind == commandPeerAnswer {
		subject, input = "peer collaboration", "peer collaboration input"
	} else if kind == commandSendBack {
		subject, input = "send-back", "send-back input"
	}
	message := "factoryctl: " + subject + " failed\n"
	var remote *api.RemoteError
	switch {
	case errors.Is(err, api.ErrInvalidClient):
		message = "factoryctl: attempt client configuration is invalid\n"
	case errors.Is(err, api.ErrInvalidInput):
		message = "factoryctl: " + input + " is invalid\n"
	case errors.Is(err, context.Canceled):
		message = "factoryctl: " + subject + " canceled\n"
	case errors.Is(err, context.DeadlineExceeded):
		message = "factoryctl: " + subject + " timed out\n"
	case errors.As(err, &remote):
		message = "factoryctl: " + subject + ": " + remote.Error() + "\n"
	}
	_, _ = io.WriteString(stderr, message)
}

// newOperatorID mints one fresh lowercase 16-byte hex identity client-side,
// exactly the shape the operator API validates. Identities are never derived
// from user text.
func newOperatorID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(value)
	if !validHumanRequestKey(encoded) {
		return "", errors.New("factoryctl: minted identity is invalid")
	}
	return encoded, nil
}

func runOperator(ctx context.Context, command attemptCommand, getenv func(string) string, stdout, stderr io.Writer) int {
	socket := getenv("DARK_FACTORY_SOCKET")
	token := getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE")
	if socket == "" || token == "" {
		_, _ = io.WriteString(stderr, "factoryctl: operator client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewOperatorClient(socket, token)
	if err != nil {
		_, _ = io.WriteString(stderr, "factoryctl: operator client configuration is invalid\n")
		return exitFailure
	}
	timeout := attemptRequestTimeout
	if command.kind == commandIntake {
		timeout = 120 * time.Second
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if command.kind >= commandContentCreate && command.kind <= commandContentAttachments {
		return runContent(callContext, client, command, stdout, stderr)
	}
	if command.kind >= commandOutcomeWrite && command.kind <= commandOutcomeList {
		return runOutcome(callContext, client, command, stdout, stderr)
	}
	switch command.kind {
	case commandIntake:
		result, callErr := client.Intake(callContext, command.intake)
		if callErr != nil {
			return writeWebFailure(stderr, "intake", callErr)
		}
		return writeJSON(stdout, result)
	case commandStatus:
		snapshot, callErr := client.Snapshot(callContext)
		if callErr != nil {
			return writeWebFailure(stderr, "status", callErr)
		}
		return writeJSON(stdout, snapshot)
	case commandOperatorTerminalObserve:
		result, callErr := client.TerminalObserve(callContext, api.TerminalObserveInput{ProjectID: command.project, TaskID: command.id, RunID: command.run, Cursor: command.offset, MaxBytes: command.maxBytes, Text: command.terminalText})
		if callErr != nil {
			writeFailure(stderr, command.kind, callErr)
			return exitFailure
		}
		encoded, err := result.MarshalDisplayJSON()
		if err != nil {
			writeFailure(stderr, command.kind, err)
			return exitFailure
		}
		return writeJSON(stdout, json.RawMessage(encoded))
	case commandHumanList:
		result, callErr := client.HumanRequests(callContext)
		if callErr != nil {
			return writeWebFailure(stderr, "human requests", callErr)
		}
		return writeJSON(stdout, result)
	case commandWorkerOperation:
		result, callErr := client.WorkerOperation(callContext, command.operationID)
		if callErr != nil {
			return writeWebFailure(stderr, "worker operation", callErr)
		}
		return writeJSON(stdout, result)
	case commandHumanReply:
		result, callErr := client.HumanReply(callContext, api.OverseerHumanReplyInput{OperationID: command.operationID, RequestID: command.id, ExpectedRevision: command.expectedRevision, Reply: command.text})
		if callErr != nil {
			return writeWebFailure(stderr, "human reply", callErr)
		}
		return writeJSON(stdout, result)
	case commandAccountsDiscover:
		accounts, callErr := client.DiscoverAccounts(callContext)
		if callErr != nil {
			return writeWebFailure(stderr, "account discover", callErr)
		}
		return writeJSON(stdout, accounts)
	case commandAccountsList:
		accounts, callErr := client.DiscoverAccounts(callContext)
		if callErr != nil {
			return writeWebFailure(stderr, "account list", callErr)
		}
		snapshot, callErr := client.Snapshot(callContext)
		if callErr != nil {
			return writeWebFailure(stderr, "account list", callErr)
		}
		type item struct {
			api.DiscoveredAccount
			State      string   `json:"state"`
			Reason     string   `json:"reason"`
			SelectedBy []string `json:"selected_by,omitempty"`
		}
		result := make([]item, 0, len(accounts.Accounts))
		for _, account := range accounts.Accounts {
			entry := item{DiscoveredAccount: account, State: "available", Reason: "not linked"}
			if account.UnavailableReason != "" {
				entry.State, entry.Reason = "unavailable", account.UnavailableReason
			}
			if account.LinkedID != "" {
				if account.UnavailableReason == "" {
					entry.State, entry.Reason = "linked", "linked but no idle worker selects it"
				}
				for _, agent := range snapshot.Agents {
					if agent.AccountID == account.LinkedID {
						entry.SelectedBy = append(entry.SelectedBy, agent.ID)
					}
				}
				if account.UnavailableReason == "" && len(entry.SelectedBy) != 0 {
					entry.State, entry.Reason = "selected", "selected by existing worker"
				}
			}
			result = append(result, entry)
		}
		return writeJSON(stdout, result)
	case commandAccountLink:
		result, callErr := client.LinkAccount(callContext, api.AccountLinkInput{Provider: command.provider, Home: command.root, Label: command.label})
		if callErr != nil {
			return writeWebFailure(stderr, "account link", callErr)
		}
		return writeJSON(stdout, result)
	case commandAgentSelectAccount:
		result, callErr := client.SelectAgentAccount(callContext, api.AgentAccountSelectInput{AgentID: command.agent, ExpectedRevision: command.expectedRevision, AccountID: command.account})
		if callErr != nil {
			return writeWebFailure(stderr, "agent select-account", callErr)
		}
		return writeJSON(stdout, result)
	case commandAgentSelectModel:
		result, callErr := client.SelectAgentModel(callContext, api.AgentModelSelectInput{AgentID: command.agent, ExpectedRevision: command.expectedRevision, Model: command.model, ReasoningEffort: command.reasoningEffort})
		if callErr != nil {
			return writeWebFailure(stderr, "agent select-model", callErr)
		}
		return writeJSON(stdout, result)
	case commandAgentPaths:
		result, callErr := client.AgentPaths(callContext, api.AgentPathsInput{AgentID: command.agent})
		if callErr != nil {
			return writeWebFailure(stderr, "agent paths", callErr)
		}
		return writeJSON(stdout, result)
	case commandProjectCreate:
		id, err := newOperatorID()
		if err != nil {
			return writeWebFailure(stderr, "project create", err)
		}
		result, callErr := client.CreateProject(callContext, api.CreateProjectInput{ID: id, Name: command.name, Root: command.root})
		if callErr != nil {
			return writeWebFailure(stderr, "project create", callErr)
		}
		return writeJSON(stdout, struct {
			ID       string `json:"id"`
			Head     uint64 `json:"head"`
			Revision uint64 `json:"revision"`
		}{ID: id, Head: result.Head, Revision: result.Revision})
	case commandProjectLimits:
		result, callErr := client.SetProjectLimits(callContext, api.ProjectLimitsInput{ProjectID: command.project, ExpectedRevision: command.expectedRevision, RunBudget: command.toolBudget, MaxRunSeconds: command.maxRunSeconds})
		if callErr != nil {
			return writeWebFailure(stderr, "project limits", callErr)
		}
		return writeJSON(stdout, result)
	case commandProjectRepository:
		if command.provider == "list" {
			result, err := client.ProjectRepositories(callContext, command.project)
			if err != nil {
				return writeWebFailure(stderr, "project repository list", err)
			}
			return writeJSON(stdout, result)
		}
		if command.provider == "remove" {
			if err := client.RemoveProjectRepository(callContext, command.repository, command.expectedRevision); err != nil {
				return writeWebFailure(stderr, "project repository remove", err)
			}
			return writeJSON(stdout, struct{}{})
		}
		repositoryID := command.repository
		if command.provider == "add" && repositoryID == "" {
			var mintErr error
			repositoryID, mintErr = newOperatorID()
			if mintErr != nil {
				return writeWebFailure(stderr, "project repository add", mintErr)
			}
		}
		input := api.ProjectRepositoryInput{Action: command.provider, ID: repositoryID, ProjectID: command.project, Name: command.name, Root: command.root, BaseRef: command.body, ExpectedRevision: command.expectedRevision}
		if command.provider == "enable" {
			input.Action = "enabled"
			value := true
			input.Enabled = &value
		}
		if command.provider == "disable" {
			input.Action = "enabled"
			value := false
			input.Enabled = &value
		}
		result, err := client.ProjectRepository(callContext, input)
		if err != nil {
			return writeWebFailure(stderr, "project repository", err)
		}
		return writeJSON(stdout, result)
	case commandAgentCreate:
		id, err := newOperatorID()
		if err != nil {
			return writeWebFailure(stderr, "agent create", err)
		}
		result, callErr := client.CreateAgent(callContext, api.CreateAgentInput{
			ID: id, ProjectID: command.project, Name: command.name, Role: command.role,
			Provider: command.provider, Model: command.model, ReasoningEffort: command.reasoningEffort,
			AccountID: command.account, ToolBudgetLimit: command.toolBudget,
		})
		if callErr != nil {
			return writeWebFailure(stderr, "agent create", callErr)
		}
		return writeJSON(stdout, struct {
			ID       string `json:"id"`
			Head     uint64 `json:"head"`
			Revision uint64 `json:"revision"`
		}{ID: id, Head: result.Head, Revision: result.Revision})
	case commandAgentIdlePolicy:
		result, callErr := client.SetAgentIdlePolicy(callContext, api.AgentIdlePolicyInput{AgentID: command.agent, ExpectedRevision: command.expectedRevision, Policy: command.provider, AfterSeconds: command.maxRunSeconds, Instruction: command.text, RunBudget: command.toolBudget})
		if callErr != nil {
			return writeWebFailure(stderr, "agent idle-policy", callErr)
		}
		return writeJSON(stdout, result)
	case commandTaskAdd:
		id, incarnation := command.id, command.run
		if id == "" {
			var err error
			id, err = newOperatorID()
			if err != nil {
				return writeWebFailure(stderr, "task add", err)
			}
			incarnation, err = newOperatorID()
			if err != nil {
				return writeWebFailure(stderr, "task add", err)
			}
		}
		result, callErr := client.EnqueueTask(callContext, api.EnqueueTaskInput{ID: id, ProjectID: command.project, RepositoryID: command.repository, AssignedAgentID: anyWorkerAgent(command.agent), IncarnationID: incarnation, Title: command.title, Body: command.body, Priority: command.priority})
		if callErr != nil {
			return writeWebFailure(stderr, "task add", callErr)
		}
		return writeJSON(stdout, struct {
			ID            string `json:"id"`
			IncarnationID string `json:"incarnation_id"`
			Head          uint64 `json:"head"`
			Revision      uint64 `json:"revision"`
		}{ID: id, IncarnationID: incarnation, Head: result.Head, Revision: result.Revision})
	case commandTaskSendBack:
		result, callErr := client.SendBackTask(callContext, api.SendBackInput{TaskID: command.id, Note: command.text})
		if callErr != nil {
			return writeWebFailure(stderr, "task send-back", callErr)
		}
		return writeJSON(stdout, struct {
			ID       string `json:"id"`
			Head     uint64 `json:"head"`
			Revision uint64 `json:"revision"`
		}{ID: command.id, Head: result.Head, Revision: result.Revision})
	case commandTaskRecovery:
		recovery, callErr := client.TaskRecovery(callContext, api.TaskRecoveryInput{TaskID: command.id, IncarnationID: command.run})
		if callErr != nil {
			return writeWebFailure(stderr, "task recovery", callErr)
		}
		return writeJSON(stdout, recovery)
	case commandTaskRead:
		result, callErr := client.ReadTask(callContext, api.TaskReadInput{TaskID: command.id, ExpectedRevision: command.expectedRevision, Offset: command.offset})
		if callErr != nil {
			return writeWebFailure(stderr, "task read", callErr)
		}
		return writeJSON(stdout, result)
	case commandOverseerTaskUpdate:
		input := api.OverseerTaskUpdateInput{TaskID: command.id, ExpectedRevision: command.expectedRevision, Cancel: command.cancel, Retry: command.retry}
		if command.title != "" {
			input.Title = &command.title
		}
		if command.bodySet {
			input.Body = &command.body
		}
		if command.prioritySet {
			input.Priority = &command.priority
		}
		if command.agent != "" {
			input.AssignedAgentID = &command.agent
		}
		result, callErr := client.UpdateTask(callContext, input)
		if callErr != nil {
			return writeWebFailure(stderr, "task update", callErr)
		}
		return writeJSON(stdout, result)
	case commandOverseerAgentUpdate:
		input := api.OverseerAgentUpdateInput{AgentID: command.agent, ExpectedRevision: command.expectedRevision}
		if command.archiveSet {
			input.Archived = &command.archived
		} else {
			input.Paused = &command.paused
		}
		result, callErr := client.UpdateAgent(callContext, input)
		if callErr != nil {
			return writeWebFailure(stderr, "agent update", callErr)
		}
		return writeJSON(stdout, result)
	case commandOverseerStopWorker:
		result, callErr := client.StopRun(callContext, api.OverseerRunStopInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision})
		if callErr != nil {
			return writeWebFailure(stderr, "worker stop", callErr)
		}
		return writeJSON(stdout, result)
	case commandOverseerReplaceWorker:
		result, callErr := client.ReplaceRun(callContext, api.OverseerRunReplaceInput{OverseerRunStopInput: api.OverseerRunStopInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision}, SuccessorTaskID: command.project, SuccessorIncarnationID: command.account, Instruction: command.text})
		if callErr != nil {
			return writeWebFailure(stderr, "worker replace", callErr)
		}
		return writeJSON(stdout, result)
	case commandOverseerMessageWorker:
		result, callErr := client.MessageWorker(callContext, api.OverseerWorkerMessageInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision, Message: command.text})
		if callErr != nil {
			return writeWebFailure(stderr, "worker message", callErr)
		}
		return writeJSON(stdout, result)
	case commandOverseerInterruptWorker:
		result, callErr := client.InterruptWorker(callContext, api.OverseerWorkerInterruptInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision})
		if callErr != nil {
			return writeWebFailure(stderr, "worker interrupt", callErr)
		}
		return writeJSON(stdout, result)
	case commandDispatch:
		revision := command.expectedRevision
		if revision == 0 {
			snapshot, callErr := client.Snapshot(callContext)
			if callErr != nil {
				return writeWebFailure(stderr, "dispatch", callErr)
			}
			revision = snapshot.Factory.Revision
		}
		result, callErr := client.SetDispatch(callContext, revision, command.enabled)
		if callErr != nil {
			return writeWebFailure(stderr, "dispatch", callErr)
		}
		return writeJSON(stdout, struct {
			Enabled  bool   `json:"enabled"`
			Head     uint64 `json:"head"`
			Revision uint64 `json:"revision"`
		}{Enabled: command.enabled, Head: result.Head, Revision: result.Revision})
	case commandCapacity:
		result, callErr := client.SetCapacity(callContext, command.expectedRevision, command.capacity)
		if callErr != nil {
			return writeWebFailure(stderr, "capacity", callErr)
		}
		return writeJSON(stdout, struct {
			Workers      uint64 `json:"workers"`
			OverseerLane uint64 `json:"overseer_lane"`
			Head         uint64 `json:"head"`
			Revision     uint64 `json:"revision"`
		}{Workers: uint64(command.capacity), OverseerLane: 1, Head: result.Head, Revision: result.Revision})
	default:
		return exitUsage
	}
}

func runOverseer(ctx context.Context, command attemptCommand, getenv func(string) string, stdout, stderr io.Writer) int {
	socket := getenv("DARK_FACTORY_SOCKET")
	if socket == "" {
		_, _ = io.WriteString(stderr, "factoryctl: overseer client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewAttemptClientFromEnvironment(socket)
	if err != nil {
		_, _ = io.WriteString(stderr, "factoryctl: overseer client configuration is invalid\n")
		return exitFailure
	}
	callContext, cancel := context.WithTimeout(ctx, serviceRequestTimeout)
	defer cancel()
	if command.kind == commandOverseerStatus {
		result, callErr := client.OverseerSnapshotPage(callContext, api.OverseerSnapshotInput{TaskID: command.id, Offset: command.offset, ExpectedHead: command.head, TextOffset: command.textOffset})
		if callErr != nil {
			return writeWebFailure(stderr, "overseer status", callErr)
		}
		return writeJSON(stdout, result)
	}
	var result api.MutationResult
	switch command.kind {
	case commandOverseerTaskAdd:
		id, incarnation := command.id, command.project
		if id == "" {
			var idErr error
			id, idErr = newOperatorID()
			if idErr != nil {
				return writeWebFailure(stderr, "overseer task add", idErr)
			}
			incarnation, idErr = newOperatorID()
			if idErr != nil {
				return writeWebFailure(stderr, "overseer task add", idErr)
			}
		}
		result, err = client.OverseerEnqueueTask(callContext, api.OverseerTaskCreateInput{ID: id, AssignedAgentID: anyWorkerAgent(command.agent), IncarnationID: incarnation, Title: command.title, Body: command.body, Priority: command.priority, Prerequisites: command.prerequisites, ConflictPaths: command.conflictPaths})
	case commandOverseerTaskUpdate:
		input := api.OverseerTaskUpdateInput{TaskID: command.id, ExpectedRevision: command.expectedRevision, Cancel: command.cancel, Retry: command.retry}
		if command.title != "" {
			input.Title = &command.title
		}
		if command.bodySet {
			input.Body = &command.body
		}
		if command.prioritySet {
			priority := command.priority
			input.Priority = &priority
		}
		if command.agent != "" {
			agent := command.agent
			input.AssignedAgentID = &agent
		}
		result, err = client.OverseerUpdateTask(callContext, input)
	case commandOverseerTaskSendBack:
		result, err = client.SendBack(callContext, api.SendBackInput{TaskID: command.id, Note: command.text})
	case commandOverseerAgentUpdate:
		input := api.OverseerAgentUpdateInput{AgentID: command.agent, ExpectedRevision: command.expectedRevision}
		if command.archiveSet {
			input.Archived = &command.archived
		} else {
			input.Paused = &command.paused
		}
		result, err = client.OverseerUpdateAgent(callContext, input)
	case commandOverseerStopWorker:
		result, err = client.OverseerStopRun(callContext, api.OverseerRunStopInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision})
	case commandOverseerReplaceWorker:
		result, err = client.OverseerReplaceRun(callContext, api.OverseerRunReplaceInput{OverseerRunStopInput: api.OverseerRunStopInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision}, SuccessorTaskID: command.project, SuccessorIncarnationID: command.account, Instruction: command.text})
	case commandOverseerMessageWorker:
		result, err = client.OverseerMessageWorker(callContext, api.OverseerWorkerMessageInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision, Message: command.text})
	case commandOverseerInterruptWorker:
		result, err = client.OverseerInterruptWorker(callContext, api.OverseerWorkerInterruptInput{OperationID: command.operationID, TaskID: command.id, ExpectedTaskRevision: command.taskRevision, RunID: command.run, ExpectedRunRevision: command.runRevision})
	case commandOverseerReplyHuman:
		result, err = client.OverseerReplyHuman(callContext, api.OverseerHumanReplyInput{OperationID: command.operationID, RequestID: command.id, ExpectedRevision: command.expectedRevision, Reply: command.text})
	default:
		return exitUsage
	}
	if err != nil {
		return writeWebFailure(stderr, "overseer control", err)
	}
	return writeJSON(stdout, result)
}

func runWeb(ctx context.Context, command attemptCommand, getenv func(string) string, stdout, stderr io.Writer) int {
	socket := getenv("DARK_FACTORY_SOCKET")
	if socket == "" {
		_, _ = io.WriteString(stderr, "factoryctl: web client configuration is invalid\n")
		return exitFailure
	}
	if command.kind == commandWebStatus {
		if !socketDaemonPresent(socket) {
			return writeJSON(stdout, api.WebStatus{State: "stopped", Ready: false})
		}
	}
	token := getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE")
	if token == "" {
		_, _ = io.WriteString(stderr, "factoryctl: web client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewOperatorClient(socket, token)
	if err != nil {
		if command.kind == commandWebStatus && errors.Is(err, api.ErrInvalidClient) {
			return writeJSON(stdout, api.WebStatus{State: "stopped", Ready: false})
		}
		_, _ = io.WriteString(stderr, "factoryctl: web client configuration is invalid\n")
		return exitFailure
	}
	callContext, cancel := context.WithTimeout(ctx, attemptRequestTimeout)
	defer cancel()
	switch command.kind {
	case commandWebStatus:
		result, callErr := client.WebStatus(callContext)
		if callErr != nil {
			if errors.Is(callErr, api.ErrInvalidClient) || errors.Is(callErr, api.ErrTransport) {
				return writeJSON(stdout, api.WebStatus{State: "stopped", Ready: false})
			}
			return writeWebFailure(stderr, "web status", callErr)
		}
		return writeJSON(stdout, result)
	case commandWebListClients:
		result, callErr := client.WebListClients(callContext, command.after)
		if callErr != nil {
			return writeWebFailure(stderr, "web list clients", callErr)
		}
		return writeJSON(stdout, result)
	case commandWebRevoke:
		result, callErr := client.WebRevokeClient(callContext, command.id, command.expectedRevision)
		if callErr != nil {
			return writeWebFailure(stderr, "web revoke", callErr)
		}
		return writeJSON(stdout, result)
	default:
		return exitUsage
	}
}

// socketDaemonPresent distinguishes a missing or stale pathname from a
// daemon that can actually accept a local connection. The probe sends no
// request and is used only by the read-only status command; all other web
// commands still require the authenticated API client.
func socketDaemonPresent(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func writeJSON(stdout io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		return exitFailure
	}
	return 0
}

func writeWebFailure(stderr io.Writer, subject string, err error) int {
	message := "factoryctl: " + subject + " failed\n"
	var remote *api.RemoteError
	switch {
	case errors.Is(err, context.Canceled):
		message = "factoryctl: " + subject + " canceled\n"
	case errors.Is(err, context.DeadlineExceeded):
		message = "factoryctl: " + subject + " timed out\n"
	case errors.As(err, &remote):
		message = "factoryctl: " + subject + ": " + remote.Error() + "\n"
	}
	_, _ = io.WriteString(stderr, message)
	return exitFailure
}

func validReasoningEffort(value string) bool {
	switch value {
	case "", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}
