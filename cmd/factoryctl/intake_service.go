package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const intakeServiceUsage = "factoryctl intake service install|uninstall|status --home ABSOLUTE\nfactoryctl intake service migrate --home ABSOLUTE --legacy-config ABSOLUTE [--preview | --plan HASH [--acknowledge-policy-narrowing]]\n"

func runIntakeService(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE") != "" {
		_, _ = io.WriteString(stderr, "Intake service management requires the local operator.\n")
		return exitFailure
	}
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall" && args[0] != "status" && args[0] != "migrate") {
		_, _ = io.WriteString(stderr, intakeServiceUsage)
		return exitUsage
	}
	flags := flag.NewFlagSet("intake service", flag.ContinueOnError)
	flags.SetOutput(stderr)
	home := flags.String("home", "", "installed factory home")
	legacyConfig := flags.String("legacy-config", "", "existing operator-owned intake configuration")
	preview := flags.Bool("preview", false, "inspect cutover without mutation")
	acknowledge := flags.Bool("acknowledge-policy-narrowing", false, "accept reviewed App/bot author policy narrowing")
	plan := flags.String("plan", "", "apply or resume the reviewed cutover plan hash")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*home) || filepath.Clean(*home) != *home || *home == "/" {
		return exitUsage
	}
	if args[0] == "migrate" {
		if !filepath.IsAbs(*legacyConfig) || (*preview && *plan != "") || (*acknowledge && *plan == "") {
			return exitUsage
		}
	} else if *legacyConfig != "" || *preview || *plan != "" || *acknowledge {
		return exitUsage
	}
	if runtime.GOOS != "darwin" {
		_, _ = io.WriteString(stderr, "Managed intake requires macOS launchd.\n")
		return exitFailure
	}
	canonical, err := filepath.EvalSymlinks(*home)
	if err != nil {
		return writeWebFailure(stderr, "intake service home", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return exitFailure
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return exitFailure
	}
	script, err := installedIntakeController(executable)
	if err != nil && args[0] != "install" && args[0] != "migrate" {
		script, err = recordedIntakeController(canonical)
	}
	if err != nil {
		_, _ = io.WriteString(stderr, "Run this command from the installed release or Homebrew factoryctl with its libexec/dark-factory controller assets.\n")
		return exitFailure
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		_, _ = io.WriteString(stderr, "Install Python 3.9 or newer, then retry intake service installation.\n")
		return exitFailure
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		return exitFailure
	}
	arguments := []string{script, "--managed", "--factory-home", canonical, "--factoryctl", executable, "--service", args[0]}
	if args[0] == "migrate" {
		arguments = append(arguments, "--legacy-config", *legacyConfig)
		if *acknowledge {
			arguments = append(arguments, "--acknowledge-policy-narrowing")
		}
		if *plan != "" {
			arguments = append(arguments, "--plan", *plan)
		}
	}
	command := exec.CommandContext(ctx, python, arguments...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return exitFailure
	}
	return 0
}

func installedIntakeController(executable string) (string, error) {
	if filepath.Base(executable) != "factoryctl" {
		return "", fmt.Errorf("invalid installed executable")
	}
	prefix := filepath.Dir(executable)
	if filepath.Base(prefix) == "bin" {
		prefix = filepath.Dir(prefix)
	}
	script := filepath.Join(prefix, "libexec", "dark-factory", "factory-autonomy.py")
	if _, err := privateControllerFile(script, 8<<20, false); err != nil {
		return "", err
	}
	return script, nil
}

// A service's private receipt lets status/uninstall survive invocation through
// the daemon's copied factoryctl. Install always takes a current release path.
func recordedIntakeController(home string) (string, error) {
	data, err := privateControllerFile(home+".intake/service.json", 16384, true)
	if err != nil {
		return "", err
	}
	var receipt struct {
		Script       string `json:"script"`
		ScriptDigest string `json:"script_digest"`
		Factoryctl   string `json:"factoryctl"`
	}
	if json.Unmarshal(data, &receipt) != nil || !filepath.IsAbs(receipt.Factoryctl) {
		return "", fmt.Errorf("invalid controller receipt")
	}
	script, err := installedIntakeController(receipt.Factoryctl)
	if err != nil || script != receipt.Script {
		return "", fmt.Errorf("invalid controller path")
	}
	data, err = privateControllerFile(script, 8<<20, false)
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != receipt.ScriptDigest {
		return "", fmt.Errorf("controller changed")
	}
	return script, nil
}

func privateControllerFile(path string, maximum int64, private bool) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("invalid controller path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > maximum || stat.Nlink != 1 || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) || (private && (info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1)) {
		return nil, fmt.Errorf("unsafe controller file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("controller changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, fmt.Errorf("controller read failed")
	}
	return data, nil
}
