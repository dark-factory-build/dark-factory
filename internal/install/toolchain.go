package install

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// Toolchain read roots are explicit startup authority, not inferred PATH parents.
// Keep their total below the provider's existing bounded permission argument.
func ValidToolchainReadRoots(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2048 {
		return false
	}
	roots := filepath.SplitList(value)
	if len(roots) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, root := range roots {
		if !validServicePath(root) || root == "/" || seen[root] || len(strings.Split(strings.Trim(root, "/"), "/")) < 3 {
			return false
		}
		for _, part := range strings.Split(root, "/") {
			switch strings.ToLower(part) {
			case ".ssh", ".codex", ".claude", ".aws", ".config", ".gnupg":
				return false
			}
		}
		seen[root] = true
	}
	return true
}

func ToolchainReadRootsAllowed(value, accountHome string, privatePaths ...string) bool {
	if !ValidToolchainReadRoots(value) {
		return false
	}
	for _, root := range filepath.SplitList(value) {
		if containsToolchainPath(root, accountHome) {
			return false
		}
		for _, private := range privatePaths {
			if private != "" && (containsToolchainPath(root, private) || containsToolchainPath(private, root)) {
				return false
			}
		}
	}
	return true
}

// Startup requires current-user ownership and refuses aliases or writable roots.
func CheckToolchainReadRoots(value, accountHome string, privatePaths ...string) error {
	if !ToolchainReadRootsAllowed(value, accountHome, privatePaths...) {
		return ErrServicePlist
	}
	for _, root := range filepath.SplitList(value) {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root {
			return ErrServicePlist
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return ErrServicePlist
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return ErrServicePlist
		}
	}
	return nil
}

func containsToolchainPath(root, path string) bool {
	return root == path || strings.HasPrefix(path, root+string(filepath.Separator))
}

// ValidToolPath is shared by managed installation and daemon/provider launch.
func ValidToolPath(value string) bool {
	if value == "" || len(value) > runner.MaxEnvironmentEntryBytes-len("PATH=") {
		return false
	}
	seen := map[string]bool{}
	for _, component := range filepath.SplitList(value) {
		if !validServicePath(component) || component == "/" || seen[component] {
			return false
		}
		seen[component] = true
	}
	return len(seen) > 0
}
