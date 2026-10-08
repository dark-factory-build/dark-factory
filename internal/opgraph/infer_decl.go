package opgraph

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"go.yaml.in/yaml/v3"
)

// declarations reads what deployment platforms and package managers read:
// wrangler, compose, Procfile, fly.toml, vercel.json and dependency manifests.
func (run *inference) declarations(repository Repository, names []string) {
	for _, name := range names {
		body := repository.Files[name]
		dir, base := path.Dir(name), strings.ToLower(path.Base(name))
		at := Location{Repository: repository.ID, Path: name}
		here := owner{repo: repository.ID, file: name}
		switch {
		case strings.HasPrefix(base, "wrangler."):
			run.wrangler(repository.ID, dir, base, body, at)
		case strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose."):
			run.compose(repository.ID, dir, body, at)
		case base == "procfile":
			for _, line := range strings.Split(string(body), "\n") {
				process, _, found := strings.Cut(line, ":")
				process = strings.TrimSpace(process)
				if !found || process == "" || strings.HasPrefix(process, "#") || process == "release" {
					continue
				}
				run.addUnit(unit{repo: repository.ID, root: dir, key: "procfile:" + process, label: process, deployed: true,
					role: role(process, false), names: []string{process}, evidence: static("procfile", process, Declared), at: at})
			}
		case base == "fly.toml":
			var fly struct {
				App         string `toml:"app"`
				HTTPService struct {
					InternalPort int `toml:"internal_port"`
				} `toml:"http_service"`
			}
			if toml.Unmarshal(body, &fly) == nil && fly.App != "" {
				run.addUnit(unit{repo: repository.ID, root: dir, key: "fly:" + fly.App, label: fly.App, deployed: true, role: "web",
					names: []string{fly.App}, hosts: []string{fly.App + ".fly.dev"}, evidence: static("fly", "app", Declared), at: at})
			}
		case base == "vercel.json":
			var vercel struct {
				Crons []struct{ Path, Schedule string } `json:"crons"`
			}
			if json.Unmarshal(body, &vercel) == nil {
				for _, cron := range vercel.Crons {
					run.find(finding{owner: here, kind: Ingress, key: "cron:" + cron.Path, label: "cron " + cron.Schedule, trigger: "timer", edge: Handles,
						selectors: map[string]string{"http.route": cron.Path}, evidence: static("vercel", "crons", Declared), at: at})
				}
			}
		case base == "package.json":
			run.packageJSON(repository.ID, dir, body, at, names)
		case base == "cargo.toml":
			var cargo struct {
				Package      struct{ Name string }   `toml:"package"`
				Dependencies map[string]any          `toml:"dependencies"`
				Bin          []struct{ Name string } `toml:"bin"`
			}
			if toml.Unmarshal(body, &cargo) != nil {
				continue
			}
			binary := len(cargo.Bin) > 0
			for _, other := range names {
				binary = binary || other == path.Join(dir, "src/main.rs")
			}
			if binary && cargo.Package.Name != "" {
				run.addUnit(unit{repo: repository.ID, root: dir, key: "cargo:" + cargo.Package.Name, label: cargo.Package.Name, runtime: "process",
					names: []string{cargo.Package.Name}, evidence: static("cargo", "binary target", Declared), at: at})
			}
			run.manifest(keys(cargo.Dependencies), here, at, "Cargo.toml")
		case base == "manage.py" || base == "config.ru":
			framework := map[string]string{"manage.py": "django", "config.ru": "rack"}[base]
			label := path.Base(dir)
			if dir == "." {
				label = repository.Name
			}
			run.addUnit(unit{repo: repository.ID, root: dir, key: framework + ":" + dir, label: label, runtime: "process", role: "web",
				names: []string{label}, evidence: static(framework, base, Declared), at: at})
		case base == "requirements.txt":
			var packages []string
			for _, line := range strings.Split(string(body), "\n") {
				if name := requirementName.FindString(strings.TrimSpace(line)); name != "" {
					packages = append(packages, strings.ToLower(name))
				}
			}
			run.manifest(packages, here, at, base)
		case base == "pyproject.toml":
			var project struct {
				Project struct{ Dependencies []string } `toml:"project"`
				Tool    struct {
					Poetry struct {
						Dependencies map[string]any `toml:"dependencies"`
					} `toml:"poetry"`
				} `toml:"tool"`
			}
			if toml.Unmarshal(body, &project) == nil {
				packages := keys(project.Tool.Poetry.Dependencies)
				for _, requirement := range project.Project.Dependencies {
					packages = append(packages, strings.ToLower(requirementName.FindString(requirement)))
				}
				run.manifest(packages, here, at, base)
			}
		case base == "gemfile":
			var gems []string
			for _, match := range gemName.FindAllStringSubmatch(string(body), -1) {
				gems = append(gems, match[1])
			}
			run.manifest(gems, here, at, base)
		case base == "pom.xml" || strings.HasPrefix(base, "build.gradle"):
			var artifacts []string
			for _, match := range jvmArtifact.FindAllStringSubmatch(string(body), -1) {
				for _, group := range match[1:] {
					if group != "" {
						artifacts = append(artifacts, group)
					}
				}
			}
			run.manifest(artifacts, here, at, base)
		}
	}
}

var (
	requirementName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)
	gemName         = regexp.MustCompile(`(?m)^\s*gem\s+['"]([^'"]+)['"]`)
	jvmArtifact     = regexp.MustCompile(`<artifactId>([^<]+)</artifactId>|['"][\w.-]+:([\w.-]+)(?::[^'"]*)?['"]`)
)

func (run *inference) manifest(packages []string, found owner, at Location, detail string) {
	sort.Strings(packages)
	for _, name := range packages {
		if entry := knownClient(name, manifestClients); entry != nil {
			run.client(*entry, found, at, "manifest", detail+" "+name)
		}
	}
}

var (
	workerRole = regexp.MustCompile(`worker|job|queue|celery|sidekiq|consumer|clock|scheduler`)
	webRole    = regexp.MustCompile(`web|app|api|server|frontend|http`)
)

func role(name string, ports bool) string {
	name = strings.ToLower(name)
	switch {
	case workerRole.MatchString(name):
		return "worker"
	case ports || webRole.MatchString(name):
		return "web"
	}
	return ""
}

type wranglerConfig struct {
	Name     string `json:"name" toml:"name"`
	Routes   []any  `json:"routes" toml:"routes"`
	Route    any    `json:"route" toml:"route"`
	Triggers struct {
		Crons []string `json:"crons" toml:"crons"`
	} `json:"triggers" toml:"triggers"`
	KV []struct {
		Binding string `json:"binding" toml:"binding"`
	} `json:"kv_namespaces" toml:"kv_namespaces"`
	D1 []struct {
		Binding  string `json:"binding" toml:"binding"`
		Database string `json:"database_name" toml:"database_name"`
	} `json:"d1_databases" toml:"d1_databases"`
	R2 []struct {
		Binding string `json:"binding" toml:"binding"`
		Bucket  string `json:"bucket_name" toml:"bucket_name"`
	} `json:"r2_buckets" toml:"r2_buckets"`
	DurableObjects struct {
		Bindings []struct {
			Name  string `json:"name" toml:"name"`
			Class string `json:"class_name" toml:"class_name"`
		} `json:"bindings" toml:"bindings"`
	} `json:"durable_objects" toml:"durable_objects"`
	Queues struct {
		Producers []struct {
			Queue string `json:"queue" toml:"queue"`
		} `json:"producers" toml:"producers"`
		Consumers []struct {
			Queue string `json:"queue" toml:"queue"`
		} `json:"consumers" toml:"consumers"`
	} `json:"queues" toml:"queues"`
	Services []struct {
		Service string `json:"service" toml:"service"`
	} `json:"services" toml:"services"`
	Observability struct {
		Enabled bool `json:"enabled" toml:"enabled"`
	} `json:"observability" toml:"observability"`
}

var jsonComment = regexp.MustCompile(`(?m)("(?:[^"\\]|\\.)*")|//[^\n]*|/\*[\s\S]*?\*/|,(\s*[}\]])`)

// jsonc removes comments and trailing commas, leaving strings intact.
func jsonc(body []byte) []byte {
	return jsonComment.ReplaceAllFunc(body, func(match []byte) []byte {
		switch {
		case match[0] == '"':
			return match
		case match[0] == ',':
			return match[1:]
		}
		return nil
	})
}

func (run *inference) wrangler(repo, dir, base string, body []byte, at Location) {
	var config wranglerConfig
	var err error
	if strings.HasSuffix(base, ".toml") {
		_, err = toml.Decode(string(body), &config)
	} else {
		err = json.Unmarshal(jsonc(body), &config)
	}
	if err != nil || config.Name == "" {
		return
	}
	declared := static("wrangler", base, Declared)
	worker := unit{repo: repo, root: dir, key: "wrangler:" + config.Name, label: config.Name, runtime: "worker", deployed: true, role: "web",
		names: []string{config.Name}, evidence: declared, at: at}
	routes := config.Routes
	if config.Route != nil {
		routes = append(routes, config.Route)
	}
	here := owner{units: []string{worker.key}}
	for _, route := range routes {
		pattern := ""
		switch value := route.(type) {
		case string:
			pattern = value
		case map[string]any:
			pattern, _ = value["pattern"].(string)
		}
		if pattern == "" {
			continue
		}
		host := strings.SplitN(pattern, "/", 2)[0]
		worker.hosts = append(worker.hosts, host)
		run.find(finding{owner: here, kind: Ingress, key: "route:" + pattern, label: pattern, trigger: "request", edge: Handles,
			selectors: map[string]string{"server.address": strings.TrimSuffix(host, "*")}, evidence: declared, at: at})
	}
	run.addUnit(worker)
	for _, cron := range config.Triggers.Crons {
		run.find(finding{owner: here, kind: Ingress, key: "cron:" + cron, label: "cron " + cron, trigger: "timer", edge: Handles, evidence: declared, at: at})
	}
	store := func(key, label, system string) {
		run.find(finding{owner: here, kind: Store, key: key, label: label, edge: Uses,
			selectors: map[string]string{"db.system.name": system}, evidence: declared, at: at})
	}
	for _, binding := range config.DurableObjects.Bindings {
		store("do:"+binding.Class, binding.Class, "cloudflare.durable_object")
	}
	for _, binding := range config.KV {
		store("kv:"+binding.Binding, binding.Binding, "cloudflare.kv")
	}
	for _, binding := range config.D1 {
		store("d1:"+binding.Database, binding.Database, "cloudflare.d1")
	}
	for _, binding := range config.R2 {
		store("r2:"+binding.Bucket, binding.Bucket, "cloudflare.r2")
	}
	for _, producer := range config.Queues.Producers {
		run.find(finding{owner: here, kind: Queue, key: "queue:" + producer.Queue, label: producer.Queue, shared: true, edge: Publishes,
			selectors: map[string]string{"messaging.destination.name": producer.Queue}, evidence: declared, at: at})
	}
	for _, consumer := range config.Queues.Consumers {
		run.find(finding{owner: here, kind: Queue, key: "queue:" + consumer.Queue, label: consumer.Queue, shared: true, edge: Consumes,
			selectors: map[string]string{"messaging.destination.name": consumer.Queue}, evidence: declared, at: at})
	}
	for _, service := range config.Services {
		run.bindings = append(run.bindings, serviceBinding{from: worker.key, to: service.Service, at: at})
	}
}

type serviceBinding struct {
	from, to string
	at       Location
}

var composeInfrastructure = []struct {
	image  string
	kind   Kind
	system string
}{
	{"postgres", Store, "postgresql"}, {"postgis", Store, "postgresql"}, {"mysql", Store, "mysql"}, {"mariadb", Store, "mysql"},
	{"redis", Store, "redis"}, {"valkey", Store, "redis"}, {"memcached", Store, "memcached"}, {"mongo", Store, "mongodb"},
	{"elasticsearch", Store, "elasticsearch"}, {"opensearch", Store, "elasticsearch"}, {"minio", Store, "s3"},
	{"rabbitmq", Queue, "rabbitmq"}, {"kafka", Queue, "kafka"}, {"redpanda", Queue, "kafka"}, {"nats", Queue, "nats"},
}

func (run *inference) compose(repo, dir string, body []byte, at Location) {
	var file struct {
		Services map[string]struct {
			Image     string `yaml:"image"`
			Build     any    `yaml:"build"`
			Ports     []any  `yaml:"ports"`
			DependsOn any    `yaml:"depends_on"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(body, &file) != nil {
		return
	}
	declared := static("compose", path.Base(at.Path), Declared)
	names := keys(file.Services)
	built := []string{}
	dependents := map[string][]string{}
	for _, name := range names {
		service := file.Services[name]
		var dependsOn []string
		switch value := service.DependsOn.(type) {
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok {
					dependsOn = append(dependsOn, text)
				}
			}
		case map[string]any:
			dependsOn = keys(value)
		}
		context := ""
		switch value := service.Build.(type) {
		case string:
			context = value
		case map[string]any:
			context, _ = value["context"].(string)
			if context == "" {
				context = "."
			}
		}
		if context == "" {
			continue
		}
		key := "compose:" + name
		built = append(built, key)
		for _, dependency := range dependsOn {
			dependents[dependency] = append(dependents[dependency], key)
		}
		run.addUnit(unit{repo: repo, root: path.Join(dir, context), key: key, label: name, runtime: "process", deployed: true,
			role: role(name, len(service.Ports) > 0), names: []string{name}, evidence: declared, at: at})
		for _, port := range service.Ports {
			text := strings.Split(strings.TrimSpace(toString(port)), "/")[0]
			parts := strings.Split(text, ":")
			container := parts[len(parts)-1]
			run.find(finding{owner: owner{units: []string{key}}, kind: Ingress, key: "listen:tcp:" + container, label: "tcp listener :" + container,
				trigger: "request", edge: Handles, selectors: map[string]string{"network.transport": "tcp", "server.port": container}, evidence: declared, at: at})
		}
	}
	for _, name := range names {
		image := strings.ToLower(file.Services[name].Image)
		if file.Services[name].Build != nil || image == "" {
			continue
		}
		image = path.Base(strings.SplitN(image, ":", 2)[0])
		for _, infrastructure := range composeInfrastructure {
			if !strings.Contains(image, infrastructure.image) {
				continue
			}
			users := dependents[name]
			if len(users) == 0 {
				users = built
			}
			entry := client(infrastructure.image, infrastructure.kind, infrastructure.system)
			entry.shared = true
			run.client(entry, owner{units: users}, at, "manifest", "compose service "+name)
			break
		}
	}
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case map[string]any:
		if target, ok := typed["target"]; ok {
			return toString(target)
		}
	}
	return ""
}

var serverFrameworks = []string{"express", "fastify", "koa", "hono", "@hono/node-server", "@nestjs/core", "restify"}

func (run *inference) packageJSON(repo, dir string, body []byte, at Location, names []string) {
	var manifest struct {
		Name                 string            `json:"name"`
		Main                 string            `json:"main"`
		Bin                  any               `json:"bin"`
		Scripts              map[string]string `json:"scripts"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if json.Unmarshal(body, &manifest) != nil {
		return
	}
	deps := map[string]bool{}
	for _, group := range []map[string]string{manifest.Dependencies, manifest.PeerDependencies, manifest.OptionalDependencies} {
		for name := range group {
			deps[name] = true
		}
	}
	run.packages = append(run.packages, jsPackage{repo: repo, dir: dir, name: manifest.Name, deps: deps})
	label := manifest.Name
	if label == "" {
		label = path.Base(dir)
	}
	declared := static("package.json", "dependencies", Declared)
	worker := false
	for _, other := range names {
		worker = worker || path.Dir(other) == dir && strings.HasPrefix(path.Base(other), "wrangler.")
	}
	switch {
	case worker:
		// The wrangler declaration is the unit; its package supplies dependencies.
		run.unitDeps = append(run.unitDeps, unitDeps{repo: repo, root: dir, deps: deps})
	case deps["next"]:
		server := unit{repo: repo, root: dir, key: "next:" + label + ":server", label: label, runtime: "server", deployed: true, role: "web",
			names: []string{label}, deps: deps, evidence: static("nextjs", "next dependency", Declared), at: at}
		browser := server
		browser.key, browser.label, browser.runtime, browser.role = "next:"+label+":browser", label+" (browser)", "browser", ""
		browser.names = []string{label + "-browser"}
		run.addUnit(server)
		run.addUnit(browser)
	case manifest.Bin != nil:
		run.addUnit(unit{repo: repo, root: dir, key: "node:" + label, label: label, runtime: "cli", names: []string{label}, deps: deps, evidence: declared, at: at})
	default:
		framework := ""
		for _, candidate := range serverFrameworks {
			if deps[candidate] && framework == "" {
				framework = candidate
			}
		}
		switch {
		case framework != "":
			run.addUnit(unit{repo: repo, root: dir, key: "node:" + label, label: label, runtime: "process", role: "web",
				names: []string{label}, deps: deps, evidence: static("package.json", framework+" dependency", Inferred), at: at})
		case manifest.Scripts["start"] != "":
			// A package that starts as a process is one, framework or not (a queue worker).
			run.addUnit(unit{repo: repo, root: dir, key: "node:" + label, label: label, runtime: "process", role: role(label, false),
				names: []string{label}, deps: deps, evidence: static("package.json", "start script", Inferred), at: at})
		}
	}
	// A process's entry files bound what of its package it runs.
	var entries []string
	switch bin := manifest.Bin.(type) {
	case string:
		entries = append(entries, bin)
	case map[string]any:
		for _, name := range keys(bin) {
			entries = append(entries, toString(bin[name]))
		}
	}
	if start := strings.Fields(manifest.Scripts["start"]); len(start) > 1 && contains([]string{"node", "tsx", "ts-node", "bun"}, start[0]) {
		entries = append(entries, start[len(start)-1])
	}
	if manifest.Main != "" {
		entries = append(entries, manifest.Main)
	}
	for _, held := range run.units {
		if held.repo == repo && held.key == "node:"+label {
			for _, entry := range entries {
				held.entries = append(held.entries, path.Join(dir, entry))
			}
		}
	}
	run.manifest(keys(deps), owner{repo: repo, file: at.Path}, at, "package.json")
}

type unitDeps struct {
	repo, root string
	deps       map[string]bool
}

func keys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
