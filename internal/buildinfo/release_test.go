package buildinfo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var releaseAssetNames = []string{
	"dark-factory-v1.2.3-aarch64-apple-darwin.tar.gz", "dark-factory-v1.2.3-x86_64-apple-darwin.tar.gz",
	"SHA256SUMS", "latest.json", "dark-factory.rb",
}

func TestPackageReleaseIsDeterministicAndConsistent(t *testing.T) {
	root := t.TempDir()
	arm, intel := filepath.Join(root, "arm"), filepath.Join(root, "intel")
	for _, component := range []string{"factoryd", "factory-runner", "factoryctl"} {
		buildFixture(t, arm, component, component, "1.2.3", fixtureSource, "darwin/arm64", "")
		buildFixture(t, intel, component, component, "1.2.3", fixtureSource, "darwin/amd64", "")
	}
	// Unrelated residue in an input directory is never an archive member.
	if err := os.WriteFile(filepath.Join(arm, "factory-tui"), []byte("obsolete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first")
	mustPackage(t, first, "x86_64-apple-darwin", intel, "aarch64-apple-darwin", arm)
	// Reversed target order and new source mtimes change no published byte.
	for _, directory := range []string{arm, intel} {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if err := os.Chtimes(filepath.Join(directory, entry.Name()), time.Now(), time.Unix(1<<31, 0)); err != nil {
				t.Fatal(err)
			}
		}
	}
	second := filepath.Join(root, "second")
	mustPackage(t, second, "aarch64-apple-darwin", arm, "x86_64-apple-darwin", intel)
	entries, err := os.ReadDir(second)
	if err != nil || len(entries) != len(releaseAssetNames) {
		t.Fatalf("output has %d entries (%v); want exactly the five assets", len(entries), err)
	}
	for _, name := range releaseAssetNames {
		if !bytes.Equal(readFile(t, filepath.Join(first, name)), readFile(t, filepath.Join(second, name))) {
			t.Fatalf("%s differs between identical packaging runs", name)
		}
	}

	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(readFile(t, filepath.Join(first, "SHA256SUMS"))), "\n"), "\n") {
		sum, name, _ := strings.Cut(line, "  ")
		sums[name] = sum
	}
	if len(sums) != 3 {
		t.Fatalf("SHA256SUMS names %d files; want 3", len(sums))
	}
	for name, sum := range sums {
		if digest := sha256.Sum256(readFile(t, filepath.Join(first, name))); hex.EncodeToString(digest[:]) != sum {
			t.Fatalf("SHA256SUMS does not match %s", name)
		}
	}
	var manifest struct {
		Version, Tag, Source string
		Assets               map[string]releaseAsset
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(first, "latest.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.2.3" || manifest.Tag != "v1.2.3" || manifest.Source != fixtureSource || len(manifest.Assets) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, target := range releaseTargets {
		identity, _ := Expected("1.2.3", fixtureSource, target[1])
		archive := "dark-factory-v1.2.3-" + target[0] + ".tar.gz"
		asset := manifest.Assets[target[0]]
		content := readFile(t, filepath.Join(first, archive))
		if asset.URL != "https://github.com/example/project/releases/download/v1.2.3/"+archive || asset.SHA256 != sums[archive] ||
			asset.Bytes != int64(len(content)) || asset.UnpackedBytes <= asset.Bytes || asset.BuildID != identity.BuildID() {
			t.Fatalf("%s manifest entry = %+v", target[0], asset)
		}
		inputs := map[string]string{"darwin/arm64": arm, "darwin/amd64": intel}
		assertArchive(t, content, inputs[target[1]], asset.UnpackedBytes)
	}
	formula := string(readFile(t, filepath.Join(first, "dark-factory.rb")))
	if !strings.Contains(formula, `sha256 "`+sums["latest.json"]+`"`) || !strings.Contains(formula, `sha256 "`+sums[releaseAssetNames[0]]+`"`) {
		t.Fatal("formula does not pin the packaged checksums")
	}
}

func TestPackageReleaseRefusesBadInputWithoutPartialOutput(t *testing.T) {
	root := t.TempDir()
	arm, intel := filepath.Join(root, "arm"), filepath.Join(root, "intel")
	for _, component := range []string{"factoryd", "factory-runner", "factoryctl"} {
		buildFixture(t, arm, component, component, "1.2.3", fixtureSource, "darwin/arm64", "")
	}
	// A wrong embedded release identity is rejected.
	buildFixture(t, intel, "factoryd", "factoryd", "1.2.4", fixtureSource, "darwin/amd64", "")
	output := filepath.Join(root, "out")
	if err := PackageRelease(packageArguments(output, "aarch64-apple-darwin", arm, "x86_64-apple-darwin", intel)); err == nil || !strings.Contains(err.Error(), "linked receipt") {
		t.Fatalf("identity mismatch = %v", err)
	}
	// A missing binary in the second target also leaves nothing behind.
	buildFixture(t, intel, "factoryd", "factoryd", "1.2.3", fixtureSource, "darwin/amd64", "")
	if err := PackageRelease(packageArguments(output, "aarch64-apple-darwin", arm, "x86_64-apple-darwin", intel)); err == nil {
		t.Fatal("incomplete target was packaged")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 2 {
		t.Fatalf("failed packaging left output or staging: %v", entries)
	}
	// An existing output, even an empty partial one, is never replaced.
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PackageRelease(packageArguments(output, "aarch64-apple-darwin", arm, "x86_64-apple-darwin", arm)); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output = %v", err)
	}
	for _, arguments := range [][]string{
		packageArguments(filepath.Join(root, "dup"), "aarch64-apple-darwin", arm, "aarch64-apple-darwin", arm),
		packageArguments(filepath.Join(root, "bad"), "aarch64-apple-darwin", arm, "i386-apple-darwin", arm),
		{"1.2.3", fixtureSource, filepath.Join(root, "tag"), "example/project", "aarch64-apple-darwin", arm, "x86_64-apple-darwin", intel},
		{"v1.2.3", fixtureSource, filepath.Join(root, "repo"), "example", "aarch64-apple-darwin", arm, "x86_64-apple-darwin", intel},
	} {
		if PackageRelease(arguments) == nil {
			t.Fatalf("invalid arguments accepted: %v", arguments)
		}
	}
}

func TestFormulaRendersExactCandidate(t *testing.T) {
	data := map[string]any{
		"Tag": "v1.2.3", "Version": "1.2.3", "Prerelease": false, "Source": fixtureSource, "Repository": "example/project",
		"ArmBuildID": strings.Repeat("a", 64), "IntelBuildID": strings.Repeat("b", 64),
		"ArmSHA": strings.Repeat("c", 64), "IntelSHA": strings.Repeat("d", 64), "ManifestSHA": strings.Repeat("e", 64),
	}
	got := new(strings.Builder)
	if err := formulaTemplate.Execute(got, data); err != nil {
		t.Fatal(err)
	}
	if want := string(readFile(t, "testdata/dark-factory.rb")); got.String() != want {
		t.Fatalf("formula differs from testdata/dark-factory.rb:\n%s", got)
	}
	data["Tag"], data["Version"], data["Prerelease"] = "v1.2.3-rc.1", "1.2.3-rc.1", true
	got.Reset()
	if err := formulaTemplate.Execute(got, data); err != nil {
		t.Fatal(err)
	}
	if strings.Count(got.String(), "  version ") != 1 || !strings.Contains(got.String(), "latest.json\"\n  version \"1.2.3-rc.1\"\n  sha256") {
		t.Fatalf("prerelease formula does not declare its version once:\n%s", got)
	}
}

func assertArchive(t *testing.T, content []byte, inputs string, unpacked int64) {
	t.Helper()
	if !bytes.Equal(content[4:8], []byte{0, 0, 0, 0}) {
		t.Fatal("archive embeds its packaging time")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(compressed)
	var names []string
	var total int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		member, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		if header.Mode != 0o755 || header.Uid != 0 || header.Gid != 0 || header.Uname != "root" || header.Gname != "wheel" ||
			!header.ModTime.Equal(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || header.Typeflag != tar.TypeReg {
			t.Fatalf("archive member metadata is not normalized: %+v", header)
		}
		if !bytes.Equal(member, readFile(t, filepath.Join(inputs, header.Name))) {
			t.Fatalf("%s is not the verified input binary", header.Name)
		}
		names = append(names, header.Name)
		total += int64(len(member))
	}
	if fmt.Sprint(names) != "[factoryd factory-runner factoryctl]" || total != unpacked {
		t.Fatalf("archive members = %v (%d bytes, manifest %d)", names, total, unpacked)
	}
}

func packageArguments(output, firstTarget, firstDir, secondTarget, secondDir string) []string {
	return []string{"v1.2.3", fixtureSource, output, "example/project", firstTarget, firstDir, secondTarget, secondDir}
}

func mustPackage(t *testing.T, output, firstTarget, firstDir, secondTarget, secondDir string) {
	t.Helper()
	if err := PackageRelease(packageArguments(output, firstTarget, firstDir, secondTarget, secondDir)); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
