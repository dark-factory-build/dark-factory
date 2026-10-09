package opgraph

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixture repositories cover each extractor; every test infers them together
// so cross-repository linking is exercised by the same graph.
func fixture() []Repository {
	return []Repository{
		{ID: "core", Name: "core", Files: map[string][]byte{
			"go.mod": []byte("module example.com/core\n"),
			"cmd/server/main.go": []byte(`package main
import ("net"; "net/http"; "os/exec"; "time"; "example.com/core/internal/api")
const ListenAddress = "127.0.0.1:9000"
func main() {
	listener, _ := net.Listen("tcp", address())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", nil)
	http.Serve(listener, mux)
	exec.Command("/usr/bin/git", "status")
	go loop()
	api.Call()
}
func address() string { return "" }
func loop() { for range time.NewTicker(time.Second).C {} }
func unused() { exec.Command("never-called") }
`),
			"cmd/tool/main.go": []byte(`package main
import ("example.com/core/internal/api"; "example.com/core/internal/broker")
func main() { api.Call(); client := broker.New(); client.Send(nil) }
`),
			"internal/broker/broker.go": []byte(`package broker
import "net/http"
const Origin = "https://broker.payments.io"
type Client struct{ origin, docs string }
func New() *Client { return &Client{origin: Origin, docs: "https://docs.unread.io"} }
func (c *Client) Send(ctx context.Context) { http.NewRequestWithContext(ctx, "POST", c.origin+"/v1/send", nil) }
`),
			"internal/api/api.go": []byte(`package api
import ("net/http"; _ "github.com/jackc/pgx/v5")
const relayURL = "wss://relay.example.net/host"
func Call() { http.Get("https://api.payments.io/v1/charge") }
func Listen() { http.HandleFunc("/only-if-called", nil) }
`),
			"internal/api/api_test.go": []byte(`package api
func TestX() { http.Get("https://test-only.io/") }
`),
			"worker/wrangler.jsonc": []byte(`{
	// comment, with a "quoted" word
	"name": "edge-worker",
	"routes": [{ "pattern": "edge.example.dev", "custom_domain": true }],
	"triggers": { "crons": ["*/5 * * * *"] },
	"durable_objects": { "bindings": [{ "name": "ROOM", "class_name": "Room" }] },
	"queues": { "producers": [{ "binding": "Q", "queue": "jobs" }] },
}`),
			"worker/src/index.ts":    []byte(`export default { fetch() { return fetch("https://api.payments.io/v2") } }`),
			"consumer/wrangler.toml": []byte("name = \"consumer\"\n[[queues.consumers]]\nqueue = \"jobs\"\n"),
		}},
		{ID: "site", Name: "site", Files: map[string][]byte{
			"package.json":                   []byte(`{"name":"site","dependencies":{"next":"15","@core/client":"1","@vercel/postgres":"1"}}`),
			"app/page.tsx":                   []byte(`export default function Page() {}`),
			"app/(marketing)/about/page.tsx": []byte(`export default function About() {}`),
			"app/blog/[slug]/page.tsx":       []byte(`export default function Post() {}`),
			"app/api/items/route.ts":         []byte("export async function GET() {}\nexport const POST = () => {}\n"),
			"app/_private/page.tsx":          []byte(`export default function Hidden() {}`),
			"lib/data.ts":                    []byte(`const ENDPOINT = "https://edge.example.dev/socket"` + "\n" + `// fetch("https://commented.io")` + "\n" + `const DOCS = "https://docs.only-a-link.io"`),
		}},
		{ID: "client", Name: "client", Files: map[string][]byte{
			"package.json":  []byte(`{"name":"@core/client"}`),
			"src/socket.ts": []byte("export const connect = () => new WebSocket(`ws://127.0.0.1:9000/stream`)\n"),
		}},
		{ID: "django", Name: "shop", Files: map[string][]byte{
			"manage.py":          []byte("import django"),
			"requirements.txt":   []byte("Django==5\ncelery>=5\npsycopg2-binary\n"),
			"shop/urls.py":       []byte(`urlpatterns = [path("orders/<int:pk>/", view), path("health", view)]`),
			"shop/tasks.py":      []byte("@shared_task\ndef send_receipt(order):\n    pass\n"),
			"docker-compose.yml": []byte("services:\n  web:\n    build: .\n    ports: [\"8000:8000\"]\n    depends_on: [db]\n  worker:\n    build: .\n  db:\n    image: postgres:16\n"),
		}},
		{ID: "rails", Name: "rails", Files: map[string][]byte{
			"config.ru":        []byte("run Rails.application"),
			"Gemfile":          []byte("gem 'rails'\ngem \"pg\"\ngem 'sidekiq'\n"),
			"config/routes.rb": []byte("Rails.application.routes.draw do\n  get '/status', to: 'status#show'\n  resources :orders\nend\n"),
		}},
		{ID: "docs", Name: "docs-only", Files: map[string][]byte{"tool.sh": []byte("echo")}},
	}
}

func infer(t *testing.T) (Graph, map[string]Node, map[string][]Node) {
	t.Helper()
	graph, err := Infer("system", fixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Node{}
	byLabel := map[string][]Node{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
		byLabel[node.Label] = append(byLabel[node.Label], node)
	}
	return graph, byID, byLabel
}

func one(t *testing.T, byLabel map[string][]Node, label string) Node {
	t.Helper()
	if len(byLabel[label]) != 1 {
		t.Fatalf("%q: %d nodes, want 1", label, len(byLabel[label]))
	}
	return byLabel[label][0]
}

func edge(graph Graph, byID map[string]Node, from, kind, to string) bool {
	for _, item := range graph.Edges {
		if byID[item.From].Label == from && string(item.Kind) == kind && byID[item.To].Label == to {
			return true
		}
	}
	return false
}

func TestInferGo(t *testing.T) {
	graph, byID, byLabel := infer(t)
	server := one(t, byLabel, "server")
	if server.Kind != Processor || server.Runtime != "process" || server.Selectors["service.name"] != "server" {
		t.Fatalf("server unit = %+v", server)
	}
	if tool := one(t, byLabel, "tool"); tool.Runtime != "cli" {
		t.Fatalf("tool runtime = %q, want cli", tool.Runtime)
	}
	listener := one(t, byLabel, "tcp listener (main) 127.0.0.1:9000")
	if listener.Unit != server.ID || listener.Selectors["server.port"] != "9000" || State(listener.Evidence) != "static" {
		t.Fatalf("listener = %+v", listener)
	}
	route := one(t, byLabel, "GET /items/{id}")
	if route.Selectors["http.route"] != "/items/{id}" || route.Selectors["http.request.method"] != "GET" {
		t.Fatalf("route = %+v", route)
	}
	for _, want := range [][3]string{
		{"GET /items/{id}", "handles", "server"},
		{"server", "calls", "git"},
		{"server", "runs", "loop"},
		{"server", "calls", "api.payments.io"},
		{"tool", "calls", "api.payments.io"},
		{"tool", "calls", "broker.payments.io"}, // a URL constant stored in a field a request reads
		{"server", "uses", "postgresql"},
	} {
		if !edge(graph, byID, want[0], want[1], want[2]) {
			t.Errorf("missing edge %v", want)
		}
	}
	for _, absent := range []string{"never-called", "test-only.io", "/only-if-called", "docs.unread.io"} {
		if len(byLabel[absent]) != 0 {
			t.Errorf("%q came from code no binary calls", absent)
		}
	}
	if len(byLabel["relay.example.net"]) != 0 {
		t.Error("a reserved host became a party")
	}
}

func TestInferDeclarations(t *testing.T) {
	graph, byID, byLabel := infer(t)
	worker := one(t, byLabel, "edge-worker")
	if worker.Runtime != "worker" {
		t.Fatalf("worker runtime = %q", worker.Runtime)
	}
	cron := one(t, byLabel, "cron */5 * * * *")
	if cron.Trigger != "timer" || cron.Unit != worker.ID {
		t.Fatalf("cron = %+v", cron)
	}
	jobs := one(t, byLabel, "jobs")
	if jobs.Kind != Queue || jobs.Unit != "" {
		t.Fatalf("queue = %+v", jobs)
	}
	for _, want := range [][3]string{
		{"edge-worker", "publishes", "jobs"},
		{"jobs", "consumes", "consumer"},
		{"edge-worker", "uses", "Room"},
		{"edge.example.dev", "handles", "edge-worker"},
	} {
		if !edge(graph, byID, want[0], want[1], want[2]) {
			t.Errorf("missing edge %v", want)
		}
	}
	// Django: compose units win over the manage.py guess at the same root.
	web, worker2 := one(t, byLabel, "web"), one(t, byLabel, "worker")
	if len(byLabel["shop"]) != 0 {
		t.Error("manage.py unit was not shadowed by compose")
	}
	orders := one(t, byLabel, "/orders/<int:pk>/")
	if orders.Unit != web.ID {
		t.Errorf("route belongs to %q, want web", byID[orders.Unit].Label)
	}
	if task := one(t, byLabel, "send_receipt"); task.Unit != worker2.ID || task.Kind != Job {
		t.Errorf("celery task = %+v", task)
	}
	if !edge(graph, byID, "web", "uses", "celery") || !edge(graph, byID, "web", "uses", "postgresql") {
		t.Error("Django manifests did not reach the web unit")
	}
	// Rails.
	rails := one(t, byLabel, "rails")
	if status := one(t, byLabel, "GET /status"); status.Unit != rails.ID {
		t.Error("rails route not owned by its rack unit")
	}
	one(t, byLabel, "GET /orders/:id")
	one(t, byLabel, "PATCH /orders/:id")
	if !edge(graph, byID, "rails", "uses", "sidekiq") {
		t.Error("sidekiq queue missing")
	}
	// Nothing recognised still yields a unit with an honest unknown ingress.
	fallback := one(t, byLabel, "docs-only")
	if unknown := one(t, byLabel, "No recognised entry points"); unknown.Unit != fallback.ID || unknown.Kind != Unknown {
		t.Errorf("fallback = %+v", unknown)
	}
}

func TestInferNextAndCrossRepository(t *testing.T) {
	graph, byID, byLabel := infer(t)
	server, browser := one(t, byLabel, "site"), one(t, byLabel, "site (browser)")
	if server.Runtime != "server" || browser.Runtime != "browser" {
		t.Fatal("next app is not a server and a browser unit")
	}
	var routes []string
	for _, node := range graph.Nodes {
		if node.Unit == server.ID && node.Kind == Ingress {
			routes = append(routes, node.Label)
		}
	}
	sort.Strings(routes)
	if want := []string{"GET /", "GET /about", "GET /api/items", "GET /blog/[slug]", "POST /api/items"}; !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %v, want %v", routes, want)
	}
	// A library's websocket address resolves to the declared listener of a
	// unit in another repository, through the dependent's browser unit.
	if !edge(graph, byID, "site (browser)", "calls", "tcp listener (main) 127.0.0.1:9000") {
		t.Error("loopback address did not link to the declaring listener")
	}
	// A constant's name links only to a declared unit, never invents a party.
	if !edge(graph, byID, "site", "calls", "edge.example.dev") {
		t.Error("declared host did not link")
	}
	for _, absent := range []string{"docs.only-a-link.io", "commented.io"} {
		if len(byLabel[absent]) != 0 {
			t.Errorf("%q became a party", absent)
		}
	}
	if !edge(graph, byID, "site", "uses", "postgresql") {
		t.Error("manifest store missing")
	}
}

func TestIdentityIsStable(t *testing.T) {
	first, _, _ := infer(t)
	repositories := fixture()
	for left, right := 0, len(repositories)-1; left < right; left, right = left+1, right-1 {
		repositories[left], repositories[right] = repositories[right], repositories[left]
	}
	repositories[0].Files["unrelated.py"] = []byte("x = 1")
	second, err := Infer("system", repositories, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(graph Graph) []string {
		var result []string
		for _, node := range graph.Nodes {
			result = append(result, node.ID+node.Label)
		}
		return result
	}
	if !reflect.DeepEqual(ids(first), ids(second)) {
		t.Fatal("order or unrelated files changed identities")
	}
	if ID("s", "u", "k") == ID("s", "uk", "") || ID("s", "u", "k") == ID("t", "u", "k") {
		t.Fatal("ambiguous identity")
	}
}

func TestNormaliseRoute(t *testing.T) {
	for _, item := range [][2]string{
		{"/users/{id}", "/users/{}"}, {"/users/:id", "/users/{}"}, {"/users/[id]", "/users/{}"}, {"/users/<int:pk>/", "/users/{}"},
		{"/docs/[...slug]", "/docs/{*}"}, {"/docs/[[...slug]]", "/docs/{*}"}, {"/files/{path...}", "/files/{*}"}, {"/static/*", "/static/{*}"},
		{"/orders(.:format)", "/orders"}, {"orders/", "/orders"}, {"/", "/"},
	} {
		if got := normaliseRoute(item[0]); got != item[1] {
			t.Errorf("normaliseRoute(%q) = %q, want %q", item[0], got, item[1])
		}
	}
	if strings.Contains(normaliseRoute("/a/{b}"), "b") {
		t.Fatal("parameter name survived")
	}
}

func TestInferenceSettlesOnOneAnswer(t *testing.T) {
	repositories := []Repository{
		{ID: "b", Name: "b", Files: map[string][]byte{
			"wrangler.toml": []byte("name = \"b\"\nroute = \"b.dev/*\"\n"),
			"package.json":  []byte(`{"name":"b","dependencies":{"hono":"4"}}`),
			"src/index.ts":  []byte("app.get('/items', list)\napp.post('/items', add)\n"),
		}},
		{ID: "a", Name: "a", Files: map[string][]byte{
			"package.json": []byte(`{"name":"a","dependencies":{"next":"15"}}`),
			"app/page.tsx": []byte("'use client'\nexport default function Page() { api.get('/orders'); fetch('https://b.dev/items') }\n"),
		}},
	}
	var first []Edge
	for attempt := 0; attempt < 30; attempt++ {
		graph, err := Infer("s", repositories, nil)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]Node{}
		for _, node := range graph.Nodes {
			byID[node.ID] = node
			if node.Label == "GET /orders" {
				t.Fatal("a browser call became an entry point")
			}
		}
		if attempt == 0 {
			first = graph.Edges
			found := false
			for _, edge := range graph.Edges {
				found = found || byID[edge.To].Label == "GET /items" && edge.Kind == Calls
			}
			if !found {
				t.Fatal("the call did not land on the read route")
			}
		} else if !reflect.DeepEqual(first, graph.Edges) {
			t.Fatal("inference changed between identical runs")
		}
	}
}

func TestCIJoinsUnitsAndNamesItsRepository(t *testing.T) {
	graph, err := Infer("s", []Repository{
		{ID: "r-core", Name: "org/core", Files: map[string][]byte{
			"go.mod":                  []byte("module example.com/core\n"),
			"cmd/gate/main.go":        []byte("package main\nfunc main() {}\n"),
			".github/workflows/a.yml": []byte("on: pull_request\njobs:\n  gate:\n    steps:\n      - run: go run ./cmd/gate\n"),
		}},
		{ID: "r-site", Name: "org/site", Files: map[string][]byte{
			"wrangler.toml":           []byte("name = \"api\"\n"),
			".github/workflows/b.yml": []byte("on: push\njobs:\n  t:\n    steps:\n      - run: echo\n"),
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Node{}
	labels := map[string]bool{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
		labels[node.Label] = true
	}
	if !labels["GitHub Actions \u00b7 core"] || !labels["GitHub Actions \u00b7 site"] {
		t.Fatalf("CI units are not named after their repository: %v", labels)
	}
	for _, edge := range graph.Edges {
		if byID[edge.From].Kind == Job && edge.Kind == Calls && strings.Contains(byID[edge.To].Label, "gate") {
			return
		}
	}
	t.Fatal("the job running ./cmd/gate has no edge to the gate unit")
}

// A host the platform reports a unit serves (a Worker's custom domain) is
// that unit's: a call to it, read from code or seen at runtime, reaches the
// unit instead of an outside party.
func TestPlatformHostReachesItsUnit(t *testing.T) {
	repositories := []Repository{
		{ID: "w", Name: "w", Files: map[string][]byte{"wrangler.toml": []byte("name = \"gate\"\n")}},
		{ID: "c", Name: "c", Files: map[string][]byte{
			"go.mod":        []byte("module example.com/c\n"),
			"cmd/c/main.go": []byte("package main\nimport \"net/http\"\nfunc main() { http.Get(\"https://gate.darkfactory.build/x\") }\n"),
		}},
	}
	outside := func(graph Graph) bool {
		for _, node := range graph.Nodes {
			if node.Kind == External && node.Label == "gate.darkfactory.build" {
				return true
			}
		}
		return false
	}
	before, err := Infer("s", repositories, nil)
	if err != nil || !outside(before) {
		t.Fatalf("without the platform host the call is outside: %v", err)
	}
	graph, err := Infer("s", repositories, map[string][]string{"gate": {"gate.darkfactory.build"}})
	if err != nil || outside(graph) {
		t.Fatalf("the platform host is still an outside party: %v", err)
	}
	ingress := ""
	for _, node := range graph.Nodes {
		if node.Kind == Ingress && node.Selectors["server.address"] == "gate.darkfactory.build" {
			ingress = node.ID
		}
	}
	found := false
	for _, edge := range graph.Edges {
		found = found || edge.To == ingress && edge.Kind == Calls
	}
	if ingress == "" || !found {
		t.Fatalf("the call does not reach the unit's host: %+v", graph.Edges)
	}
	live := Overlay("s", graph, []Observation{{Source: "otlp", Kind: "client", Start: 0, End: 1, Count: 1,
		Attributes: map[string]string{"service.name": "c"}, Peer: map[string]string{"server.address": "gate.darkfactory.build"}}}, nil, nil, 1, 60_000)
	seen := false
	for key, status := range live.Edges {
		seen = seen || key[1] == ingress && status.Observation == Observed
	}
	if outside(live.Graph) || !seen {
		t.Fatal("a runtime call to the platform host did not reach the unit")
	}
}
