package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
		if served := latestPublishedRelease(nil); served != cached {
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
	if value := latestPublishedRelease(nil); value != (api.PublishedRelease{}) {
		t.Fatalf("a refused read invented a release: %+v", value)
	}
	if refused.Before(time.Now().Add(releaseInterval - time.Minute)) {
		t.Fatalf("a refused read reopened the window early: %v", refused)
	}

	open(`{"tag_name":"v0.4.3"}`)
	served := settle()
	if value := latestPublishedRelease(nil); value.Version != "v0.4.3" {
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
	fixture.daemon.ConfigureHost(home, "/usr/bin:/bin")
	events := make(chan string, 8)
	build, worker, upgrade, exit, limit, poll := releaseBuild, releaseWorker, releaseUpgrade, releaseExit, releaseDrainLimit, releaseDrainPoll
	t.Cleanup(func() {
		releaseBuild, releaseWorker, releaseUpgrade, releaseExit, releaseDrainLimit, releaseDrainPoll = build, worker, upgrade, exit, limit, poll
	})
	releaseBuild = func(_ context.Context, _ *Daemon, root string, _ change.RepositorySourceIdentity, sha, _ string) (buildinfo.Identity, error) {
		events <- "build " + root
		value, _ := buildinfo.Expected("1.2.3", sha, "darwin/arm64")
		return value, nil
	}
	releaseWorker = func(context.Context, *Daemon, string, string, string) error { return nil }
	releaseUpgrade = func(_ context.Context, upgradeHome, _ string, identity buildinfo.Identity) error {
		if _, err := os.Stat(install.UpgradeBackupPath(upgradeHome)); err != nil {
			t.Errorf("upgrade without a backup: %v", err)
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
	releaseWorker = func(_ context.Context, _ *Daemon, _, _, sha string) error {
		events <- "worker " + sha
		return nil
	}
	sha := strings.Repeat("a", 40)
	delivery, err := fixture.daemon.Release(context.Background(), sha, true)
	if err != nil || delivery.State != "running" || delivery.Phase != "build" || delivery.ID != "release:"+sha {
		t.Fatalf("start = %+v, %v", delivery, err)
	}
	// The Worker is deployed before factoryd is staged (#1512).
	for _, want := range []string{"build /self-repository", "worker " + sha, "upgrade " + sha, "exit"} {
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
	releaseUpgrade = func(context.Context, string, string, buildinfo.Identity) error {
		events <- "upgrade"
		return errors.New("receipt")
	}
	sha := strings.Repeat("d", 40)
	if _, err := fixture.daemon.Release(context.Background(), sha, true); err != nil {
		t.Fatal(err)
	}
	delivery := awaitRelease(t, fixture.daemon, sha, func(value kernel.ProductionDelivery) bool { return value.State == "failed" })
	if delivery.Phase != "stage" || delivery.Reason != "upgrade: receipt" || fixture.daemon.releaseHold.Load() {
		t.Fatalf("failed upgrade = %+v, hold %t", delivery, fixture.daemon.releaseHold.Load())
	}
	if <-events != "build /self-repository" || <-events != "upgrade" || len(events) != 0 {
		t.Fatal("a failed upgrade went on to restart")
	}
	// A backup never outlives its release.
	if _, err := os.Lstat(install.UpgradeBackupPath(fixture.daemon.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed release left its backup: %v", err)
	}
}

// A Worker that does not deploy stops the release before factoryd is staged.
func TestReleaseRecordsAFailedWorkerAndNeverStagesFactoryd(t *testing.T) {
	fixture, settle, events := releaseFixture(t)
	settle()
	releaseWorker = func(context.Context, *Daemon, string, string, string) error {
		return errors.New("release: control-plane health_failed rolled back")
	}
	sha := strings.Repeat("9", 40)
	if _, err := fixture.daemon.Release(context.Background(), sha, true); err != nil {
		t.Fatal(err)
	}
	delivery := awaitRelease(t, fixture.daemon, sha, func(value kernel.ProductionDelivery) bool { return value.State == "failed" })
	if delivery.Phase != "stage" || delivery.Reason != "worker: release: control-plane health_failed rolled back" || fixture.daemon.releaseHold.Load() {
		t.Fatalf("failed worker = %+v, hold %t", delivery, fixture.daemon.releaseHold.Load())
	}
	if <-events != "build /self-repository" || len(events) != 0 {
		t.Fatal("a failed Worker deploy went on to stage factoryd")
	}
}

// #1512: a release whose range touches control-plane/ deploys the Worker with
// scripts/release.sh; one that does not skips it.
func TestDeployWorkerRunsOnlyWhenTheControlPlaneChanged(t *testing.T) {
	tree := t.TempDir()
	git := func(args ...string) string {
		output, err := exec.Command(change.TrustedGitExecutable, append([]string{"-C", tree, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(name, content string, mode os.FileMode) {
		path := filepath.Join(tree, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "--quiet")
	write("scripts/release.sh", "#!/bin/sh\necho \"$1\" >>deployed\n[ -z \"$(cat fail 2>/dev/null)\" ] || { echo noise; echo 'release: control-plane deploy_failed' >&2; exit 1; }\n", 0o755)
	write("control-plane/worker.rs", "one\n", 0o644)
	write(".gitignore", "deployed\nfail\n", 0o644)
	git("add", "-A")
	git("commit", "--quiet", "-m", "running")
	running := git("rev-parse", "HEAD")
	write("README.md", "docs\n", 0o644)
	git("add", "-A")
	git("commit", "--quiet", "-m", "factoryd only")
	unchanged := git("rev-parse", "HEAD")
	write("control-plane/worker.rs", "two\n", 0o644)
	git("add", "-A")
	git("commit", "--quiet", "-m", "worker contract")
	changed := git("rev-parse", "HEAD")

	daemon := &Daemon{toolPath: "/usr/bin:/bin"}
	ctx := context.Background()
	deployed := func() string {
		data, _ := os.ReadFile(filepath.Join(tree, "deployed"))
		_ = os.Remove(filepath.Join(tree, "deployed"))
		return string(data)
	}
	if err := deployWorker(ctx, daemon, tree, running, unchanged); err != nil || deployed() != "" {
		t.Fatalf("unchanged control plane deployed: %v", err)
	}
	if err := deployWorker(ctx, daemon, tree, running, changed); err != nil || deployed() != changed+"\n" {
		t.Fatalf("changed control plane not deployed: %v", err)
	}
	// A build that is not a release cannot say which Worker is live.
	if err := deployWorker(ctx, daemon, tree, "", unchanged); err != nil || deployed() != unchanged+"\n" {
		t.Fatalf("unknown running build not deployed: %v", err)
	}
	write("fail", "1", 0o644)
	if err := deployWorker(ctx, daemon, tree, running, changed); err == nil || !strings.HasSuffix(err.Error(), ": release: control-plane deploy_failed") {
		t.Fatalf("failed deploy = %v", err)
	}
}

func TestTickReleasesEachNewBaseTipOnce(t *testing.T) {
	fixture, settle, events := releaseFixture(t)
	settle()
	head, poll, batch := releaseHead, releasePoll, releaseBatch
	t.Cleanup(func() { releaseHead, releasePoll, releaseBatch = head, poll, batch })
	releaseBatch = 0
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

	releaseUpgrade = func(context.Context, string, string, buildinfo.Identity) error { return errors.New("receipt") }
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

func TestTickBatchesBaseTipsIntoOneRelease(t *testing.T) {
	fixture, settle, events := releaseFixture(t)
	settle()
	head, poll, batch := releaseHead, releasePoll, releaseBatch
	t.Cleanup(func() { releaseHead, releasePoll, releaseBatch = head, poll, batch })
	var tip atomic.Value
	releaseHead = func(context.Context, *Daemon, string) (string, error) { return tip.Load().(string), nil }
	releasePoll, releaseBatch = 0, time.Hour
	tick := func(sha string) {
		t.Helper()
		tip.Store(sha)
		fixture.daemon.tickRelease(context.Background())
		time.Sleep(50 * time.Millisecond)
	}
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	tick(first)
	opened := fixture.daemon.releaseSince.Load()
	if len(events) != 0 || opened == 0 {
		t.Fatalf("a new tip did not wait: %d events, window %d", len(events), opened)
	}
	tick(second)
	if len(events) != 0 || fixture.daemon.releaseSince.Load() != opened {
		t.Fatal("a newer tip released early or reopened the window")
	}
	// The window closes: the current tip is released once, the passed one never.
	fixture.daemon.releaseSince.Store(opened - int64(releaseBatch))
	tick(second)
	if got := <-events; got != "build /self-repository" {
		t.Fatalf("closed window: %q", got)
	}
	<-events
	<-events
	awaitRelease(t, fixture.daemon, second, func(value kernel.ProductionDelivery) bool { return value.Phase == "trial" })
	if _, _, found, err := fixture.store.Delivery(context.Background(), "release:"+first); err != nil || found {
		t.Fatalf("passed tip released: %t, %v", found, err)
	}
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

// #1390: reading an older commit's release once started it and downgraded
// the factory. A release must descend from the running build.
func TestReleaseRefusesACommitOlderThanTheRunningBuild(t *testing.T) {
	tree := t.TempDir()
	git := func(args ...string) string {
		output, err := exec.Command(change.TrustedGitExecutable, append([]string{"-C", tree, "-c", "user.name=test", "-c", "user.email=test@example.invalid"}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("commit", "--quiet", "--allow-empty", "-m", "older")
	older := git("rev-parse", "HEAD")
	git("commit", "--quiet", "--allow-empty", "-m", "running")
	running := git("rev-parse", "HEAD")
	ctx := context.Background()
	if err := releaseDescends(ctx, "ROOT", tree, running, older); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("older release = %v", err)
	}
	// A running build the checkout lacks names the fix, not a downgrade.
	if err := releaseDescends(ctx, "ROOT", tree, strings.Repeat("1", 40), running); err == nil || !strings.Contains(err.Error(), "git -C ROOT fetch --unshallow origin") {
		t.Fatalf("missing running build = %v", err)
	}
	git("commit", "--quiet", "--allow-empty", "-m", "newer")
	for _, sha := range []string{running, git("rev-parse", "HEAD")} {
		if err := releaseDescends(ctx, "ROOT", tree, running, sha); err != nil {
			t.Fatalf("release %s = %v", sha, err)
		}
	}
}
