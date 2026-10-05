// Command release-artifact is a repository-owned build tool, not a shipped
// runtime binary. It builds and packages the release assets, and
// publishes them as a GitHub release through `gh`.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-artifact:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 4 && arguments[0] == "build" {
		// The checked-out tree is the release source; both targets cross-compile.
		for _, target := range [][2]string{{"aarch64-apple-darwin", "darwin/arm64"}, {"x86_64-apple-darwin", "darwin/amd64"}} {
			identity, err := buildinfo.BuildRelease(context.Background(), ".", arguments[2], target[1], filepath.Join(arguments[3], target[0]), os.Environ(), nil)
			if err != nil {
				return err
			}
			if "v"+identity.Version() != arguments[1] {
				return fmt.Errorf("tag %s does not match VERSION %s", arguments[1], identity.Version())
			}
		}
		return nil
	}
	if len(arguments) > 0 && arguments[0] == "package" {
		if err := buildinfo.PackageRelease(arguments[1:]); err != nil {
			return err
		}
		fmt.Println("packaged", arguments[3], "for aarch64-apple-darwin and x86_64-apple-darwin")
		return nil
	}
	if len(arguments) > 0 && arguments[0] == "publish" {
		return buildinfo.PublishRelease(arguments[1:], gh)
	}
	return fmt.Errorf("usage: release-artifact build TAG SOURCE_SHA OUT_DIR | package TAG SOURCE_SHA OUT_DIR OWNER/REPO TARGET BIN_DIR TARGET BIN_DIR | publish TAG EXPECTED_COMMIT OWNER/REPO ASSET...")
}

func gh(arguments ...string) ([]byte, string, error) {
	var stderr bytes.Buffer
	command := exec.Command("gh", arguments...)
	command.Stderr = &stderr
	stdout, err := command.Output()
	return stdout, stderr.String(), err
}
