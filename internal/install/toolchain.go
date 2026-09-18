package install

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const trustedSystemToolchainRoot = "/Library/Developer/CommandLineTools"

const supportedNodeVersion = "v22.20.0"
const supportedGoVersion = "1.27.0"

// SupportedToolchain derives the small, optional capability contract used by
// the default Darwin launch. It names only the pinned Node installation and
// Rustup's selected toolchain metadata; missing installations are omitted so
// an otherwise valid factoryd startup does not become dependent on optional
// developer tools.
func SupportedToolchain(accountHome string) (toolPath, readRoots string) {
	if accountHome == "" || !filepath.IsAbs(accountHome) || filepath.Clean(accountHome) != accountHome {
		return "", ""
	}
	nodeRoot := filepath.Join(accountHome, ".nvm", "versions", "node", supportedNodeVersion)
	nodeBin := filepath.Join(nodeRoot, "bin")
	cargoBin := filepath.Join(accountHome, ".cargo", "bin")
	rustupHome := filepath.Join(accountHome, ".rustup")
	homebrewGoLibexec := filepath.Join("/opt/homebrew/Cellar/go", supportedGoVersion, "libexec")
	intelGoLibexec := filepath.Join("/usr/local/Cellar/go", supportedGoVersion, "libexec")
	var paths, roots []string
	for _, path := range []string{nodeBin, cargoBin} {
		if canonicalDirectory(path) {
			paths = append(paths, path)
		}
	}
	for _, path := range []string{nodeRoot, cargoBin, rustupHome, homebrewGoLibexec, intelGoLibexec} {
		if canonicalDirectory(path) {
			roots = append(roots, path)
		}
	}
	// The default tool path's /opt/homebrew/bin and /usr/local/bin entries
	// are Homebrew opt-symlinks; the sandbox's read grant covers only the
	// resolved libexec target, so the Go binary is reachable only from its
	// canonical libexec/bin, not through the unpermitted symlink chain.
	for _, libexec := range []string{homebrewGoLibexec, intelGoLibexec} {
		if goBin := filepath.Join(libexec, "bin"); canonicalDirectory(goBin) {
			paths = append(paths, goBin)
		}
	}
	return strings.Join(paths, string(filepath.ListSeparator)), strings.Join(roots, string(filepath.ListSeparator))
}

func canonicalDirectory(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0022 == 0
}

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

// Startup requires current-user ownership, except for the exact root-owned
// Apple Command Line Tools installation, and refuses aliases or writable roots.
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
		if !ok || (stat.Uid != uint32(os.Geteuid()) && !trustedSystemToolchainOwnership(root, stat.Uid)) {
			return ErrServicePlist
		}
	}
	return nil
}

func trustedSystemToolchainOwnership(root string, uid uint32) bool {
	return root == trustedSystemToolchainRoot && uid == 0
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
