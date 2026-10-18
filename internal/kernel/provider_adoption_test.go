package kernel

import (
	"context"
	"reflect"
	"testing"
)

func TestProviderAdoptionEmptyState(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	got, found, err := store.ProviderAdoption(context.Background(), ProviderCodex)
	if err != nil || found || got != (ProviderAdoption{}) {
		t.Fatalf("empty adoption = %+v, found=%v, err=%v", got, found, err)
	}
	all, err := store.ListProviderAdoptions(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("empty adoption list = %+v, err=%v", all, err)
	}
}

func TestProviderAdoptionPersistsActiveAndOneCandidateAcrossRestart(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	want := ProviderAdoption{Provider: ProviderCodex, ActiveVersion: "1.2.3", ActiveCommitment: "/tools/codex@old", CandidateVersion: "1.3.0", CandidateCommitment: "/tools/codex@new"}
	if err := store.SetProviderAdoption(ctx, want, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err := reopened.ProviderAdoption(ctx, ProviderCodex)
	want.UpdatedAt = mustTime(t, 10)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened adoption = %+v, found=%v, err=%v, want %+v", got, found, err, want)
	}
	if err := reopened.SetProviderAdoption(ctx, ProviderAdoption{Provider: ProviderCodex, ActiveVersion: "1.3.0", ActiveCommitment: "/tools/codex@new"}, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	got, found, err = reopened.ProviderAdoption(ctx, ProviderCodex)
	if err != nil || !found || got.CandidateVersion != "" || got.ActiveVersion != "1.3.0" {
		t.Fatalf("candidate replacement = %+v, found=%v, err=%v", got, found, err)
	}
}

func TestV45HomeMigratesProviderAdoptionSchema(t *testing.T) {
	store, path := newTestStore(t)
	downgradeHome(t, store, v45UserVersion)
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, found, err := reopened.ProviderAdoption(context.Background(), ProviderClaudeCode); err != nil || found || got != (ProviderAdoption{}) {
		t.Fatalf("migrated empty adoption = %+v, found=%v, err=%v", got, found, err)
	}
}
