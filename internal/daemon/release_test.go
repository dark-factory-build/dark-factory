package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// No other test in this package reaches GitHub: the shared window starts closed
// and the release test opens it against its own server.
func init() { releaseNext = time.Now().Add(24 * time.Hour) }

func TestPublishedReleaseIsValidatedCachedAndNeverBlocksTheConsole(t *testing.T) {
	bodies := make(chan string, 1)
	agents := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		agent := request.Header.Get("User-Agent")
		agents <- agent
		// GitHub refuses a REST request that does not name its application, so
		// every assertion below depends on this request carrying that name.
		if agent != releaseUserAgent {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(writer, <-bodies)
	}))
	defer server.Close()
	read := func(body string) (api.PublishedRelease, error) {
		bodies <- body
		return readPublishedRelease(context.Background(), server.Client(), server.URL)
	}

	value, err := read(`{"tag_name":"v0.4.2","html_url":"https://evil.test/anything"}`)
	if err != nil || value != (api.PublishedRelease{Version: "v0.4.2", URL: releaseTagURL + "v0.4.2"}) {
		t.Fatalf("release = %+v, err = %v", value, err)
	}
	if agent := <-agents; agent != releaseUserAgent {
		t.Fatalf("user agent = %q, want %q", agent, releaseUserAgent)
	}
	for _, body := range []string{
		`{"tag_name":"v0.4.2","draft":true}`,
		`{"tag_name":"v0.4.2","prerelease":true}`,
		`{"tag_name":"../../other/repo/releases/tag/v1"}`,
		`{"tag_name":"v1 <script>"}`,
		`{"tag_name":""}`,
		`not json`,
	} {
		if _, err := read(body); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}

	// Waits for the started refresh to finish and reports the window it left.
	settle := func() time.Time {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			releaseMu.Lock()
			pending, next := releasePending, releaseNext
			releaseMu.Unlock()
			if !pending {
				return next
			}
			if time.Now().After(deadline) {
				t.Fatal("the release refresh never settled")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	open := func(body string) {
		t.Helper()
		releaseMu.Lock()
		cached := releaseValue
		releaseNext = time.Time{}
		releaseMu.Unlock()
		bodies <- body
		// The refresh cannot retake the lock before this call returns, so a
		// console request is answered from the cache it had, never from GitHub.
		if served := latestPublishedRelease(); served != cached {
			t.Fatalf("a console request waited for GitHub: %+v", served)
		}
	}

	releaseMu.Lock()
	releaseEndpoint, releaseClient = server.URL, server.Client()
	releaseValue, releasePending = api.PublishedRelease{}, false
	releaseMu.Unlock()

	// A refused read closes the window exactly as a served one does, so a
	// rate-limited or offline host never reaches out more often than a healthy one.
	open(`not json`)
	refused := settle()
	if value := latestPublishedRelease(); value != (api.PublishedRelease{}) {
		t.Fatalf("a refused read invented a release: %+v", value)
	}
	if refused.Before(time.Now().Add(releaseInterval - time.Minute)) {
		t.Fatalf("a refused read reopened the window early: %v", refused)
	}

	open(`{"tag_name":"v0.4.3"}`)
	served := settle()
	if value := latestPublishedRelease(); value.Version != "v0.4.3" {
		t.Fatalf("the cached release never arrived: %+v", value)
	}
	if served.Before(time.Now().Add(releaseInterval - time.Minute)) {
		t.Fatalf("a served release did not cache: %v", served)
	}
	releaseMu.Lock()
	releaseNext = time.Now().Add(24 * time.Hour)
	releaseMu.Unlock()
}

// releaseFixture is a publishing project with a registered checkout of
// factoryd's own repository, a home with its service directory, a live run
// (settle ends it), and every release side effect replaced by a recorder.
func releaseFixture(t *testing.T) (*dispatchFixture, func(), chan string) {
	t.Helper()
	fixture, project, _, settle := publishedTask(t)
	ctx := context.Background()
	repository, err := kernel.RepositoryIDFromBytes(mustIDBytes(t, testID(240)))
	if err != nil {
		t.Fatal(err)
	}
	identity := kernel.RepositorySourceIdentity{RootDevice: 1, RootInode: 4, GitDevice: 1, GitInode: 5, OriginDigest: [32]byte{2}, PublicationRepository: selfRepository}
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: repository, ProjectID: project, Name: "self", Root: "/self-repository", BaseRef: "main", SourceIdentity: &identity}, mustKernelTime(t, 4)); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "factory")
	if err := os.MkdirAll(install.ServiceDirectoryPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.ConfigureGate(home, "/usr/bin:/bin")
	events := make(chan string, 8)
	build, upgrade, exit, limit, poll := releaseBuild, releaseUpgrade, releaseExit, releaseDrainLimit, releaseDrainPoll
	t.Cleanup(func() {
		releaseBuild, releaseUpgrade, releaseExit, releaseDrainLimit, releaseDrainPoll = build, upgrade, exit, limit, poll
	})
	releaseBuild = func(_ context.Context, _ *Daemon, root string, _ change.RepositorySourceIdentity, sha, _ string) (buildinfo.Identity, error) {
		events <- "build " + root
		value, _ := buildinfo.Expected("1.2.3", sha, "darwin/arm64")
		return value, nil
	}
	releaseUpgrade = func(_ context.Context, upgradeHome, _ string, identity buildinfo.Identity, userVersion int) error {
		if _, err := os.Stat(install.UpgradeBackupPath(upgradeHome)); err != nil || userVersion != kernel.SchemaVersion {
			t.Errorf("upgrade without a backup: %v, user_version %d", err, userVersion)
		}
		events <- "upgrade " + identity.Source()
		return nil
	}
	releaseExit = func() { events <- "exit" }
	releaseDrainLimit, releaseDrainPoll = 200*time.Millisecond, 5*time.Millisecond
	return fixture, settle, events
}

func awaitRelease(t *testing.T, daemon *Daemon, sha string, settled func(kernel.ProductionDelivery) bool) kernel.ProductionDelivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		delivery, err := daemon.Release(context.Background(), sha, false)
		if err == nil && settled(delivery) {
			return delivery
		}
		if time.Now().After(deadline) {
			t.Fatalf("release = %+v, %v", delivery, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReleaseBuildsDrainsBacksUpAndSwapsThenRestarts(t *testing.T) {
	fixture, settle, events := releaseFixture(t)
	settle()
	sha := strings.Repeat("a", 40)
	delivery, err := fixture.daemon.Release(context.Background(), sha, true)
	if err != nil || delivery.State != "running" || delivery.Phase != "build" || delivery.ID != "release:"+sha {
		t.Fatalf("start = %+v, %v", delivery, err)
	}
	for _, want := range []string{"build /self-repository", "upgrade " + sha, "exit"} {
		if got := <-events; got != want {
			t.Fatalf("event %q, want %q", got, want)
		}
	}
	delivery = awaitRelease(t, fixture.daemon, sha, func(value kernel.ProductionDelivery) bool { return value.Phase == "trial" })
	if delivery.State != "running" || !fixture.daemon.releaseHold.Load() {
		t.Fatalf("restarting release = %+v, hold %t", delivery, fixture.daemon.releaseHold.Load())
	}
	// The restarted build records the outcome.
	if err := fixture.daemon.FinishRelease(context.Background(), sha, "verified", ""); err != nil {
		t.Fatal(err)
	}
	if delivery, err = fixture.daemon.Release(context.Background(), sha, false); err != nil || delivery.State != "verified" || delivery.VerifiedAt == 0 || delivery.Phase != "" {
		t.Fatalf("verified release = %+v, %v", delivery, err)
	}
}

func TestReleaseDrainTimeoutReleasesTheHoldAndNeverWritesDispatch(t *testing.T) {
	fixture, _, events := releaseFixture(t)
	before, err := fixture.store.Factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("b", 40)
	if _, err := fixture.daemon.Release(context.Background(), sha, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.daemon.Release(context.Background(), strings.Repeat("c", 40), true); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("second concurrent release = %v", err)
	}
	delivery := awaitRelease(t, fixture.daemon, sha, func(value kernel.ProductionDelivery) bool { return value.State == "failed" })
	if delivery.Phase != "drain" || !strings.HasPrefix(delivery.Reason, "drain_timeout: run ") || !strings.HasSuffix(delivery.Reason, "is admitted") {
		t.Fatalf("timed out release = %+v", delivery)
	}
	if fixture.daemon.releaseHold.Load() || fixture.daemon.releaseBusy.Load() {
		t.Fatal("a timed out drain kept admission held")
	}
	if got := <-events; got != "build /self-repository" || len(events) != 0 {
		t.Fatalf("timed out release went on to %q", got)
	}
	after, err := fixture.store.Factory(context.Background())
	if err != nil || after.Revision != before.Revision || after.DispatchEnabled != before.DispatchEnabled {
		t.Fatalf("dispatch moved: %+v -> %+v, %v", before, after, err)
	}
}

func TestReleaseRecordsAFailedUpgradeAndNeverRestarts(t *testing.T) {
	fixture, settle, events := releaseFixture(t)
	settle()
	releaseUpgrade = func(context.Context, string, string, buildinfo.Identity, int) error {
		events <- "upgrade"
		return errors.New("receipt")
	}
	sha := strings.Repeat("d", 40)
	if _, err := fixture.daemon.Release(context.Background(), sha, true); err != nil {
		t.Fatal(err)
	}
	delivery := awaitRelease(t, fixture.daemon, sha, func(value kernel.ProductionDelivery) bool { return value.State == "failed" })
	if delivery.Phase != "swap" || delivery.Reason != "upgrade: receipt" || fixture.daemon.releaseHold.Load() {
		t.Fatalf("failed upgrade = %+v, hold %t", delivery, fixture.daemon.releaseHold.Load())
	}
	if <-events != "build /self-repository" || <-events != "upgrade" || len(events) != 0 {
		t.Fatal("a failed upgrade went on to restart")
	}
}

func TestTickReleasesEachNewBaseTipOnce(t *testing.T) {
	fixture, settle, events := releaseFixture(t)
	settle()
	head, poll := releaseHead, releasePoll
	t.Cleanup(func() { releaseHead, releasePoll = head, poll })
	var tip atomic.Value
	releaseHead = func(_ context.Context, _ *Daemon, root string) (string, error) {
		if root != "/self-repository" {
			t.Errorf("observed %q", root)
		}
		return tip.Load().(string), nil
	}
	releasePoll = 0
	tick := func(sha string) {
		t.Helper()
		tip.Store(sha)
		fixture.daemon.tickRelease(context.Background())
		time.Sleep(50 * time.Millisecond)
	}
	moved, failed, newer := strings.Repeat("e", 40), strings.Repeat("f", 40), strings.Repeat("1", 40)
	tick(moved)
	if got := <-events; got != "build /self-repository" {
		t.Fatalf("moved tip: %q", got)
	}
	<-events
	<-events
	awaitRelease(t, fixture.daemon, moved, func(value kernel.ProductionDelivery) bool { return value.Phase == "trial" })
	if err := fixture.daemon.FinishRelease(context.Background(), moved, "verified", ""); err != nil {
		t.Fatal(err)
	}
	fixture.daemon.releaseHold.Store(false)
	fixture.daemon.releaseBusy.Store(false)
	tick(moved)
	if len(events) != 0 {
		t.Fatal("a released tip was released again")
	}

	releaseUpgrade = func(context.Context, string, string, buildinfo.Identity, int) error { return errors.New("receipt") }
	tick(failed)
	awaitRelease(t, fixture.daemon, failed, func(value kernel.ProductionDelivery) bool { return value.State == "failed" })
	<-events
	tick(failed)
	if len(events) != 0 {
		t.Fatal("a failed tip was retried")
	}
	tick(newer)
	if got := <-events; got != "build /self-repository" {
		t.Fatalf("newer tip: %q", got)
	}
	awaitRelease(t, fixture.daemon, newer, func(value kernel.ProductionDelivery) bool { return value.State == "failed" })
}

func TestSchedulerAdmitsNothingWhileAReleaseHoldsAdmission(t *testing.T) {
	daemon := newSchedulerTestDaemon(t)
	daemon.releaseHold.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var calls atomic.Int64
	go func() {
		done <- daemon.RunScheduler(ctx, SupervisorSpec{scheduledAttempt: func(_ context.Context, spec SupervisorSpec) (kernel.Run, error) {
			calls.Add(1)
			spec.admissionObserved(false)
			return kernel.Run{}, fmt.Errorf("%w: empty", kernel.ErrConflict)
		}})
	}()
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("the scheduler admitted work during a release hold")
	}
	daemon.releaseHold.Store(false)
	daemon.notifyScheduler()
	waitSchedulerCalls(t, &calls, 1)
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatal(err)
	}
}
