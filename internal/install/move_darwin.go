//go:build darwin

package install

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	_ "github.com/ncruces/go-sqlite3/driver"
	"golang.org/x/sys/unix"
)

// moveAfterPublishHook is package-local test instrumentation for the narrow
// interval where the destination is visible but the source lease is retained.
var moveAfterPublishHook func()

func moveHome(ctx context.Context, from, to string) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if from == "" || to == "" || !filepath.IsAbs(from) || !filepath.IsAbs(to) || filepath.Clean(from) != from || filepath.Clean(to) != to || from == to {
		return ErrInvalidHome
	}
	if _, err := os.Lstat(to); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidHome
	}
	if info, err := os.Lstat(from); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidHome
	}
	if relative, err := filepath.Rel(from, to); err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "." {
		return ErrInvalidHome
	}
	for _, parentPath := range []string{filepath.Dir(from), filepath.Dir(to)} {
		parent, err := openParent(parentPath)
		if err != nil {
			return err
		}
		if err := parent.close(); err != nil {
			return err
		}
	}
	moveLease, err := unix.Open(from, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(moveLease)
	if err := unix.Flock(moveLease, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.Join(ErrBusy, err)
	}
	defer unix.Flock(moveLease, unix.LOCK_UN)
	lockedHome, err := OpenOperationalHome(ctx, from)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lockedHome.closeForMove()) }()
	if _, err := os.Lstat(ServiceDirectoryPath(from)); err == nil {
		return ErrBusy
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	backupDir := filepath.Join(filepath.Dir(from), "."+filepath.Base(from)+".move-backup-"+fmt.Sprint(time.Now().UnixNano()))
	stage := filepath.Join(filepath.Dir(to), "."+filepath.Base(to)+".move-stage-"+fmt.Sprint(time.Now().UnixNano()))
	for _, artifact := range []string{backupDir, stage} {
		if _, err := os.Lstat(artifact); err == nil || !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("move staging path already exists: %s", artifact)
		}
	}
	cleanupBeforePublish := func(err error) error {
		backupErr := os.RemoveAll(backupDir)
		stageErr := os.RemoveAll(stage)
		if backupErr != nil || stageErr != nil {
			return errors.Join(err, backupErr, stageErr, fmt.Errorf("move artifacts retained: backup=%s stage=%s", backupDir, stage))
		}
		return err
	}
	if err := copyTree(from, backupDir); err != nil {
		return cleanupBeforePublish(err)
	}
	if err := verifyTreeEqual(from, backupDir); err != nil {
		return cleanupBeforePublish(fmt.Errorf("verify move backup: %w", err))
	}
	if err := copyTree(from, stage); err != nil {
		return cleanupBeforePublish(err)
	}
	if err := rewriteDatabasePaths(ctx, filepath.Join(stage, databaseName), from, to); err != nil {
		return cleanupBeforePublish(err)
	}
	stagedHome, err := OpenOperationalHome(ctx, stage)
	if err != nil {
		return cleanupBeforePublish(err)
	}
	if err := stagedHome.Close(); err != nil {
		return cleanupBeforePublish(err)
	}
	if err := bindMoveLock(stage, from); err != nil {
		return cleanupBeforePublish(err)
	}
	old := from + ".move-old"
	if _, err := os.Lstat(old); err == nil || !errors.Is(err, os.ErrNotExist) {
		return cleanupBeforePublish(fmt.Errorf("move recovery path already exists: %s", old))
	}
	if err := os.Rename(from, old); err != nil {
		return cleanupBeforePublish(err)
	}
	if err := os.Rename(stage, to); err != nil {
		return errors.Join(err, rollbackMove(to, old, nil), fmt.Errorf("backup retained at %s", backupDir))
	}
	if moveAfterPublishHook != nil {
		moveAfterPublishHook()
	}
	worktreeRestore, err := repairMovedWorktrees(ctx, filepath.Join(to, databaseName), to)
	if err != nil {
		return errors.Join(err, rollbackMove(to, old, nil), fmt.Errorf("backup retained at %s", backupDir))
	}
	publishedHome, err := openOperationalHomeWithLock(ctx, to, lockedHome.state.lock)
	if err != nil {
		return errors.Join(err, worktreeRestore.rollback(), rollbackMove(to, old, nil), fmt.Errorf("backup retained at %s", backupDir))
	}
	if err := publishedHome.Close(); err != nil {
		return errors.Join(err, worktreeRestore.rollback(), rollbackMove(to, old, nil), fmt.Errorf("backup retained at %s", backupDir))
	}
	if err := worktreeRestore.cleanup(); err != nil {
		return errors.Join(err, fmt.Errorf("destination proved; backup retained at %s", backupDir))
	}
	if err := os.RemoveAll(old); err != nil {
		return errors.Join(err, fmt.Errorf("destination proved; old home retained at %s", old))
	}
	if err := os.RemoveAll(backupDir); err != nil {
		return err
	}
	return nil
}

func bindMoveLock(stage, source string) error {
	for _, name := range []string{lockAnchorName, lockName} {
		if err := os.Remove(filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	for _, name := range []string{lockName, lockAnchorName} {
		if err := os.Link(filepath.Join(source, lockName), filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	return nil
}

func rollbackMove(destination, old string, restore func() error) error {
	var restoreErr error
	if restore != nil {
		restoreErr = restore()
	}
	if err := os.RemoveAll(destination); err != nil {
		return errors.Join(restoreErr, err)
	}
	return errors.Join(restoreErr, os.Rename(old, strings.TrimSuffix(old, ".move-old")))
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported home member %s", path)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if filepath.Base(path) == lockAnchorName {
			if err := os.Link(filepath.Join(dst, lockName), target); err != nil {
				return err
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}

func verifyTreeEqual(source, copyRoot string) error {
	seen := map[string]bool{}
	if err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		seen[rel] = true
		copyPath := filepath.Join(copyRoot, rel)
		copyInfo, err := os.Lstat(copyPath)
		if err != nil {
			return err
		}
		if info.Mode() != copyInfo.Mode() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported backup member %s", path)
		}
		if info.IsDir() {
			return nil
		}
		if info.Size() != copyInfo.Size() {
			return fmt.Errorf("backup size mismatch for %s", rel)
		}
		left, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		right, err := os.ReadFile(copyPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return fmt.Errorf("backup content mismatch for %s", rel)
		}
		if filepath.Base(path) == lockAnchorName {
			leftInfo, _ := os.Stat(filepath.Join(copyRoot, lockName))
			if leftInfo == nil || leftInfo.Sys().(*syscall.Stat_t).Ino != copyInfo.Sys().(*syscall.Stat_t).Ino {
				return fmt.Errorf("backup hard-link mismatch for %s", rel)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return filepath.Walk(copyRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(copyRoot, path)
		if err != nil {
			return err
		}
		if !seen[rel] {
			return fmt.Errorf("backup has unexpected member %s", rel)
		}
		return nil
	})
}

type worktreeRepairRollback struct {
	restore func() error
	cleanup func() error
}

func (r worktreeRepairRollback) rollback() error {
	if err := r.restore(); err != nil {
		return err
	}
	return r.cleanup()
}

func repairMovedWorktrees(ctx context.Context, path, home string) (worktreeRepairRollback, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return worktreeRepairRollback{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT changes.id, projects.root, changes.repository_dev, changes.repository_inode, changes.head_commit FROM changes JOIN projects ON projects.id = changes.project_id WHERE changes.phase IN ('available', 'retained')`)
	if err != nil {
		return worktreeRepairRollback{}, err
	}
	type changeRepair struct {
		id, head []byte
		root     string
		dev, ino int64
	}
	var repairs []changeRepair
	for rows.Next() {
		var id, head []byte
		var root string
		var dev, ino int64
		if err := rows.Scan(&id, &root, &dev, &ino, &head); err != nil {
			rows.Close()
			return worktreeRepairRollback{}, err
		}
		repairs = append(repairs, changeRepair{id: append([]byte(nil), id...), head: append([]byte(nil), head...), root: root, dev: dev, ino: ino})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return worktreeRepairRollback{}, err
	}
	rows.Close()
	git, err := exec.LookPath("git")
	if err != nil {
		return worktreeRepairRollback{}, err
	}
	type gitWorktreeBackup struct {
		worktrees string
		root      string
		backup    string
		existed   bool
	}
	backups := make(map[string]gitWorktreeBackup)
	cleanup := func() error {
		var cleanupErr error
		for _, backup := range backups {
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(backup.root))
		}
		if cleanupErr != nil {
			paths := make([]string, 0, len(backups))
			for _, backup := range backups {
				paths = append(paths, backup.root)
			}
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("Git worktree backups retained: %s", strings.Join(paths, ", ")))
		}
		return cleanupErr
	}
	restore := func() error {
		var restoreErr error
		for _, backup := range backups {
			if err := os.RemoveAll(backup.worktrees); err != nil {
				restoreErr = errors.Join(restoreErr, err)
				continue
			}
			if backup.existed {
				restoreErr = errors.Join(restoreErr, copyTree(backup.backup, backup.worktrees))
			}
		}
		return restoreErr
	}
	restoreFailure := func(cause error) error {
		if restoreErr := restore(); restoreErr != nil {
			return errors.Join(cause, restoreErr, fmt.Errorf("Git worktree backup retained under external repository metadata"))
		}
		return errors.Join(cause, cleanup())
	}
	for _, repair := range repairs {
		root := repair.root
		if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
			return worktreeRepairRollback{}, errors.Join(fmt.Errorf("registered Change repository is missing: %s", root), cleanup())
		}
		info, statErr := os.Stat(root)
		if statErr != nil {
			return worktreeRepairRollback{}, errors.Join(fmt.Errorf("registered Change repository is missing: %s", root), cleanup())
		}
		identity, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int64(identity.Dev) != repair.dev || int64(identity.Ino) != repair.ino {
			return worktreeRepairRollback{}, errors.Join(fmt.Errorf("registered Change repository identity changed: %s", root), cleanup())
		}
		commonOutput, runErr := exec.CommandContext(ctx, git, "-C", root, "rev-parse", "--git-common-dir").Output()
		if runErr != nil {
			return worktreeRepairRollback{}, errors.Join(fmt.Errorf("find Git metadata for %s: %w", root, runErr), cleanup())
		}
		common := strings.TrimSpace(string(commonOutput))
		if !filepath.IsAbs(common) {
			common = filepath.Join(root, common)
		}
		common, err = filepath.Abs(common)
		if err != nil {
			return worktreeRepairRollback{}, errors.Join(err, cleanup())
		}
		worktrees := filepath.Join(common, "worktrees")
		if _, ok := backups[worktrees]; ok {
			continue
		}
		backupDir, mkErr := os.MkdirTemp(filepath.Dir(worktrees), ".factory-move-worktrees-")
		if mkErr != nil {
			return worktreeRepairRollback{}, errors.Join(mkErr, cleanup())
		}
		backup := gitWorktreeBackup{worktrees: worktrees, root: backupDir, backup: filepath.Join(backupDir, "worktrees")}
		if _, statErr := os.Stat(worktrees); statErr == nil {
			backup.existed = true
			if copyErr := copyTree(worktrees, backup.backup); copyErr != nil {
				_ = os.RemoveAll(backupDir)
				return worktreeRepairRollback{}, errors.Join(copyErr, cleanup())
			}
		}
		backups[worktrees] = backup
	}
	for _, repair := range repairs {
		root := repair.root
		changePath := filepath.Join(ChangesPath(home), hex.EncodeToString(repair.id))
		if info, statErr := os.Stat(changePath); statErr != nil || !info.IsDir() {
			err = fmt.Errorf("registered Change worktree is missing: %s", changePath)
			return worktreeRepairRollback{}, restoreFailure(err)
		}
		command := exec.CommandContext(ctx, git, "-C", root, "worktree", "repair", changePath)
		if output, runErr := command.CombinedOutput(); runErr != nil {
			err = fmt.Errorf("repair Change worktree %s: %w: %s", changePath, runErr, strings.TrimSpace(string(output)))
			return worktreeRepairRollback{}, restoreFailure(err)
		}
		top, runErr := exec.CommandContext(ctx, git, "-C", changePath, "rev-parse", "--show-toplevel").Output()
		if runErr != nil || strings.TrimSpace(string(top)) != changePath {
			err = fmt.Errorf("Change worktree identity mismatch: %s", changePath)
			return worktreeRepairRollback{}, restoreFailure(err)
		}
		if len(repair.head) > 0 {
			head, headErr := exec.CommandContext(ctx, git, "-C", changePath, "rev-parse", "HEAD").Output()
			if headErr != nil || !strings.EqualFold(strings.TrimSpace(string(head)), hex.EncodeToString(repair.head)) {
				err = fmt.Errorf("Change worktree head mismatch: %s", changePath)
				return worktreeRepairRollback{}, restoreFailure(err)
			}
		}
	}
	return worktreeRepairRollback{restore: restore, cleanup: cleanup}, nil
}

func rewriteDatabasePaths(ctx context.Context, path, from, to string) error {
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE phase <> 'terminal'`).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrBusy
	}
	// These are the schema's path-bearing columns. In particular, do not
	// rewrite prose, JSON, result payloads, or digest inputs that merely happen
	// to contain the old spelling.
	for _, pathColumn := range []struct{ table, column string }{
		{"projects", "root"}, {"project_repositories", "root"},
		{"accounts", "home"}, {"resources", "path"},
	} {
		qtable, qcolumn := quoteSQLiteIdentifier(pathColumn.table), quoteSQLiteIdentifier(pathColumn.column)
		if _, err := tx.ExecContext(ctx, `UPDATE `+qtable+` SET `+qcolumn+` = CASE WHEN `+qcolumn+` = ? THEN ? ELSE ? || substr(`+qcolumn+`, ?) END WHERE `+qcolumn+` = ? OR instr(`+qcolumn+`, ? || '/') = 1`, from, to, to, utf8.RuneCountInString(from)+1, from, from); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
