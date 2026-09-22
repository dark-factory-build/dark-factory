//go:build darwin

package install

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func moveTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestMoveHomeRelocatesStoppedTemporaryHome(t *testing.T) {
	parent := moveTempDir(t)
	from := filepath.Join(parent, "old")
	to := filepath.Join(parent, "new")
	if _, err := Init(context.Background(), from); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(from, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MoveHome(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("old home remains: %v", err)
	}
	if _, err := Doctor(context.Background(), to); err != nil {
		t.Fatal(err)
	}
}

func TestMoveHomeRefusesExistingTargetWithoutMutation(t *testing.T) {
	parent := moveTempDir(t)
	from := filepath.Join(parent, "old")
	to := filepath.Join(parent, "new")
	if _, err := Init(context.Background(), from); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(to, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MoveHome(context.Background(), from, to); err == nil {
		t.Fatal("move accepted an existing target")
	}
	if _, err := Doctor(context.Background(), from); err != nil {
		t.Fatalf("source changed after refusal: %v", err)
	}
}

func TestMoveHomeRefusesConcurrentOperationalWriter(t *testing.T) {
	parent := moveTempDir(t)
	from := filepath.Join(parent, "old")
	to := filepath.Join(parent, "new")
	if _, err := Init(context.Background(), from); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenOperationalHome(context.Background(), from)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := MoveHome(context.Background(), from, to); !errors.Is(err, ErrBusy) {
		t.Fatalf("move with concurrent writer = %v, want busy", err)
	}
	if _, err := withServiceMutation(context.Background(), from, func(*serviceHomeCapability) (ServiceStatus, error) {
		t.Fatal("service mutation entered while operational lease was held")
		return ServiceStatus{}, nil
	}); !errors.Is(err, ErrBusy) {
		t.Fatalf("service mutation with operational writer = %v, want busy", err)
	}
	if _, err := Doctor(context.Background(), from); err != nil {
		t.Fatalf("source after concurrent-writer refusal = %v", err)
	}
}

func TestMoveHomeKeepsPublishedDestinationLockedUntilValidation(t *testing.T) {
	parent := moveTempDir(t)
	from := filepath.Join(parent, "old")
	to := filepath.Join(parent, "new")
	if _, err := Init(context.Background(), from); err != nil {
		t.Fatal(err)
	}
	var openErr error
	moveAfterPublishHook = func() {
		opened, err := OpenOperationalHome(context.Background(), to)
		openErr = err
		if opened != nil {
			_ = opened.Close()
		}
	}
	defer func() { moveAfterPublishHook = nil }()
	if err := MoveHome(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(openErr, ErrBusy) {
		t.Fatalf("post-publish operational open = %v, want busy", openErr)
	}
}

func TestMoveHomeRelocatesToPathsWithURIDelimiters(t *testing.T) {
	for _, name := range []string{"new?copy", "new#1"} {
		t.Run(name, func(t *testing.T) {
			parent := moveTempDir(t)
			from := filepath.Join(parent, "old")
			to := filepath.Join(parent, name)
			if _, err := Init(context.Background(), from); err != nil {
				t.Fatal(err)
			}
			if err := MoveHome(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if _, err := Doctor(context.Background(), to); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("parent after move = %v, %v; want only %q", entries, err, name)
			}
		})
	}
}

func TestMoveHomeRefusesDestinationCreatedDuringStaging(t *testing.T) {
	parent := moveTempDir(t)
	from := filepath.Join(parent, "old")
	to := filepath.Join(parent, "new")
	if _, err := Init(context.Background(), from); err != nil {
		t.Fatal(err)
	}
	moveBeforePublishHook = func() {
		if err := os.Mkdir(to, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { moveBeforePublishHook = nil }()
	if err := MoveHome(context.Background(), from, to); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("move onto a destination created during staging = %v, want EEXIST", err)
	}
	if _, err := Doctor(context.Background(), from); err != nil {
		t.Fatalf("source after refused publication = %v", err)
	}
	if entries, err := os.ReadDir(to); err != nil || len(entries) != 0 {
		t.Fatalf("foreign destination after refused publication = %v, %v; want the empty directory untouched", entries, err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 2 {
		t.Fatalf("parent after refused publication = %v, %v; want only old and new", entries, err)
	}
}

func TestMoveHomeRefusesEmptyServiceArtifact(t *testing.T) {
	parent := moveTempDir(t)
	from := filepath.Join(parent, "old")
	to := filepath.Join(parent, "new")
	if _, err := Init(context.Background(), from); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ServiceDirectoryPath(from), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MoveHome(context.Background(), from, to); !errors.Is(err, ErrBusy) {
		t.Fatalf("move with empty service artifact = %v, want busy", err)
	}
	if _, err := os.Stat(from); err != nil {
		t.Fatalf("source changed after empty service-artifact refusal: %v", err)
	}
}

func TestRepairMovedWorktreesRestoresExternalMetadataOnVerificationFailure(t *testing.T) {
	ctx := context.Background()
	root := moveTempDir(t)
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	if err := os.WriteFile(filepath.Join(repository, "README"), []byte("proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README")
	runGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	commit := runGit(t, repository, "rev-parse", "HEAD")
	commitBytes, err := hex.DecodeString(string(commit))
	if err != nil {
		t.Fatal(err)
	}
	from, to := filepath.Join(root, "old"), filepath.Join(root, "new")
	if _, err := Init(ctx, from); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	store, err := home.OpenStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, kernel.NewProject{ID: mustProjectID(20), Name: "project", Root: repository}, mustTimeForMove(1))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(21), ProjectID: project.ID, Name: "worker", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 10}, mustTimeForMove(2))
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(22), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: mustIncarnationID(23), Title: "repair"}, mustTimeForMove(3))
	if err != nil {
		t.Fatal(err)
	}
	changeID := mustChangeID(24)
	changePath := filepath.Join(ChangesPath(from), hex.EncodeToString(changeID.Bytes()))
	runGit(t, repository, "worktree", "add", "-q", changePath, "HEAD")
	stat, err := os.Stat(repository)
	if err != nil {
		t.Fatal(err)
	}
	repositoryStat := stat.Sys().(*syscall.Stat_t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(from, databaseName)
	raw, err := sql.Open("sqlite3", "file:"+database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, object_format, base_commit, repository_dev, repository_inode, head_commit, prepared_at_ms, available_at_ms, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'available', 'sha1', ?, ?, ?, ?, 4, 5, 3, 1, 5)`, changeID.Bytes(), project.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), commitBytes, int64(repositoryStat.Dev), int64(repositoryStat.Ino), bytes.Repeat([]byte{9}, 20)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(from, to); err != nil {
		t.Fatal(err)
	}
	before := string(runGit(t, repository, "worktree", "list", "--porcelain"))
	if _, err := repairMovedWorktrees(ctx, filepath.Join(to, databaseName), to); err == nil {
		t.Fatal("repair accepted a Change with the wrong head")
	}
	after := string(runGit(t, repository, "worktree", "list", "--porcelain"))
	if before != after {
		t.Fatalf("external Git metadata changed after failed repair:\nbefore=%s\nafter=%s", before, after)
	}
	raw, err = sql.Open("sqlite3", "file:"+filepath.Join(to, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE changes SET repository_dev = repository_dev + 1`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	before = string(runGit(t, repository, "worktree", "list", "--porcelain"))
	if _, err := repairMovedWorktrees(ctx, filepath.Join(to, databaseName), to); err == nil {
		t.Fatal("repair accepted a replacement repository identity")
	}
	after = string(runGit(t, repository, "worktree", "list", "--porcelain"))
	if before != after {
		t.Fatalf("external Git metadata changed after identity refusal:\nbefore=%s\nafter=%s", before, after)
	}
}

func mustTimeForMove(value int64) kernel.UnixMillis {
	at, _ := kernel.NewUnixMillis(value)
	return at
}

func TestMoveHomeRelocatesPopulatedProjectChangeAndTerminalRun(t *testing.T) {
	ctx := context.Background()
	root := moveTempDir(t)
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	if err := os.WriteFile(filepath.Join(repository, "README"), []byte("proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README")
	runGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	commitHex := string(runGit(t, repository, "rev-parse", "HEAD"))
	commit, err := hex.DecodeString(commitHex)
	if err != nil {
		t.Fatal(err)
	}
	unicodeParent := filepath.Join(root, "josé")
	if err := os.Mkdir(unicodeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	from, to := filepath.Join(unicodeParent, "old"), filepath.Join(unicodeParent, "new")
	if _, err := Init(ctx, from); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	store, err := home.OpenStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projectTime, _ := kernel.NewUnixMillis(1)
	agentTime, _ := kernel.NewUnixMillis(2)
	taskTime, _ := kernel.NewUnixMillis(3)
	project, err := store.CreateProject(ctx, kernel.NewProject{ID: mustProjectID(1), Name: "project", Root: repository}, projectTime)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(2), ProjectID: project.ID, Name: "worker", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 10}, agentTime)
	if err != nil {
		t.Fatal(err)
	}
	incarnation := mustIncarnationID(4)
	task, err := store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(3), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnation, Title: "terminal"}, taskTime)
	if err != nil {
		t.Fatal(err)
	}
	changeID, runID := mustChangeID(5), mustRunID(6)
	changePath := filepath.Join(ChangesPath(from), hex.EncodeToString(changeID.Bytes()))
	runGit(t, repository, "worktree", "add", "-q", changePath, "HEAD")
	stat, err := os.Stat(repository)
	if err != nil {
		t.Fatal(err)
	}
	identity := stat.Sys().(*syscall.Stat_t)
	database := filepath.Join(from, databaseName)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite3", "file:"+database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	baseDigest := bytes.Repeat([]byte{1}, 32)
	if _, err := raw.Exec(`INSERT INTO changes(id, project_id, task_id, task_incarnation_id, phase, object_format, base_commit, repository_dev, repository_inode, head_commit, prepared_at_ms, available_at_ms, revision, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, 'available', 'sha1', ?, ?, ?, ?, 4, 5, 3, 1, 5)`, changeID.Bytes(), project.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), commit, int64(identity.Dev), int64(identity.Ino), commit); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO runs(id, project_id, agent_id, task_id, task_incarnation_id, admitted_task_work_revision, change_id, admitted_change_revision, role, provider, verification_policy, phase, proposal_kind, proposal_result, terminal_kind, terminal_result, credential_digest, result_proof_digest, credential_revoked_at_ms, provider_exit_kind, provider_exit_sequence, provider_exit_code, provider_exit_at_ms, runner_exit_kind, runner_exit_sequence, runner_exit_code, runner_exit_at_ms, revision, admitted_at_ms, running_at_ms, finalizing_at_ms, terminal_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, 1, ?, 1, 'worker', 'codex', 'none', 'terminal', 'succeeded', 'done', 'succeeded', 'done', ?, ?, 8, 'code', 1, 0, 9, 'code', 1, 0, 9, 1, 6, 7, 8, 9, 9)`, runID.Bytes(), project.ID.Bytes(), agent.ID.Bytes(), task.ID.Bytes(), task.IncarnationID.Bytes(), changeID.Bytes(), baseDigest, bytes.Repeat([]byte{2}, 32)); err != nil {
		t.Fatal(err)
	}
	resourceRows := []struct {
		id         []byte
		kind, path string
		pid, pgid  any
		birth      any
	}{
		{idBytes(8), "runtime_root", filepath.Join(from, "runtime"), nil, nil, nil},
		{idBytes(9), "runner_process", "", 123, 123, bytes.Repeat([]byte{3}, 32)},
		{idBytes(10), "provider_process", "", 124, 124, bytes.Repeat([]byte{4}, 32)},
		{idBytes(11), "provider_group", "", 124, 124, bytes.Repeat([]byte{4}, 32)},
	}
	for _, resource := range resourceRows {
		if _, err := raw.Exec(`INSERT INTO resources(id, run_id, kind, state, path, path_dev, path_inode, pid, pgid, birth_digest, revision, declared_at_ms, activated_at_ms, updated_at_ms, released_at_ms) VALUES(?, ?, ?, 'released', NULLIF(?, ''), CASE WHEN ? = 'runtime_root' THEN 1 ELSE NULL END, CASE WHEN ? = 'runtime_root' THEN 2 ELSE NULL END, ?, ?, ?, 2, 6, 7, 9, 9)`, resource.id, runID.Bytes(), resource.kind, resource.path, resource.kind, resource.kind, resource.pid, resource.pgid, resource.birth); err != nil {
			t.Fatalf("insert %s resource: %v", resource.kind, err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO terminal_sessions(id, run_id, state, revision, declared_at_ms, activated_at_ms, closed_at_ms, updated_at_ms) VALUES(?, ?, 'closed', 1, 6, 7, 9, 9)`, idBytes(7), runID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE tasks SET status = 'succeeded', result = 'done', completed_at_ms = 9, revision = 2, updated_at_ms = 9 WHERE id = ?`, task.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE changes SET phase = 'retained', settled_run_id = ?, revision = 4, updated_at_ms = 9 WHERE id = ?`, runID.Bytes(), changeID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := MoveHome(ctx, from, to); err != nil {
		t.Fatal(err)
	}
	validated, err := OpenOperationalHome(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	if err := validated.Close(); err != nil {
		t.Fatal(err)
	}
	newHome, err := OpenOperationalHome(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	newStore, err := newHome.OpenStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := newStore.Snapshot(ctx)
	if err != nil || snapshot.Factory.ActiveRuns != 0 {
		t.Fatalf("new-home status = %+v, %v", snapshot.Factory, err)
	}
	_ = newStore.Close()
	_ = newHome.Close()
}

func idBytes(value byte) []byte { return bytes.Repeat([]byte{value}, 16) }
func mustProjectID(v byte) kernel.ProjectID {
	id, _ := kernel.ProjectIDFromBytes(idBytes(v))
	return id
}
func mustAgentID(v byte) kernel.AgentID { id, _ := kernel.AgentIDFromBytes(idBytes(v)); return id }
func mustTaskID(v byte) kernel.TaskID   { id, _ := kernel.TaskIDFromBytes(idBytes(v)); return id }
func mustIncarnationID(v byte) kernel.IncarnationID {
	id, _ := kernel.IncarnationIDFromBytes(idBytes(v))
	return id
}
func mustChangeID(v byte) kernel.ChangeID { id, _ := kernel.ChangeIDFromBytes(idBytes(v)); return id }
func mustRunID(v byte) kernel.RunID       { id, _ := kernel.RunIDFromBytes(idBytes(v)); return id }
func runGit(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return bytes.TrimSpace(output)
}
