//go:build darwin

package linear

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/install"
)

func TestConnectionSurvivesRestartAndDisconnectRevokesReads(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "home")
	if _, err := install.Init(ctx, path); err != nil {
		t.Fatal(err)
	}
	home, err := install.OpenOperationalHome(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	fake := testHost(t, func(map[string]any) (int, string) {
		return 200, `{"data":{"teams":{"nodes":[{"id":"` + testTeam + `","name":"Engineering","key":"ENG"}]}}}`
	})
	h.client = fake.client
	if _, err := h.Connect(ctx, fake.key); err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	home, err = install.OpenOperationalHome(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	h, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	h.client = fake.client
	if teams, err := h.Teams(ctx); err != nil || len(teams) != 1 {
		t.Fatalf("restart: %+v %v", teams, err)
	}
	if err := h.Disconnect(); err != nil {
		t.Fatal(err)
	}
	h, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	h.client = fake.client
	if _, err := h.Teams(ctx); err != ErrUnavailable {
		t.Fatal("disconnected key retained", err)
	}
}
