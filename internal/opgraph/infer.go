package opgraph

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Infer builds the static Operational Graph of one system from its
// repositories. It never executes project code: every extractor parses a
// declaration its framework reads, or matches a recorded code pattern.
func Infer(system string, repositories []Repository) (Graph, error) {
	run := &inference{}
	for _, repository := range repositories {
		run.repository(repository)
	}
	return run.resolve(system, repositories)
}

// unit is a deployable unit found in a repository before identities exist.
type unit struct {
	repo, root, key, label, runtime string
	// deployed units come from deployment declarations and win over units
	// guessed from code at the same root.
	deployed bool
	role     string   // web | worker | "" picks among units sharing a root
	names    []string // runtime names: service.name, Worker script, process
	hosts    []string // hosts the unit is reachable at
	evidence Evidence
	at       Location
	deps     map[string]bool // package dependencies, for library ownership
	modules  []string        // code areas beyond root, for Go binaries
	entries  []string        // script entry files, when the package names them
	reach    map[string]bool // files its entries import, transitively; nil is every file
	id       string
}

// owner says which units a finding belongs to.
type owner struct {
	units   []string // explicit unit keys (Go reachability)
	repo    string
	file    string
	browser bool // the finding runs in a browser when its unit has one
}

type finding struct {
	owner     owner
	kind      Kind
	key       string
	label     string
	trigger   string
	shared    bool // a system-wide party, not a per-unit copy
	edge      EdgeKind
	selectors map[string]string
	evidence  Evidence
	at        Location
}

// call is an outbound address resolved after every unit's reachability is known.
type call struct {
	owner    owner
	target   *url.URL
	evidence Evidence
	at       Location
}

type inference struct {
	units     []*unit
	findings  []finding
	calls     []call
	packages  []jsPackage
	hints     []listenHint
	bindings  []serviceBinding
	hostHints []hostHint
	unitDeps  []unitDeps
	// scriptImports holds each script file's relative imports.
	scriptImports map[string][]string
}

type jsPackage struct {
	repo, dir, name string
	deps            map[string]bool
}

type listenHint struct {
	units []string
	addr  string
	at    Location
}

func (run *inference) repository(repository Repository) {
	names := make([]string, 0, len(repository.Files))
	for name := range repository.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	run.declarations(repository, names)
	run.goSource(repository, names)
	run.scripts(repository, names)
}

func (run *inference) addUnit(candidate unit) *unit {
	for _, held := range run.units {
		if held.repo == candidate.repo && held.key == candidate.key {
			held.names = append(held.names, candidate.names...)
			held.hosts = append(held.hosts, candidate.hosts...)
			return held
		}
	}
	run.units = append(run.units, &candidate)
	return run.units[len(run.units)-1]
}

func (run *inference) find(found finding) {
	if found.selectors == nil {
		found.selectors = map[string]string{}
	}
	run.findings = append(run.findings, found)
}

func static(source, detail, confidence string) Evidence {
	return Evidence{Origin: "static", Source: source, Detail: detail, Confidence: confidence}
}

// units drops code-guessed units shadowed by a deployment declaration.
func (run *inference) effectiveUnits() []*unit {
	result := []*unit{}
	for _, candidate := range run.units {
		shadowed := false
		if !candidate.deployed {
			var shadows []*unit
			for _, other := range run.units {
				if other.deployed && other.repo == candidate.repo && other.root == candidate.root {
					shadows = append(shadows, other)
				}
			}
			shadowed = len(shadows) > 0
			// The declaration names the process; the code lends its runtime,
			// and its names only when one process alone can carry them.
			for _, other := range shadows {
				if len(shadows) == 1 {
					other.names = append(other.names, candidate.names...)
					other.hosts = append(other.hosts, candidate.hosts...)
				}
				if other.runtime == "" {
					other.runtime = candidate.runtime
				}
			}
		}
		if !shadowed {
			result = append(result, candidate)
		}
	}
	return result
}

// ownerUnits resolves an owner to unit IDs.
func (run *inference) ownerUnits(units []*unit, found owner, kind Kind, trigger string) []*unit {
	if len(found.units) > 0 {
		result := []*unit{}
		for _, candidate := range units {
			for _, key := range found.units {
				if candidate.key == key {
					result = append(result, candidate)
				}
			}
		}
		return result
	}
	deepest := -1
	var atRoot []*unit
	for _, candidate := range units {
		if candidate.repo != found.repo || !within(candidate.root, found.file) {
			continue
		}
		if depth := len(candidate.root); depth > deepest {
			deepest, atRoot = depth, []*unit{candidate}
		} else if depth == deepest {
			atRoot = append(atRoot, candidate)
		}
	}
	if len(atRoot) == 0 {
		atRoot = run.dependents(units, found)
	}
	// Like a Go function no binary calls, a script file no entry imports
	// belongs to no process.
	reached := atRoot[:0:0]
	for _, candidate := range atRoot {
		if candidate.reach == nil || candidate.reach[found.file] || grammars[strings.ToLower(path.Ext(found.file))] == "" {
			reached = append(reached, candidate)
		}
	}
	return choose(reached, found.browser, kind, trigger)
}

// dependents finds units depending on the JavaScript library holding a file.
func (run *inference) dependents(units []*unit, found owner) []*unit {
	library := -1
	for index, candidate := range run.packages {
		if candidate.repo == found.repo && within(candidate.dir, found.file) && (library < 0 || len(candidate.dir) > len(run.packages[library].dir)) {
			library = index
		}
	}
	if library < 0 || run.packages[library].name == "" {
		return nil
	}
	result := []*unit{}
	for _, candidate := range units {
		if candidate.deps[run.packages[library].name] {
			result = append(result, candidate)
		}
	}
	return result
}

func choose(candidates []*unit, browser bool, kind Kind, trigger string) []*unit {
	pick := func(keep func(*unit) bool) []*unit {
		result := []*unit{}
		for _, candidate := range candidates {
			if keep(candidate) {
				result = append(result, candidate)
			}
		}
		return result
	}
	if hasBrowser := len(pick(func(u *unit) bool { return u.runtime == "browser" })) > 0; hasBrowser {
		candidates = pick(func(u *unit) bool { return (u.runtime == "browser") == browser })
	}
	role := ""
	switch {
	case kind == Ingress && trigger == "request":
		role = "web"
	case kind == Job:
		role = "worker"
	}
	if role != "" {
		if preferred := pick(func(u *unit) bool { return u.role == role }); len(preferred) > 0 {
			return preferred
		}
	}
	return candidates
}

func (run *inference) resolve(system string, repositories []Repository) (Graph, error) {
	builder := NewBuilder(system)
	units := run.effectiveUnits()
	// Last resort: a repository nothing recognised is still one unit.
	for _, repository := range repositories {
		found := false
		for _, candidate := range units {
			found = found || candidate.repo == repository.ID && candidate.runtime != "ci" // CI builds the code; it is not the code
		}
		// A library repository belongs to the units depending on it.
		for _, library := range run.packages {
			for _, candidate := range units {
				found = found || library.repo == repository.ID && library.name != "" && candidate.deps[library.name]
			}
		}
		if !found {
			fallback := &unit{repo: repository.ID, root: ".", key: "repository", label: repository.Name,
				evidence: static("fallback", "no recognised framework", Heuristic)}
			units = append(units, fallback)
			run.find(finding{owner: owner{repo: repository.ID, file: "."}, kind: Unknown, key: "unrecognised", label: "No recognised entry points",
				edge: Handles, evidence: static("fallback", "no recognised framework", Heuristic)})
		}
	}
	for _, candidate := range units {
		node := builder.Node(Processor, "", candidate.repo+":"+candidate.key, candidate.label)
		candidate.id = node.ID
		node.Runtime = candidate.runtime
		at := candidate.at
		node.Add(candidate.evidence, &at)
		modules := candidate.modules
		if len(modules) == 0 {
			modules = []string{candidate.root}
		}
		sort.Strings(modules)
		for _, module := range modules {
			if len(node.Modules) < maxModules {
				node.Modules = append(node.Modules, Location{Repository: candidate.repo, Path: module})
			}
		}
		if len(candidate.names) > 0 {
			node.Select("service.name", candidate.names[0]) // its own name first
		}
	}
	for _, candidate := range units {
		if candidate.runtime == "browser" {
			// The framework serves its browser code from the server unit.
			for _, server := range units {
				if server.repo == candidate.repo && server.root == candidate.root && server.runtime == "server" {
					builder.Edge(candidate.id, server.id, Calls, static("nextjs", "browser code loads from its server", Declared))
				}
			}
		}
	}
	for _, extra := range run.unitDeps {
		for _, candidate := range units {
			if candidate.repo == extra.repo && candidate.root == extra.root {
				candidate.deps = extra.deps
			}
		}
	}
	run.reachScripts(units, repositories)
	run.applyHints(units)
	for _, hint := range run.hostHints {
		for _, owned := range run.ownerUnits(units, hint.owner, Ingress, "request") {
			owned.hosts = append(owned.hosts, hint.host)
		}
	}
	for _, found := range run.findings {
		for _, owned := range run.ownerUnits(units, found.owner, found.kind, found.trigger) {
			parent := owned.id
			if found.shared {
				parent = ""
			}
			node := builder.Node(found.kind, parent, found.key, found.label)
			node.Trigger = found.trigger
			for key, value := range found.selectors {
				node.Select(key, value)
			}
			at := found.at
			node.Add(found.evidence, &at)
			switch found.edge {
			case Handles, Consumes:
				builder.Edge(node.ID, owned.id, found.edge, found.evidence)
			case "":
			default:
				builder.Edge(owned.id, node.ID, found.edge, found.evidence)
			}
		}
	}
	run.resolveCalls(units, builder)
	for _, binding := range run.bindings {
		for _, from := range units {
			for _, to := range units {
				if from.key == binding.from && to.runtime == "worker" && contains(to.names, binding.to) {
					builder.Edge(from.id, to.id, Calls, static("wrangler", "service binding", Declared))
				}
			}
		}
	}
	for _, candidate := range units {
		node := builder.Lookup(candidate.id)
		if node.Runtime == "" {
			node.Runtime = "cli"
			for _, edge := range builder.edges {
				if edge.To == node.ID && edge.Kind == Handles {
					node.Runtime = "process"
				}
			}
		}
	}
	return builder.Graph()
}

// applyHints gives a unit's one unaddressed TCP listener the one listening
// address its code names, so callers elsewhere can find it.
func (run *inference) applyHints(units []*unit) {
	for _, candidate := range units {
		var addresses []listenHint
		for _, hint := range run.hints {
			for _, key := range hint.units {
				if key == candidate.key && !containsHint(addresses, hint.addr) {
					addresses = append(addresses, hint)
				}
			}
		}
		var open []int
		for index, found := range run.findings {
			if found.kind == Ingress && found.trigger == "request" && found.selectors["network.transport"] == "tcp" && found.selectors["server.port"] == "" {
				for _, key := range found.owner.units {
					if key == candidate.key {
						open = append(open, index)
					}
				}
			}
		}
		if len(open) != 1 || len(addresses) != 1 {
			continue
		}
		host, port, _ := net.SplitHostPort(addresses[0].addr)
		found := &run.findings[open[0]]
		found.selectors["server.address"], found.selectors["server.port"] = host, port
		found.label += " " + addresses[0].addr
		run.findings = append(run.findings, finding{owner: found.owner, kind: Ingress, key: found.key, trigger: "request",
			evidence: static("go-ast", "listen address named by "+addresses[0].at.Path, Heuristic), at: addresses[0].at, selectors: map[string]string{}})
	}
}

func contains(values []string, value string) bool {
	for _, held := range values {
		if held == value {
			return true
		}
	}
	return false
}

func containsHint(hints []listenHint, addr string) bool {
	for _, hint := range hints {
		if hint.addr == addr {
			return true
		}
	}
	return false
}

// resolveCalls turns outbound addresses into calls: to a unit in this
// system that declares the address, otherwise to an external party.
//
// A heuristic address (a constant's name, not a call) never creates a party
// on its own: it only links to a unit, or to a party stronger evidence found.
func (run *inference) resolveCalls(units []*unit, builder *Builder) {
	calls := append([]call{}, run.calls...)
	sort.SliceStable(calls, func(i, j int) bool {
		return calls[i].evidence.Confidence != Heuristic && calls[j].evidence.Confidence == Heuristic
	})
	for _, outbound := range calls {
		callers := run.ownerUnits(units, outbound.owner, External, "")
		if len(callers) == 0 {
			continue
		}
		host := strings.ToLower(outbound.target.Hostname())
		if reservedHost(host) {
			continue
		}
		target := run.reachable(units, builder, outbound.target)
		if target == "" {
			if loopback(host) {
				continue // a local address nothing in the system declares
			}
			if outbound.evidence.Confidence == Heuristic && builder.Lookup(ID(builder.system, "", "host:"+host)) == nil {
				continue
			}
			node := builder.Node(External, "", "host:"+host, host)
			node.Select("server.address", host)
			at := outbound.at
			node.Add(outbound.evidence, &at)
			target = node.ID
		}
		for _, caller := range callers {
			builder.Edge(caller.id, target, Calls, outbound.evidence)
		}
	}
}

// reachable finds the node a system unit exposes at an address: a matching
// route, else its listener, else the unit itself.
func (run *inference) reachable(units []*unit, builder *Builder, target *url.URL) string {
	host, port := strings.ToLower(target.Hostname()), target.Port()
	route := normaliseRoute(target.Path)
	for _, candidate := range units {
		reached := false
		for _, declared := range candidate.hosts {
			reached = reached || hostMatches(declared, host)
		}
		var listener, routed string
		ids := make([]string, 0, len(builder.nodes))
		for id := range builder.nodes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			node := builder.nodes[id]
			if node.Unit != candidate.id || node.Kind != Ingress {
				continue
			}
			if !loopback(host) && node.Selectors["server.address"] != "" && hostMatches(node.Selectors["server.address"], host) {
				reached, listener = true, node.ID
			}
			if loopback(host) && node.Selectors["server.port"] != "" && (port == "" || node.Selectors["server.port"] == port) && loopback(node.Selectors["server.address"]) {
				reached, listener = true, node.ID
			}
			// A call names a path, not a method: the read (GET or any) wins.
			if method := node.Selectors["http.request.method"]; route != "" && route != "/" && normaliseRoute(node.Selectors["http.route"]) == route &&
				(routed == "" || method == "" || method == "GET") {
				routed = node.ID
			}
		}
		if !reached {
			continue
		}
		switch {
		case routed != "":
			return routed
		case listener != "":
			return listener
		default:
			return candidate.id
		}
	}
	return ""
}

func hostMatches(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(strings.SplitN(pattern, "/", 2)[0], "*"))
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(host, pattern[1:])
	}
	return pattern != "" && pattern == host
}

func loopback(host string) bool {
	return host == "localhost" || host == "0.0.0.0" || host == "::1" || strings.HasPrefix(host, "127.")
}

// reservedHost holds documentation, test and schema hosts (RFC 2606 and
// friends) that never name a real party.
func reservedHost(host string) bool {
	if host == "" || !strings.Contains(host, ".") && !loopback(host) {
		return true
	}
	for _, suffix := range []string{".example", ".test", ".invalid", ".localhost", ".local", ".internal", "example.com", "example.org", "example.net", "w3.org", "schema.org", "json-schema.org"} {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func within(root, file string) bool {
	return root == "." || file == root || strings.HasPrefix(file, root+"/")
}

var routeParameter = regexp.MustCompile(`\{[^}]*\.\.\.\}|\[\[?\.\.\.[^\]]*\]\]?|\{[^}]*\}|\[[^\]]*\]|<[^>]*>|:[A-Za-z_][A-Za-z0-9_]*|\*[A-Za-z0-9_]*`)

// normaliseRoute makes route templates from any framework comparable:
// parameters become {} and catch-alls {*}; formats and trailing slashes go.
func normaliseRoute(route string) string {
	if route == "" {
		return ""
	}
	route = strings.TrimSuffix(route, "(.:format)")
	route = routeParameter.ReplaceAllStringFunc(route, func(match string) string {
		if strings.Contains(match, "...") || strings.HasPrefix(match, "*") {
			return "{*}"
		}
		return "{}"
	})
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	if len(route) > 1 {
		route = strings.TrimSuffix(route, "/")
	}
	return path.Clean(route)
}

// parseTarget accepts absolute http(s)/ws(s) addresses, keeping only scheme,
// host, port and a path template: never credentials or query strings.
func parseTarget(value string) *url.URL {
	if index := strings.Index(value, "${"); index >= 0 {
		value = value[:index]
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return nil
	}
	switch parsed.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return nil
	}
	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}
}
