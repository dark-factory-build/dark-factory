package kernel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
)

func browserTestID(t *testing.T, first byte) BrowserClientID {
	t.Helper()
	raw := make([]byte, IDBytes)
	raw[0] = first
	id, err := BrowserClientIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func browserTestBoot(t *testing.T, first byte) BootID {
	t.Helper()
	raw := make([]byte, IDBytes)
	raw[0] = first
	id, err := BootIDFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBrowserPairingConsumesAndDerivesIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := Create(ctx, filepath.Join(t.TempDir(), "kernel.db"), FactoryConfig{}, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := elliptic.Marshal(elliptic.P256(), key.X, key.Y)
	digest := HashBrowserChallenge([]byte("raw challenge never enters Store"))
	boot := browserTestBoot(t, 1)
	if _, err := store.CreateBrowserPairingChallenge(ctx, digest, boot, "https://app.example", BrowserCapabilityObserve|BrowserCapabilityTerminalInput, UnixMillis{value: 10}, UnixMillis{value: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemBrowserPairingChallenge(ctx, digest, browserTestBoot(t, 2), "https://app.example", browserTestID(t, 2), publicKey, UnixMillis{value: 11}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong boot error = %v", err)
	}
	clientID := browserTestID(t, 2)
	client, err := store.RedeemBrowserPairingChallenge(ctx, digest, boot, "https://app.example", clientID, publicKey, UnixMillis{value: 11})
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, _ := validateBrowserKey(publicKey)
	if client.Fingerprint != wantFingerprint || !client.CapabilityMask.Has(BrowserCapabilityTerminalInput) {
		t.Fatalf("client identity = %+v", client)
	}
	if _, err := store.RedeemBrowserPairingChallenge(ctx, digest, boot, "https://app.example", browserTestID(t, 3), publicKey, UnixMillis{value: 11}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replay error = %v", err)
	}
	principal, err := store.AuthenticateBrowserClient(ctx, clientID)
	if err != nil || principal.ClientID() != clientID {
		t.Fatalf("active principal = %v/%v, err=%v", principal.ClientID(), clientID, err)
	}
	loaded, found, err := store.BrowserClient(ctx, principal.ClientID())
	if err != nil || !found || loaded.CapabilityMask != client.CapabilityMask || loaded.PublicKey == nil {
		t.Fatalf("principal reload = %+v, found=%v, err=%v", loaded, found, err)
	}
	revokedClient, err := store.RevokeBrowserClient(ctx, clientID, client.Revision, UnixMillis{value: 12})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeBrowserClient(ctx, clientID, revokedClient.Revision, UnixMillis{value: 13}); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	revoked, err := store.AuthenticateBrowserClient(ctx, clientID)
	if !errors.Is(err, ErrUnauthorized) || revoked.ClientID() != (BrowserClientID{}) {
		t.Fatalf("revoked auth = %+v, %v", revoked, err)
	}
}

func TestTerminalLeaseGuardsAndPrivateChronology(t *testing.T) {
	store, run, keys := runningOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := elliptic.Marshal(elliptic.P256(), privateKey.X, privateKey.Y)
	clientID := browserTestID(t, 33)
	boot := browserTestBoot(t, 34)
	digest := HashBrowserChallenge([]byte("lease challenge"))
	if _, err := store.CreateBrowserPairingChallenge(ctx, digest, boot, "https://app.example", BrowserCapabilityObserve|BrowserCapabilityTerminalInput, UnixMillis{value: 30}, UnixMillis{value: 40}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemBrowserPairingChallenge(ctx, digest, boot, "https://app.example", clientID, publicKey, UnixMillis{value: 31}); err != nil {
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	beforeFactory, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireTerminalLease(ctx, run.ID, session.ID, clientID, run.Revision, session.Revision, UnixMillis{value: 31})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Generation != 1 || lease.ExpiresAt.Int64() != 31+BrowserTerminalLeaseTTL {
		t.Fatalf("lease = %+v", lease)
	}
	afterRun, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterSession := terminalSessionForRunTest(t, store, run.ID)
	afterFactory, err := store.Factory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.Revision != run.Revision || afterSession.Revision != session.Revision || afterFactory.Head != beforeFactory.Head {
		t.Fatalf("lease changed lifecycle authority: run %v/%v session %v/%v head %v/%v", afterRun.Revision, run.Revision, afterSession.Revision, session.Revision, afterFactory.Head, beforeFactory.Head)
	}
	if afterSession.UpdatedAt != session.UpdatedAt || afterRun.UpdatedAt != run.UpdatedAt {
		t.Fatalf("lease changed lifecycle chronology: run %v/%v session %v/%v", afterRun.UpdatedAt, run.UpdatedAt, afterSession.UpdatedAt, session.UpdatedAt)
	}
	reserved, err := store.ReserveTerminalInputSequence(ctx, run.ID, session.ID, clientID, lease.Generation, 1, run.Revision, session.Revision, UnixMillis{value: 32})
	if err != nil || reserved.Sequence != 1 {
		t.Fatalf("reserve = %+v, %v", reserved, err)
	}
	reservedRun, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	reservedSession := terminalSessionForRunTest(t, store, run.ID)
	if reservedRun.Revision != run.Revision || reservedRun.UpdatedAt != run.UpdatedAt || reservedSession.Revision != session.Revision || reservedSession.UpdatedAt != session.UpdatedAt {
		t.Fatalf("reserve changed lifecycle authority: run=%+v session=%+v", reservedRun, reservedSession)
	}
	if _, err := store.ReserveTerminalInputSequence(ctx, run.ID, session.ID, clientID, lease.Generation, 1, run.Revision, session.Revision, UnixMillis{value: 32}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("duplicate sequence = %v", err)
	}
	renewed, err := store.RenewTerminalLease(ctx, run.ID, session.ID, clientID, lease.Generation, run.Revision, session.Revision, UnixMillis{value: 33})
	if err != nil || renewed.ExpiresAt.Int64() != 33+BrowserTerminalLeaseTTL {
		t.Fatalf("renew = %+v, %v", renewed, err)
	}
	renewedRun, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	renewedSession := terminalSessionForRunTest(t, store, run.ID)
	if renewedRun.Revision != run.Revision || renewedRun.UpdatedAt != run.UpdatedAt || renewedSession.Revision != session.Revision || renewedSession.UpdatedAt != session.UpdatedAt {
		t.Fatalf("renew changed lifecycle authority: run=%+v session=%+v", renewedRun, renewedSession)
	}
	released, err := store.ReleaseTerminalLease(ctx, run.ID, session.ID, clientID, lease.Generation, run.Revision, session.Revision, UnixMillis{value: 34})
	if err != nil || released.Generation != 2 {
		t.Fatalf("release = %+v, %v", released, err)
	}
	releasedRun, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	releasedSession := terminalSessionForRunTest(t, store, run.ID)
	if releasedRun.Revision != run.Revision || releasedRun.UpdatedAt != run.UpdatedAt || releasedSession.Revision != session.Revision || releasedSession.UpdatedAt != session.UpdatedAt {
		t.Fatalf("release changed lifecycle authority: run=%+v session=%+v", releasedRun, releasedSession)
	}
	if _, err := store.ReleaseTerminalLease(ctx, run.ID, session.ID, clientID, 2, run.Revision, session.Revision, UnixMillis{value: 34}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("release replay error = %v", err)
	}
	lease, err = store.AcquireTerminalLease(ctx, run.ID, session.ID, clientID, run.Revision, session.Revision, UnixMillis{value: 35})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewFailureProposal(FailureProtocol, "terminal test")
	if err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, UnixMillis{value: 40})
	if err != nil {
		t.Fatal(err)
	}
	if finalizing.Phase != RunFinalizing {
		t.Fatalf("finalizing run = %+v", finalizing)
	}
	cleared := terminalSessionForRunTest(t, store, run.ID)
	if cleared.LeaseClientID != nil || cleared.LeaseExpiresAt != nil || cleared.LastInputSequence != 0 || cleared.LeaseGeneration != lease.Generation+1 {
		t.Fatalf("finalization did not revoke lease: %+v", cleared)
	}
	if cleared.Revision != session.Revision || cleared.UpdatedAt != session.UpdatedAt {
		t.Fatalf("finalization lease cleanup changed session lifecycle: before=%v/%v after=%v/%v", session.Revision, session.UpdatedAt, cleared.Revision, cleared.UpdatedAt)
	}
	if finalizing.Revision.Int64() != run.Revision.Int64()+1 || finalizing.UpdatedAt.Int64() != 40 {
		t.Fatalf("finalization run lifecycle = %+v", finalizing)
	}
}
