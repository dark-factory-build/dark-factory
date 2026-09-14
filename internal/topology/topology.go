// Package topology derives a bounded, deterministic graph from a source tree.
package topology

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxDepth                   = 32
	maxFiles                   = 50_000
	maxNodes                   = 4_096
	maxEdges                   = 16_384
	maxBytes             int64 = 1 << 30
	maxAnalyzerFileBytes       = 4 << 20
)

var ErrBounds = errors.New("topology bounds exceeded")

type NodeKind string

const (
	NodeRepository NodeKind = "repository"
	NodeModule     NodeKind = "module"
	NodePackage    NodeKind = "package"
	NodeDirectory  NodeKind = "directory"
)

type EdgeKind string

const EdgeImports EdgeKind = "imports"

type Snapshot struct {
	Digest         string `json:"digest"`
	SourceRevision string `json:"source_revision,omitempty"`
	Nodes          []Node `json:"nodes"`
	Edges          []Edge `json:"edges"`
}

type Node struct {
	ID           string     `json:"id"`
	ParentID     string     `json:"parent_id"`
	Kind         NodeKind   `json:"kind"`
	RelativePath string     `json:"relative_path"`
	Label        string     `json:"label"`
	Language     string     `json:"language"`
	SizeBucket   string     `json:"size_bucket"`
	Inventory    *Inventory `json:"inventory,omitempty"`
}

type Edge struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Kind   EdgeKind `json:"kind"`
	Weight uint32   `json:"weight"`
}

type limits struct {
	depth, files, nodes, edges int
	bytes                      int64
}

var defaultLimits = limits{depth: maxDepth, files: maxFiles, nodes: maxNodes, edges: maxEdges, bytes: maxBytes}

type goPackage struct {
	names, imports map[string]uint32
}

type jsPackage struct {
	name string
	deps map[string]struct{}
}

type languageFile struct{ dir, language string }

type discovery struct {
	dirs      map[string]int64
	inventory map[string]*Inventory
	files     int
	modules   map[string]string
	goPkgs    map[string]*goPackage
	jsPkgs    map[string]jsPackage
	languages []languageFile
}

type packageFact struct {
	label                          string
	goCode, javascript, typescript bool
}

type pathEdge struct{ from, to string }

type analysis struct {
	packages map[string]packageFact
	imports  map[pathEdge]uint32
}

// Build scans root without running project commands.
// project salts every node id, so two projects holding the same path are
// never served the same id.
func Build(ctx context.Context, root, project string) (Snapshot, error) {
	return build(ctx, root, project, defaultLimits)
}

// NodeForPath returns the deepest topology node containing relativePath.
func NodeForPath(snapshot Snapshot, relativePath string) (Node, bool) {
	relativePath = path.Clean(filepath.ToSlash(relativePath))
	if relativePath == ".." || path.IsAbs(relativePath) || strings.HasPrefix(relativePath, "../") {
		return Node{}, false
	}
	bestDepth, bestKind := -1, -1
	var best Node
	for _, node := range snapshot.Nodes {
		if !contains(node.RelativePath, relativePath) {
			continue
		}
		nodeDepth, kind := depth(node.RelativePath), nodeKindOrder(node.Kind)
		if nodeDepth > bestDepth || nodeDepth == bestDepth && kind > bestKind {
			best, bestDepth, bestKind = node, nodeDepth, kind
		}
	}
	return best, bestDepth >= 0
}

func build(ctx context.Context, root, project string, bounds limits) (Snapshot, error) {
	found, err := discover(ctx, root, bounds)
	if err != nil {
		return Snapshot{}, err
	}
	analyzed, err := analyze(found, bounds.edges)
	if err != nil {
		return Snapshot{}, err
	}
	revision := sourceRevision(root)
	nodes, edges, err := graph(found, analyzed, project, bounds.nodes, bounds.edges)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{SourceRevision: revision, Nodes: nodes, Edges: edges}
	result.Digest = graphDigest(nodes, edges)
	return result, nil
}

func graphDigest(nodes []Node, edges []Edge) string {
	return digest("dark-factory/topology/graph/v1\x00", struct {
		Nodes []Node `json:"nodes"`
		Edges []Edge `json:"edges"`
	}{nodes, edges})
}

func discover(ctx context.Context, root string, bounds limits) (*discovery, error) {
	if bounds.depth < 0 || bounds.files < 1 || bounds.nodes < 1 || bounds.edges < 0 || bounds.bytes < 0 {
		return nil, fmt.Errorf("%w: invalid limits", ErrBounds)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect topology root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("topology root is not a directory")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve topology root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve topology root symlinks: %w", err)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open topology root: %w", err)
	}
	defer rootFS.Close()
	result := &discovery{
		dirs: make(map[string]int64), inventory: make(map[string]*Inventory), modules: make(map[string]string),
		goPkgs: make(map[string]*goPackage), jsPkgs: make(map[string]jsPackage),
	}
	var total int64
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// The walk can visit tens of thousands of entries. It runs on a request
		// with a deadline, so it stops when that deadline does.
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && ignored(entry.Name()) {
				return filepath.SkipDir
			}
			if depth(rel) > bounds.depth {
				return bound("depth", bounds.depth)
			}
			if _, exists := result.dirs[rel]; !exists && len(result.dirs) == bounds.nodes {
				return bound("node count", bounds.nodes)
			}
			result.dirs[rel] = 0
			result.inventory[rel] = &Inventory{Samples: []string{}}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			return nil
		}
		if depth(rel) > bounds.depth {
			return bound("depth", bounds.depth)
		}
		if result.files == bounds.files {
			return bound("file count", bounds.files)
		}
		if fileInfo.Size() > bounds.bytes-total {
			return bound("byte count", bounds.bytes)
		}
		total += fileInfo.Size()
		result.files++
		dir := path.Dir(rel)
		result.dirs[dir] += fileInfo.Size()
		inventory := result.inventory[dir]
		inventory.Direct.add(classify(rel))
		sampleName := path.Base(rel)
		if len(inventory.Samples) < 3 && len(sampleName) <= 128 && utf8.ValidString(sampleName) {
			inventory.Samples = append(inventory.Samples, sampleName)
		} else {
			inventory.SamplesOmitted++
		}
		switch strings.ToLower(path.Ext(rel)) {
		case ".js", ".jsx", ".mjs", ".cjs":
			result.languages = append(result.languages, languageFile{dir, "javascript"})
		case ".ts", ".tsx", ".mts", ".cts":
			result.languages = append(result.languages, languageFile{dir, "typescript"})
		}
		base := path.Base(rel)
		if base != "go.mod" && base != "package.json" && path.Ext(rel) != ".go" {
			return nil
		}
		body, err := readSmallRoot(rootFS, rel, maxAnalyzerFileBytes)
		if errors.Is(err, ErrBounds) {
			// A generated or vendored file past the analyzer bound contributes no
			// imports rather than failing the whole tree. Its bytes already count
			// toward its directory, and the tree-wide bounds stay hard stops. The
			// body must stay non-nil: a nil one sends the parser to the disk.
			body, err = []byte{}, nil
		}
		if err != nil {
			return err
		}
		if base == "go.mod" {
			result.modules[dir] = moduleName(body)
			return nil
		}
		if base == "package.json" {
			result.jsPkgs[dir] = parseJSPackage(body)
			return nil
		}
		pkg := result.goPkgs[dir]
		if pkg == nil {
			pkg = &goPackage{make(map[string]uint32), make(map[string]uint32)}
			result.goPkgs[dir] = pkg
		}
		parsed, _ := parser.ParseFile(token.NewFileSet(), rel, body, parser.ImportsOnly)
		if parsed != nil {
			// A file with no package clause the parser could read — an oversize
			// one included — reports the empty name, which would otherwise win
			// the sorted vote and cost the package its name.
			if parsed.Name.Name != "" {
				pkg.names[parsed.Name.Name]++
			}
			for _, imported := range parsed.Imports {
				if value, err := strconv.Unquote(imported.Path.Value); err == nil {
					pkg.imports[value]++
				}
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrBounds) {
			return nil, err
		}
		return nil, fmt.Errorf("discover topology: %w", err)
	}
	dirs := keys(result.dirs)
	sort.Slice(dirs, func(i, j int) bool { return depth(dirs[i]) > depth(dirs[j]) })
	for _, dir := range dirs {
		inventory := result.inventory[dir]
		inventory.Total.plus(inventory.Direct)
		if dir != "." {
			result.inventory[path.Dir(dir)].Total.plus(inventory.Total)
			result.dirs[path.Dir(dir)] += result.dirs[dir]
		}
	}
	return result, nil
}

func analyze(found *discovery, edgeLimit int) (analysis, error) {
	result := analysis{make(map[string]packageFact), make(map[pathEdge]uint32)}
	addImport := func(edge pathEdge, weight uint32) error {
		if _, exists := result.imports[edge]; !exists && len(result.imports) == edgeLimit {
			return bound("edge count", edgeLimit)
		}
		result.imports[edge] += weight
		return nil
	}
	for dir, manifest := range found.jsPkgs {
		result.packages[dir] = packageFact{label: label(manifest.name, dir, "package"), javascript: true}
	}
	for dir, pkg := range found.goPkgs {
		if _, _, ok := nearestModule(dir, found.modules); !ok {
			continue
		}
		fact := result.packages[dir]
		if fact.label == "" {
			fact.label = label(first(keys(pkg.names)), dir, "package")
		}
		fact.goCode = true
		result.packages[dir] = fact
	}
	jsRoots := keys(found.jsPkgs)
	sort.Slice(jsRoots, func(i, j int) bool { return depth(jsRoots[i]) > depth(jsRoots[j]) })
	for _, file := range found.languages {
		for _, root := range jsRoots {
			if contains(root, file.dir) {
				fact := result.packages[root]
				fact.typescript = fact.typescript || file.language == "typescript"
				fact.javascript = fact.javascript || file.language == "javascript"
				result.packages[root] = fact
				break
			}
		}
	}
	localGo := make(map[string][]string)
	for dir, pkg := range result.packages {
		moduleDir, moduleName, ok := nearestModule(dir, found.modules)
		if !pkg.goCode || !ok || moduleName == "" {
			continue
		}
		name := moduleName
		if suffix := relative(moduleDir, dir); suffix != "." {
			name += "/" + suffix
		}
		localGo[name] = append(localGo[name], dir)
	}
	for from, pkg := range found.goPkgs {
		for imported, weight := range pkg.imports {
			if targets := localGo[imported]; result.packages[from].goCode && len(targets) == 1 && targets[0] != from {
				if err := addImport(pathEdge{from, targets[0]}, weight); err != nil {
					return analysis{}, err
				}
			}
		}
	}
	localJS := make(map[string][]string)
	for dir, pkg := range found.jsPkgs {
		if pkg.name != "" {
			localJS[pkg.name] = append(localJS[pkg.name], dir)
		}
	}
	for from, pkg := range found.jsPkgs {
		for dependency := range pkg.deps {
			if targets := localJS[dependency]; len(targets) == 1 && targets[0] != from {
				if err := addImport(pathEdge{from, targets[0]}, 1); err != nil {
					return analysis{}, err
				}
			}
		}
	}
	return result, nil
}

func graph(found *discovery, analyzed analysis, project string, nodeLimit, edgeLimit int) ([]Node, []Edge, error) {
	add := func(nodes *[]Node, node Node) error {
		if len(*nodes) == nodeLimit {
			return bound("node count", nodeLimit)
		}
		*nodes = append(*nodes, node)
		return nil
	}
	repository := nodeID(project, NodeRepository, ".")
	nodes := []Node{{ID: repository, Kind: NodeRepository, RelativePath: ".", Label: "repository", SizeBucket: bucket(found.dirs["."])}}
	primary := map[string]string{".": repository}
	dirs := keys(found.dirs)
	sort.Slice(dirs, func(i, j int) bool {
		return depth(dirs[i]) < depth(dirs[j]) || depth(dirs[i]) == depth(dirs[j]) && dirs[i] < dirs[j]
	})
	for _, dir := range dirs {
		parent := repository
		if dir != "." {
			parent = primary[path.Dir(dir)]
		}
		moduleName, module := found.modules[dir]
		pkg, isPackage := analyzed.packages[dir]
		if module {
			id := nodeID(project, NodeModule, dir)
			if err := add(&nodes, Node{ID: id, ParentID: parent, Kind: NodeModule, RelativePath: dir, Label: label(moduleName, dir, "module"), Language: "go", SizeBucket: bucket(found.dirs[dir])}); err != nil {
				return nil, nil, err
			}
			primary[dir], parent = id, id
		}
		if isPackage {
			id := nodeID(project, NodePackage, dir)
			if err := add(&nodes, Node{ID: id, ParentID: parent, Kind: NodePackage, RelativePath: dir, Label: pkg.label, Language: language(pkg), SizeBucket: bucket(found.dirs[dir])}); err != nil {
				return nil, nil, err
			}
			if !module {
				primary[dir] = id
			}
		}
		if dir != "." && !module && !isPackage {
			id := nodeID(project, NodeDirectory, dir)
			if err := add(&nodes, Node{ID: id, ParentID: parent, Kind: NodeDirectory, RelativePath: dir, Label: path.Base(dir), SizeBucket: bucket(found.dirs[dir])}); err != nil {
				return nil, nil, err
			}
			primary[dir] = id
		}
	}
	for i := range nodes {
		nodes[i].Inventory = found.inventory[nodes[i].RelativePath]
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].RelativePath < nodes[j].RelativePath || nodes[i].RelativePath == nodes[j].RelativePath && nodes[i].Kind < nodes[j].Kind
	})
	if len(analyzed.imports) > edgeLimit {
		return nil, nil, bound("edge count", edgeLimit)
	}
	edges := make([]Edge, 0, len(analyzed.imports))
	for edge, weight := range analyzed.imports {
		edges = append(edges, Edge{nodeID(project, NodePackage, edge.from), nodeID(project, NodePackage, edge.to), EdgeImports, weight})
	}
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].From < edges[j].From || edges[i].From == edges[j].From && (edges[i].To < edges[j].To || edges[i].To == edges[j].To && edges[i].Kind < edges[j].Kind)
	})
	return nodes, edges, nil
}

func parseJSPackage(body []byte) jsPackage {
	var value struct {
		Name                 string            `json:"name"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	result := jsPackage{deps: make(map[string]struct{})}
	if json.Unmarshal(body, &value) != nil {
		return result
	}
	result.name = strings.TrimSpace(value.Name)
	for _, dependencies := range []map[string]string{value.Dependencies, value.DevDependencies, value.PeerDependencies, value.OptionalDependencies} {
		for name := range dependencies {
			result.deps[name] = struct{}{}
		}
	}
	return result
}

func moduleName(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			if value, err := strconv.Unquote(fields[1]); err == nil {
				return value
			}
			return fields[1]
		}
	}
	return ""
}

func nearestModule(dir string, modules map[string]string) (string, string, bool) {
	for current := dir; ; current = path.Dir(current) {
		if name, ok := modules[current]; ok {
			return current, name, true
		}
		if current == "." {
			return "", "", false
		}
	}
}

func sourceRevision(root string) string {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	if !info.IsDir() {
		body, err := readSmall(gitDir, 4096)
		value := strings.TrimSpace(string(body))
		if err != nil || !strings.HasPrefix(value, "gitdir: ") {
			return ""
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(value, "gitdir: "))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	head, err := readSmall(filepath.Join(gitDir, "HEAD"), 4096)
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(head))
	if oid := gitOID(value); oid != "" {
		return oid
	}
	ref := strings.TrimSpace(strings.TrimPrefix(value, "ref: "))
	if !strings.HasPrefix(value, "ref: refs/") || path.Clean(ref) != ref || strings.Contains(ref, "..") {
		return ""
	}
	search := []string{gitDir}
	if body, err := readSmall(filepath.Join(gitDir, "commondir"), 4096); err == nil {
		common := strings.TrimSpace(string(body))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		search = append(search, common)
	}
	for _, directory := range search {
		if body, err := readSmall(filepath.Join(directory, filepath.FromSlash(ref)), 4096); err == nil {
			if oid := gitOID(strings.TrimSpace(string(body))); oid != "" {
				return oid
			}
		}
		if body, err := readSmall(filepath.Join(directory, "packed-refs"), 4<<20); err == nil {
			for _, line := range strings.Split(string(body), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[1] == ref {
					return gitOID(fields[0])
				}
			}
		}
	}
	return ""
}

func readSmall(name string, limit int64) ([]byte, error) {
	before, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return readOpened(file, before, limit)
}

var errNotRegularFile = errors.New("not a regular file")

func readSmallRoot(root *os.Root, name string, limit int64) ([]byte, error) {
	name = filepath.FromSlash(name)
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	return readOpened(file, before, limit)
}

func readOpened(file *os.File, before fs.FileInfo, limit int64) ([]byte, error) {
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errNotRegularFile
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(body)) > limit {
		err = ErrBounds
	}
	return body, err
}

// A dot directory at any depth is tooling state, not the project's code: .git,
// caches, and local toolchains such as .tools all hide there. The root itself
// is exempt, so a project checked out under a dot directory still builds.
func ignored(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "coverage", "__pycache__":
		return true
	}
	return false
}

func label(value, rel, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 256 && utf8.ValidString(value) && !path.IsAbs(value) && !filepath.IsAbs(value) && filepath.VolumeName(value) == "" && !strings.ContainsAny(value, "\x00\r\n") {
		return value
	}
	if rel == "." {
		return fallback
	}
	return path.Base(rel)
}

func language(pkg packageFact) string {
	if pkg.goCode && (pkg.javascript || pkg.typescript) {
		return "mixed"
	}
	if pkg.goCode {
		return "go"
	}
	if pkg.typescript {
		return "typescript"
	}
	return "javascript"
}

func bucket(bytes int64) string {
	switch {
	case bytes == 0:
		return "empty"
	case bytes <= 4<<10:
		return "tiny"
	case bytes <= 64<<10:
		return "small"
	case bytes <= 1<<20:
		return "medium"
	default:
		return "large"
	}
}

func nodeID(project string, kind NodeKind, rel string) string {
	sum := sha256.Sum256([]byte("dark-factory/topology/node/v1\x00" + project + "\x00" + string(kind) + "\x00" + rel))
	return hex.EncodeToString(sum[:])
}

func nodeKindOrder(kind NodeKind) int {
	switch kind {
	case NodePackage:
		return 3
	case NodeModule:
		return 2
	case NodeDirectory:
		return 1
	case NodeRepository:
		return 0
	default:
		return -1
	}
}

func digest(domain string, value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(append([]byte(domain), body...))
	return hex.EncodeToString(sum[:])
}

func gitOID(value string) string {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 20 && len(decoded) != 32 {
		return ""
	}
	return strings.ToLower(value)
}

func depth(rel string) int {
	if rel == "." {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func contains(parent, child string) bool {
	return parent == "." || parent == child || strings.HasPrefix(child, parent+"/")
}

func relative(parent, child string) string {
	if parent == "." {
		return child
	}
	if parent == child {
		return "."
	}
	return strings.TrimPrefix(child, parent+"/")
}

func bound(name string, limit any) error {
	return fmt.Errorf("%w: %s exceeds %v", ErrBounds, name, limit)
}

func first[T any](values []T) (zero T) {
	if len(values) != 0 {
		return values[0]
	}
	return zero
}

func keys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

// Inventory counts eligible scanned physical files. Total includes Direct and
// descendants; nodes sharing a path describe the same inventory, never additive
// child totals. Samples name at most three direct files in lexical order.
type Inventory struct {
	Direct         InventoryCounts `json:"direct"`
	Total          InventoryCounts `json:"total"`
	Samples        []string        `json:"samples"`
	SamplesOmitted uint32          `json:"samples_omitted"`
}
type InventoryCounts struct {
	Source        uint32 `json:"source"`
	Tests         uint32 `json:"tests"`
	Documentation uint32 `json:"documentation"`
	Configuration uint32 `json:"configuration"`
	Assets        uint32 `json:"assets"`
	Unclassified  uint32 `json:"unclassified"`
}

func (counts *InventoryCounts) plus(other InventoryCounts) {
	counts.Source += other.Source
	counts.Tests += other.Tests
	counts.Documentation += other.Documentation
	counts.Configuration += other.Configuration
	counts.Assets += other.Assets
	counts.Unclassified += other.Unclassified
}
func (counts *InventoryCounts) add(category string) {
	switch category {
	case "source":
		counts.Source++
	case "tests":
		counts.Tests++
	case "documentation":
		counts.Documentation++
	case "configuration":
		counts.Configuration++
	case "assets":
		counts.Assets++
	default:
		counts.Unclassified++
	}
}

// Classification is filename-only, in precedence order: explicit test names
// and test-directory source files, documentation, configuration, assets, source,
// then unclassified. Ambiguous files remain unclassified; tests imply no result.
func classify(relative string) string {
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
