//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func (fixture *manageFixture) printIdle() launchctlResult {
	output := fixture.service() + " = {\n\tpath = " + fixture.plistPath() + "\n\tstate = not running\n\tprogram = " + serviceProgramPath(fixture.home) + "\n}\n"
	return launchctlResult{status: 0, stdout: []byte(output)}
}

func (fixture *manageFixture) printAbsent() []launchctlResult {
	return []launchctlResult{{status: launchctlNotFound}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n")}}
}

func manageBinaryBody(name, build string) []byte {
	return []byte("#!binary " + name + " " + build + "\n")
}

// writeSourceBinaries makes the fixture's sibling set a distinct build, so the
// next install is an upgrade instead of the recognized repeat.
func (fixture *manageFixture) writeSourceBinaries(t *testing.T, build string) {
	t.Helper()
	for _, name := range serviceBinaryNames {
		if err := os.WriteFile(filepath.Join(fixture.sourceDir, name), manageBinaryBody(name, build), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// assertInstalledBuild proves every installed binary is that build's and that
// the receipt names that build's factoryd — the agreement every service verb
// depends on.
func (fixture *manageFixture) assertInstalledBuild(t *testing.T, build string) {
	t.Helper()
	current := filepath.Join(ServiceDirectoryPath(fixture.home), "bin", "current")
	for _, name := range serviceBinaryNames {
		got, err := os.ReadFile(filepath.Join(current, name))
		if err != nil || !bytes.Equal(got, manageBinaryBody(name, build)) {
			t.Fatalf("installed %s = %q, want build %q (%v)", name, got, build, err)
		}
	}
	program := sha256.Sum256(manageBinaryBody(serviceProgramName, build))
	receipt, present, err := readServiceReceipt(fixture.home)
	if err != nil || !present || receipt.ProgramDigest != hex.EncodeToString(program[:]) {
		t.Fatalf("receipt = %+v present=%t err=%v, want the %q program digest", receipt, present, err, build)
	}
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
	alreadyStopped := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err = serviceStopAt(context.Background(), fixture.home, fixture.userHome, fixture.config, alreadyStopped.run)
	if err != nil || status.State != ServiceInstalled || len(alreadyStopped.calls) != 2 {
		t.Fatalf("repeat stop = %+v, %v, calls=%q", status, err, alreadyStopped.calls)
	}
	loadedIdleStop := &recordedLaunchctl{results: []launchctlResult{fixture.printIdle(), {status: 0}, {status: launchctlNotFound}}}
	status, err = serviceStopAt(context.Background(), fixture.home, fixture.userHome, fixture.config, loadedIdleStop.run)
	if err != nil || status.State != ServiceInstalled || len(loadedIdleStop.calls) != 3 || loadedIdleStop.calls[1][0] != "bootout" {
		t.Fatalf("loaded-idle stop = %+v, %v, calls=%q", status, err, loadedIdleStop.calls)
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
	loadedIdleStart := &recordedLaunchctl{results: []launchctlResult{fixture.printIdle(), {status: 0}, {status: launchctlNotFound}, {status: 0}, fixture.printRunning(4500)}}
	status, err = serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, loadedIdleStart.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 4500}) || len(loadedIdleStart.calls) != 5 || loadedIdleStart.calls[1][0] != "bootout" || loadedIdleStart.calls[3][0] != "bootstrap" {
		t.Fatalf("loaded-idle start = %+v, %v, calls=%q", status, err, loadedIdleStart.calls)
	}

	// Uninstall removes every artifact and proves absence.
	uninstall := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(4400), {status: 0}, {status: launchctlNotFound}}}
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

func TestServiceInstallUpgradesADifferentBuildInPlace(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(100))}
	if status := fixture.install(t, first.run); status != (ServiceStatus{State: ServiceRunning, PID: 100}) {
		t.Fatalf("install status = %+v", status)
	}
	fixture.assertInstalledBuild(t, "first")
	homeBefore := snapshotServiceTrees(t, fixture.home)

	// Same sibling set: the install is recognized, launchd is only observed.
	repeat := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(100)}}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, repeat.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 100}) {
		t.Fatalf("repeat install = %+v, %v", status, err)
	}
	if len(repeat.calls) != 1 || repeat.calls[0][0] != "print" {
		t.Fatalf("repeat install verbs = %q", repeat.calls)
	}
	fixture.assertInstalledBuild(t, "first")

	// A different sibling set is moved into place: bootout, then bootstrap.
	fixture.writeSourceBinaries(t, "second")
	upgrade := &recordedLaunchctl{results: []launchctlResult{
		fixture.printRunning(100),
		{status: 0}, {status: launchctlNotFound},
		{status: 0}, fixture.printRunning(200),
	}}
	status, err = serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, upgrade.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 200}) {
		t.Fatalf("upgrade = %+v, %v", status, err)
	}
	verbs := []string{}
	for _, call := range upgrade.calls {
		verbs = append(verbs, call[0])
	}
	if strings.Join(verbs, ",") != "print,bootout,print,bootstrap,print" {
		t.Fatalf("upgrade verbs = %q", verbs)
	}
	if got := upgrade.calls[3]; len(got) != 3 || got[1] != fmt.Sprintf("gui/%d", os.Geteuid()) || got[2] != fixture.plistPath() {
		t.Fatalf("upgrade bootstrap call = %q", got)
	}
	fixture.assertInstalledBuild(t, "second")

	// The upgrade is a service-directory operation only: the data home, its
	// database, socket directory, and operator token are untouched.
	if homeAfter := snapshotServiceTrees(t, fixture.home); !reflect.DeepEqual(homeBefore, homeAfter) {
		t.Fatalf("upgrade changed the data home\nbefore: %#v\nafter:  %#v", homeBefore, homeAfter)
	}

	// The upgraded installation is what status reports afterwards.
	after := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(200)}}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, after.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 200}) {
		t.Fatalf("status after upgrade = %+v, %v", status, err)
	}
}

func TestServiceInstallRefusesToUpgradeBehindAForeignReceipt(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(300))}
	fixture.install(t, first.run)
	receipt, present, err := readServiceReceipt(fixture.home)
	if err != nil || !present {
		t.Fatalf("receipt present=%t err=%v", present, err)
	}
	receiptPath := filepath.Join(ServiceDirectoryPath(fixture.home), serviceReceiptName)
	otherHomeDigest := func() string {
		_, digest, err := ServicePlist(filepath.Join(fixture.root, "other-factory"), fixture.config.Label)
		if err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(digest[:])
	}()

	// The invoking sibling set differs, so only the receipt decides whether the
	// upgrade may proceed.
	fixture.writeSourceBinaries(t, "second")
	for name, foreign := range map[string]serviceReceipt{
		"different label": {
			Version: serviceReceiptVersion, Label: manageTestLabel + ".other",
			PlistPath:   filepath.Join(fixture.plistDir, manageTestLabel+".other.plist"),
			PlistDigest: receipt.PlistDigest, ProgramDigest: receipt.ProgramDigest,
		},
		"different home": {
			Version: serviceReceiptVersion, Label: receipt.Label, PlistPath: receipt.PlistPath,
			PlistDigest: otherHomeDigest, ProgramDigest: receipt.ProgramDigest,
		},
	} {
		body, err := encodeServiceReceipt(foreign)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receiptPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		refuse := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(300)}}
		status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, refuse.run)
		if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
			t.Fatalf("%s install = %+v, %v", name, status, err)
		}
		if len(refuse.calls) != 1 || refuse.calls[0][0] != "print" {
			t.Fatalf("%s install verbs = %q", name, refuse.calls)
		}
		current := filepath.Join(ServiceDirectoryPath(fixture.home), "bin", "current")
		for _, binary := range serviceBinaryNames {
			body, readErr := os.ReadFile(filepath.Join(current, binary))
			if readErr != nil || string(body) != "#!binary "+binary+" first\n" {
				t.Fatalf("%s replaced %s: %q, %v", name, binary, body, readErr)
			}
		}
		got, readErr := os.ReadFile(receiptPath)
		if readErr != nil || string(got) != string(body) {
			t.Fatalf("%s rewrote the foreign receipt: %q, %v", name, got, readErr)
		}
	}
}

func TestServiceInstallLeavesAnUpgradeStartableWhenBootstrapFails(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(400))}
	fixture.install(t, first.run)

	fixture.writeSourceBinaries(t, "second")
	failed := &recordedLaunchctl{results: []launchctlResult{
		fixture.printRunning(400),
		{status: 0}, {status: launchctlNotFound},
		{status: 5},
	}}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, failed.run)
	if status.State != ServiceInstalled || !errors.Is(err, ErrServiceLaunchctl) {
		t.Fatalf("failed bootstrap = %+v, %v", status, err)
	}

	// The receipt already names the new binaries, so the refusal leaves an
	// installation status reports truthfully instead of residue.
	fixture.assertInstalledBuild(t, "second")
	unloaded := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, unloaded.run)
	if err != nil || status != (ServiceStatus{State: ServiceInstalled}) {
		t.Fatalf("status after failed bootstrap = %+v, %v", status, err)
	}

	start := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(401))}
	status, err = serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, start.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 401}) {
		t.Fatalf("start after failed bootstrap = %+v, %v", status, err)
	}
	fixture.assertInstalledBuild(t, "second")
}

// factoryd is published last, so a sibling that fails to copy after the bootout
// leaves the program and the receipt still agreeing: the installation survives
// as an installation, and one more command — not an uninstall — recovers it.
func TestServiceInstallLeavesAnUpgradeRecoverableWhenASiblingCopyFails(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(600))}
	fixture.install(t, first.run)

	// The invoking directory is nobody's property but the operator's: it can
	// lose a binary after the set was proved and the job was booted out.
	current := filepath.Join(ServiceDirectoryPath(fixture.home), "bin", "current")
	fixture.writeSourceBinaries(t, "second")
	booted := true
	vanish := func(_ context.Context, arguments ...string) launchctlResult {
		switch arguments[0] {
		case "print":
			if booted {
				return fixture.printRunning(600)
			}
			return launchctlResult{status: launchctlNotFound}
		case "bootout":
			booted = false
			if err := os.Remove(filepath.Join(fixture.sourceDir, "factory-runner")); err != nil {
				t.Fatal(err)
			}
			return launchctlResult{status: 0}
		default:
			return launchctlResult{status: -1, err: fmt.Errorf("unexpected verb %q", arguments[0])}
		}
	}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, vanish)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("blocked upgrade = %+v, %v", status, err)
	}
	program, err := os.ReadFile(filepath.Join(current, serviceProgramName))
	if err != nil || !bytes.Equal(program, manageBinaryBody(serviceProgramName, "first")) {
		t.Fatalf("factoryd was published before its siblings: %q, %v", program, err)
	}
	receipt, present, err := readServiceReceipt(fixture.home)
	if err != nil || !present {
		t.Fatalf("receipt present=%t err=%v", present, err)
	}
	digest := sha256.Sum256(manageBinaryBody(serviceProgramName, "first"))
	if receipt.ProgramDigest != hex.EncodeToString(digest[:]) {
		t.Fatal("the receipt no longer names the installed program")
	}

	// Status is an honest installation and start brings the daemon back, so
	// the failure never needs an uninstall.
	unloaded := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, unloaded.run)
	if err != nil || status != (ServiceStatus{State: ServiceInstalled}) {
		t.Fatalf("status after the blocked upgrade = %+v, %v", status, err)
	}
	start := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(601))}
	status, err = serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, start.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 601}) {
		t.Fatalf("start after the blocked upgrade = %+v, %v", status, err)
	}

	// The sibling set is mixed — the copy that ran published the new factoryctl
	// — but the program launchd runs and the receipt that proves it agree, and
	// that is everything any verb needs. With the invoking set whole again the
	// same install command completes the upgrade.
	fixture.writeSourceBinaries(t, "second")
	retry := &recordedLaunchctl{results: []launchctlResult{
		fixture.printRunning(601),
		{status: 0}, {status: launchctlNotFound},
		{status: 0}, fixture.printRunning(602),
	}}
	status, err = serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, retry.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 602}) {
		t.Fatalf("retried upgrade = %+v, %v", status, err)
	}
	fixture.assertInstalledBuild(t, "second")
}

// An upgrade publishes over exactly the staging names this engine writes, so
// its own crash residue must not send the operator to uninstall. Anything else
// wearing those names is not this installation's to delete.
func TestServiceUpgradeClearsItsOwnStageResidueButNotForeignBytes(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(900))}
	fixture.install(t, first.run)
	serviceDir := ServiceDirectoryPath(fixture.home)
	current := filepath.Join(serviceDir, "bin", "current")

	// A directory wearing a stage name is not an owned regular file: the
	// upgrade refuses it without booting the running daemon out.
	decoy := filepath.Join(current, ".factoryctl.stage")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.writeSourceBinaries(t, "second")
	foreign := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(900)}}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, foreign.run)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("upgrade over a foreign stage name = %+v, %v", status, err)
	}
	if len(foreign.calls) != 1 || foreign.calls[0][0] != "print" {
		t.Fatalf("upgrade over a foreign stage name verbs = %q", foreign.calls)
	}
	if _, statErr := os.Lstat(decoy); statErr != nil {
		t.Fatal("the foreign stage name was deleted")
	}
	if err := os.Remove(decoy); err != nil {
		t.Fatal(err)
	}
	fixture.assertInstalledBuild(t, "first")

	// This engine's own staged writes are its residue to resolve: the upgrade
	// clears them and completes instead of demanding an uninstall.
	stages := []string{
		filepath.Join(current, ".factoryd.stage"),
		filepath.Join(current, ".factoryctl.stage"),
		filepath.Join(current, ".factory-runner.stage"),
		filepath.Join(serviceDir, "."+serviceReceiptName+".stage"),
	}
	for _, stage := range stages {
		if err := os.WriteFile(stage, []byte("crash residue"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	upgrade := &recordedLaunchctl{results: []launchctlResult{
		fixture.printRunning(900),
		{status: 0}, {status: launchctlNotFound},
		{status: 0}, fixture.printRunning(901),
	}}
	status, err = serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, upgrade.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 901}) {
		t.Fatalf("upgrade over stage residue = %+v, %v", status, err)
	}
	for _, stage := range stages {
		if _, statErr := os.Lstat(stage); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("stage survived the upgrade: %s", stage)
		}
	}
	fixture.assertInstalledBuild(t, "second")
}

// A fresh install proves the invoking set before it creates the first
// directory, so a refusal leaves the account exactly as it found it.
func TestServiceInstallCreatesNothingForAnIncompleteInvokingSet(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	if err := os.Remove(filepath.Join(fixture.sourceDir, "factory-runner")); err != nil {
		t.Fatal(err)
	}
	absent := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, absent.run)
	if !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("incomplete fresh install = %+v, %v", status, err)
	}
	if _, statErr := os.Lstat(ServiceDirectoryPath(fixture.home)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("the refused install created the service directory")
	}
	if _, statErr := os.Lstat(fixture.plistPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("the refused install wrote the plist")
	}

	// The whole set installs normally afterwards, with no residue to resolve.
	fixture.writeSourceBinaries(t, "first")
	full := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(950))}
	if status := fixture.install(t, full.run); status.State != ServiceRunning {
		t.Fatalf("install after the refusal = %+v", status)
	}
	fixture.assertInstalledBuild(t, "first")
}

func TestServiceInstallRefusesAnIncompleteSiblingSet(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(700))}
	fixture.install(t, first.run)

	// An invoking directory missing a sibling refuses before any mutation; the
	// running installation is exactly as it was observed.
	fixture.writeSourceBinaries(t, "second")
	if err := os.Remove(filepath.Join(fixture.sourceDir, "factory-runner")); err != nil {
		t.Fatal(err)
	}
	partial := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(700)}}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, partial.run)
	if !errors.Is(err, ErrServiceAmbiguous) || status != (ServiceStatus{State: ServiceRunning, PID: 700}) {
		t.Fatalf("incomplete invoking set = %+v, %v", status, err)
	}
	if len(partial.calls) != 1 || partial.calls[0][0] != "print" {
		t.Fatalf("incomplete invoking set verbs = %q", partial.calls)
	}
	fixture.assertInstalledBuild(t, "first")

	// A source this account does not exclusively own refuses the same way.
	fixture.writeSourceBinaries(t, "second")
	if err := os.Chmod(filepath.Join(fixture.sourceDir, "factory-runner"), 0o770); err != nil {
		t.Fatal(err)
	}
	unowned := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(700)}}
	status, err = serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, unowned.run)
	if !errors.Is(err, ErrServiceForeign) || status != (ServiceStatus{State: ServiceRunning, PID: 700}) {
		t.Fatalf("group-writable invoking set = %+v, %v", status, err)
	}
	fixture.assertInstalledBuild(t, "first")
	if err := os.Chmod(filepath.Join(fixture.sourceDir, "factory-runner"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Install refuses an installed set missing a sibling, because it replaces
	// all three and cannot prove what the incomplete tree is.
	if err := os.Remove(filepath.Join(ServiceDirectoryPath(fixture.home), "bin", "current", "factoryctl")); err != nil {
		t.Fatal(err)
	}
	incomplete := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(700)}}
	status, err = serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, incomplete.run)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("incomplete installation = %+v, %v", status, err)
	}
	if len(incomplete.calls) != 1 || incomplete.calls[0][0] != "print" {
		t.Fatalf("incomplete installation verbs = %q", incomplete.calls)
	}

	// Only install refuses. The receipt names factoryd alone, so status still
	// reports the installation and start still loads it — the documented
	// diagnosis has to match what the operator will actually see.
	observed := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(700)}}
	status, err = inspectServiceAtHome(context.Background(), fixture.home, fixture.userHome, fixture.config, observed.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 700}) {
		t.Fatalf("status of the incomplete installation = %+v, %v", status, err)
	}
	restart := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(702))}
	status, err = serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, restart.run)
	if err != nil || status != (ServiceStatus{State: ServiceRunning, PID: 702}) {
		t.Fatalf("start of the incomplete installation = %+v, %v", status, err)
	}

	// Uninstall is the route back, and the same command installs again.
	remove := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(700), {status: 0}, {status: launchctlNotFound}}}
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, remove.run)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("uninstall of the incomplete installation = %+v, %v", status, err)
	}
	again := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(701))}
	if status := fixture.install(t, again.run); status.State != ServiceRunning {
		t.Fatalf("install after uninstall = %+v", status)
	}
	fixture.assertInstalledBuild(t, "second")
}

func TestReplaceExactFileRewritesOnlyThisInstallationsBytes(t *testing.T) {
	root := serviceTestRoot(t)
	directory := filepath.Join(root, "durable")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "receipt")
	previous, contents := []byte("previous\n"), []byte("contents\n")

	// Nothing to replace: the caller proved a file it can no longer find.
	if err := replaceExactFile(directory, "receipt", previous, contents, 0o600); !errors.Is(err, ErrServiceResidue) {
		t.Fatalf("missing file = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the refusal created the file")
	}

	// Different bytes: something replaced the file after it was read.
	foreign := []byte("someone else's bytes\n")
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceExactFile(directory, "receipt", previous, contents, 0o600); !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("foreign bytes = %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, foreign) {
		t.Fatalf("foreign bytes mutated: %q, %v", body, err)
	}

	// The exact bytes are rewritten, and a repeat is the recognized no-op.
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"replace", "repeat"} {
		if err := replaceExactFile(directory, "receipt", previous, contents, 0o600); err != nil {
			t.Fatalf("%s = %v", pass, err)
		}
		if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, contents) {
			t.Fatalf("%s left %q, %v", pass, body, err)
		}
	}
}

// The receipt is read before the bootout and rewritten after three copies, so
// the window is real: a receipt replaced inside it must refuse rather than be
// overwritten with a program digest that was never proved against it.
func TestServiceUpgradeRefusesAReceiptReplacedInsideItsOwnWindow(t *testing.T) {
	fixture := newManageFixture(t)
	fixture.writeSourceBinaries(t, "first")
	first := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(800))}
	fixture.install(t, first.run)
	receiptPath := filepath.Join(ServiceDirectoryPath(fixture.home), serviceReceiptName)
	receipt, present, err := readServiceReceipt(fixture.home)
	if err != nil || !present {
		t.Fatalf("receipt present=%t err=%v", present, err)
	}
	foreign := receipt
	foreign.Label = manageTestLabel + ".other"
	foreign.PlistPath = filepath.Join(fixture.plistDir, foreign.Label+".plist")
	foreignBody, err := encodeServiceReceipt(foreign)
	if err != nil {
		t.Fatal(err)
	}

	fixture.writeSourceBinaries(t, "second")
	booted := true
	swap := func(_ context.Context, arguments ...string) launchctlResult {
		switch arguments[0] {
		case "print":
			if booted {
				return fixture.printRunning(800)
			}
			return launchctlResult{status: launchctlNotFound}
		case "bootout":
			// Inside the upgrade's own window, between the receipt read and the
			// receipt rewrite.
			booted = false
			if err := os.WriteFile(receiptPath, foreignBody, 0o600); err != nil {
				t.Fatal(err)
			}
			return launchctlResult{status: 0}
		default:
			return launchctlResult{status: -1, err: fmt.Errorf("unexpected verb %q", arguments[0])}
		}
	}
	status, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, swap)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("swapped receipt upgrade = %+v, %v", status, err)
	}
	body, readErr := os.ReadFile(receiptPath)
	if readErr != nil || !bytes.Equal(body, foreignBody) {
		t.Fatalf("the swapped receipt was rewritten: %q, %v", body, readErr)
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
	resolve := &recordedLaunchctl{results: fixture.printAbsent()}
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
	blocked := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(88), {status: 0}, {status: launchctlNotFound}}}
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

func TestServiceMutationLockRejectsEveryConcurrentVerbBeforeLaunchctl(t *testing.T) {
	fixture := newManageFixture(t)
	var calls atomic.Int64
	launchctl := func(context.Context, ...string) launchctlResult {
		calls.Add(1)
		return launchctlResult{status: -1, err: errors.New("launchctl must not run")}
	}
	_, err := withServiceMutation(context.Background(), fixture.home, func(*serviceHomeCapability) (ServiceStatus, error) {
		tests := []struct {
			name string
			run  func() (ServiceStatus, error)
		}{
			{"install", func() (ServiceStatus, error) {
				return serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, launchctl)
			}},
			{"start", func() (ServiceStatus, error) {
				return serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, launchctl)
			}},
			{"stop", func() (ServiceStatus, error) {
				return serviceStopAt(context.Background(), fixture.home, fixture.userHome, fixture.config, launchctl)
			}},
			{"uninstall", func() (ServiceStatus, error) {
				return serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, launchctl)
			}},
		}
		for _, test := range tests {
			if _, err := test.run(); !errors.Is(err, ErrBusy) {
				t.Fatalf("%s concurrent mutation = %v", test.name, err)
			}
		}
		return ServiceStatus{State: ServiceAbsent}, nil
	})
	if err != nil {
		t.Fatalf("hold mutation lock: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("concurrent mutations made %d launchctl calls", calls.Load())
	}
}

func TestServiceMutationLockIsIndependentFromOperationalHomeLock(t *testing.T) {
	t.Run("service then operational", func(t *testing.T) {
		fixture := newManageFixture(t)
		_, err := withServiceMutation(context.Background(), fixture.home, func(*serviceHomeCapability) (ServiceStatus, error) {
			home, err := OpenOperationalHome(context.Background(), fixture.home)
			if err != nil {
				return ServiceStatus{}, err
			}
			return ServiceStatus{State: ServiceAbsent}, home.Close()
		})
		if err != nil {
			t.Fatalf("operational lock while service lock held: %v", err)
		}
	})

	t.Run("operational then service", func(t *testing.T) {
		fixture := newManageFixture(t)
		home, err := OpenOperationalHome(context.Background(), fixture.home)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := home.Close(); err != nil {
				t.Errorf("close operational home: %v", err)
			}
		}()
		if _, err := withServiceMutation(context.Background(), fixture.home, func(*serviceHomeCapability) (ServiceStatus, error) {
			return ServiceStatus{State: ServiceAbsent}, nil
		}); err != nil {
			t.Fatalf("service lock while operational lock held: %v", err)
		}
	})
}

func TestServiceUninstallIsEvidenceFirst(t *testing.T) {
	fixture := newManageFixture(t)
	deny := func(context.Context, ...string) launchctlResult {
		t.Fatal("a mutating launchctl verb ran without ownership evidence")
		return launchctlResult{}
	}

	// A foreign plist under the requested label refuses the whole uninstall
	// before any mutation: no bootout, no deletion, bytes retained.
	if err := os.WriteFile(fixture.plistPath(), []byte("operator property"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, deny)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("foreign plist uninstall = %+v, %v", status, err)
	}
	body, readErr := os.ReadFile(fixture.plistPath())
	if readErr != nil || string(body) != "operator property" {
		t.Fatalf("foreign plist mutated: %q, %v", body, readErr)
	}
	if err := os.Remove(fixture.plistPath()); err != nil {
		t.Fatal(err)
	}

	// No receipt, no plist: nothing proves the label, so no launchctl verb
	// runs at all; the empty result is provable absence.
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, deny)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("no-evidence uninstall = %+v, %v", status, err)
	}

	// A valid receipt for a DIFFERENT label refuses: deleting this home's
	// service directory would break that label's installation.
	full := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(91))}
	fixture.install(t, full.run)
	otherConfig := fixture.config
	otherConfig.Label = manageTestLabel + ".other"
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, otherConfig, deny)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("label-mismatch uninstall = %+v, %v", status, err)
	}
	otherDirectory := fixture.config
	otherDirectory.PlistDirectory = filepath.Join(fixture.root, "other-plists")
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, otherDirectory, deny)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceForeign) {
		t.Fatalf("plist-path-mismatch uninstall = %+v, %v", status, err)
	}

	// With the matching receipt an exact observation precedes the mutating
	// bootout, and removal completes as before.
	proven := &recordedLaunchctl{results: []launchctlResult{fixture.printRunning(91), {status: 0}, {status: launchctlNotFound}}}
	status, err = serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, proven.run)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("proven uninstall = %+v, %v", status, err)
	}
	if len(proven.calls) < 2 || proven.calls[0][0] != "print" || proven.calls[1][0] != "bootout" {
		t.Fatalf("proven uninstall verbs = %q", proven.calls)
	}
}

func TestServiceUninstallSkipsBootoutWhenAlreadyUnloaded(t *testing.T) {
	fixture := newManageFixture(t)
	installed := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(92))}
	fixture.install(t, installed.run)

	unloaded := &recordedLaunchctl{results: fixture.printAbsent()}
	status, err := serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, unloaded.run)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("unloaded uninstall = %+v, %v", status, err)
	}
	if len(unloaded.calls) != 2 || unloaded.calls[0][0] != "print" || unloaded.calls[1][0] != "error" {
		t.Fatalf("unloaded uninstall verbs = %q", unloaded.calls)
	}
	if _, err := os.Lstat(ServiceDirectoryPath(fixture.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service directory survived unloaded uninstall")
	}
	if _, err := os.Lstat(fixture.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist survived unloaded uninstall")
	}
}

func TestServiceUninstallSerializesAbsentObservationAgainstStart(t *testing.T) {
	fixture := newManageFixture(t)
	installed := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(94))}
	fixture.install(t, installed.run)

	var loaded atomic.Bool
	var startCalls atomic.Int64
	startLaunchctl := func(_ context.Context, arguments ...string) launchctlResult {
		startCalls.Add(1)
		switch arguments[0] {
		case "print":
			if loaded.Load() {
				return fixture.printRunning(95)
			}
			return launchctlResult{status: launchctlNotFound}
		case "error":
			return launchctlResult{status: 0, stdout: []byte(launchctlNotFoundText + "\n")}
		case "bootstrap":
			loaded.Store(true)
			return launchctlResult{status: 0}
		default:
			return launchctlResult{status: -1, err: fmt.Errorf("unexpected start verb %q", arguments[0])}
		}
	}

	observedAbsent := make(chan struct{})
	releaseObservation := make(chan struct{})
	var observedOnce sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseObservation) })
	uninstallLaunchctl := func(_ context.Context, arguments ...string) launchctlResult {
		switch arguments[0] {
		case "print":
			// Capture absence before exposing the interleaving. Without the
			// mutation lock, start can bootstrap while this stale observation
			// is paused and uninstall will then delete its live artifacts.
			wasLoaded := loaded.Load()
			observedOnce.Do(func() { close(observedAbsent) })
			<-releaseObservation
			if wasLoaded {
				return fixture.printRunning(95)
			}
			return launchctlResult{status: launchctlNotFound}
		case "error":
			return launchctlResult{status: 0, stdout: []byte(launchctlNotFoundText + "\n")}
		case "bootout":
			loaded.Store(false)
			return launchctlResult{status: 0}
		default:
			return launchctlResult{status: -1, err: fmt.Errorf("unexpected uninstall verb %q", arguments[0])}
		}
	}

	type result struct {
		status ServiceStatus
		err    error
	}
	uninstallDone := make(chan result, 1)
	go func() {
		status, err := serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, uninstallLaunchctl)
		uninstallDone <- result{status: status, err: err}
	}()
	select {
	case <-observedAbsent:
	case <-time.After(3 * time.Second):
		t.Fatal("uninstall did not reach the absent observation")
	}

	startDone := make(chan result, 1)
	go func() {
		status, err := serviceStartAt(context.Background(), fixture.home, fixture.userHome, fixture.config, startLaunchctl)
		startDone <- result{status: status, err: err}
	}()
	var start result
	select {
	case start = <-startDone:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent start did not refuse the held mutation lock")
	}
	releaseOnce.Do(func() { close(releaseObservation) })
	var uninstall result
	select {
	case uninstall = <-uninstallDone:
	case <-time.After(3 * time.Second):
		t.Fatal("uninstall did not finish after observation release")
	}
	if !errors.Is(start.err, ErrBusy) {
		t.Fatalf("concurrent start = %+v, %v", start.status, start.err)
	}
	if startCalls.Load() != 0 {
		t.Fatalf("concurrent start made %d launchctl calls", startCalls.Load())
	}
	if uninstall.err != nil || uninstall.status.State != ServiceAbsent {
		t.Fatalf("uninstall = %+v, %v", uninstall.status, uninstall.err)
	}
	if loaded.Load() {
		t.Fatal("start installed a live job behind uninstall's absent observation")
	}
	if _, err := os.Lstat(ServiceDirectoryPath(fixture.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service directory survived serialized uninstall")
	}
	if _, err := os.Lstat(fixture.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist survived serialized uninstall")
	}
}

func TestServiceUninstallRefusesForeignLoadedJobBeforeBootout(t *testing.T) {
	fixture := newManageFixture(t)
	installed := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(93))}
	fixture.install(t, installed.run)

	foreignResult := fixture.printRunning(93)
	foreignResult.stdout = []byte(strings.Replace(string(foreignResult.stdout), fixture.plistPath(), fixture.plistPath()+".foreign", 1))
	foreign := &recordedLaunchctl{results: []launchctlResult{foreignResult}}
	status, err := serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, foreign.run)
	if status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceLaunchctl) {
		t.Fatalf("foreign loaded job uninstall = %+v, %v", status, err)
	}
	if len(foreign.calls) != 1 || foreign.calls[0][0] != "print" {
		t.Fatalf("foreign loaded job verbs = %q", foreign.calls)
	}
	if _, err := os.Lstat(ServiceDirectoryPath(fixture.home)); err != nil {
		t.Fatal("service directory was removed")
	}
	if _, err := os.Lstat(fixture.plistPath()); err != nil {
		t.Fatal("plist was removed")
	}
}

func TestServiceStageResidueRefusesInstallAndResolvesThroughUninstall(t *testing.T) {
	fixture := newManageFixture(t)
	serviceDir := ServiceDirectoryPath(fixture.home)
	current := filepath.Join(serviceDir, "bin", "current")
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	stages := []string{
		filepath.Join(current, ".factoryd.stage"),
		filepath.Join(serviceDir, "."+serviceReceiptName+".stage"),
		filepath.Join(fixture.plistDir, "."+fixture.config.plistName()+".stage"),
	}
	for _, stage := range stages {
		if err := os.WriteFile(stage, []byte("crash residue"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Install refuses the collision instead of silently deleting it.
	absent := &recordedLaunchctl{results: fixture.printAbsent()}
	if _, err := serviceInstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, fixture.sourceDir, absent.run); !errors.Is(err, ErrServiceResidue) {
		t.Fatalf("install over stage residue = %v", err)
	}

	// Uninstall resolves exactly its own stage names without launchd contact
	// (no receipt and no plist means no evidence, and none is needed for the
	// name-scoped residue).
	deny := func(context.Context, ...string) launchctlResult {
		t.Fatal("launchctl ran during stage-residue resolution")
		return launchctlResult{}
	}
	status, err := serviceUninstallAt(context.Background(), fixture.home, fixture.userHome, fixture.config, deny)
	if err != nil || status.State != ServiceAbsent {
		t.Fatalf("stage-residue uninstall = %+v, %v", status, err)
	}
	for _, stage := range stages {
		if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stage survived uninstall: %s", stage)
		}
	}
	if _, err := os.Lstat(serviceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service directory survived stage-residue uninstall")
	}

	// The cleaned home installs normally afterwards.
	full := &recordedLaunchctl{results: append(fixture.printAbsent(), launchctlResult{status: 0}, fixture.printRunning(92))}
	if status := fixture.install(t, full.run); status.State != ServiceRunning {
		t.Fatalf("install after resolution = %+v", status)
	}
}

func TestStagedWritersRefuseCollisionsInsteadOfDeleting(t *testing.T) {
	fixture := newManageFixture(t)
	destination := filepath.Join(fixture.root, "dest")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{".factoryd.stage", ".note.stage"} {
		if err := os.WriteFile(filepath.Join(destination, stage), []byte("someone else's bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := copyServiceBinary(filepath.Join(fixture.sourceDir, "factoryd"), destination, "factoryd"); !errors.Is(err, ErrServiceResidue) {
		t.Fatalf("binary stage collision = %v", err)
	}
	if err := writeExactFile(destination, "note", []byte("payload"), 0o600); !errors.Is(err, ErrServiceResidue) {
		t.Fatalf("file stage collision = %v", err)
	}
	for _, stage := range []string{".factoryd.stage", ".note.stage"} {
		body, err := os.ReadFile(filepath.Join(destination, stage))
		if err != nil || string(body) != "someone else's bytes" {
			t.Fatalf("stage %s mutated: %q, %v", stage, body, err)
		}
	}
}
