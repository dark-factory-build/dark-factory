package opgraph

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/opgraph/treesitter"
)

// Parse bounds. Larger files are generated or vendored in practice; the
// repository bound caps total parse work. Both are byte counts over files in
// name order, so what is skipped is the same on every run.
const (
	maxParseBytes      = 256 << 10
	maxRepositoryParse = 32 << 20
)

var grammars = map[string]string{
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".mts": "typescript", ".tsx": "tsx",
	".py": "python", ".rb": "ruby", ".java": "java", ".kt": "kotlin", ".rs": "rust",
}

// span is one captured syntax node.
type span struct {
	text       string
	start, end int
	line       int
}

func (s span) contains(other span) bool { return s.start <= other.start && other.end <= s.end }

// site is a call, construction, decorator, annotation or attribute.
type site struct {
	span
	object, name string
	args         []span
	pairs        []pair
	target       *definition // what a decorator decorates
}

type pair struct{ whole, key, value span }

type definition struct {
	span
	name, super string
	nameAt      int
	class       bool
	decorators  []*site
}

// facts are one file's syntax in the vocabulary the queries share.
type facts struct {
	language, file string
	calls          []*site
	decorators     []*site
	definitions    []*definition
	values         map[string]span // first binding of each name
	imports        []string
	exports        []string
	globals        map[string]bool
	directives     []string
	strings        []span
	bySpan         map[[2]int]*site
	resolving      map[string]bool
}

// parseScripts parses every non-Go source file, one grammar at a time, and
// hands each file's facts to the framework extractors.
func (run *inference) parseScripts(repository Repository, names []string, extract func(*facts)) {
	byGrammar := map[string][]string{}
	for _, name := range names {
		if grammar := grammars[strings.ToLower(path.Ext(name))]; grammar != "" {
			byGrammar[grammar] = append(byGrammar[grammar], name)
		}
	}
	budget := maxRepositoryParse
	for _, grammar := range keys(byGrammar) {
		var parser *treesitter.Parser
		var query *treesitter.Query
		for _, name := range byGrammar[grammar] {
			body := repository.Files[name]
			if len(body) > maxParseBytes || len(body) > budget {
				continue
			}
			budget -= len(body)
			if parser == nil {
				var err error
				if parser, err = treesitter.New(context.Background(), grammar); err == nil {
					query, err = parser.Query(queries[grammar])
				}
				if err != nil {
					if parser != nil {
						parser.Close()
					}
					break // the grammar is unusable here; its files yield nothing
				}
			}
			found, err := parseFile(parser, query, grammar, name, body)
			if err != nil {
				// A file past its bounds yields no evidence; a trap also
				// leaves the instance unusable, so the next file gets a new one.
				if !errors.Is(err, treesitter.ErrBudget) {
					parser.Close()
					parser = nil
				}
				continue
			}
			extract(found)
		}
		if parser != nil {
			parser.Close()
		}
	}
}

func parseFile(parser *treesitter.Parser, query *treesitter.Query, language, name string, body []byte) (*facts, error) {
	tree, err := parser.Parse(body)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	matches, err := tree.Matches(query)
	if err != nil {
		return nil, err
	}
	found := &facts{language: language, file: name, values: map[string]span{}, globals: map[string]bool{}, bySpan: map[[2]int]*site{}, resolving: map[string]bool{}}
	var pairs []pair
	for _, match := range matches {
		captures := map[string][]span{}
		for _, capture := range match.Captures {
			captures[capture.Name] = append(captures[capture.Name], span{text: capture.Text, start: capture.Start, end: capture.End, line: capture.Line})
		}
		first := func(name string) span {
			if held := captures[name]; len(held) > 0 {
				return held[0]
			}
			return span{}
		}
		switch {
		case len(captures["call"]) > 0:
			whole := first("call")
			call := found.bySpan[[2]int{whole.start, whole.end}]
			if call == nil {
				call = &site{span: whole}
				found.calls = append(found.calls, call)
				found.bySpan[[2]int{whole.start, whole.end}] = call
			}
			call.merge(first("call.object").text, first("call.name").text, captures["call.arg"])
		case len(captures["decorator"]) > 0:
			whole := first("decorator")
			var decorator *site
			for _, held := range found.decorators {
				if held.span == whole {
					decorator = held
				}
			}
			if decorator == nil {
				decorator = &site{span: whole}
				found.decorators = append(found.decorators, decorator)
			}
			decorator.merge(first("decorator.object").text, lastSegment(first("decorator.name").text), captures["decorator.arg"])
		case len(captures["pair"]) > 0:
			pairs = append(pairs, pair{whole: first("pair"), key: first("pair.key"), value: first("pair.value")})
		case len(captures["const.name"]) > 0:
			if _, held := found.values[first("const.name").text]; !held {
				found.values[first("const.name").text] = first("const.value")
			}
		case len(captures["definition"]) > 0:
			name := first("definition.name")
			found.definitions = append(found.definitions, &definition{span: first("definition"), name: name.text, nameAt: name.start, super: first("definition.super").text, class: len(captures["definition.class"]) > 0})
		case len(captures["import"]) > 0:
			found.imports = append(found.imports, strings.Trim(first("import").text, "'\"`"))
		case len(captures["export"]) > 0:
			found.exports = append(found.exports, first("export").text)
		case len(captures["global"]) > 0:
			found.globals[first("global").text] = true
		case len(captures["directive"]) > 0:
			found.directives = append(found.directives, strings.Trim(first("directive").text, "'\""))
		case len(captures["string"]) > 0:
			found.strings = append(found.strings, first("string"))
		}
	}
	// A call with a trailing block (Kotlin's f(x) { }) is one call, not a call
	// inside a call; a chain's calls (a.f().f()) differ in their receivers.
	sort.SliceStable(found.calls, func(i, j int) bool {
		return found.calls[i].start < found.calls[j].start || found.calls[i].start == found.calls[j].start && found.calls[i].end > found.calls[j].end
	})
	calls := found.calls[:0]
	for _, call := range found.calls {
		if last := len(calls) - 1; last < 0 || calls[last].start != call.start || calls[last].name != call.name || calls[last].object != call.object {
			calls = append(calls, call)
		}
	}
	found.calls = calls
	found.attach(pairs)
	return found, nil
}

// attach gives each keyword argument to the innermost call or decorator
// holding it, drops it from positional arguments, and gives each decorator
// the definition it precedes.
func (found *facts) attach(pairs []pair) {
	sites := append(append([]*site{}, found.calls...), found.decorators...)
	sort.SliceStable(sites, func(i, j int) bool {
		return sites[i].start < sites[j].start || sites[i].start == sites[j].start && sites[i].end > sites[j].end
	})
	for _, item := range pairs {
		whole := item.whole
		for index := sort.Search(len(sites), func(i int) bool { return sites[i].start > whole.start }) - 1; index >= 0; index-- {
			if sites[index].contains(whole) && sites[index].span != whole {
				sites[index].pairs = append(sites[index].pairs, item)
				kept := sites[index].args[:0]
				for _, arg := range sites[index].args {
					if arg.start != whole.start || arg.end != whole.end {
						kept = append(kept, arg)
					}
				}
				sites[index].args = kept
				break
			}
		}
	}
	sort.SliceStable(found.definitions, func(i, j int) bool { return found.definitions[i].nameAt < found.definitions[j].nameAt })
	for _, decorator := range found.decorators {
		index := sort.Search(len(found.definitions), func(i int) bool { return found.definitions[i].nameAt >= decorator.end })
		if index < len(found.definitions) {
			decorator.target = found.definitions[index]
			decorator.target.decorators = append(decorator.target.decorators, decorator)
		}
	}
}

// enclosing is the innermost definition holding a span.
func (found *facts) enclosing(at span, keep func(*definition) bool) *definition {
	var best *definition
	for _, candidate := range found.definitions {
		if candidate.contains(at) && candidate.span != at && (keep == nil || keep(candidate)) && (best == nil || best.contains(candidate.span)) {
			best = candidate
		}
	}
	return best
}

// within lists the calls inside a span, the span's own call included.
func (found *facts) within(at span) []*site {
	var result []*site
	for _, call := range found.calls {
		if at.contains(call.span) {
			result = append(result, call)
		}
	}
	return result
}

// merge adds what one match saw of a site: queries see a site's name and
// each of its arguments in separate matches.
func (s *site) merge(object, name string, args []span) {
	if name != "" {
		s.object, s.name = object, name
	}
	for _, arg := range args {
		at := sort.Search(len(s.args), func(i int) bool { return s.args[i].start >= arg.start })
		if at == len(s.args) || s.args[at] != arg {
			s.args = append(s.args[:at], append([]span{arg}, s.args[at:]...)...)
		}
	}
}

func (s *site) keyword(names ...string) (span, bool) {
	for _, item := range s.pairs {
		for _, name := range names {
			if strings.Trim(strings.TrimSpace(item.key.text), `:"'`) == name {
				return item.value, true
			}
		}
	}
	return span{}, false
}

func lastSegment(name string) string {
	name = strings.TrimSpace(name)
	if index := strings.LastIndexAny(name, ".:"); index >= 0 {
		return name[index+1:]
	}
	return name
}

// unresolved stands for a part of a string the file does not determine;
// parseTarget cuts an address there, and a route containing it is dropped.
const unresolved = "${}"

// str resolves a string expression within the file: literals, interpolation
// and concatenation of literals and named constants, and the URL wrappers
// languages put around them. complete is false when any part is unknown.
func (found *facts) str(expr span) (value string, complete bool) {
	return found.eval(expr, 0)
}

func (found *facts) eval(expr span, depth int) (string, bool) {
	text := strings.TrimSpace(expr.text)
	if depth > 8 || text == "" {
		return unresolved, false
	}
	if call := found.bySpan[[2]int{expr.start, expr.end}]; call != nil {
		return found.wrapped(call, depth)
	}
	terms := splitTerms(text)
	if len(terms) > 1 {
		var builder strings.Builder
		for _, term := range terms {
			offset := expr.start + strings.Index(expr.text, term)
			value, ok := found.eval(span{text: term, start: offset, end: offset + len(term), line: expr.line}, depth+1)
			builder.WriteString(value)
			if !ok {
				return builder.String(), false
			}
		}
		return builder.String(), true
	}
	for _, suffix := range []string{".to_string()", ".to_owned()", ".into()", ".toString()", ".freeze"} {
		text = strings.TrimSuffix(text, suffix)
	}
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
		inner := strings.TrimSpace(text[1 : len(text)-1])
		offset := expr.start + strings.Index(expr.text, inner)
		return found.eval(span{text: inner, start: offset, end: offset + len(inner)}, depth+1)
	}
	if strings.HasPrefix(text, "&") { // Rust borrows
		inner := strings.TrimLeft(text, "&")
		offset := expr.start + strings.Index(expr.text, inner)
		return found.eval(span{text: inner, start: offset, end: offset + len(inner)}, depth+1)
	}
	if value, ok, literal := found.literal(text, depth); literal {
		return value, ok
	}
	if strings.HasPrefix(text, ":") && identifier(text[1:]) { // a Ruby symbol
		return text[1:], true
	}
	if identifier(strings.NewReplacer(".", "", "::", "").Replace(text)) {
		return found.constant(lastSegment(text), depth)
	}
	return unresolved, false
}

func (found *facts) constant(name string, depth int) (string, bool) {
	value, ok := found.values[name]
	if !ok || found.resolving[name] {
		return unresolved, false
	}
	found.resolving[name] = true
	defer delete(found.resolving, name)
	return found.eval(value, depth+1)
}

// wrapped resolves a call that only carries a string: URL constructors,
// string conversions and Rust's format!.
func (found *facts) wrapped(call *site, depth int) (string, bool) {
	object := lastSegment(call.object)
	switch {
	case call.name == "format" && len(call.args) > 0:
		pattern, ok := found.eval(call.args[0], depth+1)
		if !ok {
			return unresolved, false
		}
		var builder strings.Builder
		next := 1
		for index := 0; index < len(pattern); index++ {
			switch {
			case strings.HasPrefix(pattern[index:], "{{"):
				builder.WriteByte('{')
				index++
			case pattern[index] == '{':
				end := strings.IndexByte(pattern[index:], '}')
				if end < 0 {
					return builder.String() + unresolved, false
				}
				name, _, _ := strings.Cut(pattern[index+1:index+end], ":")
				var value string
				var resolved bool
				if name == "" && next < len(call.args) {
					value, resolved = found.eval(call.args[next], depth+1)
					next++
				} else if name != "" {
					value, resolved = found.constant(name, depth)
				}
				builder.WriteString(value)
				if !resolved {
					return builder.String(), false
				}
				index += end
			default:
				builder.WriteByte(pattern[index])
			}
		}
		return builder.String(), true
	case len(call.args) > 0 && (urlWrapper(call) || call.name == "from" && object == "String" || call.name == "String" || call.name == "str"):
		return found.eval(call.args[0], depth+1)
	}
	return unresolved, false
}

// urlWrapper is a call that builds an address object from a string.
func urlWrapper(call *site) bool {
	object := lastSegment(call.object)
	return call.name == "URL" || call.name == "URI" || call.name == "Uri" ||
		(call.name == "create" || call.name == "parse" || call.name == "new") && (object == "URI" || object == "URL" || object == "Url" || object == "Uri" || object == "url")
}

// literal decodes a string literal of any supported language. It reports
// literal=false for anything else.
func (found *facts) literal(text string, depth int) (value string, complete, literal bool) {
	prefix := 0
	for prefix < len(text) && prefix < 3 && strings.ContainsRune("rRbBuUfF", rune(text[prefix])) {
		prefix++
	}
	modifiers := strings.ToLower(text[:prefix])
	body := text[prefix:]
	hashes := 0
	if strings.Contains(modifiers, "r") && found.language == "rust" {
		for hashes < len(body) && body[hashes] == '#' {
			hashes++
		}
		body = strings.TrimSuffix(body[hashes:], strings.Repeat("#", hashes))
	}
	quote := ""
	for _, candidate := range []string{`"""`, `'''`, `"`, `'`, "`"} {
		if len(body) >= 2*len(candidate) && strings.HasPrefix(body, candidate) && strings.HasSuffix(body, candidate) {
			quote = candidate
			break
		}
	}
	if quote == "" || quote == "'" && (found.language == "rust" || found.language == "java" || found.language == "kotlin") {
		return "", false, false
	}
	body = body[len(quote) : len(body)-len(quote)]
	raw := strings.Contains(modifiers, "r")
	open, close := "", "}"
	switch {
	case found.language == "javascript" || found.language == "typescript" || found.language == "tsx":
		if quote == "`" {
			open = "${"
		}
	case found.language == "python":
		if strings.Contains(modifiers, "f") {
			open = "{"
		}
	case found.language == "ruby":
		if quote == `"` {
			open = "#{"
		}
	case found.language == "kotlin":
		open = "${"
	}
	var builder strings.Builder
	complete = true
	for index := 0; index < len(body); index++ {
		switch {
		case !raw && body[index] == '\\' && index+1 < len(body):
			index++
			switch body[index] {
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			default:
				builder.WriteByte(body[index])
			}
		case open == "{" && strings.HasPrefix(body[index:], "{{"):
			builder.WriteByte('{')
			index++
		case open != "" && strings.HasPrefix(body[index:], open):
			end := strings.Index(body[index:], close)
			if end < 0 {
				return builder.String() + unresolved, false, true
			}
			expression := strings.TrimSpace(body[index+len(open) : index+end])
			resolved, ok := unresolved, false
			if identifier(expression) {
				resolved, ok = found.constant(expression, depth)
			}
			builder.WriteString(resolved)
			complete = complete && ok
			if !ok {
				return builder.String(), false, true
			}
			index += end
		case found.language == "kotlin" && body[index] == '$' && index+1 < len(body) && identifierStart(body[index+1]):
			end := index + 1
			for end < len(body) && identifierPart(body[end]) {
				end++
			}
			resolved, ok := found.constant(body[index+1:end], depth)
			builder.WriteString(resolved)
			if !ok {
				return builder.String(), false, true
			}
			index = end - 1
		default:
			builder.WriteByte(body[index])
		}
	}
	return builder.String(), complete, true
}

// splitTerms splits a concatenation at top-level plus signs.
func splitTerms(text string) []string {
	var terms []string
	depth, start := 0, 0
	var quote byte
	for index := 0; index < len(text); index++ {
		c := text[index]
		switch {
		case quote != 0:
			if c == '\\' {
				index++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == '+' && depth == 0:
			terms = append(terms, strings.TrimSpace(text[start:index]))
			start = index + 1
		}
	}
	return append(terms, strings.TrimSpace(text[start:]))
}

func identifierStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func identifierPart(c byte) bool { return identifierStart(c) || c >= '0' && c <= '9' }

func identifier(text string) bool {
	if text == "" || !identifierStart(text[0]) {
		return false
	}
	for index := 1; index < len(text); index++ {
		if !identifierPart(text[index]) {
			return false
		}
	}
	return true
}

// elements splits a list, array or tuple literal into its items.
func elements(list span) []span {
	text := strings.TrimSpace(list.text)
	offset := list.start + strings.Index(list.text, text)
	for _, prefix := range []string{"&", "%w", "%i", "arrayOf", "listOf", "setOf", "vec!"} {
		if strings.HasPrefix(text, prefix) {
			text, offset = text[len(prefix):], offset+len(prefix)
		}
	}
	if len(text) < 2 || !strings.ContainsRune("[{(", rune(text[0])) {
		return []span{list}
	}
	words := strings.HasPrefix(strings.TrimSpace(list.text), "%")
	text, offset = text[1:len(text)-1], offset+1
	var items []span
	depth, start := 0, 0
	var quote byte
	emit := func(end int) {
		item := strings.TrimSpace(text[start:end])
		if item != "" {
			at := offset + start + strings.Index(text[start:end], item)
			items = append(items, span{text: item, start: at, end: at + len(item), line: list.line})
		}
	}
	for index := 0; index < len(text); index++ {
		c := text[index]
		switch {
		case quote != 0:
			if c == '\\' {
				index++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case depth == 0 && (c == ',' || words && c == ' '):
			emit(index)
			start = index + 1
		}
	}
	emit(len(text))
	if words { // %w(a b): bare words are strings
		for index := range items {
			items[index].text = "'" + items[index].text + "'"
		}
	}
	return items
}
