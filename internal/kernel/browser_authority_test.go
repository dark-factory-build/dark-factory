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
	revokedClient, err := store.RevokeBrowserClient(ctx, clientID, client.Revision, UnixMillis{value: 12})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeBrowserClient(ctx, clientID, revokedClient.Revision, UnixMillis{value: 13}); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	revoked, err := store.AuthenticateBrowserClient(ctx, clientID)
	if !errors.Is(err, ErrUnauthorized) || revoked.ID != (BrowserClientID{}) {
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
	reserved, err := store.ReserveTerminalInputSequence(ctx, run.ID, session.ID, clientID, lease.Generation, 1, run.Revision, session.Revision, UnixMillis{value: 32})
	if err != nil || reserved.Sequence != 1 {
		t.Fatalf("reserve = %+v, %v", reserved, err)
	}
	if _, err := store.ReserveTerminalInputSequence(ctx, run.ID, session.ID, clientID, lease.Generation, 1, run.Revision, session.Revision, UnixMillis{value: 32}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("duplicate sequence = %v", err)
	}
	renewed, err := store.RenewTerminalLease(ctx, run.ID, session.ID, clientID, lease.Generation, run.Revision, session.Revision, UnixMillis{value: 33})
	if err != nil || renewed.ExpiresAt.Int64() != 33+BrowserTerminalLeaseTTL {
		t.Fatalf("renew = %+v, %v", renewed, err)
	}
	released, err := store.ReleaseTerminalLease(ctx, run.ID, session.ID, clientID, lease.Generation, run.Revision, session.Revision, UnixMillis{value: 34})
	if err != nil || released.Generation != 2 {
		t.Fatalf("release = %+v, %v", released, err)
	}
	if _, err := store.ReleaseTerminalLease(ctx, run.ID, session.ID, clientID, 2, run.Revision, session.Revision, UnixMillis{value: 34}); err != nil {
		t.Fatalf("release replay = %v", err)
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
}
