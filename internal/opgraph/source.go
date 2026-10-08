package opgraph

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"path/filepath"
	"strings"
)

// Archive bounds: one repository may hold at most this much analysable source.
const (
	maxArchiveFiles        = 50_000
	maxArchiveBytes  int64 = 1 << 30
	maxReadFileBytes       = 1 << 20
)

// Repository is one source of a system: an exact revision's analysable files.
type Repository struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Revision string            `json:"revision,omitempty"`
	Files    map[string][]byte `json:"-"`
}

// ReadArchive keeps the analysable regular files of one immutable git archive.
// Links, submodules, ignored trees and tests cannot contribute evidence.
func ReadArchive(ctx context.Context, archive []byte) (map[string][]byte, error) {
	files := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(archive))
	count, size := 0, int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, err := reader.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if item.Typeflag != tar.TypeReg {
			continue
		}
		name := item.Name
		if name == "." || path.Clean(name) != name || !filepath.IsLocal(name) {
			return nil, errors.New("invalid archived source path")
		}
		count++
		size += item.Size
		if count > maxArchiveFiles || size > maxArchiveBytes {
			return nil, ErrBounds
		}
		if item.Size > maxReadFileBytes || !Analysable(name) {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(reader, maxReadFileBytes))
		if err != nil {
			return nil, err
		}
		files[name] = body
	}
}

// Analysable says whether a path can hold operational evidence: source in a
// recognised language, or a manifest or deployment declaration. Tests,
// fixtures, examples, documentation and vendored trees never can.
func Analysable(name string) bool {
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		if part == ".github" {
			continue
		}
		if ignored(part) {
			return false
		}
	}
	if workflowFile(name) {
		return true
	}
	if Classify(name) == "tests" {
		return false
	}
	base := strings.ToLower(path.Base(name))
	switch base {
	case "go.mod", "package.json", "cargo.toml", "wrangler.toml", "wrangler.json", "wrangler.jsonc", "vercel.json",
		"procfile", "fly.toml", "dockerfile", "requirements.txt", "pyproject.toml", "gemfile", "pom.xml",
		"build.gradle", "build.gradle.kts", "manage.py", "config.ru", "routes.rb", "page.mdx":
		return true
	}
	if strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose.") {
		return true
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".py", ".rb", ".rs", ".java", ".kt":
		return !strings.HasSuffix(base, ".d.ts") && !strings.HasSuffix(base, ".d.mts")
	}
	return false
}

// A dot directory is tooling state; the rest are dependencies, build output,
// or code that never runs in production.
func ignored(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "node_modules", "vendor", "dist", "build", "target", "coverage", "__pycache__", "out",
		"testdata", "fixtures", "__fixtures__", "examples", "example", "e2e", "mocks", "__mocks__":
		return true
	}
	return false
}

// Classify is filename-only, in precedence order: explicit test names
// and test-directory source files, documentation, configuration, assets, source,
// then unclassified. Ambiguous files remain unclassified; tests imply no result.
func Classify(relative string) string {
	name := strings.ToLower(path.Base(relative))
	extension := strings.ToLower(path.Ext(name))
	source := strings.Contains("|.go|.js|.jsx|.mjs|.cjs|.ts|.tsx|.mts|.cts|.py|.rs|.c|.h|.cc|.cpp|.hpp|.java|.kt|.swift|.rb|.php|.sh|.sql|.css|.scss|.html|.vue|.svelte|", "|"+extension+"|") && extension != ""
	testDir := false
	for _, part := range strings.Split(strings.ToLower(relative), "/") {
		if part == "test" || part == "tests" || part == "__tests__" {
			testDir = true
		}
	}
	if source && (testDir || strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") || strings.HasPrefix(name, "test_") || strings.HasSuffix(strings.TrimSuffix(name, extension), "_test")) {
		return "tests"
	}
	if extension == ".md" || extension == ".mdx" || extension == ".rst" || extension == ".adoc" || name == "readme" || name == "license" || name == "licence" {
		return "documentation"
	}
	if extension == ".json" || extension == ".yaml" || extension == ".yml" || extension == ".toml" || extension == ".ini" || extension == ".cfg" || extension == ".lock" || name == "go.mod" || name == "go.sum" || name == "makefile" || name == "dockerfile" || name == ".gitignore" || name == ".env" {
		return "configuration"
	}
	if strings.Contains("|.png|.jpg|.jpeg|.gif|.webp|.svg|.ico|.avif|.woff|.woff2|.ttf|.otf|.mp3|.wav|.mp4|.webm|.pdf|", "|"+extension+"|") && extension != "" {
		return "assets"
	}
	if source {
		return "source"
	}
	return "unclassified"
}
