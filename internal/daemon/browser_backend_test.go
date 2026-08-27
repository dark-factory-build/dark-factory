//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const adapterOrigin = "https://app.darkfactory.build"

type adapterFixture struct {
	store   *kernel.Store
	backend *browserBackend
	server  *browser.Server
	key     *ecdsa.PrivateKey
	client  kernel.BrowserClient
	clock   time.Time
}

func newAdapterFixture(t *testing.T, capabilities kernel.BrowserCapabilityMask) *adapterFixture {
	t.Helper()
	clock := time.UnixMilli(2_000)
	store, err := kernel.Create(context.Background(), filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{DispatchEnabled: true, Capacity: 8}, adapterTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	randomBytes := append(bytes.Repeat([]byte{0x70}, kernel.IDBytes), bytes.Repeat([]byte{0x71}, kernel.IDBytes)...)
	randomBytes = append(randomBytes, bytes.Repeat([]byte{0x72}, kernel.IDBytes*8)...)
	backend, err := newBrowserBackend(store, func() time.Time { return clock }, bytes.NewReader(randomBytes))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	server, err := browser.Listen(browser.Config{Address: "127.0.0.1:0", AllowedOrigins: []string{adapterOrigin}, Backend: backend})
	if err != nil {
		_ = backend.close()
		_ = store.Close()
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &adapterFixture{store: store, backend: backend, server: server, key: key, clock: clock}
	challenge := bytes.Repeat([]byte{0x43}, browserprotocol.ChallengeSize)
	if _, err := store.CreateBrowserPairingChallenge(context.Background(), kernel.HashBrowserChallenge(challenge), backend.boot, adapterOrigin, capabilities, adapterTime(t, 1_000), adapterTime(t, 3_000)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close browser server: %v", err)
		}
		if err := backend.close(); err != nil {
			t.Errorf("close browser backend: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return fixture
}

func (fixture *adapterFixture) pair(t *testing.T) *websocket.Conn {
	t.Helper()
	connection := adapterDial(t, fixture.server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	challenge := bytes.Repeat([]byte{0x43}, browserprotocol.ChallengeSize)
	publicKey := elliptic.Marshal(elliptic.P256(), fixture.key.PublicKey.X, fixture.key.PublicKey.Y)
	transcript, err := browserprotocol.BuildPairTranscript(browserprotocol.PairTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), Challenge: challenge, PublicKeySEC1: publicKey,
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := browserprotocol.EncodePairProve("pair", browserprotocol.PairProve{
		Challenge: hex.EncodeToString(challenge), PublicKeySEC1: hex.EncodeToString(publicKey), Signature: hex.EncodeToString(adapterSign(t, fixture.key, transcript)),
	})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, proof)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypePairResult || frame.ID != "pair" {
		t.Fatalf("pair result = %+v", frame)
	}
	result := frame.Body.(browserprotocol.PairResult)
	rawClient := adapterHex(t, result.ClientID, browserprotocol.ClientIDSize)
	clientID, err := kernel.BrowserClientIDFromBytes(rawClient)
	if err != nil {
		t.Fatal(err)
	}
	client, found, err := fixture.store.BrowserClient(context.Background(), clientID)
	if err != nil || !found {
		t.Fatalf("durable paired client = %+v, found=%v, err=%v", client, found, err)
	}
	fixture.client = client
	want := browserprotocol.CapabilityObserve
	if client.CapabilityMask.Has(kernel.BrowserCapabilityPrivateHumanRequestDetail) {
		want |= browserprotocol.CapabilityPrivateHumanRequestDetail
	}
	if result.Capabilities != want {
		t.Fatalf("advertised capabilities = %d, want implemented %d", result.Capabilities, want)
	}
	return connection
}

func (fixture *adapterFixture) authenticate(t *testing.T) *websocket.Conn {
	t.Helper()
	connection := adapterDial(t, fixture.server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	transcript, err := browserprotocol.BuildAuthTranscript(browserprotocol.AuthTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), ClientID: fixture.client.ID.Bytes(),
		ValidatedHost: fixture.server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := browserprotocol.EncodeAuthProve("auth", browserprotocol.AuthProve{ClientID: fixture.client.ID.String(), Signature: hex.EncodeToString(adapterSign(t, fixture.key, transcript))})
	if err != nil {
		t.Fatal(err)
	}
	adapterWrite(t, connection, proof)
	frame := adapterRead(t, connection)
	if frame.Type != browserprotocol.TypeAuthResult || frame.ID != "auth" {
		t.Fatalf("auth result = %+v", frame)
	}
	return connection
}

func adapterDial(t *testing.T, server *browser.Server) *websocket.Conn {
	t.Helper()
	header := make(http.Header)
	header.Set("Origin", adapterOrigin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws://"+server.Addr()+browser.Path, &websocket.DialOptions{HTTPHeader: header, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	return connection
}

func adapterRead(t *testing.T, connection *websocket.Conn) browserprotocol.ControlFrame {
	t.Helper()
	frame, _ := adapterReadPayload(t, connection)
	return frame
}

func adapterReadPayload(t *testing.T, connection *websocket.Conn) (browserprotocol.ControlFrame, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	kind, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageText {
		t.Fatalf("frame kind = %d", kind)
	}
	frame, err := browserprotocol.DecodeServerControl(payload)
	if err != nil {
		t.Fatalf("decode server frame %q: %v", payload, err)
	}
	return frame, payload
}

func adapterWrite(t *testing.T, connection *websocket.Conn, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := connection.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func adapterSign(t *testing.T, key *ecdsa.PrivateKey, transcript []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(transcript)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	result := make([]byte, browserprotocol.SignatureSize)
	r.FillBytes(result[:32])
	s.FillBytes(result[32:])
	return result
}

func adapterHex(t *testing.T, value string, size int) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		t.Fatalf("fixed hex %q = %x, %v", value, decoded, err)
	}
	return decoded
}

func adapterTime(t *testing.T, value int64) kernel.UnixMillis {
	t.Helper()
	result, err := kernel.NewUnixMillis(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func adapterID(t *testing.T, seed byte) []byte {
	t.Helper()
	value := bytes.Repeat([]byte{seed}, kernel.IDBytes)
	value[len(value)-1] ^= 0x5a
	return value
}

func TestBrowserAdapterPairsAuthenticatesPagesAndReloadsRevocation(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail|kernel.BrowserCapabilityHumanActions|kernel.BrowserCapabilityTerminalInput)
	ctx := context.Background()
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, 1))
	agentID, _ := kernel.AgentIDFromBytes(adapterID(t, 2))
	taskID, _ := kernel.TaskIDFromBytes(adapterID(t, 3))
	incarnationID, _ := kernel.IncarnationIDFromBytes(adapterID(t, 4))
	project, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: "public project", Root: "/PRIVATE_ROOT_SENTINEL"}, adapterTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: "public agent", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ExecutionMode: kernel.ExecutionWorkspaceWrite, Model: "PRIVATE_MODEL_SENTINEL", ToolBudgetLimit: 4}, adapterTime(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: "public task", Body: "PRIVATE_BODY_SENTINEL"}, adapterTime(t, 12)); err != nil {
		t.Fatal(err)
	}

	paired := fixture.pair(t)
	stateGet, _ := browserprotocol.EncodeStateGet("state-1", browserprotocol.StateGet{})
	adapterWrite(t, paired, stateGet)
	frame, payload := adapterReadPayload(t, paired)
	if frame.Type != browserprotocol.TypeStateSnapshot {
		t.Fatalf("state frame = %+v", frame)
	}
	snapshot := frame.Body.(browserprotocol.StateSnapshot)
	for page := 1; snapshot.NextCursor != nil; page++ {
		next, _ := browserprotocol.EncodeStateGet(fmt.Sprintf("state-page-%d", page), browserprotocol.StateGet{Cursor: snapshot.NextCursor})
		adapterWrite(t, paired, next)
		frame, pagePayload := adapterReadPayload(t, paired)
		if frame.Type != browserprotocol.TypeStateSnapshot {
			t.Fatalf("state continuation = %+v", frame)
		}
		payload = append(payload, pagePayload...)
		snapshot = frame.Body.(browserprotocol.StateSnapshot)
	}
	for _, sentinel := range []string{"PRIVATE_ROOT_SENTINEL", "PRIVATE_MODEL_SENTINEL", "PRIVATE_BODY_SENTINEL"} {
		if bytes.Contains(payload, []byte(sentinel)) {
			t.Fatalf("public state leaked %q: %s", sentinel, payload)
		}
	}

	authenticated := fixture.authenticate(t)
	stateGet, _ = browserprotocol.EncodeStateGet("state-2", browserprotocol.StateGet{})
	adapterWrite(t, authenticated, stateGet)
	if frame := adapterRead(t, authenticated); frame.Type != browserprotocol.TypeStateSnapshot {
		t.Fatalf("authenticated state = %+v", frame)
	}
	revoked, err := fixture.store.RevokeBrowserClient(ctx, fixture.client.ID, fixture.client.Revision, adapterTime(t, 2_100))
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	stateGet, _ = browserprotocol.EncodeStateGet("state-3", browserprotocol.StateGet{})
	adapterWrite(t, authenticated, stateGet)
	frame = adapterRead(t, authenticated)
	if frame.Type != browserprotocol.TypeError || frame.Body.(browserprotocol.Error).Code != browserprotocol.ErrorUnauthorized {
		t.Fatalf("post-revoke operation = %+v", frame)
	}
}

func TestBrowserAdapterBadPairProofDoesNotConsumeChallenge(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	connection := adapterDial(t, fixture.server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	challenge := bytes.Repeat([]byte{0x43}, browserprotocol.ChallengeSize)
	publicKey := elliptic.Marshal(elliptic.P256(), fixture.key.PublicKey.X, fixture.key.PublicKey.Y)
	bad, _ := browserprotocol.EncodePairProve("bad", browserprotocol.PairProve{Challenge: hex.EncodeToString(challenge), PublicKeySEC1: hex.EncodeToString(publicKey), Signature: strings.Repeat("01", browserprotocol.SignatureSize)})
	adapterWrite(t, connection, bad)
	if frame := adapterRead(t, connection); frame.Type != browserprotocol.TypeError || frame.Body.(browserprotocol.Error).Code != browserprotocol.ErrorUnauthorized {
		t.Fatalf("bad proof = %+v", frame)
	}
	// A failed proof closes only its connection. The same durable challenge is
	// still redeemable with the exact signed transcript on a fresh connection.
	paired := fixture.pair(t)
	_ = paired
	if fixture.client.ID == (kernel.BrowserClientID{}) {
		t.Fatal("correct proof after bad proof did not pair")
	}
	_ = hello
}

func TestBrowserAdapterFixedHeadPaginationAndSubscribeBridgesCommit(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	ctx := context.Background()
	for index := 1; index <= 9; index++ {
		id, _ := kernel.ProjectIDFromBytes(adapterID(t, byte(20+index)))
		if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: id, Name: fmt.Sprintf("project-%02d", index), Root: fmt.Sprintf("/project/%02d", index)}, adapterTime(t, int64(20+index))); err != nil {
			t.Fatal(err)
		}
	}
	connection := fixture.pair(t)
	request, _ := browserprotocol.EncodeStateGet("factory", browserprotocol.StateGet{})
	adapterWrite(t, connection, request)
	factory := adapterRead(t, connection).Body.(browserprotocol.StateSnapshot)
	if factory.NextCursor == nil {
		t.Fatal("factory page omitted continuation")
	}
	request, _ = browserprotocol.EncodeStateGet("projects-1", browserprotocol.StateGet{Cursor: factory.NextCursor})
	adapterWrite(t, connection, request)
	first := adapterRead(t, connection).Body.(browserprotocol.StateSnapshot)
	projects, ok := first.Items.Projects()
	if !ok || len(projects) != kernel.PublicStatePageSize || first.NextCursor == nil || first.Head != factory.Head {
		t.Fatalf("first project page = %+v, items=%d", first, len(projects))
	}
	request, _ = browserprotocol.EncodeStateGet("projects-2", browserprotocol.StateGet{Cursor: first.NextCursor})
	adapterWrite(t, connection, request)
	second := adapterRead(t, connection).Body.(browserprotocol.StateSnapshot)
	projects, ok = second.Items.Projects()
	if !ok || len(projects) != 1 || second.Head != factory.Head {
		t.Fatalf("second project page = %+v, items=%d", second, len(projects))
	}

	newID, _ := kernel.ProjectIDFromBytes(adapterID(t, 60))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: newID, Name: "after snapshot", Root: "/after-snapshot"}, adapterTime(t, 100)); err != nil {
		t.Fatal(err)
	}
	subscribe, _ := browserprotocol.EncodeStateSubscribe("watch", browserprotocol.StateSubscribe{After: factory.Head})
	adapterWrite(t, connection, subscribe)
	eventFrame := adapterRead(t, connection)
	if eventFrame.Type != browserprotocol.TypeStateEvent {
		t.Fatalf("snapshot-to-subscribe bridge = %+v", eventFrame)
	}
	event, ok := eventFrame.Body.(browserprotocol.StateEvent).EntityChanged()
	if !ok || event.Sequence != factory.Head+1 || event.EntityKind != browserprotocol.StateProject || event.EntityID != newID.String() {
		t.Fatalf("bridged event = %+v", eventFrame.Body)
	}
}

func TestBrowserAdapterObserveOnlyCannotReadPrivateDetail(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	fixture.pair(t)
	requestID, _ := kernel.HumanRequestIDFromBytes(adapterID(t, 90))
	revision, _ := kernel.NewRevision(1)
	_, err := fixture.backend.HumanRequestDetail(context.Background(), rawBrowserClient(fixture.client.ID), browserprotocol.HumanRequestDetailGet{RequestID: requestID.String(), ExpectedRevision: decimalRevision(revision)})
	if !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("observe-only private detail = %v", err)
	}
}

func TestBrowserAdapterHumanRequestDetailAndExactTombstone(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail|kernel.BrowserCapabilityHumanActions)
	fixture.pair(t)
	run := adapterRunningRun(t, fixture.store, 100)
	var key [kernel.IDBytes]byte
	copy(key[:], adapterID(t, 120))
	request, err := fixture.store.CreateHumanQuestionForAttempt(context.Background(), run.CredentialDigest, kernel.NewHumanQuestion{
		IdempotencyKey: key, QuestionText: "QUESTION_PRIVATE_SENTINEL",
	}, adapterTime(t, 500))
	if err != nil {
		t.Fatal(err)
	}
	entityRequest := browserprotocol.StateEntityGet{Kind: browserprotocol.StateHumanRequest, ID: request.ID.String()}
	entity, err := fixture.backend.StateEntity(context.Background(), rawBrowserClient(fixture.client.ID), entityRequest)
	if err != nil || bool(entity.Deleted) || entity.Revision != decimalRevision(request.Revision) {
		t.Fatalf("open request entity = %+v, %v", entity, err)
	}
	item, ok := entity.Item.HumanRequest()
	if !ok || strings.Contains(fmt.Sprintf("%+v", item), "QUESTION_PRIVATE_SENTINEL") {
		t.Fatalf("unsafe public HumanRequest item = %+v", item)
	}
	detail, err := fixture.backend.HumanRequestDetail(context.Background(), rawBrowserClient(fixture.client.ID), browserprotocol.HumanRequestDetailGet{
		RequestID: request.ID.String(), ExpectedRevision: decimalRevision(request.Revision),
	})
	if err != nil || detail.Question != "QUESTION_PRIVATE_SENTINEL" {
		t.Fatalf("authorized detail = %+v, %v", detail, err)
	}
	deliveryID, _ := kernel.HumanRequestDeliveryIDFromBytes(adapterID(t, 121))
	delivery, err := fixture.store.BeginHumanReply(context.Background(), fixture.client.ID, request.ID, request.Revision, deliveryID, "reply", adapterTime(t, 501))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.AcknowledgeHumanReply(context.Background(), request.ID, delivery.DeliveryID, delivery.Revision, adapterTime(t, 502)); err != nil {
		t.Fatal(err)
	}
	tombstone, err := fixture.backend.StateEntity(context.Background(), rawBrowserClient(fixture.client.ID), entityRequest)
	if err != nil || !bool(tombstone.Deleted) || tombstone.Revision != browserprotocol.Decimal(request.Revision.Int64()+2) || !tombstone.Item.IsDeleted() {
		t.Fatalf("resolved request tombstone = %+v, %v", tombstone, err)
	}
	missingID := hex.EncodeToString(adapterID(t, 122))
	_, err = fixture.backend.StateEntity(context.Background(), rawBrowserClient(fixture.client.ID), browserprotocol.StateEntityGet{Kind: browserprotocol.StateHumanRequest, ID: missingID})
	if !errors.Is(err, browser.ErrNotFound) {
		t.Fatalf("never-existing request = %v", err)
	}
}

func TestBrowserAdapterSubscriptionReloadsAuthorityAndJoins(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	fixture.pair(t)
	state, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := fixture.backend.SubscribeState(context.Background(), rawBrowserClient(fixture.client.ID), decimalSequence(state.Head))
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, 130))
	if _, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: projectID, Name: "subscription", Root: "/subscription"}, adapterTime(t, 600)); err != nil {
		t.Fatal(err)
	}
	fixture.backend.signalStateChanged()
	update := adapterNextUpdate(t, subscription)
	changed, ok := update.Event.EntityChanged()
	if !ok || changed.Sequence != browserprotocol.Decimal(state.Head.Int64()+1) || changed.EntityID != projectID.String() || update.Floor == 0 {
		t.Fatalf("subscription update = %+v", update)
	}
	subscription.Cancel()
	adapterWaitSubscription(t, subscription)
	if err := subscription.Err(); err != nil {
		t.Fatalf("cancelled subscription error = %v", err)
	}
	fixture.backend.subMu.Lock()
	remaining := len(fixture.backend.subs)
	fixture.backend.subMu.Unlock()
	if remaining != 0 {
		t.Fatalf("joined subscription remained registered: %d", remaining)
	}

	// A new subscription cannot keep streaming from an authority cached by the
	// paired WebSocket after durable revocation.
	revoked, err := fixture.store.RevokeBrowserClient(context.Background(), fixture.client.ID, fixture.client.Revision, adapterTime(t, 2_100))
	if err != nil || revoked.RevokedAt == nil {
		t.Fatal(err)
	}
	if _, err := fixture.backend.SubscribeState(context.Background(), rawBrowserClient(fixture.client.ID), decimalSequence(state.Head)); !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("post-revoke subscription = %v", err)
	}
}

func TestBrowserAdapterRunInvalidationRestartsAndSuppressesLaterEvents(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	fixture.pair(t)
	before, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := fixture.backend.SubscribeState(context.Background(), rawBrowserClient(fixture.client.ID), decimalSequence(before.Head))
	if err != nil {
		t.Fatal(err)
	}
	adapterRunningRun(t, fixture.store, 140)
	laterID, _ := kernel.ProjectIDFromBytes(adapterID(t, 170))
	if _, err := fixture.store.CreateProject(context.Background(), kernel.NewProject{ID: laterID, Name: "must be suppressed", Root: "/must-be-suppressed"}, adapterTime(t, 900)); err != nil {
		t.Fatal(err)
	}
	fixture.backend.signalStateChanged()
	seenRestart := false
	for !seenRestart {
		update := adapterNextUpdate(t, subscription)
		if update.Restart != nil {
			if update.Restart.Reason != browserprotocol.RestartHiddenDependency {
				t.Fatalf("run restart = %+v", update.Restart)
			}
			seenRestart = true
			continue
		}
		changed, ok := update.Event.EntityChanged()
		if ok && changed.EntityID == laterID.String() {
			t.Fatal("public event after hidden run dependency escaped before restart")
		}
	}
	adapterWaitSubscription(t, subscription)
	if err := subscription.Err(); err != nil {
		t.Fatalf("restart subscription error = %v", err)
	}
}

func TestBrowserAdapterFutureCursorRestartsAndChangeIsHiddenAdvance(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	fixture.pair(t)
	state, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	future := browserprotocol.Decimal(state.Head.Int64() + 1)
	subscription, err := fixture.backend.SubscribeState(context.Background(), rawBrowserClient(fixture.client.ID), future)
	if err != nil {
		t.Fatal(err)
	}
	update := adapterNextUpdate(t, subscription)
	if update.Restart == nil || update.Restart.Reason != browserprotocol.RestartGap || update.Restart.Head != decimalSequence(state.Head) {
		t.Fatalf("future-cursor restart = %+v", update)
	}
	adapterWaitSubscription(t, subscription)

	changeID := hex.EncodeToString(adapterID(t, 180))
	revision, _ := kernel.NewRevision(3)
	sequence, _ := kernel.NewEventSequence(9)
	head, _ := kernel.NewEventSequence(12)
	floor, _ := kernel.NewEventSequence(1)
	event, restart, err := projectInvalidation(kernel.WatchBatch{Head: head, Floor: floor}, kernel.Invalidation{Sequence: sequence, EntityKind: "change", EntityID: changeID, Revision: revision})
	if err != nil || restart != nil {
		t.Fatalf("Change projection = %+v, %+v, %v", event, restart, err)
	}
	hidden, ok := event.HiddenAdvance()
	if !ok || hidden.Sequence != 9 || hidden.Head != 12 {
		t.Fatalf("Change hidden advance = %+v", event)
	}
}

func TestBrowserAdapterRestartUsesNewBootAndDurableClient(t *testing.T) {
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	paired := fixture.pair(t)
	_ = paired.CloseNow()
	oldIdentity, err := fixture.backend.Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.backend.close(); err != nil {
		t.Fatal(err)
	}
	randomBytes := append(bytes.Repeat([]byte{0x7f}, kernel.IDBytes), bytes.Repeat([]byte{0x7e}, kernel.IDBytes*2)...)
	restarted, err := newBrowserBackend(fixture.store, func() time.Time { return fixture.clock }, bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatal(err)
	}
	server, err := browser.Listen(browser.Config{Address: "127.0.0.1:0", AllowedOrigins: []string{adapterOrigin}, Backend: restarted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = restarted.close()
	})
	newIdentity, err := restarted.Identity(context.Background())
	if err != nil || newIdentity.DaemonID != oldIdentity.DaemonID || newIdentity.BootID == oldIdentity.BootID {
		t.Fatalf("restart identity old=%+v new=%+v err=%v", oldIdentity, newIdentity, err)
	}
	connection := adapterDial(t, server)
	hello := adapterRead(t, connection).Body.(browserprotocol.Hello)
	transcript, err := browserprotocol.BuildAuthTranscript(browserprotocol.AuthTranscript{
		DaemonID: adapterHex(t, hello.DaemonID, browserprotocol.DaemonIDSize), BootID: adapterHex(t, hello.BootID, browserprotocol.BootIDSize),
		ConnectionNonce: adapterHex(t, hello.ConnectionNonce, browserprotocol.NonceSize), ClientID: fixture.client.ID.Bytes(),
		ValidatedHost: server.Addr(), ValidatedOrigin: adapterOrigin,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := browserprotocol.EncodeAuthProve("restart-auth", browserprotocol.AuthProve{ClientID: fixture.client.ID.String(), Signature: hex.EncodeToString(adapterSign(t, fixture.key, transcript))})
	adapterWrite(t, connection, proof)
	if frame := adapterRead(t, connection); frame.Type != browserprotocol.TypeAuthResult {
		t.Fatalf("restart auth = %+v", frame)
	}
}

func TestDaemonCloseJoinsHijackedBrowserConnectionsBeforeStoreClose(t *testing.T) {
	store, err := kernel.Create(context.Background(), filepath.Join(t.TempDir(), "kernel.sqlite"), kernel.FactoryConfig{Capacity: 2}, adapterTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	daemon, err := newDaemon(store, func() time.Time { return time.UnixMilli(2_000) })
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := daemon.ListenBrowser("127.0.0.1:0", []string{adapterOrigin})
	if err != nil {
		t.Fatal(err)
	}
	connection := adapterDial(t, runtime.server)
	if frame := adapterRead(t, connection); frame.Type != browserprotocol.TypeHello {
		t.Fatalf("hello = %+v", frame)
	}
	closed := make(chan error, 1)
	go func() { closed <- daemon.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("daemon close = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon close did not join browser connection")
	}
	select {
	case <-runtime.server.ServeDone():
	default:
		t.Fatal("daemon close returned before browser listener joined")
	}
	if _, err := store.Factory(context.Background()); err != nil {
		t.Fatalf("browser close prematurely closed Store: %v", err)
	}
	if _, err := daemon.ListenBrowser("127.0.0.1:0", []string{adapterOrigin}); !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("browser listener after daemon close = %v", err)
	}
}

func adapterNextUpdate(t *testing.T, subscription browser.StateSubscription) browser.StateUpdate {
	t.Helper()
	select {
	case update, ok := <-subscription.Updates():
		if !ok {
			t.Fatalf("subscription closed early: %v", subscription.Err())
		}
		return update
	case <-time.After(3 * time.Second):
		t.Fatal("subscription update timed out")
		return browser.StateUpdate{}
	}
}

func adapterWaitSubscription(t *testing.T, subscription browser.StateSubscription) {
	t.Helper()
	select {
	case <-subscription.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not join")
	}
}

func adapterRunningRun(t *testing.T, store *kernel.Store, seed byte) kernel.Run {
	t.Helper()
	ctx := context.Background()
	projectID, _ := kernel.ProjectIDFromBytes(adapterID(t, seed))
	agentID, _ := kernel.AgentIDFromBytes(adapterID(t, seed+1))
	taskID, _ := kernel.TaskIDFromBytes(adapterID(t, seed+2))
	incarnationID, _ := kernel.IncarnationIDFromBytes(adapterID(t, seed+3))
	project, err := store.CreateProject(ctx, kernel.NewProject{ID: projectID, Name: fmt.Sprintf("run-project-%d", seed), Root: fmt.Sprintf("/run-project-%d", seed)}, adapterTime(t, 200))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, kernel.NewAgent{ID: agentID, ProjectID: project.ID, Name: fmt.Sprintf("run-agent-%d", seed), Role: kernel.RoleOrchestrator, Provider: kernel.ProviderCodex, ExecutionMode: kernel.ExecutionWorkspaceWrite, ToolBudgetLimit: 4}, adapterTime(t, 201))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTask(ctx, kernel.NewTask{ID: taskID, ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID, Title: "run task"}, adapterTime(t, 202)); err != nil {
		t.Fatal(err)
	}
	runID, _ := kernel.RunIDFromBytes(adapterID(t, seed+4))
	sessionID, _ := kernel.TerminalSessionIDFromBytes(adapterID(t, seed+5))
	attemptDigest, _ := kernel.AttemptDigestFromBytes(bytes.Repeat([]byte{seed + 6}, kernel.DigestBytes))
	resourceID := func(value byte) kernel.ResourceID {
		id, _ := kernel.ResourceIDFromBytes(adapterID(t, value))
		return id
	}
	keys := kernel.AdmissionKeys{
		RunID: runID, TerminalSessionID: sessionID, AttemptDigest: attemptDigest, RuntimeRoot: fmt.Sprintf("/runtime/adapter-%d", seed),
		Resources: kernel.AdmissionResourceIDs{RuntimeRoot: resourceID(seed + 7), RunnerProcess: resourceID(seed + 8), ProviderProcess: resourceID(seed + 9), ProviderGroup: resourceID(seed + 10)},
	}
	admission, err := store.AdmitNext(ctx, agent.ID, keys, adapterTime(t, 300))
	if err != nil || admission.Run == nil {
		t.Fatalf("admission = %+v, %v", admission, err)
	}
	resources, err := store.Resources(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var provider, group kernel.Resource
	for index, resource := range resources {
		switch resource.Kind {
		case kernel.ResourceProviderProcess:
			provider = resource
		case kernel.ResourceProviderGroup:
			group = resource
		case kernel.ResourceRuntimeRoot:
			identity, _ := kernel.NewPathResourceIdentity(10, int64(10_000+int(seed)))
			if _, err := store.ActivateResource(ctx, runID, resource.ID, resource.Revision, identity, adapterTime(t, int64(310+index))); err != nil {
				t.Fatal(err)
			}
		default:
			identity := adapterProcessIdentity(t, int64(20_000+int(seed)*100+index))
			if _, err := store.ActivateResource(ctx, runID, resource.ID, resource.Revision, identity, adapterTime(t, int64(310+index))); err != nil {
				t.Fatal(err)
			}
		}
	}
	providerIdentity := adapterProcessIdentity(t, int64(30_000+int(seed)))
	if _, _, err := store.ActivateProviderResources(ctx, runID, provider.ID, provider.Revision, group.ID, group.Revision, providerIdentity, adapterTime(t, 320)); err != nil {
		t.Fatal(err)
	}
	session, found, err := store.TerminalSessionForRun(ctx, runID)
	if err != nil || !found {
		t.Fatalf("terminal session = %+v, found=%v, err=%v", session, found, err)
	}
	running, err := store.ActivateRun(ctx, runID, session.ID, admission.Run.Revision, session.Revision, adapterTime(t, 330))
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func adapterProcessIdentity(t *testing.T, seed int64) kernel.ResourceIdentity {
	t.Helper()
	digest := sha256.Sum256([]byte(fmt.Sprintf("birth-%d", seed)))
	birth, err := kernel.BirthDigestFromBytes(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	identity, err := kernel.NewProcessResourceIdentity(seed, seed, birth)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func rawBrowserClient(id kernel.BrowserClientID) [browserprotocol.ClientIDSize]byte {
	var result [browserprotocol.ClientIDSize]byte
	copy(result[:], id.Bytes())
	return result
}
