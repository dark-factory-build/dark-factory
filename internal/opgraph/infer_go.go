package opgraph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type goPackage struct {
	dir, importPath, name string
	files                 map[string]*ast.File
	imports               map[string]bool
	main                  bool
}

// goSource reads Go with the standard compiler front end: no build, no
// type check, no module download.
func (run *inference) goSource(repository Repository, names []string) {
	modules := map[string]string{}
	for _, name := range names {
		if path.Base(name) == "go.mod" {
			for _, line := range strings.Split(string(repository.Files[name]), "\n") {
				if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "module" {
					modules[path.Dir(name)] = strings.Trim(fields[1], `"`)
				}
			}
		}
	}
	packages := map[string]*goPackage{}
	byImport := map[string]*goPackage{}
	set := token.NewFileSet()
	for _, name := range names {
		if path.Ext(name) != ".go" {
			continue
		}
		file, err := parser.ParseFile(set, name, repository.Files[name], parser.SkipObjectResolution)
		if err != nil || file.Name == nil {
			continue
		}
		dir := path.Dir(name)
		pkg := packages[dir]
		if pkg == nil {
			pkg = &goPackage{dir: dir, name: file.Name.Name, files: map[string]*ast.File{}, imports: map[string]bool{}, importPath: goImportPath(modules, dir)}
			packages[dir] = pkg
			if pkg.importPath != "" {
				byImport[pkg.importPath] = pkg
			}
		}
		if file.Name.Name != pkg.name {
			continue
		}
		pkg.files[name] = file
		for _, imported := range file.Imports {
			if value, err := strconv.Unquote(imported.Path.Value); err == nil {
				pkg.imports[value] = true
			}
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" && pkg.name == "main" {
				pkg.main = true
			}
		}
	}
	// Each package belongs to every binary that links it.
	reach := map[string][]string{}          // package dir -> units using it
	reached := map[*ast.FuncDecl][]string{} // function -> units calling it
	dirs := make([]string, 0, len(packages))
	for dir := range packages {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		pkg := packages[dir]
		if !pkg.main || pkg.importPath == "" {
			continue
		}
		key := "go:" + pkg.importPath
		label := path.Base(pkg.importPath)
		run.addUnit(unit{repo: repository.ID, root: dir, key: key, label: label, names: []string{label},
			evidence: static("go-ast", "package main", Declared), at: Location{Repository: repository.ID, Path: dir}})
		linked := map[*goPackage]bool{}
		queue := []*goPackage{pkg}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if linked[current] {
				continue
			}
			linked[current] = true
			for imported := range current.imports {
				if next := byImport[imported]; next != nil {
					queue = append(queue, next)
				}
			}
		}
		for decl, owner := range reachableFuncs(pkg, linked, byImport) {
			reached[decl] = append(reached[decl], key)
			if !contains(reach[owner.dir], key) {
				reach[owner.dir] = append(reach[owner.dir], key)
			}
		}
	}
	consts := map[string]map[string]string{}
	for _, dir := range dirs {
		pkg := packages[dir]
		consts[pkg.importPath] = map[string]string{}
	}
	// Two passes: constants may refer to constants of other packages.
	for pass := 0; pass < 2; pass++ {
		for _, dir := range dirs {
			pkg := packages[dir]
			for _, file := range pkg.files {
				for _, decl := range file.Decls {
					general, ok := decl.(*ast.GenDecl)
					if !ok || general.Tok != token.CONST && general.Tok != token.VAR {
						continue
					}
					for _, spec := range general.Specs {
						values, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for index, name := range values.Names {
							if index < len(values.Values) {
								if value, ok := goString(values.Values[index], file, pkg, consts); ok {
									consts[pkg.importPath][name.Name] = value
								}
							}
						}
					}
				}
			}
		}
	}
	for _, candidate := range run.units {
		if candidate.repo != repository.ID || !strings.HasPrefix(candidate.key, "go:") {
			continue
		}
		for _, dir := range dirs {
			if contains(reach[dir], candidate.key) {
				candidate.modules = append(candidate.modules, dir)
			}
		}
	}
	for _, dir := range dirs {
		pkg := packages[dir]
		units := reach[dir]
		if len(units) == 0 {
			continue // linked into no binary in this repository
		}
		names := make([]string, 0, len(pkg.files))
		for name := range pkg.files {
			names = append(names, name)
		}
		sort.Strings(names)
		fields, reads := map[string][]*url.URL{}, []fieldRead{}
		for _, name := range names {
			(&goWalk{run: run, repository: repository, set: set, pkg: pkg, file: pkg.files[name], name: name, packageUnits: units, reached: reached, consts: consts, fields: fields, reads: &reads}).walk()
		}
		// ponytail: fields match by name across the package's types, like
		// methods in reachableFuncs; add types if two types' fields collide.
		for _, read := range reads {
			for _, target := range fields[read.field] {
				read.call.target = target
				run.calls = append(run.calls, read.call)
			}
		}
		for imported := range pkg.imports {
			if known := knownClient(imported, goClients); known != nil {
				run.client(*known, owner{units: units}, Location{Repository: repository.ID, Path: dir}, "manifest", "go import "+imported)
			}
		}
	}
}

func goImportPath(modules map[string]string, dir string) string {
	best := ""
	for root := range modules {
		if within(root, dir) && (best == "" || len(root) > len(best)) {
			best = root
		}
	}
	if best == "" && modules["."] == "" {
		return ""
	}
	if best == "" {
		best = "."
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(dir, best), "/")
	if best == "." {
		rel = dir
		if dir == "." {
			rel = ""
		}
	}
	if rel == "" {
		return modules[best]
	}
	return modules[best] + "/" + rel
}

// goString resolves a string constant expression: literals, concatenation,
// and named constants of this or another package in the repository.
func goString(expr ast.Expr, file *ast.File, pkg *goPackage, consts map[string]map[string]string) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			text, err := strconv.Unquote(value.Value)
			return text, err == nil
		}
	case *ast.ParenExpr:
		return goString(value.X, file, pkg, consts)
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			left, ok := goString(value.X, file, pkg, consts)
			right, ok2 := goString(value.Y, file, pkg, consts)
			return left + right, ok && ok2
		}
	case *ast.Ident:
		text, ok := consts[pkg.importPath][value.Name]
		return text, ok
	case *ast.SelectorExpr:
		if ident, ok := value.X.(*ast.Ident); ok {
			if imported := importFor(file, ident.Name); imported != "" {
				text, ok := consts[imported][value.Sel.Name]
				return text, ok
			}
		}
	}
	return "", false
}

func importFor(file *ast.File, alias string) string {
	for _, imported := range file.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(value)
		if strings.HasPrefix(name, "v") && len(name) <= 3 { // module major version suffix
			name = path.Base(path.Dir(value))
		}
		name = strings.TrimPrefix(strings.TrimPrefix(name, "go-"), "go.")
		if imported.Name != nil {
			name = imported.Name.Name
		}
		if name == alias {
			return value
		}
	}
	return ""
}

type goWalk struct {
	run        *inference
	repository Repository
	set        *token.FileSet
	pkg        *goPackage
	file       *ast.File
	name       string
	units      []string
	// packageUnits own package-level declarations; reached owns functions.
	packageUnits []string
	reached      map[*ast.FuncDecl][]string
	consts       map[string]map[string]string
	function     string
	// fields holds URLs reachable code stores in a struct field; reads are
	// request arguments built from a field, resolved once the package is walked.
	fields map[string][]*url.URL
	reads  *[]fieldRead
}

type fieldRead struct {
	field string
	call  call
}

var (
	urlName      = regexp.MustCompile(`(?i)(url|endpoint|origin|base|host|api|relay|webhook)`)
	listenName   = regexp.MustCompile(`(?i)(addr|address|listen)`)
	callName     = regexp.MustCompile(`(?i)(request|dial|get|post|put|patch|delete|head|fetch|connect|do|send|open)`)
	methodPrefix = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) `)
	routeMethods = map[string]string{"GET": "GET", "POST": "POST", "PUT": "PUT", "PATCH": "PATCH", "DELETE": "DELETE",
		"Get": "GET", "Post": "POST", "Put": "PUT", "Patch": "PATCH", "Delete": "DELETE"}
)

func (walk *goWalk) at(pos token.Pos) Location {
	return Location{Repository: walk.repository.ID, Path: walk.name, Line: walk.set.Position(pos).Line}
}

func (walk *goWalk) owner() owner { return owner{units: walk.units} }

func (walk *goWalk) str(expr ast.Expr) (string, bool) {
	return goString(expr, walk.file, walk.pkg, walk.consts)
}

func (walk *goWalk) walk() {
	for _, decl := range walk.file.Decls {
		switch value := decl.(type) {
		case *ast.FuncDecl:
			walk.units = walk.reached[value]
			if len(walk.units) == 0 {
				continue // no binary calls this function
			}
			walk.function = value.Name.Name
			if value.Recv != nil && len(value.Recv.List) == 1 {
				walk.function = receiverName(value.Recv.List[0].Type) + "." + value.Name.Name
			}
			if value.Body != nil {
				ast.Inspect(value.Body, walk.node)
			}
		case *ast.GenDecl:
			walk.function, walk.units = "", walk.packageUnits
			walk.declarations(value)
		}
	}
}

// declarations reads package-level names: URL-named addresses are heuristic
// outbound calls; address-named host:port values hint a listener's address.
func (walk *goWalk) declarations(general *ast.GenDecl) {
	for _, spec := range general.Specs {
		values, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for index, name := range values.Names {
			if index >= len(values.Values) {
				continue
			}
			text, ok := walk.str(values.Values[index])
			if !ok {
				continue
			}
			if target := parseTarget(text); target != nil && urlName.MatchString(name.Name) {
				walk.run.calls = append(walk.run.calls, call{owner: walk.owner(), target: target, at: walk.at(name.Pos()),
					evidence: static("go-ast", name.Name, Heuristic)})
			}
			if host, port, err := splitHostPort(text); err == nil && port != "" && listenName.MatchString(name.Name) {
				_ = host
				walk.run.hints = append(walk.run.hints, listenHint{units: walk.units, addr: text, at: walk.at(name.Pos())})
			}
		}
	}
}

func (walk *goWalk) node(node ast.Node) bool {
	switch value := node.(type) {
	case *ast.CallExpr:
		walk.callExpr(value)
	case *ast.BinaryExpr:
		if value.Op == token.EQL || value.Op == token.NEQ {
			for _, pair := range [][2]ast.Expr{{value.X, value.Y}, {value.Y, value.X}} {
				if isURLPath(pair[0]) {
					walk.pathRoute(pair[1])
				}
			}
		}
	case *ast.SwitchStmt:
		if value.Tag != nil && isURLPath(value.Tag) {
			for _, statement := range value.Body.List {
				if clause, ok := statement.(*ast.CaseClause); ok {
					for _, expr := range clause.List {
						walk.pathRoute(expr)
					}
				}
			}
		}
	case *ast.KeyValueExpr:
		if key, ok := value.Key.(*ast.Ident); ok {
			if text, ok := walk.str(value.Value); ok {
				if target := parseTarget(text); target != nil {
					walk.fields[key.Name] = append(walk.fields[key.Name], target)
					if urlName.MatchString(key.Name) {
						walk.run.calls = append(walk.run.calls, call{owner: walk.owner(), target: target, at: walk.at(value.Pos()),
							evidence: static("go-ast", key.Name, Heuristic)})
					}
				}
			}
		}
	case *ast.AssignStmt:
		for index, left := range value.Lhs {
			if selector, ok := left.(*ast.SelectorExpr); ok && len(value.Rhs) == len(value.Lhs) {
				if text, ok := walk.str(value.Rhs[index]); ok {
					if target := parseTarget(text); target != nil {
						walk.fields[selector.Sel.Name] = append(walk.fields[selector.Sel.Name], target)
					}
				}
			}
		}
	}
	return true
}

func isURLPath(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Path" {
		return false
	}
	inner, ok := selector.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "URL"
}

func (walk *goWalk) pathRoute(expr ast.Expr) {
	route, ok := walk.str(expr)
	if !ok || !strings.HasPrefix(route, "/") {
		return
	}
	walk.route("", route, expr.Pos(), Inferred, "request path comparison")
}

func (walk *goWalk) route(method, route string, pos token.Pos, confidence, detail string) {
	key := "http:" + strings.TrimSpace(method+" "+route)
	label := strings.TrimSpace(method + " " + route)
	walk.run.find(finding{owner: walk.owner(), kind: Ingress, key: key, label: label, trigger: TriggerRequest, edge: Handles,
		selectors: map[string]string{"http.request.method": method, "http.route": route},
		evidence:  static("go-ast", detail, confidence), at: walk.at(pos)})
}

func (walk *goWalk) callExpr(expr *ast.CallExpr) {
	selector, ok := expr.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	imported := ""
	if ident, ok := selector.X.(*ast.Ident); ok {
		imported = importFor(walk.file, ident.Name)
	}
	name := selector.Sel.Name
	switch {
	case imported == "net" && strings.HasPrefix(name, "Listen") || imported == "net/http" && strings.HasPrefix(name, "ListenAndServe"):
		walk.listener(expr, imported, name)
		return
	case name == "HandleFunc" || name == "Handle":
		if len(expr.Args) >= 2 {
			if pattern, ok := walk.str(expr.Args[0]); ok && (strings.HasPrefix(pattern, "/") || methodPrefix.MatchString(pattern)) {
				method, route := "", pattern
				if match := methodPrefix.FindString(pattern); match != "" {
					method, route = strings.TrimSpace(match), strings.TrimPrefix(pattern, match)
				}
				if slash := strings.Index(route, "/"); slash > 0 {
					route = route[slash:] // host-qualified ServeMux pattern
				}
				walk.route(method, route, expr.Pos(), Declared, "ServeMux pattern")
			}
		}
		return
	case routeMethods[name] != "" && imported == "" && len(expr.Args) >= 2:
		if route, ok := walk.str(expr.Args[0]); ok && strings.HasPrefix(route, "/") {
			walk.route(routeMethods[name], route, expr.Pos(), Inferred, "router method "+name)
			return
		}
	case imported == "os/exec" && (name == "Command" || name == "CommandContext"):
		index := 0
		if name == "CommandContext" {
			index = 1
		}
		if index < len(expr.Args) {
			program, ok := walk.str(expr.Args[index])
			if path.Base(program) == "env" { // /usr/bin/env NAME: the program is NAME
				program, ok = "", false
				for _, arg := range expr.Args[index+1:] {
					if text, resolved := walk.str(arg); resolved && !strings.HasPrefix(text, "-") && !strings.Contains(text, "=") {
						program, ok = text, true
						break
					}
				}
			}
			if ok && program != "" {
				base := path.Base(program)
				walk.run.find(finding{owner: walk.owner(), kind: External, key: "process:" + base, label: base, shared: true, edge: Calls,
					selectors: map[string]string{"process.executable.name": base},
					evidence:  static("go-ast", "exec "+base, Inferred), at: walk.at(expr.Pos())})
			}
		}
		return
	case imported == "time" && (name == "NewTicker" || name == "Tick" || name == "AfterFunc"):
		if walk.function != "" {
			label := walk.function
			walk.run.find(finding{owner: walk.owner(), kind: Job, key: "loop:" + walk.pkg.importPath + "." + walk.function, label: label, edge: Runs,
				selectors: map[string]string{"code.function.name": walk.pkg.importPath + "." + walk.function},
				evidence:  static("go-ast", "time."+name, Inferred), at: walk.at(expr.Pos())})
		}
		return
	}
	if !callName.MatchString(name) {
		return
	}
	for _, arg := range expr.Args {
		// A URL prefix names the host whatever path is appended to it.
		prefix := arg
		for {
			sum, ok := prefix.(*ast.BinaryExpr)
			if !ok || sum.Op != token.ADD {
				break
			}
			prefix = sum.X
		}
		text, ok := walk.str(arg)
		if !ok {
			text, ok = walk.str(prefix)
		}
		if ok {
			if target := parseTarget(text); target != nil {
				walk.run.calls = append(walk.run.calls, call{owner: walk.owner(), target: target, at: walk.at(arg.Pos()),
					evidence: static("go-ast", "argument to "+name, Inferred)})
			}
		} else if field, ok := prefix.(*ast.SelectorExpr); ok {
			*walk.reads = append(*walk.reads, fieldRead{field: field.Sel.Name, call: call{owner: walk.owner(), at: walk.at(arg.Pos()),
				evidence: static("go-ast", "argument to "+name+" from field "+field.Sel.Name, Inferred)}})
		}
	}
}

func (walk *goWalk) listener(expr *ast.CallExpr, imported, name string) {
	network, address := "tcp", ""
	if imported == "net/http" {
		if len(expr.Args) > 0 {
			address, _ = walk.str(expr.Args[0])
		}
	} else if len(expr.Args) >= 2 {
		if text, ok := walk.str(expr.Args[0]); ok {
			network = text
		}
		address, _ = walk.str(expr.Args[1])
	}
	transport := network
	if strings.HasPrefix(transport, "tcp") {
		transport = "tcp"
	}
	selectors := map[string]string{"network.transport": transport}
	key, label := "listen:"+walk.pkg.importPath+"."+walk.function, transport+" listener"
	if transport == "unix" {
		label = "unix socket"
	}
	if walk.function != "" {
		label += " (" + walk.function + ")"
	}
	if host, port, err := splitHostPort(address); err == nil && port != "" {
		selectors["server.address"], selectors["server.port"] = host, port
		key, label = "listen:"+transport+":"+address, transport+" listener "+address
	}
	walk.run.find(finding{owner: walk.owner(), kind: Ingress, key: key, label: label, trigger: TriggerRequest, edge: Handles,
		selectors: selectors, evidence: static("go-ast", imported+"."+name, Declared), at: walk.at(expr.Pos())})
}

func splitHostPort(address string) (string, string, error) {
	index := strings.LastIndex(address, ":")
	if index < 0 {
		return "", "", strconv.ErrSyntax
	}
	port := address[index+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", err
	}
	return strings.Trim(address[:index], "[]"), port, nil
}

func receiverName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.IndexExpr:
		return receiverName(value.X)
	case *ast.Ident:
		return value.Name
	}
	return "?"
}

// reachableFuncs walks calls and function references from main and every
// init, without types: a package function resolves exactly; a method
// resolves to every linked method of that name (an over-approximation).
func reachableFuncs(main *goPackage, linked map[*goPackage]bool, byImport map[string]*goPackage) map[*ast.FuncDecl]*goPackage {
	type index struct {
		funcs   map[string]*ast.FuncDecl
		methods map[string][]*ast.FuncDecl
	}
	indexes := map[*goPackage]index{}
	files := map[*ast.FuncDecl]*ast.File{}
	result := map[*ast.FuncDecl]*goPackage{}
	var queue []*ast.FuncDecl
	add := func(decl *ast.FuncDecl, pkg *goPackage) {
		if _, seen := result[decl]; !seen && decl.Body != nil {
			result[decl] = pkg
			queue = append(queue, decl)
		}
	}
	for pkg := range linked {
		entry := index{funcs: map[string]*ast.FuncDecl{}, methods: map[string][]*ast.FuncDecl{}}
		for _, file := range pkg.files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					files[fn] = file
					if fn.Recv == nil {
						entry.funcs[fn.Name.Name] = fn
					} else {
						entry.methods[fn.Name.Name] = append(entry.methods[fn.Name.Name], fn)
					}
				}
			}
		}
		indexes[pkg] = entry
	}
	visit := func(pkg *goPackage, file *ast.File, root ast.Node) {
		ast.Inspect(root, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SelectorExpr:
				if ident, ok := value.X.(*ast.Ident); ok {
					if target := byImport[importFor(file, ident.Name)]; target != nil {
						if linked[target] {
							if fn := indexes[target].funcs[value.Sel.Name]; fn != nil {
								add(fn, target)
							}
						}
						return false
					}
				}
				for other := range linked {
					for _, fn := range indexes[other].methods[value.Sel.Name] {
						add(fn, other)
					}
				}
				ast.Inspect(value.X, func(inner ast.Node) bool { return visitIdent(inner, indexes[pkg].funcs, pkg, add) })
				return false
			case *ast.Ident:
				visitIdent(value, indexes[pkg].funcs, pkg, add)
			}
			return true
		})
	}
	for pkg := range linked {
		for _, file := range pkg.files {
			for _, decl := range file.Decls {
				switch value := decl.(type) {
				case *ast.FuncDecl:
					if value.Recv == nil && (value.Name.Name == "init" || pkg == main && value.Name.Name == "main") {
						add(value, pkg)
					}
				case *ast.GenDecl:
					if value.Tok == token.VAR {
						visit(pkg, file, value)
					}
				}
			}
		}
	}
	for len(queue) > 0 {
		decl := queue[0]
		queue = queue[1:]
		visit(result[decl], files[decl], decl.Body)
	}
	return result
}

func visitIdent(node ast.Node, funcs map[string]*ast.FuncDecl, pkg *goPackage, add func(*ast.FuncDecl, *goPackage)) bool {
	if ident, ok := node.(*ast.Ident); ok {
		if fn := funcs[ident.Name]; fn != nil {
			add(fn, pkg)
		}
	}
	return true
}
