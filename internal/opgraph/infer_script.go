package opgraph

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// pattern is one recognised framework declaration in source text. The table
// is the extension point for languages without a parser in this package;
// every match is "inferred", never "declared".
type pattern struct {
	extensions string // "|.py|.rb|"
	file       string // exact base name, when the framework fixes it
	expression *regexp.Regexp
	kind       Kind
	trigger    string
	// build turns a match into (method, route or name) pairs.
	build func(match []string) [][2]string
}

func routeMatch(method, route int) func([]string) [][2]string {
	return func(match []string) [][2]string {
		verb := "ANY"
		if method > 0 {
			verb = strings.ToUpper(match[method])
		}
		return [][2]string{{verb, match[route]}}
	}
}

var (
	allMethods = regexp.MustCompile(`(?i)\b(get|post|put|patch|delete|head|options|any)\b`)
	patterns   = []pattern{
		{"|.js|.jsx|.mjs|.cjs|.ts|.tsx|.mts|", "", regexp.MustCompile("\\b(?:app|router|api|server|routes?|r)\\.(get|post|put|patch|delete|all|options|head)\\(\\s*['\"`](/[^'\"`]*)['\"`]"), Ingress, "request", routeMatch(1, 2)},
		{"|.py|", "", regexp.MustCompile(`@\w+\.(get|post|put|patch|delete)\(\s*['"]([^'"]+)['"]`), Ingress, "request", routeMatch(1, 2)},
		{"|.py|", "", regexp.MustCompile(`@\w+\.route\(\s*['"]([^'"]+)['"]([^)]*)`), Ingress, "request", func(match []string) [][2]string {
			if method := allMethods.FindString(match[2]); method != "" {
				return [][2]string{{strings.ToUpper(method), match[1]}}
			}
			return [][2]string{{"ANY", match[1]}}
		}},
		{"|.py|", "urls.py", regexp.MustCompile(`\b(?:re_)?path\(\s*r?['"]([^'"]*)['"]`), Ingress, "request", routeMatch(0, 1)},
		{"|.py|", "", regexp.MustCompile(`@(?:\w+\.)?(?:shared_)?task\b[^\n]*\n\s*(?:async\s+)?def\s+(\w+)`), Job, "", func(match []string) [][2]string { return [][2]string{{"", match[1]}} }},
		{"|.rb|", "routes.rb", regexp.MustCompile(`(?m)^\s*(get|post|put|patch|delete)\s+['"]([^'"]+)['"]`), Ingress, "request", routeMatch(1, 2)},
		{"|.rb|", "routes.rb", regexp.MustCompile(`(?m)^\s*resources?\s+:(\w+)`), Ingress, "request", func(match []string) [][2]string { return [][2]string{{"ANY", "/" + match[1]}} }},
		{"|.rb|", "", regexp.MustCompile(`class\s+(\w+)[^\n]*\n(?:[^\n]*\n){0,3}?\s*include\s+Sidekiq::(?:Job|Worker)`), Job, "", func(match []string) [][2]string { return [][2]string{{"", match[1]}} }},
		{"|.java|.kt|", "", regexp.MustCompile(`@(Get|Post|Put|Delete|Patch|Request)Mapping\(\s*(?:value\s*=\s*|path\s*=\s*)?\{?\s*"([^"]*)"`), Ingress, "request", func(match []string) [][2]string {
			if match[1] == "Request" {
				return [][2]string{{"ANY", match[2]}}
			}
			return [][2]string{{strings.ToUpper(match[1]), match[2]}}
		}},
		{"|.java|.kt|", "", regexp.MustCompile(`@Scheduled\([^)]*\)\s*(?:(?:public|private|protected|fun)\s+)?(?:\w+\s+)?(\w+)\s*\(`), Ingress, "timer", func(match []string) [][2]string { return [][2]string{{"", match[1]}} }},
		{"|.rs|", "", regexp.MustCompile(`\.route\(\s*"([^"]+)"\s*,\s*((?:[a-z_:]+\([^()]*\)\s*\.?\s*)+)`), Ingress, "request", func(match []string) [][2]string {
			var result [][2]string
			for _, method := range allMethods.FindAllString(match[2], -1) {
				result = append(result, [2]string{strings.ToUpper(method), match[1]})
			}
			return result
		}},
		{"|.rs|", "", regexp.MustCompile(`#\[(get|post|put|patch|delete)\(\s*"([^"]+)"`), Ingress, "request", routeMatch(1, 2)},
	}
	urlLiteral     = regexp.MustCompile("[\"'`]((?:https?|wss?)://[^\"'`\\s]+)")
	assignedName   = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*(?::\s*[^=]+)?=\s*(?:new\s+URL\(\s*)?["'` + "`" + `]`)
	networkCall    = regexp.MustCompile(`\b(fetch|WebSocket|EventSource|axios|got|ky|request|requests\.\w+|httpx\.\w+|reqwest::\w+|urlopen|HttpClient|RestTemplate|Faraday|Net::HTTP)\b`)
	browserMarker  = regexp.MustCompile(`["']use client["']|\b(window|document|navigator|localStorage|sessionStorage)\.|new WebSocket\(`)
	exportedMethod = regexp.MustCompile(`export\s+(?:async\s+)?(?:function|const)\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b`)
	calleeBefore   = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\(\s*&?(?:format!\(\s*)?["'` + "`" + `]$`)
	passedToCall   = regexp.MustCompile(`\b(?:fetch|WebSocket|EventSource)\(\s*([A-Za-z_][A-Za-z0-9_]*)\b`)
	hostLiteral    = regexp.MustCompile(`["']((?:[a-z0-9-]+\.)+[a-z]{2,})["']`)
)

// scripts reads every non-Go source file: framework file conventions,
// recognised declarations and outbound addresses.
func (run *inference) scripts(repository Repository, names []string) {
	var nextRoots []string
	for _, candidate := range run.packages {
		if candidate.repo == repository.ID && candidate.deps["next"] {
			nextRoots = append(nextRoots, candidate.dir)
		}
	}
	for _, name := range names {
		extension := strings.ToLower(path.Ext(name))
		if extension == ".go" || !strings.Contains("|.js|.jsx|.mjs|.cjs|.ts|.tsx|.mts|.mdx|.py|.rb|.rs|.java|.kt|", "|"+extension+"|") {
			continue
		}
		text := string(repository.Files[name])
		browser := browserMarker.MatchString(text) || extension == ".tsx" || extension == ".jsx"
		here := owner{repo: repository.ID, file: name, browser: browser}
		for _, root := range nextRoots {
			if within(root, name) {
				run.nextRoute(repository.ID, root, name, text)
			}
		}
		if strings.Contains(text, "@SpringBootApplication") {
			root := path.Dir(name)
			for root != "." && repository.Files[path.Join(root, "pom.xml")] == nil && repository.Files[path.Join(root, "build.gradle")] == nil && repository.Files[path.Join(root, "build.gradle.kts")] == nil {
				root = path.Dir(root)
			}
			label := path.Base(root)
			if root == "." {
				label = repository.Name
			}
			run.addUnit(unit{repo: repository.ID, root: root, key: "spring:" + root, label: label, runtime: "process", role: "web",
				names: []string{label}, evidence: static("spring", "@SpringBootApplication", Declared), at: Location{Repository: repository.ID, Path: name}})
		}
		starts := lines(text)
		for _, recognised := range patterns {
			if !strings.Contains(recognised.extensions, "|"+extension+"|") || recognised.file != "" && path.Base(name) != recognised.file {
				continue
			}
			// In browser code, api.get("/x") is a call out, not an entry point.
			if browser && recognised.kind == Ingress && strings.Contains("|.js|.jsx|.mjs|.cjs|.ts|.tsx|.mts|", "|"+extension+"|") {
				continue
			}
			for _, indexes := range recognised.expression.FindAllStringSubmatchIndex(text, -1) {
				match := make([]string, len(indexes)/2)
				for group := range match {
					if indexes[2*group] >= 0 {
						match[group] = text[indexes[2*group]:indexes[2*group+1]]
					}
				}
				at := Location{Repository: repository.ID, Path: name, Line: sort.SearchInts(starts, indexes[0]+1)}
				for _, item := range recognised.build(match) {
					run.patternFinding(recognised, item, owner{repo: repository.ID, file: name}, at)
				}
			}
		}
		run.literals(here, name, text)
		if base := path.Base(name); strings.HasPrefix(base, "middleware.") {
			for _, match := range hostLiteral.FindAllStringSubmatch(text, -1) {
				if !reservedHost(match[1]) {
					run.hostHints = append(run.hostHints, hostHint{owner: owner{repo: repository.ID, file: name}, host: match[1]})
				}
			}
		}
	}
}

type hostHint struct {
	owner owner
	host  string
}

func (run *inference) patternFinding(recognised pattern, item [2]string, found owner, at Location) {
	evidence := static("pattern", strings.TrimPrefix(path.Ext(at.Path), "."), Inferred)
	if recognised.kind == Job || recognised.trigger == "timer" {
		edge := Runs
		if recognised.kind == Ingress {
			edge = Handles
		}
		run.find(finding{owner: found, kind: recognised.kind, key: "fn:" + item[1], label: item[1], trigger: recognised.trigger, edge: edge,
			selectors: map[string]string{"code.function.name": item[1]}, evidence: evidence, at: at})
		return
	}
	route := item[1]
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	method := item[0]
	if method == "ANY" || method == "ALL" {
		method = ""
	}
	run.find(finding{owner: found, kind: Ingress, key: "http:" + strings.TrimSpace(method+" "+route), label: strings.TrimSpace(method + " " + route),
		trigger: "request", edge: Handles, selectors: map[string]string{"http.request.method": method, "http.route": route}, evidence: evidence, at: at})
}

// nextRoute applies the Next.js file conventions under app/ and pages/.
func (run *inference) nextRoute(repo, root, name, text string) {
	rel := strings.TrimPrefix(name, root+"/")
	if root == "." {
		rel = name
	}
	rel = strings.TrimPrefix(rel, "src/")
	base := path.Base(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	var segments []string
	methods := []string{}
	switch {
	case strings.HasPrefix(rel, "app/") && (stem == "page" || stem == "route"):
		for _, segment := range strings.Split(path.Dir(strings.TrimPrefix(rel, "app/")), "/") {
			if segment == "." || strings.HasPrefix(segment, "(") || strings.HasPrefix(segment, "@") || strings.HasPrefix(segment, "_") {
				if strings.HasPrefix(segment, "_") || strings.HasPrefix(segment, "(.") {
					return // private folders and intercepted routes do not route
				}
				continue
			}
			segments = append(segments, segment)
		}
		if stem == "page" {
			methods = append(methods, "GET")
		} else {
			for _, match := range exportedMethod.FindAllStringSubmatch(text, -1) {
				methods = append(methods, match[1])
			}
		}
	case strings.HasPrefix(rel, "pages/") && !strings.HasPrefix(stem, "_"):
		segments = strings.Split(strings.TrimSuffix(strings.TrimPrefix(rel, "pages/"), path.Ext(base)), "/")
		if segments[len(segments)-1] == "index" {
			segments = segments[:len(segments)-1]
		}
		method := "GET"
		if len(segments) > 0 && segments[0] == "api" {
			method = ""
		}
		methods = append(methods, method)
	default:
		return
	}
	route := "/" + strings.Join(segments, "/")
	at := Location{Repository: repo, Path: name}
	for _, method := range methods {
		run.find(finding{owner: owner{repo: repo, file: name}, kind: Ingress, key: "http:" + strings.TrimSpace(method+" "+route),
			label: strings.TrimSpace(method + " " + route), trigger: "request", edge: Handles,
			selectors: map[string]string{"http.request.method": method, "http.route": route},
			evidence:  static("nextjs", "file convention", Declared), at: at})
	}
}

// literals records outbound addresses: inside a network call (inferred) or
// assigned to an address-named constant (heuristic). Links, comments and
// other prose are skipped, never guessed into calls.
func (run *inference) literals(found owner, name, text string) {
	// Names handed to a network call anywhere in the file, found in one pass.
	passed := map[string]bool{}
	for _, match := range passedToCall.FindAllStringSubmatch(text, -1) {
		passed[match[1]] = true
	}
	lines := strings.Split(text, "\n")
	for number, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") ||
			strings.Contains(line, "href") || strings.Contains(line, "src=") {
			continue
		}
		for _, match := range urlLiteral.FindAllStringSubmatch(line, -1) {
			target := parseTarget(match[1])
			if target == nil {
				continue
			}
			at := Location{Repository: found.repo, Path: name, Line: number + 1}
			callee := calleeBefore.FindStringSubmatch(line[:strings.Index(line, match[1])])
			switch assigned := assignedName.FindStringSubmatch(line); {
			case networkCall.MatchString(line) || callee != nil && callName.MatchString(callee[1]):
				run.calls = append(run.calls, call{owner: found, target: target, at: at, evidence: static("pattern", "network call", Inferred)})
			case assigned != nil && passed[assigned[1]]:
				run.calls = append(run.calls, call{owner: found, target: target, at: at, evidence: static("pattern", assigned[1]+" passed to a network call", Inferred)})
			case assigned != nil && urlName.MatchString(assigned[1]):
				run.calls = append(run.calls, call{owner: found, target: target, at: at, evidence: static("pattern", assigned[1], Heuristic)})
			}
		}
	}
}

// lines records where each line starts, once per file.
func lines(text string) []int {
	starts := []int{0}
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}
