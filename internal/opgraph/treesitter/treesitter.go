// Package treesitter parses source with tree-sitter in pure Go. It runs the
// official web-tree-sitter runtime and the grammar repositories' released
// WebAssembly builds under wazero, linked the way Emscripten's loader links
// a main module and its side modules: one memory, one function table.
//
// Every binary is embedded, pinned by version and SHA-256, and only the
// grammars of languages actually parsed are decompressed and compiled.
package treesitter

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed wasm/*.wasm.gz
var embedded embed.FS

// pin is one released binary: where it came from and its exact digest.
type pin struct{ file, source, sha256 string }

var pins = map[string]pin{
	"runtime":    {"tree-sitter.wasm", "npm web-tree-sitter@0.25.10", "f38dcc4b43b818f9a0785bc1c6d5611a75ac4cdd428ff3f02757c34ca4e46d7f"},
	"javascript": {"tree-sitter-javascript.wasm", "tree-sitter/tree-sitter-javascript v0.25.0", "5fb488d0cabb4775a594bab85682de5ad6ce83c0d6ac997a9f82dd084d571240"},
	"typescript": {"tree-sitter-typescript.wasm", "tree-sitter/tree-sitter-typescript v0.23.2", "778025db5a8be0e70f8ccc3671e486dfeddd048c25d9e8a70c26de2e1bf6f97d"},
	"tsx":        {"tree-sitter-tsx.wasm", "tree-sitter/tree-sitter-typescript v0.23.2", "79e5da75ea62855a0cd67177685f0164eac87d5f630b3cbe1e0a099751ad30f8"},
	"python":     {"tree-sitter-python.wasm", "tree-sitter/tree-sitter-python v0.25.0", "16108b50df4ee9a30168794252ab55e7c93bfc5765d7fa0aa3e335752c515f47"},
	"ruby":       {"tree-sitter-ruby.wasm", "tree-sitter/tree-sitter-ruby v0.23.1", "09a96427d7c72f0613ed470cd9812223fc4a91d6a9c025c0235cc6bd59ff96f4"},
	"java":       {"tree-sitter-java.wasm", "tree-sitter/tree-sitter-java v0.23.5", "4fdeac4ca6ca089f06c6f7e562abcac1733cd465728cc7031ebb73c2019122c4"},
	"kotlin":     {"tree-sitter-kotlin.wasm", "fwcd/tree-sitter-kotlin 0.3.8", "c624e7443b371c28adc5d81674e73067564c12555ebe3ed96a6c8db814b7602d"},
	"rust":       {"tree-sitter-rust.wasm", "tree-sitter/tree-sitter-rust v0.24.2", "24c89bd9252255e4aebbcbd7d2d308bd92c86dd95a130fdc80efa49577b8d738"},
}

// Bounds. Work is counted in tree-sitter progress callbacks (one per hundred
// parser or query operations), not wall time, so a file that exhausts its
// budget does so on every machine and the result stays deterministic.
//
// ponytail: some shapes cost far more than their operation count (a query
// over a tree nested thousands deep is quadratic), so a per-file deadline
// backs the budget. Only such pathological files can hit it, and only they
// make a result depend on the machine; a depth bound would be the
// deterministic upgrade.
const (
	maxPages     = 2048 // 128 MiB of wasm memory per parser
	stackSize    = 256 << 10
	parseBudget  = 20_000
	queryBudget  = 20_000
	inputChunk   = 10*1024/2 - 1 // UTF-16 units per read; the glue's buffer is 10 KiB
	fileDeadline = 2 * time.Second
)

// ErrBudget reports a file that needed more work than one file may use.
var ErrBudget = errors.New("treesitter: work budget exhausted")

var (
	cache   = wazero.NewCompilationCache()
	loaded  sync.Map // name -> *binary
	loading sync.Mutex
)

type binaryModule struct {
	bytes []byte
	info  dylink
}

// load decompresses, verifies and links one pinned binary, once per process.
func load(name string) (*binaryModule, error) {
	if held, ok := loaded.Load(name); ok {
		return held.(*binaryModule), nil
	}
	loading.Lock()
	defer loading.Unlock()
	if held, ok := loaded.Load(name); ok {
		return held.(*binaryModule), nil
	}
	pinned, ok := pins[name]
	if !ok {
		return nil, fmt.Errorf("treesitter: no grammar for %q", name)
	}
	compressed, err := embedded.Open("wasm/" + pinned.file + ".gz")
	if err != nil {
		return nil, err
	}
	defer compressed.Close()
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, err
	}
	module, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return nil, err
	}
	if digest := sha256.Sum256(module); hex.EncodeToString(digest[:]) != pinned.sha256 {
		return nil, fmt.Errorf("treesitter: %s does not match its pinned digest", pinned.file)
	}
	info, err := readDylink(module)
	if err != nil {
		return nil, err
	}
	held := &binaryModule{bytes: module, info: info}
	loaded.Store(name, held)
	return held, nil
}

// runtimeExports are the C library functions the runtime exports for side
// modules; anything else a grammar imports is a host trap.
var runtimeExports = map[string]bool{"malloc": true, "calloc": true, "realloc": true, "free": true, "memcmp": true,
	"memset": true, "memcpy": true, "memmove": true, "memchr": true, "strlen": true, "strcmp": true, "strncmp": true,
	"strncat": true, "strncpy": true, "iswspace": true, "iswalnum": true, "iswalpha": true, "iswblank": true,
	"iswdigit": true, "iswlower": true, "iswupper": true, "iswxdigit": true, "towlower": true, "towupper": true}

// Parser parses one language. It is not safe for concurrent use; Close
// releases its memory.
type Parser struct {
	ctx       context.Context
	runtime   wazero.Runtime
	memory    api.Memory
	functions map[string]api.Function
	language  uint32
	parser    uint32
	input     uint32
	transfer  uint32
	text      []uint16
	budget    int
	deadline  time.Time
	exhausted bool
}

// New links the runtime and one grammar into a fresh, isolated instance.
func New(ctx context.Context, language string) (*Parser, error) {
	runtimeModule, err := load("runtime")
	if err != nil {
		return nil, err
	}
	grammar, err := load(language)
	if err != nil {
		return nil, err
	}
	p := &Parser{ctx: ctx, functions: map[string]api.Function{}}
	p.runtime = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(cache).WithMemoryLimitPages(maxPages))
	if err := p.link(runtimeModule, grammar, language); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func align(value, to uint32) uint32 { return (value + to - 1) / to * to }

func (p *Parser) link(runtimeModule, grammar *binaryModule, language string) error {
	// Layout, as Emscripten's --stack-first lays it: the shadow stack first,
	// so an overflow runs off the bottom of memory and traps instead of
	// overwriting the grammar's tables; then runtime data, grammar data, heap.
	stackTop := uint32(1024 + stackSize)
	runtimeBase := align(stackTop, 1<<runtimeModule.info.memoryAlign)
	grammarBase := align(runtimeBase+runtimeModule.info.memory, 1<<grammar.info.memoryAlign)
	heapBase := align(grammarBase+grammar.info.memory, 16)
	runtimeTable := uint32(1)
	grammarTable := runtimeTable + runtimeModule.info.table
	pages := max(512, heapBase/65536+1) // the runtime imports at least 512 pages

	host := p.runtime.NewHostModuleBuilder("hst")
	i32 := api.ValueTypeI32
	trap := func(name string) api.GoModuleFunc {
		return func(context.Context, api.Module, []uint64) { panic(errors.New("treesitter: " + name)) }
	}
	host.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
		p.read(uint32(stack[0]), uint32(stack[1]), uint32(stack[4]))
	}), []api.ValueType{i32, i32, i32, i32, i32}, nil).Export("tree_sitter_parse_callback")
	progress := api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
		p.budget--
		p.exhausted = p.exhausted || p.budget < 0 || time.Now().After(p.deadline)
		stack[0] = 0
		if p.exhausted {
			stack[0] = 1
		}
	})
	host.NewFunctionBuilder().WithGoModuleFunction(progress, []api.ValueType{i32, i32}, []api.ValueType{i32}).Export("tree_sitter_progress_callback")
	host.NewFunctionBuilder().WithGoModuleFunction(progress, []api.ValueType{i32}, []api.ValueType{i32}).Export("tree_sitter_query_progress_callback")
	host.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(context.Context, api.Module, []uint64) {}), []api.ValueType{i32, i32}, nil).Export("tree_sitter_log_callback")
	host.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
		need := (uint64(uint32(stack[0])) + 65535) / 65536
		have := uint64(p.memory.Size()) / 65536
		stack[0] = 1
		if need > have {
			if _, ok := p.memory.Grow(uint32(need - have)); !ok {
				stack[0] = 0
			}
		}
	}), []api.ValueType{i32}, []api.ValueType{i32}).Export("emscripten_resize_heap")
	host.NewFunctionBuilder().WithGoModuleFunction(trap("abort"), nil, nil).Export("_abort_js")
	host.NewFunctionBuilder().WithGoModuleFunction(trap("abort"), nil, nil).Export("abort")
	host.NewFunctionBuilder().WithGoModuleFunction(trap("assertion failed"), []api.ValueType{i32, i32, i32, i32}, nil).Export("__assert_fail")
	if _, err := host.Instantiate(p.ctx); err != nil {
		return err
	}
	// The runtime only writes to stderr when debugging and reads the clock
	// only for timeouts, which are never set: both are inert here.
	wasi := p.runtime.NewHostModuleBuilder("wasi_snapshot_preview1")
	for name, params := range map[string][]api.ValueType{
		"fd_close": {i32}, "fd_write": {i32, i32, i32, i32}, "clock_time_get": {i32, api.ValueTypeI64, i32}, "fd_seek": {i32, api.ValueTypeI64, i32, i32},
	} {
		wasi.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) { stack[0] = 52 }), params, []api.ValueType{i32}).Export(name)
	}
	if _, err := wasi.Instantiate(p.ctx); err != nil {
		return err
	}
	env, err := p.instantiate("env", definitions(pages, maxPages, grammarTable+grammar.info.table, []global{
		{"__stack_pointer", true, stackTop}, {"__memory_base", false, runtimeBase}, {"__table_base", false, runtimeTable},
	}))
	if err != nil {
		return err
	}
	p.memory = env.ExportedMemory("memory")
	if _, err := p.instantiate("GOT.mem", definitions(0, 0, 0, []global{{"__heap_base", true, heapBase}})); err != nil {
		return err
	}
	if _, err := p.instantiate("gnv", definitions(0, 0, 0, []global{{"__memory_base", false, grammarBase}, {"__table_base", false, grammarTable}})); err != nil {
		return err
	}
	runtimeBytes := bytes.Clone(runtimeModule.bytes)
	if err := renameImports(runtimeBytes, func(module, _ string, kind byte) string {
		if module == "env" && kind == 0 {
			return "hst"
		}
		return module
	}); err != nil {
		return err
	}
	ts, err := p.instantiate("tsr", runtimeBytes)
	if err != nil {
		return err
	}
	grammarBytes := bytes.Clone(grammar.bytes)
	if err := renameImports(grammarBytes, func(module, name string, kind byte) string {
		switch {
		case module != "env":
			return module
		case name == "__memory_base" || name == "__table_base":
			return "gnv"
		case kind == 0 && runtimeExports[name]:
			return "tsr"
		case kind == 0:
			return "hst"
		}
		return module
	}); err != nil {
		return err
	}
	lang, err := p.instantiate("grammar", grammarBytes)
	if err != nil {
		return err
	}
	for _, step := range []struct {
		module api.Module
		name   string
	}{{ts, "__wasm_apply_data_relocs"}, {lang, "__wasm_apply_data_relocs"}, {ts, "__wasm_call_ctors"}, {lang, "__wasm_call_ctors"}} {
		if fn := step.module.ExportedFunction(step.name); fn != nil {
			if _, err := fn.Call(p.ctx); err != nil {
				return err
			}
		}
	}
	for name := range ts.ExportedFunctionDefinitions() {
		p.functions[name] = ts.ExportedFunction(name)
	}
	result, err := lang.ExportedFunction("tree_sitter_" + language).Call(p.ctx)
	if err != nil {
		return err
	}
	p.language = uint32(result[0])
	if p.transfer, err = p.call("ts_init"); err != nil {
		return err
	}
	if _, err := p.call("ts_parser_new_wasm"); err != nil {
		return err
	}
	p.parser, p.input = p.word(p.transfer), p.word(p.transfer+4)
	if ok, err := p.call("ts_parser_set_language", p.parser, p.language); err != nil || ok == 0 {
		return errors.Join(err, fmt.Errorf("treesitter: the runtime cannot load the %s grammar", language))
	}
	return nil
}

func (p *Parser) instantiate(name string, module []byte) (api.Module, error) {
	compiled, err := p.runtime.CompileModule(p.ctx, module)
	if err != nil {
		return nil, fmt.Errorf("treesitter: compile %s: %w", name, err)
	}
	instance, err := p.runtime.InstantiateModule(p.ctx, compiled, wazero.NewModuleConfig().WithName(name).WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("treesitter: instantiate %s: %w", name, err)
	}
	return instance, nil
}

// Close releases the parser's instance and memory.
func (p *Parser) Close() {
	if p.runtime != nil {
		_ = p.runtime.Close(p.ctx)
		p.runtime = nil
	}
}

func (p *Parser) call(name string, args ...uint32) (uint32, error) {
	fn := p.functions[name]
	if fn == nil {
		return 0, fmt.Errorf("treesitter: runtime has no %s", name)
	}
	params := make([]uint64, len(args))
	for index, arg := range args {
		params[index] = uint64(arg)
	}
	results, err := fn.Call(p.ctx, params...)
	if err != nil || len(results) == 0 {
		return 0, err
	}
	return uint32(results[0]), nil
}

func (p *Parser) word(address uint32) uint32 {
	value, _ := p.memory.ReadUint32Le(address)
	return value
}

// read serves the parser's input callback: UTF-16 units from index on.
func (p *Parser) read(buffer, index, lengthRead uint32) {
	n := 0
	if int(index) < len(p.text) {
		n = min(len(p.text)-int(index), inputChunk)
		if end := int(index) + n; end < len(p.text) && p.text[end-1] >= 0xd800 && p.text[end-1] < 0xdc00 {
			n-- // never split a surrogate pair across reads
		}
	}
	chunk := make([]byte, 2*n)
	for offset, unit := range p.text[index : int(index)+n] {
		binary.LittleEndian.PutUint16(chunk[2*offset:], unit)
	}
	p.memory.Write(buffer, chunk)
	p.memory.WriteUint32Le(lengthRead, uint32(n))
}

// Tree is one parsed file. Offsets it reports are byte offsets in the source.
type Tree struct {
	parser  *Parser
	tree    uint32
	source  []byte
	offsets []int32 // UTF-16 unit -> byte offset
}

// Parse parses source; invalid UTF-8 reads as U+FFFD at the same offsets.
func (p *Parser) Parse(source []byte) (*Tree, error) {
	tree := &Tree{parser: p, source: source}
	p.text = p.text[:0]
	tree.offsets = make([]int32, 0, len(source)+1)
	for at := 0; at < len(source); {
		r, size := utf8.DecodeRune(source[at:])
		if r >= 0x10000 {
			high, low := utf16.EncodeRune(r)
			p.text = append(p.text, uint16(high), uint16(low))
			tree.offsets = append(tree.offsets, int32(at), int32(at))
		} else {
			p.text = append(p.text, uint16(r))
			tree.offsets = append(tree.offsets, int32(at))
		}
		at += size
	}
	tree.offsets = append(tree.offsets, int32(len(source)))
	p.budget, p.deadline, p.exhausted = parseBudget, time.Now().Add(fileDeadline), false
	handle, err := p.call("ts_parser_parse_wasm", p.parser, p.input, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	if handle == 0 || p.exhausted {
		if handle != 0 {
			_, _ = p.call("ts_tree_delete", handle)
		}
		// A cancelled parse is kept for resumption; the next file must start
		// fresh, not continue the abandoned one.
		if _, err := p.call("ts_parser_reset", p.parser); err != nil {
			return nil, err
		}
		return nil, ErrBudget
	}
	tree.tree = handle
	return tree, nil
}

// Close frees the tree.
func (t *Tree) Close() {
	if t.tree != 0 {
		_, _ = t.parser.call("ts_tree_delete", t.tree)
		t.tree = 0
	}
}

// Query is a compiled tree-sitter query. Predicates are not evaluated:
// callers filter captures in Go.
type Query struct {
	query uint32
	names []string
}

// Query compiles a query for this parser's language.
func (p *Parser) Query(source string) (*Query, error) {
	text, err := p.alloc([]byte(source))
	if err != nil {
		return nil, err
	}
	defer p.call("free", text)
	report, err := p.alloc(make([]byte, 8))
	if err != nil {
		return nil, err
	}
	defer p.call("free", report)
	handle, err := p.call("ts_query_new", p.language, text, uint32(len(source)), report, report+4)
	if err != nil {
		return nil, err
	}
	if handle == 0 {
		return nil, fmt.Errorf("treesitter: query error %d at byte %d", p.word(report+4), p.word(report))
	}
	query := &Query{query: handle}
	count, err := p.call("ts_query_capture_count", handle)
	if err != nil {
		return nil, err
	}
	for id := uint32(0); id < count; id++ {
		name, err := p.call("ts_query_capture_name_for_id", handle, id, report)
		if err != nil {
			return nil, err
		}
		text, _ := p.memory.Read(name, p.word(report))
		query.names = append(query.names, string(text))
	}
	return query, nil
}

func (p *Parser) alloc(data []byte) (uint32, error) {
	address, err := p.call("malloc", uint32(max(len(data), 1)))
	if err != nil {
		return 0, err
	}
	if address == 0 || !p.memory.Write(address, data) {
		return 0, errors.New("treesitter: out of memory")
	}
	return address, nil
}

// Capture is one captured node.
type Capture struct {
	Name       string
	Text       string
	Start, End int // byte offsets
	Line       int // 1-based
}

// Match is one query match, its captures in pattern order.
type Match struct {
	Pattern  int
	Captures []Capture
}

// Matches runs a query over the whole tree, in document order.
func (t *Tree) Matches(query *Query) ([]Match, error) {
	p := t.parser
	if _, err := p.call("ts_tree_root_node_wasm", t.tree); err != nil {
		return nil, err
	}
	p.budget, p.exhausted = queryBudget, false
	if _, err := p.call("ts_query_matches_wasm", query.query, t.tree, 0, 0, 0, 0, 0, 0, 1024, 0xffffffff, 0); err != nil {
		return nil, err
	}
	count, results := p.word(p.transfer), p.word(p.transfer+4)
	defer p.call("free", results)
	if p.exhausted {
		return nil, ErrBudget
	}
	var matches []Match
	at := results
	for index := uint32(0); index < count; index++ {
		match := Match{Pattern: int(p.word(at))}
		captures := p.word(at + 4)
		at += 8
		for capture := uint32(0); capture < captures; capture++ {
			id := p.word(at)
			node, _ := p.memory.Read(at+4, 20)
			at += 24
			if !p.memory.Write(p.transfer, node) {
				return nil, errBinary
			}
			end, err := p.call("ts_node_end_index_wasm", t.tree)
			if err != nil {
				return nil, err
			}
			start := binary.LittleEndian.Uint32(node[4:])
			if int(id) >= len(query.names) || start > end || int(end) >= len(t.offsets) {
				return nil, errBinary
			}
			from, to := int(t.offsets[start]), int(t.offsets[end])
			match.Captures = append(match.Captures, Capture{Name: query.names[id], Text: string(t.source[from:to]),
				Start: from, End: to, Line: int(binary.LittleEndian.Uint32(node[8:])) + 1})
		}
		matches = append(matches, match)
	}
	return matches, nil
}
