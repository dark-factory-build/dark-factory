package opgraph

import (
	"path"
	"regexp"
	"strings"
)

// scripts reads every non-Go source file through its tree-sitter grammar.
// A match is "declared" when the file imports the framework that executes
// it, and "inferred" when only the shape is recognised: the rule Go uses.
func (run *inference) scripts(repository Repository, names []string) {
	var nextRoots []string
	for _, candidate := range run.packages {
		if candidate.repo == repository.ID && candidate.deps["next"] {
			nextRoots = append(nextRoots, candidate.dir)
		}
	}
	for _, name := range names {
		if path.Base(name) == "page.mdx" {
			run.nextRoutes(repository.ID, nextRoots, name, nil)
		}
	}
	django := &djangoURLs{}
	run.parseScripts(repository, names, func(found *facts) {
		for _, call := range found.calls {
			if call.name == "require" && call.object == "" && len(call.args) > 0 {
				if value, ok := found.str(call.args[0]); ok {
					found.imports = append(found.imports, value)
				}
			}
		}
		x := &script{run: run, repository: repository, facts: found}
		switch found.language {
		case "javascript", "typescript", "tsx":
			x.javascript(nextRoots)
		case "python":
			x.python(django)
		case "ruby":
			x.ruby()
		case "java", "kotlin":
			x.jvm()
		case "rust":
			x.rust()
		}
		x.clients()
		x.outbound()
		if x.language == "javascript" || x.language == "typescript" || x.language == "tsx" {
			if run.scriptImports == nil {
				run.scriptImports = map[string][]string{}
			}
			for _, imported := range found.imports {
				if strings.HasPrefix(imported, ".") {
					run.scriptImports[repository.ID+"\x00"+found.file] = append(run.scriptImports[repository.ID+"\x00"+found.file], imported)
				}
			}
		}
	})
	django.resolve()
}

type script struct {
	run        *inference
	repository Repository
	*facts
	browser bool
}

// imports says whether the file imports a module under any of the prefixes.
func (x *script) imports(prefixes ...string) bool {
	for _, imported := range x.facts.imports {
		for _, prefix := range prefixes {
			if imported == prefix || strings.HasPrefix(imported, prefix+"/") || strings.HasPrefix(imported, prefix+".") || strings.HasPrefix(imported, prefix+"::") {
				return true
			}
		}
	}
	return false
}

func (x *script) at(s span) Location {
	return Location{Repository: x.repository.ID, Path: x.file, Line: s.line}
}

func (x *script) owner() owner { return owner{repo: x.repository.ID, file: x.file, browser: x.browser} }

func evidence(declared bool, detail string) Evidence {
	if declared {
		return static("tree-sitter", detail, Declared)
	}
	return static("tree-sitter", detail, Inferred)
}

func (x *script) route(method, route string, found Evidence, at span) {
	// In browser code these shapes are calls out, never entry points.
	if x.browser || strings.Contains(route, unresolved) {
		return
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	method = strings.ToUpper(method)
	if method == "ANY" || method == "ALL" {
		method = ""
	}
	x.run.find(finding{owner: x.owner(), kind: Ingress, key: "http:" + strings.TrimSpace(method+" "+route), label: strings.TrimSpace(method + " " + route),
		trigger: TriggerRequest, edge: Handles, selectors: map[string]string{"http.request.method": method, "http.route": route}, evidence: found, at: x.at(at)})
}

// function is a job (a background worker) or a timer ingress named by code.
func (x *script) function(kind Kind, name string, found Evidence, at span) {
	edge, trigger := Runs, Trigger("")
	if kind == Ingress {
		edge, trigger = Handles, TriggerTimer
	}
	x.run.find(finding{owner: x.owner(), kind: kind, key: "fn:" + name, label: name, trigger: trigger, edge: edge,
		selectors: map[string]string{"code.function.name": name}, evidence: found, at: x.at(at)})
}

func (x *script) queue(destination span, system string, edge EdgeKind, found Evidence) {
	for _, item := range elements(destination) {
		name, ok := x.str(item)
		if !ok || name == "" {
			continue
		}
		x.run.find(finding{owner: x.owner(), kind: Queue, key: "queue:" + name, label: name, shared: true, edge: edge,
			selectors: map[string]string{"messaging.destination.name": name, "messaging.system": system}, evidence: found, at: x.at(item)})
	}
}

// loop is a job: a repeating timer inside a function, or a whole module.
func (x *script) loop(call *site) {
	name := strings.TrimSuffix(path.Base(x.file), path.Ext(x.file))
	if within := x.enclosing(call.span, nil); within != nil {
		name = within.name
	}
	x.function(Job, name, evidence(false, call.name), call.span)
}

// joinRoute appends a route to a prefix the way routers mount them.
func joinRoute(prefix, route string) string {
	switch {
	case prefix == "":
		return route
	case route == "" || route == "/":
		return prefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(route, "/")
}

// path reads a route argument: the first positional, else a named one.
func (x *script) path(s *site, keywords ...string) (string, bool) {
	if len(s.args) > 0 {
		return x.str(s.args[0])
	}
	if value, ok := s.keyword(keywords...); ok {
		return x.str(value)
	}
	return "", true
}

// decorated finds a definition's decorator by name, in the names' order.
func decorated(target *definition, names ...string) *site {
	for _, name := range names {
		for _, decorator := range target.decorators {
			if decorator.name == name {
				return decorator
			}
		}
	}
	return nil
}

var httpMethods = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE",
	"head": "HEAD", "options": "OPTIONS", "all": "", "any": ""}

// methodsOf reads methods named in a list, a string or a RequestMethod.X.
func (x *script) methodsOf(value span) []string {
	var methods []string
	for _, item := range elements(value) {
		text, ok := x.str(item)
		if !ok {
			text = lastSegment(item.text)
		}
		if method, known := httpMethods[strings.ToLower(text)]; known {
			methods = append(methods, method)
		}
	}
	return methods
}

// messagingRule recognises a producer or consumer of a named destination
// in a file importing the client: the destination is the first argument,
// or one of the named keywords.
type messagingRule struct {
	module, call, keywords string
	edge                   EdgeKind
	system                 string
}

func (x *script) messaging(rules []messagingRule) {
	for _, rule := range rules {
		if !x.imports(rule.module) {
			continue
		}
		for _, call := range x.calls {
			if call.name != rule.call {
				continue
			}
			if rule.keywords == "" && len(call.args) > 0 {
				x.queue(call.args[0], rule.system, rule.edge, evidence(true, call.name))
			}
			for _, keyword := range strings.Fields(rule.keywords) {
				if value, ok := call.keyword(keyword); ok {
					x.queue(value, rule.system, rule.edge, evidence(true, call.name))
				}
			}
		}
	}
}

// --- JavaScript and TypeScript ---

var (
	legacyRouters = map[string]bool{"app": true, "router": true, "api": true, "server": true, "route": true, "routes": true, "r": true}
	routerMakers  = map[string]bool{"express": true, "Router": true, "Hono": true, "Fastify": true, "fastify": true, "Elysia": true}
	jsServers     = []string{"express", "hono", "fastify", "elysia", "koa-router", "@koa/router", "restify", "@hono/node-server"}
)

func (x *script) javascript(nextRoots []string) {
	ext := path.Ext(x.file)
	x.browser = ext == ".tsx" || ext == ".jsx" || contains(x.directives, "use client")
	for _, global := range []string{"window", "document", "navigator", "localStorage", "sessionStorage"} {
		x.browser = x.browser || x.globals[global]
	}
	for _, call := range x.calls {
		x.browser = x.browser || call.name == "WebSocket" && strings.HasPrefix(call.text, "new")
	}
	x.run.nextRoutes(x.repository.ID, nextRoots, x.file, x.exports)
	if strings.HasPrefix(path.Base(x.file), "middleware.") {
		for _, literal := range x.strings {
			if host, ok := x.str(literal); ok && hostLiteral.MatchString(host) && !reservedHost(host) {
				x.run.hostHints = append(x.run.hostHints, hostHint{owner: owner{repo: x.repository.ID, file: x.file}, host: host})
			}
		}
	}
	framework := x.imports(jsServers...)
	routers := map[string]bool{}
	for name, value := range x.values {
		if made := x.bySpan[[2]int{value.start, value.end}]; made != nil && routerMakers[made.name] {
			routers[name] = true
		}
	}
	// A router mounted in this file routes under its mount path.
	prefixes := map[string]string{}
	for _, call := range x.calls {
		if (call.name == "use" || call.name == "route" || call.name == "register") && len(call.args) >= 2 && routers[call.args[1].text] {
			if mount, ok := x.str(call.args[0]); ok && strings.HasPrefix(mount, "/") {
				prefixes[call.args[1].text] = mount
			}
		}
	}
	for _, call := range x.calls {
		declared := framework && routers[call.object]
		if !declared && !legacyRouters[call.object] {
			continue
		}
		if url, ok := call.keyword("url"); ok && call.name == "route" { // fastify.route({ method, url })
			route, complete := x.str(url)
			methods := []string{""}
			if value, ok := call.keyword("method"); ok {
				methods = x.methodsOf(value)
			}
			for _, method := range methods {
				if complete && strings.HasPrefix(route, "/") {
					x.route(method, joinRoute(prefixes[call.object], route), evidence(declared, "route options"), call.span)
				}
			}
			continue
		}
		method, isMethod := httpMethods[call.name]
		if !isMethod || len(call.args) < 2 {
			continue
		}
		if route, ok := x.str(call.args[0]); ok && strings.HasPrefix(route, "/") {
			x.route(method, joinRoute(prefixes[call.object], route), evidence(declared, "router method "+call.name), call.span)
		}
	}
	// NestJS: a controller's path prefixes its handlers' paths.
	nest := x.imports("@nestjs/common")
	for _, decorator := range x.decorators {
		method, ok := httpMethods[strings.ToLower(decorator.name)]
		if !ok || decorator.name == strings.ToLower(decorator.name) || decorator.target == nil || decorator.target.class {
			continue
		}
		controller := x.enclosing(decorator.target.span, func(d *definition) bool { return d.class && decorated(d, "Controller") != nil })
		if controller == nil && !nest {
			continue // a bare @Get outside any controller is not a route we know
		}
		prefix, ok := "", true
		if controller != nil {
			prefix, ok = x.path(decorated(controller, "Controller"), "path")
		}
		if route, complete := x.path(decorator, "path"); ok && complete {
			x.route(method, joinRoute("/"+strings.TrimPrefix(prefix, "/"), route), evidence(nest, "@"+decorator.name), decorator.span)
		}
	}
	x.messaging([]messagingRule{
		{"bullmq", "Queue", "", Publishes, "bullmq"}, {"bullmq", "Worker", "", Consumes, "bullmq"},
		{"bull", "Queue", "", Publishes, "bull"}, {"bull", "Bull", "", Publishes, "bull"},
		{"kafkajs", "send", "topic", Publishes, "kafka"}, {"kafkajs", "subscribe", "topic topics", Consumes, "kafka"},
		{"amqplib", "sendToQueue", "", Publishes, "rabbitmq"}, {"amqplib", "consume", "", Consumes, "rabbitmq"},
	})
	cron := x.imports("node-cron", "cron")
	for _, call := range x.calls {
		switch {
		case cron && (call.name == "schedule" || call.name == "CronJob") && len(call.args) > 0:
			if schedule, ok := x.str(call.args[0]); ok {
				x.run.find(finding{owner: x.owner(), kind: Ingress, key: "cron:" + schedule, label: "cron " + schedule, trigger: TriggerTimer, edge: Handles,
					evidence: evidence(true, call.name), at: x.at(call.span)})
			}
		case call.name == "setInterval" && call.object == "" && !x.browser:
			x.loop(call)
		}
	}
}

// nextRoutes applies the Next.js file conventions under app/ and pages/;
// exports are a route handler's exported functions.
func (run *inference) nextRoutes(repo string, roots []string, name string, exports []string) {
	for _, root := range roots {
		if within(root, name) {
			run.nextRoute(repo, root, name, exports)
		}
	}
}

func (run *inference) nextRoute(repo, root, name string, exports []string) {
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
			for _, exported := range exports {
				if method, ok := httpMethods[strings.ToLower(exported)]; ok && exported == method && method != "" {
					methods = append(methods, method)
				}
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
			label: strings.TrimSpace(method + " " + route), trigger: TriggerRequest, edge: Handles,
			selectors: map[string]string{"http.request.method": method, "http.route": route},
			evidence:  static("nextjs", "file convention", Declared), at: at})
	}
}

// --- Python ---

// djangoURLs holds URLconfs until every file is read: include() mounts one
// URLconf under another's route.
type djangoURLs struct{ files []djangoFile }

type djangoFile struct {
	script  *script
	entries []djangoEntry
}

type djangoEntry struct {
	route, include string // include names a module ("shop.urls")
	declared       bool
	at             span
}

func (x *script) python(django *djangoURLs) {
	framework := x.imports("fastapi", "flask", "starlette", "quart")
	type router struct {
		prefix string
		flask  bool
	}
	routers := map[string]router{}
	for name, value := range x.values {
		made := x.bySpan[[2]int{value.start, value.end}]
		if made == nil || !contains([]string{"FastAPI", "APIRouter", "Flask", "Blueprint", "Quart"}, made.name) {
			continue
		}
		prefix := ""
		if value, ok := made.keyword("prefix", "url_prefix"); ok {
			prefix, _ = x.str(value)
		}
		routers[name] = router{prefix: prefix, flask: made.name == "Flask" || made.name == "Blueprint" || made.name == "Quart"}
	}
	// app.include_router(router, prefix=...) mounts a router of this file.
	for _, call := range x.calls {
		if (call.name == "include_router" || call.name == "register_blueprint") && len(call.args) > 0 {
			held, ok := routers[call.args[0].text]
			value, mounted := call.keyword("prefix", "url_prefix")
			if mount, resolved := x.str(value); ok && mounted && resolved {
				held.prefix = joinRoute(mount, held.prefix)
				routers[call.args[0].text] = held
			}
		}
	}
	for _, decorator := range x.decorators {
		held, declared := routers[decorator.object]
		declared = declared && framework
		if decorator.target == nil {
			continue
		}
		detail := "@" + decorator.object + "." + decorator.name
		switch method, isMethod := httpMethods[decorator.name]; {
		case decorator.object != "" && isMethod && method != "":
			if route, ok := x.path(decorator, "path"); ok {
				x.route(method, joinRoute(held.prefix, route), evidence(declared, detail), decorator.span)
			}
		case decorator.object != "" && (decorator.name == "route" || decorator.name == "api_route"):
			route, ok := x.path(decorator, "rule", "path")
			methods := []string{""}
			if held.flask {
				methods = []string{"GET"} // Flask's default
			}
			if value, listed := decorator.keyword("methods"); listed {
				methods = x.methodsOf(value)
			}
			for _, method := range methods {
				if ok {
					x.route(method, joinRoute(held.prefix, route), evidence(declared, detail), decorator.span)
				}
			}
		case decorator.name == "shared_task" || decorator.name == "task" || decorator.name == "periodic_task":
			x.function(Job, decorator.target.name, evidence(x.imports("celery"), "@"+decorator.name), decorator.span)
		case decorator.name == "scheduled_job":
			x.function(Ingress, decorator.target.name, evidence(x.imports("apscheduler"), "@"+decorator.name), decorator.span)
		}
	}
	if path.Base(x.file) == "urls.py" || x.imports("django.urls") {
		file := djangoFile{script: x}
		for _, call := range x.calls {
			if (call.name != "path" && call.name != "re_path") || call.object != "" || len(call.args) == 0 {
				continue
			}
			route, ok := x.str(call.args[0])
			if !ok {
				continue
			}
			entry := djangoEntry{route: strings.TrimSuffix(strings.TrimPrefix(route, "^"), "$"), declared: x.imports("django.urls", "django.conf.urls"), at: call.span}
			if len(call.args) > 1 {
				if include := x.bySpan[[2]int{call.args[1].start, call.args[1].end}]; include != nil && include.name == "include" && len(include.args) > 0 {
					if entry.include, ok = x.str(include.args[0]); !ok {
						continue
					}
				}
			}
			file.entries = append(file.entries, entry)
		}
		django.files = append(django.files, file)
	}
	x.messaging([]messagingRule{
		{"kafka", "KafkaConsumer", "", Consumes, "kafka"}, {"pika", "basic_publish", "routing_key", Publishes, "rabbitmq"},
		{"pika", "basic_consume", "queue", Consumes, "rabbitmq"}, {"confluent_kafka", "produce", "topic", Publishes, "kafka"},
	})
	if x.imports("kafka", "confluent_kafka") {
		for _, call := range x.calls {
			producer := x.bySpan[[2]int{x.values[call.object].start, x.values[call.object].end}]
			switch {
			case len(call.args) == 0:
			case (call.name == "send" || call.name == "produce") && producer != nil && (producer.name == "KafkaProducer" || producer.name == "Producer"):
				x.queue(call.args[0], "kafka", Publishes, evidence(true, call.name))
			case call.name == "subscribe":
				x.queue(call.args[0], "kafka", Consumes, evidence(true, call.name))
			}
		}
	}
}

// resolve mounts each URLconf under the routes that include it; a URLconf
// nothing includes is a root.
func (django *djangoURLs) resolve() {
	byModule := map[string]int{}
	for index, file := range django.files {
		module := strings.ReplaceAll(strings.TrimSuffix(file.script.file, ".py"), "/", ".")
		for module != "" {
			if _, held := byModule[module]; !held {
				byModule[module] = index
			}
			_, module, _ = strings.Cut(module, ".")
		}
	}
	included := map[int]bool{}
	for _, file := range django.files {
		for _, entry := range file.entries {
			if target, ok := byModule[entry.include]; ok && entry.include != "" {
				included[target] = true
			}
		}
	}
	var mount func(index int, prefix string, depth int)
	mount = func(index int, prefix string, depth int) {
		file := django.files[index]
		for _, entry := range file.entries {
			route := joinRoute(prefix, entry.route)
			if entry.include == "" {
				file.script.route("", route, evidence(entry.declared, "urlpattern"), entry.at)
			} else if target, ok := byModule[entry.include]; ok && depth < 8 {
				mount(target, "/"+strings.TrimPrefix(route, "/"), depth+1)
			}
		}
	}
	for index := range django.files {
		if !included[index] {
			mount(index, "", 0)
		}
	}
}

// --- Ruby ---

var restActions = []struct{ action, method, suffix string }{
	{"index", "GET", ""}, {"create", "POST", ""}, {"new", "GET", "/new"}, {"show", "GET", "/:id"},
	{"update", "PATCH", "/:id"}, {"destroy", "DELETE", "/:id"}, {"edit", "GET", "/:id/edit"},
}

func (x *script) ruby() {
	routes := path.Base(x.file) == "routes.rb" || strings.Contains(x.file, "config/routes/")
	sinatra := x.imports("sinatra")
	for _, defined := range x.definitions {
		sinatra = sinatra || defined.super == "Sinatra::Base"
		if (defined.super == "ApplicationJob" || defined.super == "ActiveJob::Base") && defined.name != "ApplicationJob" {
			x.function(Job, defined.name, evidence(true, "< "+defined.super), defined.span)
		}
	}
	detail := "rails routes"
	if !routes {
		detail = "sinatra"
	}
	var blocks []*site
	for _, call := range x.calls {
		if call.object == "" && contains([]string{"namespace", "scope", "resources", "resource", "member", "collection"}, call.name) {
			blocks = append(blocks, call)
		}
	}
	for _, call := range x.calls {
		if call.name == "include" && len(call.args) > 0 && (call.args[0].text == "Sidekiq::Job" || call.args[0].text == "Sidekiq::Worker") {
			if class := x.enclosing(call.span, func(d *definition) bool { return d.class }); class != nil {
				x.function(Job, class.name, evidence(true, "include "+call.args[0].text), call.span)
			}
		}
		if call.object != "" || !routes && !sinatra {
			continue
		}
		prefix, ok := x.railsScope(call, blocks)
		if !ok {
			continue
		}
		switch call.name {
		case "get", "post", "put", "patch", "delete", "match", "root":
			route, complete := "", true
			switch {
			case call.name == "root":
			case len(call.args) > 0:
				route, complete = x.str(call.args[0])
			case len(call.pairs) > 0: // get "x" => "controller#action"
				route, complete = x.str(call.pairs[0].key)
			}
			methods := []string{call.name}
			switch call.name {
			case "root":
				methods = []string{"GET"}
			case "match":
				methods = []string{""}
				if value, ok := call.keyword("via"); ok {
					methods = x.methodsOf(value)
				}
			}
			for _, method := range methods {
				if complete {
					x.route(method, joinRoute(prefix, route), evidence(true, detail), call.span)
				}
			}
		case "resources", "resource":
			if routes {
				x.resources(call, prefix)
			}
		}
	}
}

// resources expands Rails resource routing into the routes it declares.
func (x *script) resources(call *site, prefix string) {
	only, except := map[string]bool{}, map[string]bool{}
	for keyword, set := range map[string]map[string]bool{"only": only, "except": except} {
		if value, ok := call.keyword(keyword); ok {
			for _, item := range elements(value) {
				name, _ := x.str(item)
				set[name] = true
			}
		}
	}
	for _, arg := range call.args {
		name, ok := x.str(arg)
		if !ok {
			continue
		}
		for _, action := range restActions {
			suffix := action.suffix
			if call.name == "resource" {
				if action.action == "index" {
					continue
				}
				suffix = strings.TrimPrefix(suffix, "/:id")
			}
			if len(only) > 0 && !only[action.action] || except[action.action] {
				continue
			}
			x.route(action.method, joinRoute(prefix, "/"+name+suffix), evidence(true, "rails routes "+call.name), call.span)
		}
	}
}

// railsScope is the path the enclosing routing blocks put before a call.
func (x *script) railsScope(call *site, blocks []*site) (string, bool) {
	var parts []string
	member := -1                   // the innermost resources part, which member and collection rewrite
	for _, outer := range blocks { // in document order: outermost first
		if outer == call || !outer.contains(call.span) {
			continue
		}
		part, ok := "", true
		switch {
		case outer.name == "namespace" && len(outer.args) > 0:
			part, ok = x.str(outer.args[0])
		case outer.name == "scope":
			if value, held := outer.keyword("path"); held {
				part, ok = x.str(value)
			} else if len(outer.args) > 0 {
				part, ok = x.str(outer.args[0])
			}
		case outer.name == "resources" && len(outer.args) > 0:
			part, ok = x.str(outer.args[0])
			member = len(parts)
			parts = append(parts, part+"/:"+singular(part)+"_id")
			continue
		case outer.name == "resource" && len(outer.args) > 0:
			part, ok = x.str(outer.args[0])
		case (outer.name == "member" || outer.name == "collection") && member >= 0:
			name, _, _ := strings.Cut(parts[member], "/:")
			parts[member] = map[string]string{"member": name + "/:id", "collection": name}[outer.name]
			continue
		default:
			continue
		}
		if !ok {
			return "", false
		}
		parts = append(parts, part)
	}
	prefix := ""
	for _, part := range parts {
		if part != "" {
			prefix = joinRoute(prefix, "/"+strings.TrimPrefix(part, "/"))
		}
	}
	return prefix, !strings.Contains(prefix, unresolved)
}

// singular is Rails' parameter name for a plural resource, for the
// regular plurals.
func singular(plural string) string {
	if strings.HasSuffix(plural, "ies") {
		return strings.TrimSuffix(plural, "ies") + "y"
	}
	return strings.TrimSuffix(plural, "s")
}

// --- Java and Kotlin ---

var jvmMappings = map[string]string{
	"GetMapping": "GET", "PostMapping": "POST", "PutMapping": "PUT", "DeleteMapping": "DELETE", "PatchMapping": "PATCH", "RequestMapping": "",
	"GET": "GET", "POST": "POST", "PUT": "PUT", "DELETE": "DELETE", "PATCH": "PATCH", "HEAD": "HEAD", "OPTIONS": "OPTIONS", // JAX-RS
	"Get": "GET", "Post": "POST", "Put": "PUT", "Delete": "DELETE", "Patch": "PATCH", // Micronaut
}

func (x *script) jvm() {
	spring := x.imports("org.springframework")
	declared := spring || x.imports("javax.ws.rs", "jakarta.ws.rs", "io.micronaut")
	for _, decorator := range x.decorators {
		target := decorator.target
		if decorator.name == "SpringBootApplication" {
			x.springApplication()
		}
		if target == nil {
			continue
		}
		if _, ok := jvmMappings[decorator.name]; ok && !target.class {
			x.mapping(decorator, target, declared)
		}
		switch decorator.name {
		case "Scheduled":
			x.function(Ingress, target.name, evidence(spring, "@Scheduled"), decorator.span)
		case "KafkaListener":
			if value, ok := decorator.keyword("topics", "value"); ok {
				x.queue(value, "kafka", Consumes, evidence(spring, "@KafkaListener"))
			}
		case "RabbitListener":
			if value, ok := decorator.keyword("queues", "value"); ok {
				x.queue(value, "rabbitmq", Consumes, evidence(spring, "@RabbitListener"))
			}
		case "JmsListener":
			if value, ok := decorator.keyword("destination"); ok {
				x.queue(value, "jms", Consumes, evidence(spring, "@JmsListener"))
			}
		}
	}
	for _, call := range x.calls {
		object := strings.ToLower(lastSegment(call.object))
		switch {
		case len(call.args) < 2:
		case call.name == "send" && strings.Contains(object, "kafka"):
			x.queue(call.args[0], "kafka", Publishes, evidence(spring, call.object+".send"))
		case call.name == "convertAndSend" && strings.Contains(object, "rabbit"):
			x.queue(call.args[len(call.args)-2], "rabbitmq", Publishes, evidence(spring, call.object+".convertAndSend"))
		case call.name == "convertAndSend" && strings.Contains(object, "jms"):
			x.queue(call.args[0], "jms", Publishes, evidence(spring, call.object+".convertAndSend"))
		}
	}
	// Ktor: routes nest inside routing { route("/p") { get("/x") { } } }.
	ktor := x.imports("io.ktor")
	var blocks []*site
	for _, call := range x.calls {
		if call.object == "" && (call.name == "routing" || call.name == "route") {
			blocks = append(blocks, call)
		}
	}
	for _, call := range x.calls {
		method, ok := httpMethods[call.name]
		if !ok || method == "" || call.object != "" || len(call.args) == 0 || len(blocks) == 0 {
			continue
		}
		routing, prefix, complete := false, "", true
		for _, outer := range blocks {
			if outer == call || !outer.contains(call.span) {
				continue
			}
			switch {
			case outer.name == "routing":
				routing = true
			case outer.name == "route" && len(outer.args) > 0:
				part, ok := x.str(outer.args[0])
				prefix, complete = joinRoute(prefix, part), complete && ok
			}
		}
		if route, ok := x.str(call.args[0]); routing && complete && ok {
			x.route(method, joinRoute(prefix, route), evidence(ktor, "ktor "+call.name), call.span)
		}
	}
}

// mapping is one handler annotation under its class's path.
func (x *script) mapping(decorator *site, target *definition, declared bool) {
	prefixes := []string{""}
	if class := x.enclosing(target.span, func(d *definition) bool { return d.class }); class != nil {
		if mapping := decorated(class, "RequestMapping", "Path", "Controller"); mapping != nil {
			prefixes = x.annotationPaths(mapping)
		}
	}
	methods, routes := []string{jvmMappings[decorator.name]}, x.annotationPaths(decorator)
	switch {
	case decorator.name == strings.ToUpper(decorator.name): // JAX-RS: the path is its own annotation
		routes = []string{""}
		if path := decorated(target, "Path"); path != nil {
			routes = x.annotationPaths(path)
		}
	case decorator.name == "RequestMapping":
		if value, ok := decorator.keyword("method"); ok {
			methods = x.methodsOf(value)
		}
	}
	for _, prefix := range prefixes {
		for _, route := range routes {
			for _, method := range methods {
				x.route(method, joinRoute(prefix, route), evidence(declared, "@"+decorator.name), decorator.span)
			}
		}
	}
}

// annotationPaths reads value, path or the positional argument; an array
// maps one handler to several paths.
func (x *script) annotationPaths(annotation *site) []string {
	value, ok := annotation.keyword("value", "path")
	if !ok && len(annotation.args) > 0 {
		value, ok = annotation.args[0], true
	}
	if !ok {
		return []string{""}
	}
	var paths []string
	for _, item := range elements(value) {
		if route, ok := x.str(item); ok {
			paths = append(paths, route)
		}
	}
	return paths
}

func (x *script) springApplication() {
	files := x.repository.Files
	root := path.Dir(x.file)
	for root != "." && files[path.Join(root, "pom.xml")] == nil && files[path.Join(root, "build.gradle")] == nil && files[path.Join(root, "build.gradle.kts")] == nil {
		root = path.Dir(root)
	}
	label := path.Base(root)
	if root == "." {
		label = x.repository.Name
	}
	x.run.addUnit(unit{repo: x.repository.ID, root: root, key: "spring:" + root, label: label, runtime: RuntimeProcess, role: "web",
		names: []string{label}, evidence: static("spring", "@SpringBootApplication", Declared), at: Location{Repository: x.repository.ID, Path: x.file}})
}

// --- Rust ---

func (x *script) rust() {
	for _, decorator := range x.decorators {
		method, ok := httpMethods[decorator.name]
		if decorator.name == "route" {
			method, ok = "", true
		}
		if !ok || decorator.target == nil || len(decorator.args) == 0 {
			continue
		}
		if route, complete := x.str(decorator.args[0]); complete {
			x.route(method, route, evidence(x.imports("actix_web", "rocket"), "#["+decorator.name+"]"), decorator.span)
		}
	}
	// Router::new().nest("/p", api()) mounts an inline router, a binding's or
	// a function's routes under /p; web::scope("/p") prefixes its chain.
	type mount struct {
		at     span
		prefix string
	}
	var mounts []mount
	for _, call := range x.calls {
		if call.name != "nest" && call.name != "scope" || len(call.args) == 0 {
			continue
		}
		prefix, ok := x.str(call.args[0])
		if !ok {
			continue
		}
		if call.name == "scope" {
			for _, outer := range x.calls {
				if strings.HasPrefix(strings.TrimSpace(outer.object), call.text) {
					mounts = append(mounts, mount{at: outer.span, prefix: prefix})
				}
			}
			continue
		}
		if len(call.args) < 2 {
			continue
		}
		router := call.args[1]
		mounts = append(mounts, mount{at: router, prefix: prefix})
		name := strings.TrimSuffix(strings.TrimSpace(router.text), "()")
		if value, ok := x.values[name]; ok {
			mounts = append(mounts, mount{at: value, prefix: prefix})
		}
		for _, defined := range x.definitions {
			if defined.name == lastSegment(name) {
				mounts = append(mounts, mount{at: defined.span, prefix: prefix})
			}
		}
	}
	framework := x.imports("axum", "actix_web", "poem")
	for _, call := range x.calls {
		if call.name != "route" || len(call.args) < 2 {
			continue
		}
		route, ok := x.str(call.args[0])
		if !ok || !strings.HasPrefix(route, "/") {
			continue
		}
		prefix := ""
		for _, held := range mounts {
			if held.at.contains(call.span) {
				prefix = joinRoute(held.prefix, prefix)
			}
		}
		var methods []string
		for _, inner := range x.within(call.args[1]) {
			if method, ok := httpMethods[inner.name]; ok && !contains(methods, method) {
				methods = append(methods, method)
			}
		}
		if len(methods) == 0 {
			methods = []string{""}
		}
		for _, method := range methods {
			x.route(method, joinRoute(prefix, route), evidence(framework, ".route"), call.span)
		}
	}
	for _, call := range x.calls {
		switch {
		case (call.name == "interval" || call.name == "interval_at") && strings.Contains(call.object, "time"):
			x.loop(call)
		case len(call.args) == 0:
		case call.name == "to" && strings.HasSuffix(call.object, "Record") && x.imports("rdkafka"):
			x.queue(call.args[0], "kafka", Publishes, evidence(true, call.object+"::to"))
		case call.name == "subscribe" && x.imports("rdkafka"):
			x.queue(call.args[0], "kafka", Consumes, evidence(true, "subscribe"))
		case call.name == "basic_publish" && len(call.args) > 1 && x.imports("lapin"):
			x.queue(call.args[1], "rabbitmq", Publishes, evidence(true, "basic_publish"))
		case call.name == "basic_consume" && x.imports("lapin"):
			x.queue(call.args[0], "rabbitmq", Consumes, evidence(true, "basic_consume"))
		}
	}
}

// --- Every language ---

// clients maps imported client libraries to the stores, queues and
// parties they talk to, as manifests do, owned by the importing file.
func (x *script) clients() {
	seen := map[string]bool{}
	for _, imported := range x.facts.imports {
		name := imported
		switch x.language {
		case "javascript", "typescript", "tsx":
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "node:") {
				continue
			}
			parts := strings.SplitN(name, "/", 3)
			name = parts[0]
			if strings.HasPrefix(name, "@") && len(parts) > 1 {
				name += "/" + parts[1]
			}
		case "python", "rust":
			fields := strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == ':' || r == '{' || r == ' ' })
			if len(fields) == 0 {
				continue
			}
			name = strings.ReplaceAll(strings.ToLower(fields[0]), "_", "-")
		case "ruby":
		default:
			continue
		}
		if entry := knownClient(name, manifestClients); entry != nil && !seen[name] {
			seen[name] = true
			x.run.client(*entry, x.owner(), Location{Repository: x.repository.ID, Path: x.file}, "tree-sitter", "import "+name)
		}
	}
}

var (
	clientNames = map[string]bool{"fetch": true, "WebSocket": true, "EventSource": true, "axios": true, "got": true, "ky": true, "urlopen": true,
		"Request": true, "AsyncClient": true, "Client": true, "Session": true, "ClientSession": true, "WebClient": true, "RestTemplate": true,
		"newBuilder": true, "uri": true, "baseUrl": true, "create": true}
	clientObjects = map[string]bool{"axios": true, "got": true, "ky": true, "requests": true, "httpx": true, "aiohttp": true, "Faraday": true,
		"HTTParty": true, "RestClient": true, "reqwest": true, "ureq": true, "WebClient": true, "HttpClient": true, "HTTP": true}
	hostLiteral = regexp.MustCompile(`^(?:[a-z0-9-]+\.)+[a-z]{2,}$`)
)

// outbound records addresses: a call's argument (inferred), or a binding
// or keyword whose name says it holds an address (heuristic). Comments,
// prose and markup are not code, so they never reach here.
func (x *script) outbound() {
	add := func(value span, confidence, detail string) {
		text, _ := x.str(value)
		if target := parseTarget(text); target != nil {
			x.run.calls = append(x.run.calls, call{owner: x.owner(), target: target, at: x.at(value), evidence: static("tree-sitter", detail, confidence)})
		}
	}
	for _, site := range x.calls {
		if urlWrapper(site) || site.name == "format" {
			continue // a wrapper is read where it is used
		}
		network := callName.MatchString(site.name) || clientNames[site.name] || clientObjects[lastSegment(site.object)]
		for _, arg := range site.args {
			if network {
				add(arg, Inferred, "argument to "+site.name)
			}
		}
		for _, item := range site.pairs {
			key := strings.Trim(strings.TrimSpace(item.key.text), `:"'`)
			switch {
			case network:
				add(item.value, Inferred, "argument to "+site.name)
			case urlName.MatchString(key):
				add(item.value, Heuristic, key)
			}
		}
	}
	for _, name := range keys(x.values) {
		if urlName.MatchString(name) {
			add(x.values[name], Heuristic, name)
		}
	}
}

type hostHint struct {
	owner owner
	host  string
}

// reachScripts gives each unit with entry files the script files those
// entries import, transitively. An entry that names no file in the
// repository (a build output) is mapped back to its source, or reach stays
// unknown and every file counts.
func (run *inference) reachScripts(units []*unit, repositories []Repository) {
	files := map[string]map[string][]byte{}
	for _, repository := range repositories {
		files[repository.ID] = repository.Files
	}
	resolve := func(repo, name string) string {
		stem := strings.TrimSuffix(name, path.Ext(name))
		for _, candidate := range []string{name, stem + ".ts", stem + ".tsx", stem + ".mts", stem + ".js", stem + ".jsx", stem + ".mjs", stem + ".cjs",
			name + ".ts", name + ".tsx", name + ".js", name + "/index.ts", name + "/index.tsx", name + "/index.js"} {
			if files[repo][candidate] != nil {
				return candidate
			}
		}
		return ""
	}
	for _, candidate := range units {
		var queue []string
		for _, entry := range candidate.entries {
			found := resolve(candidate.repo, entry)
			for _, output := range []string{"dist", "build", "lib", "out"} {
				if found == "" && strings.HasPrefix(entry, path.Join(candidate.root, output)+"/") {
					found = resolve(candidate.repo, path.Join(candidate.root, "src", strings.TrimPrefix(entry, path.Join(candidate.root, output)+"/")))
				}
			}
			if found != "" {
				queue = append(queue, found)
			}
		}
		if len(queue) == 0 {
			continue
		}
		candidate.reach = map[string]bool{}
		for len(queue) > 0 {
			file := queue[0]
			queue = queue[1:]
			if candidate.reach[file] {
				continue
			}
			candidate.reach[file] = true
			for _, imported := range run.scriptImports[candidate.repo+"\x00"+file] {
				if next := resolve(candidate.repo, path.Join(path.Dir(file), imported)); next != "" {
					queue = append(queue, next)
				}
			}
		}
	}
}
