// Command release-artifact is a repository-owned build tool, not a shipped
// runtime binary. It prints release receipts and packages the release assets.
package main

import (
	"fmt"
	"os"

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
	if len(arguments) > 0 && arguments[0] == "package" {
		if err := buildinfo.PackageRelease(arguments[1:]); err != nil {
			return err
		}
		fmt.Println("packaged", arguments[3], "for aarch64-apple-darwin and x86_64-apple-darwin")
		return nil
	}
	return fmt.Errorf("usage: release-artifact receipt VERSION SOURCE TARGET | package TAG SOURCE_SHA OUT_DIR OWNER/REPO TARGET BIN_DIR TARGET BIN_DIR")
}
