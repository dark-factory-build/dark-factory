package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// The console shows which published release exists; installing one stays a
// terminal command. One cached read of the public releases endpoint serves
// every client: a console request never waits for GitHub, an offline or
// rate-limited host keeps the previous answer or none, and a daemon no console
// asks never reaches out at all.
const (
	releaseTagURL = "https://github.com/dark-factory-build/dark-factory/releases/tag/"
	// GitHub's REST API requires a User-Agent naming the application, and a
	// library default names nothing. It deliberately carries no version or host
	// detail: this identifies the program, not the factory making the request.
	releaseUserAgent = "dark-factory-daemon (+https://github.com/dark-factory-build/dark-factory)"
	// One attempt per window, whether or not it succeeds. A refused read must
	// not make a rate-limited or offline host reach out more often than a
	// healthy one; the cached answer, or none, stands until the next window.
	releaseInterval = 6 * time.Hour
)

var (
	releaseVersion  = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	errNoRelease    = errors.New("no published release")
	releaseEndpoint = "https://api.github.com/repos/dark-factory-build/dark-factory/releases/latest"
	releaseClient   = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	releaseMu       sync.Mutex
	releaseValue    api.PublishedRelease
	releaseNext     time.Time
	releasePending  bool
)

func latestPublishedRelease(daemon *Daemon) api.PublishedRelease {
	releaseMu.Lock()
	defer releaseMu.Unlock()
	if !releasePending && !time.Now().Before(releaseNext) {
		releasePending = true
		go refreshPublishedRelease(daemon)
	}
	return releaseValue
}

func refreshPublishedRelease(daemon *Daemon) {
	releaseMu.Lock()
	client, endpoint := releaseClient, releaseEndpoint
	releaseMu.Unlock()
	// The client's own timeout bounds the whole exchange, body included.
	value, err := readPublishedRelease(context.Background(), daemon.observed(client), endpoint)
	releaseMu.Lock()
	defer releaseMu.Unlock()
	releasePending, releaseNext = false, time.Now().Add(releaseInterval)
	if err == nil {
		releaseValue = value
	}
}

// The response is untrusted input: only a bounded version tag is taken from it,
// and the link is built from that tag rather than from a supplied URL.
func readPublishedRelease(ctx context.Context, client *http.Client, endpoint string) (api.PublishedRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return api.PublishedRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", releaseUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return api.PublishedRelease{}, err
	}
	defer response.Body.Close()
	const maxBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return api.PublishedRelease{}, err
	}
	var result struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if response.StatusCode != http.StatusOK || len(body) > maxBytes || json.Unmarshal(body, &result) != nil ||
		result.Draft || result.Prerelease || !releaseVersion.MatchString(result.TagName) {
		return api.PublishedRelease{}, errNoRelease
	}
	return api.PublishedRelease{Version: result.TagName, URL: releaseTagURL + result.TagName}, nil
}

// factoryd releases only itself: the registered checkout of its own
// repository, at a commit merged into its base.
// ponytail: one fixed repository and base; take them from the project when a
// factory ships a fork of itself.
const (
	selfRepository = "dark-factory-build/dark-factory"
	selfBase       = "main"
)

var (
	// releaseDrainLimit bounds how long admission stays held (#1121).
	releaseDrainLimit = 10 * time.Minute
	releaseDrainPoll  = time.Second
	// releaseBuild, releaseWorker, releaseUpgrade and releaseExit are
	// package-test seams.
	releaseBuild   = buildRelease
	releaseWorker  = deployWorker
	releaseUpgrade = install.ServiceUpgrade
	// SIGTERM shuts down cleanly; factoryd then supervises the staged build's
	// trial because the upgrade marker names another build.
	releaseExit = func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }
	// releasePoll spaces base observations; releaseHead is a package-test seam.
	releasePoll = 2 * time.Minute
	// releaseBatch is the one restart window: the first unreleased tip seen
	// starts it, and when it closes the tip then current is released, so
	// merges inside it share one restart. Restarts stay at most one per
	// window and lag at most a window plus a poll; drainForRelease already
	// waits for live runs.
	releaseBatch = 15 * time.Minute
	releaseHead  = observeBaseHead
)

// tickRelease releases the base tip into this factory once per tip. Only a
// home with a registered checkout of factoryd's own repository releases
// itself. A tip with any release record (running, verified or failed) is never
// started again: a failed release waits for a newer tip or `factoryctl
// release`. The newest tip wins when the releaseBatch window closes, so
// merges in between are released together.
func (daemon *Daemon) tickRelease(ctx context.Context) {
	// The wall clock, not daemon.now: this cadence is not factory time.
	now := time.Now()
	if daemon.home == "" || daemon.releaseBusy.Load() || now.Before(daemon.releaseDue) {
		return
	}
	daemon.releaseDue = now.Add(releasePoll)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		_, root, _, err := daemon.selfRepositorySource(ctx)
		if err != nil {
			return
		}
		head, err := releaseHead(ctx, daemon, root)
		if err != nil {
			LogFactoryd(daemon.log, "factoryd: release: observe %s: %v\n", selfBase, err)
			return
		}
		_, _, found, err := daemon.store.Delivery(ctx, "release:"+head)
		if err != nil || found {
			if err == nil {
				daemon.releaseSince.Store(0)
			}
			return
		}
		daemon.releaseSince.CompareAndSwap(0, now.UnixNano())
		if now.Sub(time.Unix(0, daemon.releaseSince.Load())) >= releaseBatch {
			_, _ = daemon.Release(ctx, head, true)
		}
	}()
}

// observeBaseHead reads the base tip from the checkout's origin over the git
// protocol, which spends no GitHub REST quota.
func observeBaseHead(ctx context.Context, daemon *Daemon, root string) (string, error) {
	command := exec.CommandContext(ctx, change.TrustedGitExecutable, "-C", root, "ls-remote", "--exit-code", "--refs", "origin", "refs/heads/"+selfBase)
	command.Env = daemon.toolEnvironment()
	output, err := command.Output()
	fields := strings.Fields(string(output))
	if err == nil && (len(fields) != 2 || fields[1] != "refs/heads/"+selfBase || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(fields[0])) {
		err = fmt.Errorf("unexpected ls-remote output %q", output)
	}
	if err != nil {
		return "", err
	}
	return fields[0], nil
}

// Release starts (start) or reads the release of merged commit sha into this
// factory's own service. The record is the production delivery release:<sha>.
func (daemon *Daemon) Release(ctx context.Context, sha string, start bool) (kernel.ProductionDelivery, error) {
	_, existing, found, err := daemon.store.Delivery(ctx, "release:"+sha)
	if err != nil || !start {
		if err == nil && !found {
			err = kernel.ErrNotFound
		}
		return existing, err
	}
	if daemon.home == "" {
		return existing, fmt.Errorf("%w: factoryd has no home to release into", kernel.ErrConflict)
	}
	_, upgrading, err := install.ReadUpgradeMarker(daemon.home)
	if err != nil {
		return existing, err
	}
	if upgrading || !daemon.releaseBusy.CompareAndSwap(false, true) {
		if found && existing.State == "running" {
			return existing, nil
		}
		return existing, fmt.Errorf("%w: a release is already running", kernel.ErrConflict)
	}
	project, root, source, err := daemon.selfRepositorySource(ctx)
	delivery := kernel.ProductionDelivery{ID: "release:" + sha, Kind: "runtime", Destination: "factoryd", Revision: sha, State: "running", Phase: "build", PullRequests: []uint64{}}
	if buildinfo.Current().Source() == sha {
		delivery.State, delivery.Phase = "verified", ""
	}
	if err == nil {
		err = daemon.writeRelease(ctx, project, &delivery)
	}
	if err != nil || delivery.State == "verified" {
		daemon.releaseBusy.Store(false)
		return delivery, err
	}
	go daemon.release(project, root, source, delivery)
	return delivery, nil
}

func (daemon *Daemon) writeRelease(ctx context.Context, project kernel.ProjectID, delivery *kernel.ProductionDelivery) error {
	now := daemon.now().UnixMilli()
	delivery.UpdatedAt = now
	if delivery.State == "verified" {
		delivery.VerifiedAt = now
	}
	if len(delivery.Reason) > 2048 {
		delivery.Reason = delivery.Reason[:2048]
	}
	at, err := kernel.NewUnixMillis(now)
	if err == nil {
		err = daemon.store.RecordDelivery(ctx, project, selfRepository, *delivery, at)
	}
	return err
}

// FinishRelease records the outcome of release:<sha> after the restart.
func (daemon *Daemon) FinishRelease(ctx context.Context, sha, state, reason string) error {
	project, delivery, found, err := daemon.store.Delivery(ctx, "release:"+sha)
	if err != nil || !found {
		return err
	}
	delivery.State, delivery.Phase, delivery.Reason = state, "", reason
	if err = daemon.writeRelease(ctx, project, &delivery); err != nil {
		return err
	}
	if state == "verified" {
		at, err := daemon.timestamp()
		if err != nil {
			return err
		}
		_, err = daemon.store.ExpireBlockedTasks(ctx, at, at)
		return err
	}
	return nil
}

func (daemon *Daemon) selfRepositorySource(ctx context.Context) (project kernel.ProjectID, root string, source change.RepositorySourceIdentity, err error) {
	projects, err := daemon.store.PublishingProjects(ctx)
	for _, project = range projects {
		repositories, _ := daemon.store.ProjectRepositories(ctx, project)
		for _, repository := range repositories {
			identity, verified, _ := daemon.store.RepositorySourceIdentity(ctx, repository.ID)
			if repository.Enabled && verified && strings.EqualFold(identity.PublicationRepository, selfRepository) {
				rootIdentity, rootErr := change.NewRepositoryIdentity(identity.RootDevice, identity.RootInode)
				gitIdentity, gitErr := change.NewRepositoryIdentity(identity.GitDevice, identity.GitInode)
				return project, repository.Root, change.RepositorySourceIdentity{Root: rootIdentity, Git: gitIdentity, OriginDigest: identity.OriginDigest}, errors.Join(rootErr, gitErr)
			}
		}
	}
	return project, "", source, errors.Join(err, fmt.Errorf("%w: no registered checkout of %s", kernel.ErrNotFound, selfRepository))
}

// release builds, drains, backs up and stages, then shuts this build down to
// supervise the staged build's trial. Nothing launchd runs changes until that
// trial promotes it.
func (daemon *Daemon) release(project kernel.ProjectID, root string, source change.RepositorySourceIdentity, delivery kernel.ProductionDelivery) {
	ctx := daemon.cleanupCtx
	fail := func(reason string) {
		// Nothing was staged, so the backup has no release to outlive.
		_ = install.RemoveUpgrade(daemon.home)
		daemon.releaseHold.Store(false)
		daemon.releaseBusy.Store(false)
		delivery.State, delivery.Reason = "failed", reason
		_ = daemon.writeRelease(context.WithoutCancel(ctx), project, &delivery)
	}
	directory, err := os.MkdirTemp("", "dark-factory-release-")
	if err != nil {
		fail(err.Error())
		return
	}
	defer os.RemoveAll(directory)
	identity, err := releaseBuild(ctx, daemon, root, source, delivery.Revision, directory)
	if err != nil {
		fail(err.Error())
		return
	}
	delivery.Phase = "drain"
	_ = daemon.writeRelease(ctx, project, &delivery)
	daemon.releaseHold.Store(true)
	blocking, err := daemon.drainForRelease(ctx)
	if err == nil && blocking != "" {
		err = errors.New("drain_timeout" + blocking)
	}
	if err != nil {
		fail(err.Error())
		return
	}
	delivery.Phase = "stage"
	_ = daemon.writeRelease(ctx, project, &delivery)
	// The worker record names the commit whose Worker is live. A release
	// whose factoryd later fails keeps its Worker, so the record, not the
	// running build, decides; any record but a verified one deploys again.
	_, worker, found, err := daemon.store.Delivery(ctx, "worker")
	live := ""
	if err == nil && found && worker.State == "verified" {
		live = worker.Revision
	}
	worker = kernel.ProductionDelivery{ID: "worker", Kind: "worker", Destination: "control-plane", Revision: delivery.Revision, State: "running", PullRequests: []uint64{}}
	if err := daemon.writeRelease(ctx, project, &worker); err != nil {
		fail("worker: " + err.Error())
		return
	}
	err = releaseWorker(ctx, daemon, filepath.Join(directory, "tree"), live, delivery.Revision)
	worker.State = "verified"
	if err != nil {
		worker.State, worker.Reason = "failed", err.Error()
	}
	_ = daemon.writeRelease(context.WithoutCancel(ctx), project, &worker)
	if err != nil {
		fail("worker: " + err.Error())
		return
	}
	backup := install.UpgradeBackupPath(daemon.home)
	_ = os.Remove(backup) // BackupTo refuses whatever this could not remove.
	if err := daemon.store.BackupTo(ctx, backup); err != nil {
		fail("backup: " + err.Error())
		return
	}
	if err := releaseUpgrade(ctx, daemon.home, filepath.Join(directory, "bin"), identity); err != nil {
		fail("upgrade: " + err.Error())
		return
	}
	delivery.Phase = "trial"
	_ = daemon.writeRelease(ctx, project, &delivery)
	releaseExit()
}

// drainForRelease waits, admission held, until every live run either ended
// or can be adopted across the restart. It returns the run still blocking
// when the limit passes.
func (daemon *Daemon) drainForRelease(ctx context.Context) (string, error) {
	deadline := time.Now().Add(releaseDrainLimit)
	for {
		runs, err := daemon.store.RecoverableRuns(ctx)
		if err != nil {
			return "", err
		}
		blocking := ""
		for _, recovered := range runs {
			run := recovered.Run
			// A running attempt behind its runner's takeover endpoint is
			// adopted by the next daemon; every other live run must end.
			if _, err := os.Stat(filepath.Join(install.RuntimesPath(daemon.home), run.ID.String(), runner.TakeoverSocketName)); run.Phase == kernel.RunRunning && err == nil {
				continue
			}
			blocking = ": run " + run.ID.String() + " is " + run.Phase.String()
			break
		}
		if blocking == "" || time.Now().After(deadline) {
			return blocking, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(releaseDrainPoll):
		}
	}
}

// buildRelease builds the three binaries at sha, from a base-only clone of
// the registered checkout, into directory/bin as an exact release identity.
func buildRelease(ctx context.Context, daemon *Daemon, root string, source change.RepositorySourceIdentity, sha, directory string) (buildinfo.Identity, error) {
	tree := filepath.Join(directory, "tree")
	if err := change.ReviewCheckout(ctx, change.TrustedGitExecutable, root, source, tree, "", sha, sha, selfBase); err != nil {
		return buildinfo.Identity{}, fmt.Errorf("release checkout: %w", err)
	}
	if running := buildinfo.Current(); running.Release() {
		if err := releaseDescends(ctx, root, tree, running.Source(), sha); err != nil {
			return buildinfo.Identity{}, err
		}
	}
	// /usr/bin/env resolves go on the operator's tool path, not ours.
	return buildinfo.BuildRelease(ctx, tree, sha, runtime.GOOS+"/"+runtime.GOARCH, filepath.Join(directory, "bin"), daemon.toolEnvironment(), func(command *exec.Cmd) {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
		command.WaitDelay = 5 * time.Second
	})
}

// deployWorker deploys the control-plane Worker at sha with the release
// checkout's scripts/release.sh, which rolls back a Worker that does not come
// up, whenever control-plane/ differs from the live Worker's commit (always
// when that is unknown). It runs before factoryd is staged, so factoryd and
// the Worker it calls are released together (#1512).
func deployWorker(ctx context.Context, daemon *Daemon, tree, live, sha string) error {
	if live != "" {
		if _, err := gitOutput(ctx, filepath.Join(tree, ".git"), "diff", "--quiet", live, sha, "--", "control-plane"); err == nil {
			return nil
		}
	}
	command := exec.CommandContext(ctx, filepath.Join(tree, "scripts", "release.sh"), sha)
	command.Dir, command.Env = tree, daemon.toolEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 5 * time.Second
	if output, err := command.CombinedOutput(); err != nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		return fmt.Errorf("%w: %s", err, lines[len(lines)-1])
	}
	return nil
}

// releaseDescends refuses a commit that is not the running build or one of
// its descendants, so a release never downgrades the factory (#1390).
func releaseDescends(ctx context.Context, root, tree, running, sha string) error {
	_, err := gitOutput(ctx, filepath.Join(tree, ".git"), "merge-base", "--is-ancestor", running, sha)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return fmt.Errorf("%w: %s does not descend from the running build %s", kernel.ErrConflict, sha, running)
	}
	if err != nil {
		return fmt.Errorf("%w: the running build %s is not in the release checkout's history: unshallow the registered checkout (git -C %s fetch --unshallow origin) or install a build of %s", kernel.ErrConflict, running, root, selfBase)
	}
	return nil
}

// ConfigureHost sets the factory home and the operator's tool path.
func (daemon *Daemon) ConfigureHost(home, toolPath string) {
	daemon.home, daemon.toolPath = home, toolPath
}

// ConfigureLog sets the process stderr boundary used by daemon-owned logs.
func (daemon *Daemon) ConfigureLog(writer io.Writer) {
	if writer != nil {
		daemon.log = writer
	}
}

// toolEnvironment is the operator's tool path plus an allowlist, so no GitHub
// or provider token reaches a release build.
func (daemon *Daemon) toolEnvironment() []string {
	environment := []string{"PATH=" + daemon.toolPath}
	for _, value := range os.Environ() {
		if slices.Contains([]string{"HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TERM"}, strings.SplitN(value, "=", 2)[0]) {
			environment = append(environment, value)
		}
	}
	return environment
}
