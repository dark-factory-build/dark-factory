package browser

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

const (
	backendCallLimit       = 3 * time.Second
	subscriptionCloseLimit = time.Second
)

var (
	errBackendResult             = errors.New("browser: invalid backend result")
	errInvalidTerminalAttachment = errors.New("browser: invalid terminal attachment")
)

type incoming struct {
	kind websocket.MessageType
	data []byte
	err  error
}

type connection struct {
	server *Server
	ws     *websocket.Conn
	host   string
	origin string
	ctx    context.Context
	cancel context.CancelFunc
	frames chan incoming
	done   chan struct{}

	stopOnce   sync.Once
	cleanupErr error

	authenticated        bool
	principal            Principal
	pairing              bool
	authenticating       bool
	authenticatingID     [browserprotocol.ClientIDSize]byte
	seen                 map[string]struct{}
	subscription         StateSubscription
	updates              <-chan StateUpdate
	subscriptionID       string
	subscriptionSequence browserprotocol.Decimal
	subscriptionHead     browserprotocol.Decimal
	subscriptionHeadSet  bool

	attachment        TerminalAttachment
	terminalEvents    <-chan TerminalEvent
	terminalAttachID  string
	terminalAttach    browserprotocol.TerminalAttach
	terminalAnnounced bool
	terminalAck       uint64
	terminalSent      uint64
	terminalPending   *TerminalEvent
	terminalAckTimer  *time.Timer
}

func (current *connection) stop() {
	if current == nil {
		return
	}
	current.stopOnce.Do(func() {
		current.cancel()
		_ = current.ws.CloseNow()
	})
}

func (current *connection) run() {
	readerDone := make(chan struct{})
	readerStarted := false
	defer func() {
		if err := current.closeTerminal(); err != nil {
			current.recordCleanup(err)
		}
		if err := current.closeSubscription(); err != nil {
			current.recordCleanup(err)
		}
		current.stop()
		if readerStarted {
			<-readerDone
		}
		current.server.unregister(current)
		close(current.done)
	}()

	identity, err := current.server.backend.Identity(current.ctx)
	if err != nil || !nonzero(identity.DaemonID[:]) || !nonzero(identity.BootID[:]) {
		return
	}
	nonce, err := randomNonce()
	if err != nil {
		return
	}
	hello, err := browserprotocol.EncodeHello(browserprotocol.Hello{
		DaemonID:        hex.EncodeToString(identity.DaemonID[:]),
		BootID:          hex.EncodeToString(identity.BootID[:]),
		ConnectionNonce: hex.EncodeToString(nonce[:]),
	})
	if err != nil || current.write(hello) != nil {
		return
	}
	readerStarted = true
	go current.read(readerDone)
	if !current.authenticate(identity, nonce) {
		return
	}
	current.serve()
}

func (current *connection) read(done chan<- struct{}) {
	defer close(done)
	for {
		kind, data, err := current.ws.Read(current.ctx)
		message := incoming{kind: kind, data: data, err: err}
		select {
		case current.frames <- message:
		case <-current.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (current *connection) authenticate(identity Identity, nonce [browserprotocol.NonceSize]byte) bool {
	authContext, cancel := context.WithTimeout(current.ctx, authenticationLimit)
	defer cancel()
	select {
	case <-authContext.Done():
		current.sendError("", browserprotocol.ErrorUnauthorized, false)
		return false
	case <-current.ctx.Done():
		return false
	case message := <-current.frames:
		if message.err != nil || message.kind != websocket.MessageText {
			current.sendError("", browserprotocol.ErrorInvalidRequest, false)
			return false
		}
		frame, err := browserprotocol.DecodeClientControl(message.data)
		if err != nil {
			code := browserprotocol.ErrorInvalidRequest
			if unsupportedProtocolVersion(message.data) {
				code = browserprotocol.ErrorUnsupportedVersion
			}
			current.sendError("", code, false)
			return false
		}
		if frame.Type != browserprotocol.TypePairProve && frame.Type != browserprotocol.TypeAuthProve {
			current.sendError("", browserprotocol.ErrorUnauthorized, false)
			return false
		}
		current.seen = map[string]struct{}{frame.ID: {}}
		result, requested, tracked, err := current.prove(authContext, identity, nonce, frame)
		accept := err == nil && authContext.Err() == nil && validateAuthentication(result) == nil
		if accept {
			result.Principal.ConnectionID, err = newConnectionID()
			accept = err == nil
		}
		registered := false
		if requested != nil {
			if tracked {
				registered = current.server.finishAuthentication(current, *requested, result, accept)
			}
		} else if tracked {
			registered = current.server.registerPair(current, result, accept)
		}
		if !registered {
			current.sendError(frame.ID, browserprotocol.ErrorUnauthorized, false)
			return false
		}
		var response []byte
		if frame.Type == browserprotocol.TypePairProve {
			response, err = browserprotocol.EncodePairResult(frame.ID, browserprotocol.PairResult{
				ClientID: hex.EncodeToString(result.Principal.ClientID[:]), Capabilities: result.Capabilities,
			})
		} else {
			response, err = browserprotocol.EncodeAuthResult(frame.ID, browserprotocol.AuthResult{
				ClientID: hex.EncodeToString(result.Principal.ClientID[:]), Capabilities: result.Capabilities,
			})
		}
		return err == nil && current.write(response) == nil
	}
}

func (current *connection) prove(ctx context.Context, identity Identity, nonce [browserprotocol.NonceSize]byte, frame browserprotocol.ControlFrame) (Authentication, *[browserprotocol.ClientIDSize]byte, bool, error) {
	switch body := frame.Body.(type) {
	case browserprotocol.PairProve:
		challenge, err := fixedBytes(body.Challenge, browserprotocol.ChallengeSize)
		if err != nil {
			return Authentication{}, nil, false, err
		}
		publicKey, err := fixedBytes(body.PublicKeySEC1, browserprotocol.PublicKeySize)
		if err != nil {
			return Authentication{}, nil, false, err
		}
		signature, err := fixedBytes(body.Signature, browserprotocol.SignatureSize)
		if err != nil {
			return Authentication{}, nil, false, err
		}
		var request PairRequest
		request.Identity = identity
		request.ConnectionNonce = nonce
		copy(request.Challenge[:], challenge)
		copy(request.PublicKeySEC1[:], publicKey)
		copy(request.Signature[:], signature)
		request.Host, request.Origin = current.host, current.origin
		if !current.server.beginPairing(current) {
			return Authentication{}, nil, false, ErrUnauthorized
		}
		result, err := current.server.backend.Pair(ctx, request)
		return result, nil, true, err
	case browserprotocol.AuthProve:
		clientID, err := fixedBytes(body.ClientID, browserprotocol.ClientIDSize)
		if err != nil {
			return Authentication{}, nil, false, err
		}
		signature, err := fixedBytes(body.Signature, browserprotocol.SignatureSize)
		if err != nil {
			return Authentication{}, nil, false, err
		}
		var request AuthRequest
		request.Identity = identity
		request.ConnectionNonce = nonce
		copy(request.ClientID[:], clientID)
		copy(request.Signature[:], signature)
		request.Host, request.Origin = current.host, current.origin
		if !current.server.beginAuthentication(current, request.ClientID) {
			return Authentication{}, &request.ClientID, false, ErrUnauthorized
		}
		result, err := current.server.backend.Authenticate(ctx, request)
		return result, &request.ClientID, true, err
	default:
		return Authentication{}, nil, false, ErrUnauthorized
	}
}

func (current *connection) serve() {
	for {
		var updates <-chan StateUpdate
		if current.subscription != nil {
			updates = current.updates
		}
		terminalEvents := current.terminalEvents
		var ackTimeout <-chan time.Time
		if current.terminalAckTimer != nil {
			ackTimeout = current.terminalAckTimer.C
		}
		select {
		case <-current.ctx.Done():
			return
		case update, ok := <-updates:
			subscriptionID := current.subscriptionID
			if !ok || current.sendUpdate(update) != nil {
				current.sendError(subscriptionID, browserprotocol.ErrorInternal, false)
				return
			}
		case <-ackTimeout:
			return
		case event, ok := <-terminalEvents:
			if !ok {
				if err := current.closeTerminal(); err != nil {
					current.recordCleanup(err)
					return
				}
				continue
			}
			if !current.sendTerminalEvent(event) {
				return
			}
		case message := <-current.frames:
			if message.err != nil {
				return
			}
			if message.kind == websocket.MessageBinary {
				if !current.dispatchBinary(message.data) {
					return
				}
				continue
			}
			if message.kind != websocket.MessageText {
				current.sendError("", browserprotocol.ErrorInvalidRequest, false)
				return
			}
			frame, err := browserprotocol.DecodeClientControl(message.data)
			if err != nil {
				code := browserprotocol.ErrorInvalidRequest
				if unsupportedProtocolVersion(message.data) {
					code = browserprotocol.ErrorUnsupportedVersion
				}
				current.sendError("", code, false)
				return
			}
			if frame.Type == browserprotocol.TypePairProve || frame.Type == browserprotocol.TypeAuthProve || frame.Type == browserprotocol.TypeError {
				current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
				return
			}
			if frame.Type == browserprotocol.TypeTerminalAck {
				if !current.handleTerminalAck(frame.Body.(browserprotocol.TerminalAck)) {
					return
				}
				continue
			}
			if _, duplicate := current.seen[frame.ID]; duplicate {
				current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
				return
			}
			if len(current.seen) >= maxRequests {
				current.sendError(frame.ID, browserprotocol.ErrorRateLimited, true)
				return
			}
			current.seen[frame.ID] = struct{}{}
			if !current.dispatch(frame) {
				return
			}
		}
	}
}

func (current *connection) dispatch(frame browserprotocol.ControlFrame) bool {
	ctx, cancel := context.WithTimeout(current.ctx, backendCallLimit)
	defer cancel()
	var payload []byte
	var err error
	switch body := frame.Body.(type) {
	case browserprotocol.StateGet:
		var cursor *Cursor
		if body.Cursor != nil {
			decoded, decodeErr := decodeCursor(*body.Cursor)
			if decodeErr != nil {
				current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
				return false
			}
			cursor = &decoded
		}
		page, pageErr := current.server.backend.StatePage(ctx, current.principal.ClientID, cursor)
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		var restart *RestartError
		if errors.As(pageErr, &restart) {
			payload, err = browserprotocol.EncodeStateRestart(frame.ID, restart.State)
			break
		}
		if pageErr != nil {
			err = pageErr
			break
		}
		if correlationErr := validatePageCorrelation(page, cursor); correlationErr != nil {
			err = correlationErr
			break
		}
		var next *string
		if page.NextCursor != nil {
			encoded, encodeErr := encodeCursor(*page.NextCursor)
			if encodeErr != nil {
				err = encodeErr
				break
			}
			next = &encoded
		}
		payload, err = browserprotocol.EncodeStateSnapshot(frame.ID, browserprotocol.StateSnapshot{
			Head: page.Head, Kind: page.Kind, Items: page.Items, NextCursor: next,
		})
	case browserprotocol.StateEntityGet:
		entity, backendErr := current.server.backend.StateEntity(ctx, current.principal.ClientID, body)
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		if backendErr != nil {
			err = backendErr
			break
		}
		if entity.Kind != body.Kind || entity.ID != body.ID {
			err = fmt.Errorf("backend returned mismatched state entity")
			break
		}
		payload, err = browserprotocol.EncodeStateEntity(frame.ID, entity)
	case browserprotocol.HumanRequestDetailGet:
		detail, backendErr := current.server.backend.HumanRequestDetail(ctx, current.principal.ClientID, body)
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		if backendErr != nil {
			err = backendErr
			break
		}
		if detail.RequestID != body.RequestID || detail.Revision != body.ExpectedRevision {
			err = fmt.Errorf("backend returned mismatched human request detail")
			break
		}
		payload, err = browserprotocol.EncodeHumanRequestDetail(frame.ID, detail)
	case browserprotocol.TerminalTargetGet:
		target, backendErr := current.server.backend.TerminalTarget(ctx, current.principal.ClientID, body)
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		if backendErr != nil {
			err = backendErr
			break
		}
		if correlationErr := validateTerminalTargetCorrelation(body, target); correlationErr != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeTerminalTarget(frame.ID, target)
		if err != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
	case browserprotocol.TaskEnqueue:
		if current.server.taskBackend == nil {
			err = ErrUnauthorized
			break
		}
		result, backendErr := current.server.taskBackend.EnqueueTask(ctx, current.principal.ClientID, body)
		if backendErr != nil {
			err = backendErr
			break
		}
		if result.TaskID != body.TaskID || result.AgentRevision != body.ExpectedAgentRevision || result.Revision == 0 {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeTaskEnqueueResult(frame.ID, result)
	case browserprotocol.HumanRequestReply:
		if current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		result, backendErr := current.server.terminalBackend.ReplyHumanRequest(ctx, current.principal, body)
		if backendErr != nil {
			err = backendErr
			break
		}
		if validationErr := validateHumanReplyResult(body, result); validationErr != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeHumanRequestReplyResult(frame.ID, result)
	case browserprotocol.HumanRequestCancelRun:
		if current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		result, backendErr := current.server.terminalBackend.CancelHumanRequestRun(ctx, current.principal, body)
		if backendErr != nil {
			err = backendErr
			break
		}
		if validationErr := validateHumanCancelRunResult(body, result); validationErr != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeHumanRequestCancelRunResult(frame.ID, result)
	case browserprotocol.TerminalAttach:
		if current.attachment != nil || current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		attachment, backendErr := current.server.terminalBackend.AttachTerminal(ctx, TerminalAttachRequest{Principal: current.principal, Request: body})
		if backendErr != nil {
			err = backendErr
			break
		}
		if attachment == nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		events := attachment.Events()
		if events == nil {
			invalidErr := errInvalidTerminalAttachment
			if closeErr := attachment.Close(); closeErr != nil {
				err = errors.Join(invalidErr, closeErr)
				current.recordCleanup(err)
			} else {
				err = invalidErr
			}
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		current.attachment, current.terminalEvents, current.terminalAttachID, current.terminalAttach = attachment, events, frame.ID, body
		current.terminalAck = uint64(body.AfterSequence)
		current.terminalSent = current.terminalAck
		return true
	case browserprotocol.TerminalLeaseAcquire:
		if current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		result, backendErr := current.server.terminalBackend.AcquireTerminalLease(ctx, current.principal, body)
		if backendErr != nil {
			err = backendErr
			break
		}
		if validationErr := validateLeaseResult("acquired", body.RunID, body.SessionID, 0, body.ExpectedRunRevision, body.ExpectedSessionRevision, result); validationErr != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeTerminalLeaseResult(frame.ID, browserprotocol.TerminalLeaseResult(result))
	case browserprotocol.TerminalLeaseRenew:
		if current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		result, backendErr := current.server.terminalBackend.RenewTerminalLease(ctx, current.principal, body)
		if backendErr != nil {
			err = backendErr
			break
		}
		if validationErr := validateLeaseResult("renewed", body.RunID, body.SessionID, body.Generation, body.ExpectedRunRevision, body.ExpectedSessionRevision, result); validationErr != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeTerminalLeaseResult(frame.ID, browserprotocol.TerminalLeaseResult(result))
	case browserprotocol.TerminalLeaseRelease:
		if current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		result, backendErr := current.server.terminalBackend.ReleaseTerminalLease(ctx, current.principal, body)
		if backendErr != nil {
			err = backendErr
			break
		}
		if validationErr := validateLeaseResult("released", body.RunID, body.SessionID, body.Generation, body.ExpectedRunRevision, body.ExpectedSessionRevision, result); validationErr != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInternal, false)
			return false
		}
		payload, err = browserprotocol.EncodeTerminalLeaseResult(frame.ID, browserprotocol.TerminalLeaseResult(result))
	case browserprotocol.TerminalResize:
		if current.server.terminalBackend == nil {
			err = ErrUnauthorized
			break
		}
		err = current.server.terminalBackend.ResizeTerminal(ctx, TerminalResizeRequest{Principal: current.principal, Request: body})
		if err == nil {
			payload, err = browserprotocol.EncodeTerminalResized(frame.ID, browserprotocol.TerminalResized{SessionID: body.SessionID, Generation: body.Generation, Rows: body.Rows, Cols: body.Cols})
		}
	case browserprotocol.TerminalDetach:
		if current.attachment == nil || body.SessionID != current.terminalAttach.SessionID {
			err = ErrStale
			break
		}
		err = current.closeTerminal()
		if err == nil {
			payload, err = browserprotocol.EncodeTerminalDetached(frame.ID, browserprotocol.TerminalDetached{SessionID: body.SessionID})
		}
	case browserprotocol.StateSubscribe:
		if current.subscription != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
			return false
		}
		subscription, backendErr := current.server.backend.SubscribeState(ctx, current.principal.ClientID, body.After)
		if ctx.Err() != nil {
			err = current.discardSubscription(subscription, ctx.Err())
			break
		}
		if backendErr != nil {
			err = current.discardSubscription(subscription, backendErr)
			break
		}
		if subscription == nil {
			err = fmt.Errorf("invalid subscription")
			break
		}
		updates := subscription.Updates()
		if updates == nil {
			err = current.discardSubscription(subscription, fmt.Errorf("invalid subscription"))
			break
		}
		current.subscription = subscription
		current.updates = updates
		current.subscriptionID = frame.ID
		current.subscriptionSequence = body.After
		current.subscriptionHead = 0
		current.subscriptionHeadSet = false
		return true
	default:
		current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
		return false
	}
	if err != nil {
		mapped := errorFrame(err)
		current.sendError(frame.ID, mapped.Code, mapped.Retryable)
		return !errors.Is(err, ErrUnauthorized) && !errors.Is(err, ErrSubscriptionUnresolved)
	}
	if current.write(payload) != nil {
		return false
	}
	return true
}

func (current *connection) sendUpdate(update StateUpdate) error {
	if (update.Event == nil) == (update.Restart == nil) || update.Restart != nil && update.Floor != 0 {
		return fmt.Errorf("invalid state update")
	}
	if update.Event != nil {
		sequence, head, chronologyErr := eventChronology(*update.Event)
		if chronologyErr != nil || sequence == 0 || head < sequence || update.Floor > head || current.stateHeadRegressed(head) {
			return fmt.Errorf("invalid state event chronology")
		}
		if current.subscriptionSequence < update.Floor || current.subscriptionSequence == browserprotocol.Decimal(browserprotocol.MaxSQLiteInteger) || sequence != current.subscriptionSequence+1 {
			reason := browserprotocol.RestartGap
			if current.subscriptionSequence < update.Floor {
				reason = browserprotocol.RestartPruned
			}
			return current.sendRestart(browserprotocol.StateRestart{Head: head, Floor: update.Floor, Reason: reason}, false)
		}
		payload, err := browserprotocol.EncodeStateEvent(current.subscriptionID, *update.Event)
		if err == nil {
			err = current.write(payload)
		}
		if err == nil {
			current.subscriptionSequence = sequence
			current.subscriptionHead = head
			current.subscriptionHeadSet = true
		}
		return err
	}
	return current.sendRestart(*update.Restart, true)
}

func validateHumanReplyResult(request browserprotocol.HumanRequestReply, result browserprotocol.HumanRequestReplyResult) error {
	if result.RequestID != request.RequestID || (result.Status != "resolved" && result.Status != "delivery_unknown") {
		return fmt.Errorf("%w: human reply identity or status", errBackendResult)
	}
	if uint64(request.ExpectedRevision) > browserprotocol.MaxSQLiteInteger-2 || result.Revision != request.ExpectedRevision+2 {
		return fmt.Errorf("%w: human reply revision", errBackendResult)
	}
	return nil
}

func validateHumanCancelRunResult(request browserprotocol.HumanRequestCancelRun, result browserprotocol.HumanRequestCancelRunResult) error {
	if result.RequestID != request.RequestID {
		return fmt.Errorf("%w: human cancellation identity", errBackendResult)
	}
	if uint64(request.ExpectedRunRevision) > browserprotocol.MaxSQLiteInteger-1 || result.RunRevision != request.ExpectedRunRevision+1 {
		return fmt.Errorf("%w: human cancellation run revision", errBackendResult)
	}
	if uint64(request.ExpectedRequestRevision) > browserprotocol.MaxSQLiteInteger-1 || result.RequestRevision != request.ExpectedRequestRevision+1 {
		return fmt.Errorf("%w: human cancellation request revision", errBackendResult)
	}
	return nil
}

func validateTerminalTargetCorrelation(request browserprotocol.TerminalTargetGet, result browserprotocol.TerminalTarget) error {
	if result.AgentID != request.AgentID || result.AgentRevision != request.ExpectedAgentRevision || result.Head != request.ExpectedHead {
		return fmt.Errorf("%w: terminal target identity or observation", errBackendResult)
	}
	return nil
}

func validateLeaseResult(operation, runID, sessionID string, generation, expectedRun, expectedSession browserprotocol.Decimal, result TerminalLeaseResult) error {
	if result.Operation != operation || result.RunID != runID || result.SessionID != sessionID || result.Generation == 0 || uint64(result.Generation) > browserprotocol.MaxSQLiteInteger || uint64(result.LastInputSequence) > browserprotocol.MaxSQLiteInteger || uint64(result.RunRevision) > browserprotocol.MaxSQLiteInteger || uint64(result.SessionRevision) > browserprotocol.MaxSQLiteInteger || result.RunRevision != expectedRun || result.SessionRevision != expectedSession {
		return fmt.Errorf("%w: terminal lease identity or revision", errBackendResult)
	}
	switch operation {
	case "acquired":
		if result.LastInputSequence != 0 {
			return fmt.Errorf("%w: acquired terminal lease sequence", errBackendResult)
		}
	case "renewed":
		if generation == 0 || result.Generation != generation {
			return fmt.Errorf("%w: terminal lease generation", errBackendResult)
		}
	case "released":
		// Store clears the lease and increments its generation in one
		// transaction. The post-release generation is therefore the exact
		// fence for all effects issued under the released generation.
		if generation == 0 || uint64(generation) >= browserprotocol.MaxSQLiteInteger || result.Generation != generation+1 {
			return fmt.Errorf("%w: terminal lease generation", errBackendResult)
		}
		if result.LastInputSequence != 0 {
			return fmt.Errorf("%w: released terminal lease sequence", errBackendResult)
		}
	default:
		return fmt.Errorf("%w: terminal lease operation", errBackendResult)
	}
	if operation == "released" {
		if result.ExpiresAtMS != nil {
			return fmt.Errorf("%w: released terminal lease expiry", errBackendResult)
		}
	} else if result.ExpiresAtMS == nil || *result.ExpiresAtMS == 0 || uint64(*result.ExpiresAtMS) > browserprotocol.MaxSQLiteInteger {
		return fmt.Errorf("%w: active terminal lease expiry", errBackendResult)
	}
	return nil
}

func (current *connection) stateHeadRegressed(next browserprotocol.Decimal) bool {
	return current.subscriptionHeadSet && next < current.subscriptionHead
}

func (current *connection) sendRestart(restart browserprotocol.StateRestart, backendRestart bool) error {
	initialFutureCursor := backendRestart && !current.subscriptionHeadSet && restart.Reason == browserprotocol.RestartGap
	if restart.Head < current.subscriptionSequence && !initialFutureCursor || current.subscriptionHeadSet && restart.Head < current.subscriptionHead {
		return fmt.Errorf("restart chronology regressed")
	}
	payload, err := browserprotocol.EncodeStateRestart(current.subscriptionID, restart)
	if err != nil {
		return err
	}
	if err := current.closeSubscription(); err != nil {
		current.recordCleanup(err)
		return err
	}
	return current.write(payload)
}

func (current *connection) closeSubscription() error {
	if current.subscription == nil {
		return nil
	}
	subscription := current.subscription
	current.subscription = nil
	current.updates = nil
	current.subscriptionID = ""
	current.subscriptionSequence = 0
	current.subscriptionHead = 0
	current.subscriptionHeadSet = false
	return stopSubscription(subscription)
}

func (current *connection) closeTerminal() error {
	if current.attachment == nil {
		return nil
	}
	attachment := current.attachment
	if err := attachment.Close(); err != nil {
		return err
	}
	current.clearTerminal()
	return nil
}

func (current *connection) clearTerminal() {
	if current.terminalAckTimer != nil {
		current.terminalAckTimer.Stop()
		current.terminalAckTimer = nil
	}
	current.attachment = nil
	current.terminalEvents = nil
	current.terminalAttachID = ""
	current.terminalAttach = browserprotocol.TerminalAttach{}
	current.terminalAnnounced = false
	current.terminalPending = nil
	current.terminalAck, current.terminalSent = 0, 0
}

func (current *connection) armTerminalAckTimer() {
	if current.terminalAckTimer == nil {
		current.terminalAckTimer = time.NewTimer(current.server.terminalAckTimeout)
	}
}

func (current *connection) handleTerminalAck(ack browserprotocol.TerminalAck) bool {
	if current.attachment == nil || ack.SessionID != current.terminalAttach.SessionID || uint64(ack.NextSequence) <= current.terminalAck || uint64(ack.NextSequence) > current.terminalSent {
		return false
	}
	current.terminalAck = uint64(ack.NextSequence)
	if current.terminalAck == current.terminalSent && current.terminalAckTimer != nil {
		current.terminalAckTimer.Stop()
		current.terminalAckTimer = nil
	}
	if current.terminalPending != nil && current.terminalPendingReady(*current.terminalPending) {
		pending := *current.terminalPending
		current.terminalPending = nil
		current.terminalEvents = current.attachment.Events()
		return current.sendTerminalEvent(pending)
	}
	if current.terminalPending == nil {
		current.terminalEvents = current.attachment.Events()
	}
	return true
}

func (current *connection) terminalPendingReady(event TerminalEvent) bool {
	switch event.Kind {
	case TerminalEventOutput:
		return event.End >= current.terminalAck && event.End-current.terminalAck <= browserprotocol.MaxTerminalUnackedBytes
	case TerminalEventReset, TerminalEventExit:
		return current.terminalAck == current.terminalSent
	default:
		return false
	}
}

func (current *connection) holdTerminalEvent(event TerminalEvent) bool {
	if current.terminalPending != nil {
		return false
	}
	current.terminalPending = &event
	current.terminalEvents = nil
	return true
}

func (current *connection) sendTerminalEvent(event TerminalEvent) bool {
	if current.attachment == nil {
		return false
	}
	switch event.Kind {
	case TerminalEventAttached:
		// The runner echoes the cursor it actually accepted. Use that echoed
		// sequence as the credit origin; request bytes are not authority after
		// the daemon/runner attach gate has run.
		current.terminalAck = event.Sequence
		current.terminalSent = event.Sequence
		if !event.Accepted {
			payload, err := browserprotocol.EncodeTerminalReset(current.terminalAttachID, browserprotocol.TerminalReset{SessionID: current.terminalAttach.SessionID, Floor: browserprotocol.Decimal(event.Floor), Head: browserprotocol.Decimal(event.Head)})
			if err != nil || current.write(payload) != nil {
				return false
			}
			return current.closeTerminal() == nil
		}
		payload, err := browserprotocol.EncodeTerminalAttached(current.terminalAttachID, browserprotocol.TerminalAttached{SessionID: current.terminalAttach.SessionID, Floor: browserprotocol.Decimal(event.Floor), Head: browserprotocol.Decimal(event.Head), AcknowledgedSequence: browserprotocol.Decimal(event.Sequence), MaxUnackedBytes: browserprotocol.MaxTerminalUnackedBytes})
		if err != nil || current.write(payload) != nil {
			return false
		}
		current.terminalAnnounced = true
		return true
	case TerminalEventOutput:
		if !current.terminalAnnounced {
			return false
		}
		if event.End <= event.Start || event.End-event.Start != uint64(len(event.Payload)) {
			return false
		}
		if event.End <= current.terminalAck {
			return true
		}
		if event.Start != current.terminalSent {
			return false
		}
		if event.End-current.terminalAck > browserprotocol.MaxTerminalUnackedBytes {
			return current.holdTerminalEvent(event)
		}
		payload, err := browserprotocol.EncodeTerminalOutput(fixedID(current.terminalAttach.SessionID), event.Start, event.Payload)
		if err != nil || current.writeBinary(payload) != nil {
			return false
		}
		current.terminalSent = event.End
		current.armTerminalAckTimer()
		return true
	case TerminalEventReset:
		if current.terminalAck != current.terminalSent {
			return current.holdTerminalEvent(event)
		}
		payload, err := browserprotocol.EncodeTerminalReset(current.terminalAttachID, browserprotocol.TerminalReset{SessionID: current.terminalAttach.SessionID, Floor: browserprotocol.Decimal(event.Floor), Head: browserprotocol.Decimal(event.Head)})
		if err != nil || current.write(payload) != nil {
			return false
		}
		return current.closeTerminal() == nil
	case TerminalEventPTYEOF:
		if !current.terminalAnnounced {
			return false
		}
		payload, err := browserprotocol.EncodeTerminalEOF(current.terminalAttachID, browserprotocol.TerminalEOF{SessionID: current.terminalAttach.SessionID})
		return err == nil && current.write(payload) == nil
	case TerminalEventExit:
		if current.terminalAck != current.terminalSent {
			return current.holdTerminalEvent(event)
		}
		if !current.terminalAnnounced {
			return false
		}
		payload, err := browserprotocol.EncodeTerminalExit(current.terminalAttachID, browserprotocol.TerminalExit{SessionID: current.terminalAttach.SessionID, ExitCode: int64(event.ExitCode), ExitSignal: int64(event.ExitSignal), Aborted: event.Aborted})
		if err != nil || current.write(payload) != nil {
			return false
		}
		return current.closeTerminal() == nil
	default:
		return false
	}
}

func (current *connection) dispatchBinary(data []byte) bool {
	frame, err := browserprotocol.DecodeTerminalFrame(data)
	if err != nil || frame.Opcode != browserprotocol.TerminalInputOpcode || current.attachment == nil || frame.SessionID != fixedID(current.terminalAttach.SessionID) {
		current.sendError("", browserprotocol.ErrorInvalidRequest, false)
		return false
	}
	if current.server.terminalBackend == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(current.ctx, backendCallLimit)
	count, effectErr := current.server.terminalBackend.InputTerminal(ctx, TerminalInputRequest{Principal: current.principal, RunID: current.terminalAttach.RunID, SessionID: current.terminalAttach.SessionID, RunRevision: current.terminalAttach.ExpectedRunRevision, SessionRevision: current.terminalAttach.ExpectedSessionRevision, Frame: frame})
	cancel()
	status := "accepted"
	if effectErr != nil {
		status = "rejected"
		if errors.Is(effectErr, ErrTerminalPartial) {
			status = "partial"
		} else if errors.Is(effectErr, ErrTerminalUncertain) {
			status = "uncertain"
		}
	}
	if count > uint32(len(frame.Payload)) {
		return false
	}
	payload, encodeErr := browserprotocol.EncodeTerminalInputResult(current.terminalAttachID, browserprotocol.TerminalInputResult{SessionID: current.terminalAttach.SessionID, Generation: browserprotocol.Decimal(frame.LeaseGeneration), Sequence: browserprotocol.Decimal(frame.Sequence), Status: status, AcceptedBytes: browserprotocol.Decimal(count)})
	if encodeErr != nil || current.write(payload) != nil {
		return false
	}
	return true
}

func fixedID(encoded string) [16]byte {
	var result [16]byte
	raw, _ := hex.DecodeString(encoded)
	copy(result[:], raw)
	return result
}

func (current *connection) discardSubscription(subscription StateSubscription, cause error) error {
	if subscription == nil {
		return cause
	}
	if err := stopSubscription(subscription); err != nil {
		current.recordCleanup(err)
		return errors.Join(cause, err)
	}
	return cause
}

func (current *connection) recordCleanup(err error) {
	current.cleanupErr = errors.Join(current.cleanupErr, err)
	current.server.recordCleanup(err)
}

func stopSubscription(subscription StateSubscription) error {
	if subscription == nil {
		return ErrSubscriptionUnresolved
	}
	subscription.Cancel()
	done := subscription.Done()
	if done == nil {
		return ErrSubscriptionUnresolved
	}
	timer := time.NewTimer(subscriptionCloseLimit)
	defer timer.Stop()
	select {
	case <-done:
		if err := subscription.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrSubscriptionUnresolved, err)
		}
		return nil
	case <-timer.C:
		return ErrSubscriptionUnresolved
	}
}

func eventChronology(event browserprotocol.StateEvent) (browserprotocol.Decimal, browserprotocol.Decimal, error) {
	if changed, ok := event.EntityChanged(); ok {
		return changed.Sequence, changed.Head, nil
	}
	if hidden, ok := event.HiddenAdvance(); ok {
		return hidden.Sequence, hidden.Head, nil
	}
	return 0, 0, fmt.Errorf("invalid state event")
}

func validatePageCorrelation(page StatePage, cursor *Cursor) error {
	expectedKind := browserprotocol.StateFactory
	if cursor != nil {
		expectedKind = cursor.Kind
		if page.Head != cursor.Head {
			return fmt.Errorf("backend returned mismatched state head")
		}
	}
	if page.Kind != expectedKind {
		return fmt.Errorf("backend returned mismatched state kind")
	}
	if page.NextCursor == nil {
		if page.Kind != browserprotocol.StateHumanRequest {
			return fmt.Errorf("backend omitted state continuation")
		}
		return nil
	}
	next := page.NextCursor
	if next.Head != page.Head {
		return fmt.Errorf("backend returned mismatched continuation head")
	}
	if next.Kind == page.Kind {
		if page.Kind == browserprotocol.StateFactory || !next.HasAfter {
			return fmt.Errorf("backend returned invalid same-kind continuation")
		}
		return nil
	}
	if next.Kind != nextStateKind(page.Kind) || next.HasAfter {
		return fmt.Errorf("backend returned invalid kind continuation")
	}
	return nil
}

func nextStateKind(kind browserprotocol.StateKind) browserprotocol.StateKind {
	switch kind {
	case browserprotocol.StateFactory:
		return browserprotocol.StateProject
	case browserprotocol.StateProject:
		return browserprotocol.StateAgent
	case browserprotocol.StateAgent:
		return browserprotocol.StateTask
	case browserprotocol.StateTask:
		return browserprotocol.StateHumanRequest
	default:
		return ""
	}
}

func (current *connection) sendError(id string, code browserprotocol.ErrorCode, retryable bool) {
	payload, err := browserprotocol.EncodeError(id, browserprotocol.Error{Code: code, Retryable: retryable})
	if err == nil {
		_ = current.write(payload)
	}
}

func (current *connection) write(payload []byte) error {
	if len(payload) == 0 || len(payload) > browserprotocol.MaxControlBytes {
		return fmt.Errorf("invalid outbound control frame")
	}
	ctx, cancel := context.WithTimeout(current.ctx, writeLimit)
	defer cancel()
	return current.ws.Write(ctx, websocket.MessageText, payload)
}

func (current *connection) writeBinary(payload []byte) error {
	if len(payload) < browserprotocol.TerminalHeaderSize || len(payload) > browserprotocol.TerminalHeaderSize+browserprotocol.MaxTerminalPayload {
		return fmt.Errorf("invalid outbound terminal frame")
	}
	ctx, cancel := context.WithTimeout(current.ctx, writeLimit)
	defer cancel()
	return current.ws.Write(ctx, websocket.MessageBinary, payload)
}

func fixedBytes(encoded string, size int) ([]byte, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("invalid fixed hex")
	}
	return decoded, nil
}

func unsupportedProtocolVersion(payload []byte) bool {
	var envelope struct {
		Version *uint16 `json:"v"`
	}
	return json.Unmarshal(payload, &envelope) == nil && envelope.Version != nil && *envelope.Version != browserprotocol.ProtocolVersion
}
