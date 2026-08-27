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
	authenticating       bool
	authenticatingID     [browserprotocol.ClientIDSize]byte
	seen                 map[string]struct{}
	subscription         StateSubscription
	updates              <-chan StateUpdate
	subscriptionID       string
	subscriptionSequence browserprotocol.Decimal
	subscriptionHead     browserprotocol.Decimal
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
		if err := current.closeSubscription(); err != nil {
			current.cleanupErr = errors.Join(current.cleanupErr, err)
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
		registered := false
		if requested != nil {
			if tracked {
				registered = current.server.finishAuthentication(current, *requested, result, accept)
			}
		} else {
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
		result, err := current.server.backend.Pair(ctx, request)
		return result, nil, false, err
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
		select {
		case <-current.ctx.Done():
			return
		case update, ok := <-updates:
			subscriptionID := current.subscriptionID
			if !ok || current.sendUpdate(update) != nil {
				current.sendError(subscriptionID, browserprotocol.ErrorInternal, false)
				return
			}
		case message := <-current.frames:
			if message.err != nil {
				return
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
	case browserprotocol.StateSubscribe:
		if current.subscription != nil {
			current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
			return false
		}
		subscription, backendErr := current.server.backend.SubscribeState(ctx, current.principal.ClientID, body.After)
		if ctx.Err() != nil {
			if subscription != nil {
				if closeErr := stopSubscription(subscription); closeErr != nil {
					current.cleanupErr = errors.Join(current.cleanupErr, closeErr)
				}
			}
			err = ctx.Err()
			break
		}
		if backendErr != nil {
			err = backendErr
			break
		}
		if subscription == nil {
			err = fmt.Errorf("invalid subscription")
			break
		}
		updates := subscription.Updates()
		if updates == nil {
			err = fmt.Errorf("invalid subscription")
			break
		}
		current.subscription = subscription
		current.updates = updates
		current.subscriptionID = frame.ID
		current.subscriptionSequence = body.After
		current.subscriptionHead = 0
		return true
	default:
		current.sendError(frame.ID, browserprotocol.ErrorInvalidRequest, false)
		return false
	}
	if err != nil {
		mapped := errorFrame(err)
		current.sendError(frame.ID, mapped.Code, mapped.Retryable)
		return !errors.Is(err, ErrUnauthorized)
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
		if chronologyErr != nil || update.Floor > head || current.subscriptionHead != 0 && head < current.subscriptionHead {
			return fmt.Errorf("invalid state event chronology")
		}
		if current.subscriptionSequence < update.Floor || current.subscriptionSequence == browserprotocol.Decimal(browserprotocol.MaxSQLiteInteger) || sequence != current.subscriptionSequence+1 {
			reason := browserprotocol.RestartGap
			if current.subscriptionSequence < update.Floor {
				reason = browserprotocol.RestartPruned
			}
			return current.sendRestart(browserprotocol.StateRestart{Head: head, Floor: update.Floor, Reason: reason})
		}
		payload, err := browserprotocol.EncodeStateEvent(current.subscriptionID, *update.Event)
		if err == nil {
			err = current.write(payload)
		}
		if err == nil {
			current.subscriptionSequence = sequence
			current.subscriptionHead = head
		}
		return err
	}
	return current.sendRestart(*update.Restart)
}

func (current *connection) sendRestart(restart browserprotocol.StateRestart) error {
	payload, err := browserprotocol.EncodeStateRestart(current.subscriptionID, restart)
	if err != nil {
		return err
	}
	if err := current.closeSubscription(); err != nil {
		current.cleanupErr = errors.Join(current.cleanupErr, err)
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
	return stopSubscription(subscription)
}

func stopSubscription(subscription StateSubscription) error {
	if subscription == nil {
		return ErrSubscriptionUnresolved
	}
	done := subscription.Done()
	if done == nil {
		return ErrSubscriptionUnresolved
	}
	subscription.Cancel()
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
