package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntakeServiceAssetsUseOnlyReleaseOrHomebrewPrefix(t *testing.T) {
	for _, brew := range []bool{false, true} {
		root := t.TempDir()
		executable := filepath.Join(root, "factoryctl")
		if brew {
			executable = filepath.Join(root, "bin", "factoryctl")
		}
		script := filepath.Join(root, "libexec", "dark-factory", "factory-autonomy.py")
		if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
		got, err := installedIntakeController(executable)
		if err != nil || got != script {
			t.Fatalf("asset=%s err=%v", got, err)
		}
		if err := os.Chmod(script, 0777); err != nil {
			t.Fatal(err)
		}
		if _, err := installedIntakeController(executable); err == nil {
			t.Fatal("writable shared controller accepted")
		}
		if err := os.Remove(script); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "other.py"), script); err != nil {
			t.Fatal(err)
		}
		if _, err := installedIntakeController(executable); err == nil {
			t.Fatal("symlink controller accepted")
		}
	}
}

func TestIntakeServiceRecordedAssetRequiresExactDigest(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	script := filepath.Join(root, "release", "libexec", "dark-factory", "factory-autonomy.py")
	if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home+".intake", 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("fixture")
	if err := os.WriteFile(script, data, 0755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	receipt, _ := json.Marshal(map[string]string{"script": script, "script_digest": hex.EncodeToString(digest[:]), "factoryctl": filepath.Join(root, "release", "factoryctl")})
	if err := os.WriteFile(home+".intake/service.json", receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := recordedIntakeController(home); err != nil || got != script {
		t.Fatalf("receipt asset=%s err=%v", got, err)
	}
	if err := os.WriteFile(script, []byte("changed"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := recordedIntakeController(home); err == nil {
		t.Fatal("changed recorded script executed")
	}
}

func TestIntakeServiceRefusesAttemptBeforeAnyMutation(t *testing.T) {
	var output bytes.Buffer
	result := runIntakeService(context.Background(), []string{"install", "--home", "/unused"}, func(key string) string {
		if key == "DARK_FACTORY_ATTEMPT_TOKEN_FILE" {
			return "/attempt"
		}
		return ""
	}, &output, &output)
	if result != exitFailure || !strings.Contains(output.String(), "local operator") {
		t.Fatalf("result=%d output=%s", result, output.String())
	}
}

func TestIntakeServicePolicyAcknowledgementOnlyAppliesReviewedMigration(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--home", "/unused", "--acknowledge-policy-narrowing"},
		{"migrate", "--home", "/unused", "--legacy-config", "/legacy", "--preview", "--acknowledge-policy-narrowing"},
	} {
		var output bytes.Buffer
		if result := runIntakeService(context.Background(), args, func(string) string { return "" }, &output, &output); result != exitUsage {
			t.Fatalf("unreviewed policy acknowledgement: %d %s", result, output.String())
		}
	}
}

func TestManagedIntakeStatusStaysInsideServiceJSON(t *testing.T) {
	home := filepath.Join(t.TempDir(), "factory")
	if err := os.MkdirAll(home+".intake", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+".intake/service.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	status, ready := managedIntakeStatus(context.Background(), home, func(string) string { return "" }, func(_ context.Context, args []string, _ func(string) string, stdout, _ io.Writer) int {
		calls++
		if len(args) != 3 || args[0] != "status" || args[1] != "--home" || args[2] != home {
			t.Errorf("intake status arguments = %q", args)
		}
		_, _ = io.WriteString(stdout, `{"state":"scheduled","sync":{"state":"ok"}}`)
		return 0
	})
	if !ready || calls != 1 || status.State != "scheduled" || string(status.Sync) != `{"state":"ok"}` {
		t.Fatalf("managed intake status = %+v ready=%t calls=%d", status, ready, calls)
	}

	status, ready = managedIntakeStatus(context.Background(), filepath.Join(t.TempDir(), "missing"), func(string) string { return "" }, nil)
	if !ready || status.State != "absent" {
		t.Fatalf("missing receipt = %+v ready=%t", status, ready)
	}
}

func TestManagedIntakeStatusRefusesOrphanedManagedService(t *testing.T) {
	home := filepath.Join(t.TempDir(), "factory")
	status, ready := managedIntakeStatusWithAssets(context.Background(), home, func(string) string { return "" }, func(_ context.Context, args []string, _ func(string) string, _, stderr io.Writer) int {
		if len(args) != 3 || args[0] != "status" {
			t.Errorf("orphan status arguments = %q", args)
		}
		_, _ = io.WriteString(stderr, "managed service plist has no ownership receipt; refusing launchctl")
		return exitFailure
	}, true)
	if ready || status.State != "unavailable" {
		t.Fatalf("orphaned managed service = %+v ready=%t", status, ready)
	}
}

func TestManagedIntakeStatusReportsInvalidOutputAsUnavailable(t *testing.T) {
	status := intakeServiceResult([]byte("not json"), "status")
	if status.State != "unavailable" || !strings.Contains(status.Detail, "status") {
		t.Fatalf("invalid service output = %+v", status)
	}
	if status = intakeServiceResult([]byte(`{"state":"stopped"}`), "status"); status.State != "stopped" {
		t.Fatalf("stopped service state = %+v", status)
	}
}

func TestManagedIntakeInstallKeepsLegacyScheduleSeparate(t *testing.T) {
	status, diagnostic, failed := installManagedIntake(context.Background(), "/private/tmp/factory", func(string) string { return "" }, func(_ context.Context, args []string, _ func(string) string, _, stderr io.Writer) int {
		if len(args) != 3 || args[0] != "install" {
			t.Errorf("legacy install arguments = %q", args)
		}
		_, _ = io.WriteString(stderr, "legacy intake already schedules this factory; migrate its configuration and journal before installing managed intake")
		return exitFailure
	})
	if failed || len(diagnostic) != 0 || status.State != "unavailable" || !strings.Contains(status.Detail, "explicit migration") {
		t.Fatalf("legacy install = %+v diagnostic=%q failed=%t", status, diagnostic, failed)
	}
}
