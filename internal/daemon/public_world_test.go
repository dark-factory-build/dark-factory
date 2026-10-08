package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestPublicWorldPublishesOnlyWhenOptedInChangedAndDue(t *testing.T) {
	ctx := context.Background()
	fixture := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	project := relaySeedProject(t, fixture, 2, "published project")
	fixture.daemon.home = t.TempDir()
	// 100 s into a five-minute bucket, so generated_at holds for 200 s more.
	start := time.Unix(1_750_000_000, 0)
	now := start
	fixture.daemon.now = func() time.Time { return now }
	var sent [][]byte
	var feed publicFeed
	connection := new(int)
	tick := func(at time.Duration) {
		now = start.Add(at)
		fixture.daemon.publishPublicWorld(ctx, &feed, func(world []byte) any { sent = append(sent, world); return connection }, func() any { return connection })
	}
	configure := func(body string) {
		if err := os.WriteFile(filepath.Join(fixture.daemon.home, "observe.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	served := func() []byte {
		world, err := (&browserBackend{owner: fixture.daemon}).PublicWorld(ctx, project.ID.String())
		if err != nil {
			t.Fatal(err)
		}
		return world
	}

	// Off by default: one empty publish retracts an earlier run's world, then
	// nothing however long the factory runs.
	tick(0)
	tick(30 * time.Second)
	if len(sent) != 1 || sent[0] == nil || len(sent[0]) != 0 {
		t.Fatalf("unconfigured publishes = %q, want one empty retraction", sent)
	}

	// Opting in is a change, but it waits out the interval since the last publish.
	configure(`{"public_project":"` + project.ID.String() + `"}`)
	tick(59 * time.Second)
	if len(sent) != 1 {
		t.Fatalf("published %d times inside the interval", len(sent))
	}
	tick(60 * time.Second)
	if len(sent) != 2 || !bytes.Equal(sent[1], served()) {
		t.Fatalf("published %q, want exactly the served PublicWorld %q", sent[len(sent)-1], served())
	}

	// Due but unchanged: nothing. Changed (a new five-minute bucket): published.
	tick(150 * time.Second)
	if len(sent) != 2 {
		t.Fatalf("an unchanged world was republished")
	}
	tick(200 * time.Second)
	if len(sent) != 3 || bytes.Equal(sent[2], sent[1]) || !bytes.Equal(sent[2], served()) {
		t.Fatalf("the changed world was not published exactly: %q", sent[2:])
	}

	// Opting out retracts once the interval allows.
	configure(`{}`)
	tick(230 * time.Second)
	tick(260 * time.Second)
	tick(400 * time.Second)
	if len(sent) != 4 || len(sent[3]) != 0 {
		t.Fatalf("opting out published %q, want one empty retraction", sent[3:])
	}

	// A new relay connection may have lost what the old one queued, so the
	// retraction is sent again on it, once.
	connection = new(int)
	tick(460 * time.Second)
	tick(520 * time.Second)
	if len(sent) != 5 || len(sent[4]) != 0 {
		t.Fatalf("after reconnecting published %q, want the retraction once more", sent[4:])
	}
}
