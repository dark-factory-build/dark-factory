//go:build darwin

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const manageTestLabel = "com.dark-factory.test.manage"

type manageFixture struct {
	root      string
	home      string
	userHome  string
	plistDir  string
	sourceDir string
	config    ServiceConfig
}

func newManageFixture(t *testing.T) *manageFixture {
	t.Helper()
	root := serviceTestRoot(t)
	fixture := &manageFixture{
		root:     root,
		home:     filepath.Join(root, "factory"),
		userHome: filepath.Join(root, "user"),
		plistDir: filepath.Join(root, "plists"),
	}
	fixture.sourceDir = filepath.Join(root, "release")
	fixture.config = ServiceConfig{Label: manageTestLabel, PlistDirectory: fixture.plistDir}
	for _, path := range []string{fixture.userHome, fixture.plistDir, fixture.sourceDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Init(context.Background(), fixture.home); err != nil {
		t.Fatal(err)
	}
	for _, name := range serviceBinaryNames {
		if err := os.WriteFile(filepath.Join(fixture.sourceDir, name), []byte("#!binary "+name+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture *manageFixture) plistPath() string {
	return filepath.Join(fixture.plistDir, fixture.config.plistName())
}

func (fixture *manageFixture) service() string {
	return fmt.Sprintf("gui/%d/%s", os.Geteuid(), fixture.config.Label)
}

func (fixture *manageFixture) printRunning(pid int) launchctlResult {
	output := fixture.service() + " = {\n\tpath = " + fixture.plistPath() + "\n\tstate = running\n\tprogram = " + serviceProgramPath(fixture.home) + "\n\tpid = " + fmt.Sprint(pid) + "\n}\n"
	return launchctlResult{status: 0, stdout: []byte(output)}
}

func (fixture *manageFixture) printAbsent() []launchctlResult {
	return []launchctlResult{{status: launchctlNotFound}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n")}}
}

func (fixture *manageFixture) install(t *testing.T, launchctl launchctlRun) ServiceStatus {
	t.Helper()
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, launchctl)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return status
}

func TestServiceReceiptIsCanonicalAndTamperEvident(t *testing.T) {
	digest := strings.Repeat("ab", sha256.Size)
	receipt := serviceReceipt{Version: serviceReceiptVersion, Label: manageTestLabel, PlistPath: "/private/tmp/plists/" + manageTestLabel + ".plist", PlistDigest: digest, ProgramDigest: digest}
	body, err := encodeServiceReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseServiceReceipt(body)
	if err != nil || parsed != receipt {
		t.Fatalf("round trip = %+v, %v", parsed, err)
	}
	for name, mutated := range map[string][]byte{
		"trailing junk":  append(append([]byte{}, body...), 'x'),
		"reordered keys": []byte(`{"label":"` + manageTestLabel + `","version":1,"plist_path":"/private/tmp/p.plist","plist_digest":"` + digest + `","program_digest":"` + digest + `"}` + "\n"),
		"empty":          nil,
	} {
		if _, err := parseServiceReceipt(mutated); !errors.Is(err, ErrServiceReceipt) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	for name, bad := range map[string]serviceReceipt{
		"version":     {Version: 2, Label: receipt.Label, PlistPath: receipt.PlistPath, PlistDigest: digest, ProgramDigest: digest},
		"label":       {Version: 1, Label: ".bad.", PlistPath: receipt.PlistPath, PlistDigest: digest, ProgramDigest: digest},
		"digest hex":  {Version: 1, Label: receipt.Label, PlistPath: receipt.PlistPath, PlistDigest: strings.Repeat("zz", sha256.Size), ProgramDigest: digest},
		"plist name":  {Version: 1, Label: receipt.Label, PlistPath: "/private/tmp/plists/other.plist", PlistDigest: digest, ProgramDigest: digest},
		"digest size": {Version: 1, Label: receipt.Label, PlistPath: receipt.PlistPath, PlistDigest: digest[:10], ProgramDigest: digest},
	} {
		if _, err := encodeServiceReceipt(bad); !errors.Is(err, ErrServiceReceipt) {
			t.Fatalf("%s encoded: %v", name, err)
		}
	}
}

func TestServiceLabelValidationIsExact(t *testing.T) {
	for _, label := range []string{DefaultServiceLabel, "com.dark-factory.e2e.12345", "A1-b.c"} {
		if !validServiceLabel(label) {
			t.Fatalf("valid label rejected: %q", label)
		}
	}
	for _, label := range []string{"", ".lead", "trail.", "a..b", "with space", "with/slash", "with\x00nul", strings.Repeat("a", serviceMaxLabelBytes+1)} {
		if validServiceLabel(label) {
			t.Fatalf("invalid label accepted: %q", label)
		}
	}
}

func TestServiceInstallLifecycleConvergesThroughEveryVerb(t *testing.T) {
	fixture := newManageFixture(t)
	install := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(4321))}
	status := fixture.install(t, install.run)
	if status != (ServiceStatus{State: ServiceRunning, PID: 4321}) {
		t.Fatalf("install status = %+v", status)
	}
	if got := install.calls[2]; len(got) != 3 || got[0] != "bootstrap" || got[1] != fmt.Sprintf("gui/%d", os.Geteuid()) || got[2] != fixture.plistPath() {
		t.Fatalf("bootstrap call = %q", install.calls[2])
	}

	// The durable artifacts exist with exact content.
	receipt, present, err := readServiceReceipt(fixture.home)
	if err != nil || !present || receipt.Label != manageTestLabel || receipt.PlistPath != fixture.plistPath() {
		t.Fatalf("receipt = %+v present=%t err=%v", receipt, present, err)
	}
	source, err := os.ReadFile(filepath.Join(fixture.sourceDir, "factoryd"))
	if err != nil {
		t.Fatal(err)
	}
	wantProgram := sha256.Sum256(source)
	if receipt.ProgramDigest != hex.EncodeToString(wantProgram[:]) {
		t.Fatal("receipt program digest is not the installed binary's digest")
	}
	for _, name := range serviceBinaryNames {
		installed, err := os.ReadFile(filepath.Join(ServiceDirectoryPath(fixture.home), "bin", "current", name))
		if err != nil || len(installed) == 0 {
			t.Fatalf("installed %s = %v", name, err)
		}
	}

	// Status proves running through the receipt.
	inspect := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(4321)}}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, inspect.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 4321}) {
		t.Fatalf("running status = %+v, %v", status, err)
	}

	// Idempotent install over a running service recognizes and changes nothing.
	again := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(4321)}}
	status, err = serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, again.run)
	if err != nil || status.State != ServiceRunning || len(again.calls) != 1 {
		t.Fatalf("repeat install = %+v, %v, calls=%d", status, err, len(again.calls))
	}

	// Stop keeps the installation; the loaded job leaves launchd.
	stop := &recordedLaunchctl{results: append([]launchctlResult{fixture.printRunning(4321), {status: 0}}, launchctlResult{status: launchctlNotFound})}
	status, err = serviceStopAt(context.Background(), fixture.home, fixture.userHome, fixture.config, stop.run)
	if err != nil || status.State != ServiceInstalled {
		t.Fatalf("stop = %+v, %v", status, err)
	}
	if got := stop.calls[1]; len(got) != 2 || got[0] != "bootout" || got[1] != fixture.service() {
		t.Fatalf("bootout call = %q", stop.calls[1])
	}

	// Stopped status is installed, not ambiguous.
	stopped := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, stopped.run)
	if err != nil || status != (ServiceStatus{State: ServiceInstalled}) {
		t.Fatalf("stopped status = %+v, %v", status, err)
	}

	// Start bootstraps the retained plist.
	start := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(4400))}
	status, err = serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, start.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 4400}) {
		t.Fatalf("start = %+v, %v", status, err)
	}

	// Uninstall removes every artifact and proves absence.
	uninstall := &recordedLaunchctl{results: []launchctlResult{{status: 0}, {status: launchctlNotFound}}}
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, uninstall.run)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("uninstall = %+v, %v", status, err)
	}
	if _, err := os.Lstat(ServiceDirectoryPath(fixture.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service directory survived uninstall")
	}
	if _, err := os.Lstat(fixture.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist survived uninstall")
	}
	final := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, final.run)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("final status = %+v, %v", status, err)
	}
}

func TestServiceInstallRefusesForeignPlistAndResidue(t *testing.T) {
	fixture := newManageFixture(t)
	if err := os.WriteFile(fixture.plistPath(), []byte("foreign bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := &recordedLaunchctl{results: fixture.printAbsent()}
	_, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, foreign.run)
	if err == nil || errors.Is(err, ErrServiceForeign) == false && errors.Is(err, ErrServiceAmbiguous) == false {
		t.Fatalf("foreign plist install = %v", err)
	}
	if err := os.Remove(fixture.plistPath()); err != nil {
		t.Fatal(err)
	}

	// Crash residue: a receipt with no plist is surfaced as residue, install
	// refuses, and uninstall resolves it to provable absence.
	full := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(77))}
	fixture.install(t, full.run)
	if err := os.Remove(fixture.plistPath()); err != nil {
		t.Fatal(err)
	}
	residue := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err := inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, residue.run)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceResidue) {
		t.Fatalf("residue status = %+v, %v", status, err)
	}
	refuse := &recordedLaunchctl{results: fixture.printAbsent()}
	if _, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, refuse.run); !errors.Is(err, ErrServiceResidue) {
		t.Fatalf("install over residue = %v", err)
	}
	resolve := &recordedLaunchctl{results: []launchctlResult{{status: launchctlNotFound}, {status: launchctlNotFound}}}
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, resolve.run)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("uninstall residue = %+v, %v", status, err)
	}
}

func TestServiceStatusRefusesTamperedProgramAndForeignUninstall(t *testing.T) {
	fixture := newManageFixture(t)
	full := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(88))}
	fixture.install(t, full.run)

	program := serviceProgramPath(fixture.home)
	if err := os.WriteFile(program, []byte("#!tampered\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tampered := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(88)}}
	status, err := inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, tampered.run)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("tampered program status = %+v, %v", status, err)
	}

	// A foreign file inside the service tree blocks uninstall's directory
	// removal instead of being deleted.
	extra := filepath.Join(ServiceDirectoryPath(fixture.home), "bin", "current", "keepsake")
	if err := os.WriteFile(extra, []byte("not ours to delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := &recordedLaunchctl{results: []launchctlResult{{status: 0}, {status: launchctlNotFound}}}
	if _, err := serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, blocked.run); !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("uninstall with foreign file = %v", err)
	}
	if _, statErr := os.Lstat(extra); statErr != nil {
		t.Fatal("foreign file was deleted")
	}
}

func TestServiceMutationsValidateRequestsBeforeTouchingAnything(t *testing.T) {
	fixture := newManageFixture(t)
	deny := func(context.Context, ...string) launchctlResult {
		t.Fatal("launchctl invoked for an invalid request")
		return launchctlResult{}
	}
	badConfig := ServiceConfig{Label: "..bad", PlistDirectory: fixture.plistDir}
	if _, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, badConfig, fixture.sourceDir, deny); !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("bad label install = %v", err)
	}
	if _, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, "relative/source", deny); !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("bad source install = %v", err)
	}
	if _, err := serviceStartAt(nil, fixture.home, fixture.userHome, fixture.config, deny); !errors.Is(err, ErrServiceAmbiguous) { //nolint:staticcheck
		t.Fatalf("nil context start = %v", err)
	}

	// A group-writable source binary is refused before any artifact exists.
	if err := os.Chmod(filepath.Join(fixture.sourceDir, "factoryctl"), 0o770); err != nil {
		t.Fatal(err)
	}
	absent := &recordedLaunchctl{results: fixture.printAbsent()}
	if _, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, absent.run); !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("group-writable source = %v", err)
	}
	var stat unix.Stat_t
	if err := unix.Lstat(fixture.plistPath(), &stat); !errors.Is(err, unix.ENOENT) {
		t.Fatal("plist written despite refused source")
	}
	if _, present, err := readServiceReceipt(fixture.home); err != nil || present {
		t.Fatalf("receipt written despite refused source: present=%t err=%v", present, err)
	}
}
