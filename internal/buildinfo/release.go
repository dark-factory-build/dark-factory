package buildinfo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

//go:embed dark-factory.rb.tmpl
var formulaSource string

var (
	formulaTemplate = template.Must(template.New("dark-factory.rb").Parse(formulaSource))
	releaseTag      = regexp.MustCompile(`^v[0-9][A-Za-z0-9._-]*$`)
	repositoryName  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// noticesPath is read from the working directory (the checkout the
	// release workflow and factoryd's self-release package from) and ships
	// beside the binaries in every archive.
	noticesPath = "THIRD_PARTY_NOTICES"
	// releaseTargets is the one fixed packaging order; input order never matters.
	releaseTargets = [2][2]string{{"aarch64-apple-darwin", "darwin/arm64"}, {"x86_64-apple-darwin", "darwin/amd64"}}
)

// BuildRelease builds the three release binaries of the source tree at dir,
// commit source, for target ("darwin/arm64") into out as an exact release
// identity, with the Go toolchain dir's go.mod names. go resolves on the PATH
// in environment; prepare, when set, adjusts each command before it runs.
func BuildRelease(ctx context.Context, dir, source, target, out string, environment []string, prepare func(*exec.Cmd)) (Identity, error) {
	version, versionErr := os.ReadFile(filepath.Join(dir, "VERSION"))
	module, moduleErr := os.ReadFile(filepath.Join(dir, "go.mod"))
	goVersion := regexp.MustCompile(`(?m)^go ([0-9]+\.[0-9]+\.[0-9]+)$`).FindSubmatch(module)
	identity, ok := Expected(strings.TrimSpace(string(version)), source, target)
	if versionErr != nil || moduleErr != nil || goVersion == nil || !ok {
		return Identity{}, errors.New("release source has no exact VERSION or go.mod toolchain")
	}
	// factoryd embeds the console bundle (internal/browser/console), which is
	// built here rather than committed.
	for _, step := range [][]string{{"install", "--frozen-lockfile", "--ignore-scripts"}, {"run", "console"}} {
		command := exec.CommandContext(ctx, "/usr/bin/env", append([]string{"corepack", "pnpm"}, step...)...)
		command.Dir = filepath.Join(dir, "web")
		command.Env = append(environment, "CI=true")
		if prepare != nil {
			prepare(command)
		}
		if log, err := command.CombinedOutput(); err != nil {
			return Identity{}, fmt.Errorf("console %s: %v: %s", step[0], err, log[max(0, len(log)-1024):])
		}
	}
	goos, goarch, _ := strings.Cut(target, "/")
	for _, name := range []string{"factoryd", "factoryctl", "factory-runner"} {
		output := filepath.Join(out, name)
		command := exec.CommandContext(ctx, "/usr/bin/env", "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/dark-factory-build/dark-factory/internal/buildinfo.receipt="+identity.Receipt(), "-o", output, "./cmd/"+name)
		command.Dir = dir
		command.Env = append(environment, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch, "GOENV=off", "GOAUTH=off", "GOTOOLCHAIN=go"+string(goVersion[1]))
		if prepare != nil {
			prepare(command)
		}
		if log, err := command.CombinedOutput(); err != nil {
			return Identity{}, fmt.Errorf("go build %s: %v: %s", name, err, log[max(0, len(log)-1024):])
		}
		// The release artifact contract is exactly 0755; the linker honors umask.
		if err := os.Chmod(output, 0o755); err != nil {
			return Identity{}, err
		}
	}
	return identity, nil
}

type releaseAsset struct {
	SHA256, BuildID string
	Bytes           int64
}

// PackageRelease packages both macOS targets in one all-or-nothing step:
//
//	TAG SOURCE_SHA OUT_DIR OWNER/REPO TARGET BIN_DIR TARGET BIN_DIR
//
// OUT_DIR must not exist. Everything is built in a sibling staging directory
// and renamed into place only after both archives, SHA256SUMS,
// and the Homebrew formula exist. Archive members have one fixed order, mode,
// owner, and timestamp, so byte-identical binaries give byte-identical assets.
func PackageRelease(arguments []string) (result error) {
	if len(arguments) != 8 {
		return errors.New("usage: package TAG SOURCE_SHA OUT_DIR OWNER/REPO TARGET BIN_DIR TARGET BIN_DIR")
	}
	tag, source, outDir, repository := arguments[0], arguments[1], arguments[2], arguments[3]
	if !releaseTag.MatchString(tag) || !validSource(source) || !repositoryName.MatchString(repository) || outDir == "" {
		return errors.New("invalid release tag, source, output, or repository")
	}
	binDirs := map[string]string{}
	for index := 4; index < 8; index += 2 {
		target := arguments[index]
		if target != releaseTargets[0][0] && target != releaseTargets[1][0] {
			return fmt.Errorf("unsupported release target: %s", target)
		}
		if _, duplicate := binDirs[target]; duplicate {
			return fmt.Errorf("duplicate release target: %s", target)
		}
		binDirs[target] = arguments[index+1]
	}
	if _, err := os.Lstat(outDir); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release output already exists: %s", outDir)
	}
	parent := filepath.Dir(outDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".dark-factory-package.")
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			_ = os.RemoveAll(staging)
		}
	}()

	version := strings.TrimPrefix(tag, "v")
	sums := ""
	assets := map[string]releaseAsset{}
	for _, target := range releaseTargets {
		identity, ok := Expected(version, source, target[1])
		if !ok {
			return errors.New("invalid release identity")
		}
		archive := "dark-factory-" + tag + "-" + target[0] + ".tar.gz"
		_, size, sum, err := packageTarget(binDirs[target[0]], filepath.Join(staging, archive), identity)
		if err != nil {
			return fmt.Errorf("%s: %w", target[0], err)
		}
		sums += sum + "  " + archive + "\n"
		assets[target[0]] = releaseAsset{
			SHA256: sum, Bytes: size, BuildID: identity.BuildID(),
		}
	}
	arm, intel := assets[releaseTargets[0][0]], assets[releaseTargets[1][0]]
	if err := ValidateReleaseArchiveBounds(arm.Bytes, intel.Bytes); err != nil {
		return err
	}
	formula := new(strings.Builder)
	if err := formulaTemplate.Execute(formula, map[string]any{
		"Tag": tag, "Version": version,
		"Source": source, "Repository": repository,
		"ArmBuildID": arm.BuildID, "IntelBuildID": intel.BuildID, "ArmSHA": arm.SHA256, "IntelSHA": intel.SHA256,
	}); err != nil {
		return err
	}
	for name, content := range map[string]string{"SHA256SUMS": sums, "dark-factory.rb": formula.String()} {
		if err := os.WriteFile(filepath.Join(staging, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	// ponytail: Lstat-then-rename has the same small race the shell mv had;
	// rename would also succeed onto an empty directory created in between.
	if _, err := os.Lstat(outDir); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release output appeared while packaging: %s", outDir)
	}
	return os.Rename(staging, outDir)
}

// packageTarget snapshots the three verified binaries into a private payload
// and writes one canonical tar.gz from those snapshots, never the inputs.
func packageTarget(binDir, archivePath string, identity Identity) (unpacked, size int64, sum string, result error) {
	payload := archivePath + ".payload"
	if err := os.Mkdir(payload, 0o700); err != nil {
		return 0, 0, "", err
	}
	defer os.RemoveAll(payload)
	components := []string{"factoryd", "factory-runner", "factoryctl"}
	sizes := map[string]int64{}
	for _, component := range components {
		snapshot := filepath.Join(payload, component)
		if _, err := SnapshotReleaseArtifact(filepath.Join(binDir, component), snapshot, component, identity); err != nil {
			return 0, 0, "", fmt.Errorf("%s: %w", component, err)
		}
		information, err := os.Stat(snapshot)
		if err != nil {
			return 0, 0, "", err
		}
		sizes[component] = information.Size()
		unpacked += information.Size()
	}
	if err := ValidateTargetBounds(unpacked); err != nil {
		return 0, 0, "", err
	}
	notices, err := os.ReadFile(noticesPath)
	if err != nil {
		return 0, 0, "", err
	}

	output, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, 0, "", err
	}
	defer func() {
		if closeErr := output.Close(); result == nil {
			result = closeErr
		}
	}()
	hash := sha256.New()
	compressed := gzip.NewWriter(io.MultiWriter(output, hash)) // zero header mtime
	archive := tar.NewWriter(compressed)
	for _, component := range append(components, "THIRD_PARTY_NOTICES") {
		mode := int64(0o755)
		if component == "THIRD_PARTY_NOTICES" {
			sizes[component], mode = int64(len(notices)), 0o644
			if err := os.WriteFile(filepath.Join(payload, component), notices, 0o600); err != nil {
				return 0, 0, "", err
			}
		}
		if err := archive.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: component, Size: sizes[component], Mode: mode,
			Uname: "root", Gname: "wheel", ModTime: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Format: tar.FormatUSTAR,
		}); err != nil {
			return 0, 0, "", err
		}
		input, err := os.Open(filepath.Join(payload, component))
		if err != nil {
			return 0, 0, "", err
		}
		_, err = io.CopyN(archive, input, sizes[component])
		input.Close()
		if err != nil {
			return 0, 0, "", err
		}
	}
	if err := archive.Close(); err != nil {
		return 0, 0, "", err
	}
	if err := compressed.Close(); err != nil {
		return 0, 0, "", err
	}
	information, err := output.Stat()
	if err != nil {
		return 0, 0, "", err
	}
	if err := ValidateArchiveBounds(unpacked, information.Size()); err != nil {
		return 0, 0, "", err
	}
	return unpacked, information.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}
