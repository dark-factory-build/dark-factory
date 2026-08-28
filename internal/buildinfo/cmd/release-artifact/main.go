// Command release-artifact is a repository-owned build tool, not a shipped
// runtime binary. It snapshots and verifies the fixed three release artifacts.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release-artifact:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 4 && arguments[0] == "receipt" {
		identity, ok := buildinfo.Expected(arguments[1], arguments[2], arguments[3])
		if !ok {
			return fmt.Errorf("invalid release identity")
		}
		fmt.Println(identity.Receipt())
		return nil
	}
	if len(arguments) == 7 && arguments[0] == "snapshot" {
		identity, ok := buildinfo.Expected(arguments[4], arguments[5], arguments[6])
		if !ok {
			return fmt.Errorf("invalid release identity")
		}
		verified, err := buildinfo.SnapshotReleaseArtifact(arguments[1], arguments[2], arguments[3], identity)
		if err != nil {
			return err
		}
		fmt.Println(verified.BuildID())
		return nil
	}
	if len(arguments) == 3 && arguments[0] == "bounds" {
		unpacked, err := parseSize(arguments[1])
		if err != nil {
			return err
		}
		archive, err := parseSize(arguments[2])
		if err != nil {
			return err
		}
		return buildinfo.ValidateArchiveBounds(unpacked, archive)
	}
	if len(arguments) == 2 && arguments[0] == "target-bounds" {
		unpacked, err := parseSize(arguments[1])
		if err != nil {
			return err
		}
		return buildinfo.ValidateTargetBounds(unpacked)
	}
	if len(arguments) == 3 && arguments[0] == "release-bounds" {
		arm, err := parseSize(arguments[1])
		if err != nil {
			return err
		}
		intel, err := parseSize(arguments[2])
		if err != nil {
			return err
		}
		return buildinfo.ValidateReleaseArchiveBounds(arm, intel)
	}
	return fmt.Errorf("usage: release-artifact receipt VERSION SOURCE TARGET | snapshot SOURCE DESTINATION COMPONENT VERSION SOURCE_SHA TARGET | target-bounds UNPACKED | bounds UNPACKED ARCHIVE | release-bounds ARM INTEL")
}

func parseSize(value string) (int64, error) {
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size")
	}
	return size, nil
}
