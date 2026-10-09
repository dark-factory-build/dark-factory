package opgraph

import (
	"fmt"
	"testing"
	"time"
)

const minute = int64(time.Minute / time.Millisecond)

// A small graph: one worker unit with two routes and a cron, one external.
func overlayGraph() Graph {
	builder := NewBuilder("s")
	unit := builder.Node(Processor, "", "wrangler:api", "api").Select("service.name", "api").Add(static("wrangler", "", Declared), nil)
	add := func(key, label, method, route string) *Node {
		node := builder.Node(Ingress, unit.ID, key, label).Select("http.request.method", method).Select("http.route", route).Add(static("pattern", "", Inferred), nil)
		node.Trigger = "request"
		builder.Edge(node.ID, unit.ID, Handles, static("pattern", "", Inferred))
		return node
	}
	add("http:GET /users/{id}", "users", "GET", "/users/{id}")
	add("http:GET /users/me", "me", "GET", "/users/me")
	add("http:POST /orders", "orders", "POST", "/orders")
	cron := builder.Node(Ingress, unit.ID, "cron:daily", "cron").Add(static("wrangler", "", Declared), nil)
	cron.Trigger = "timer"
	cron.Select("code.function.name", "scheduled")
	external := builder.Node(External, "", "host:api.stripe.com", "api.stripe.com").Select("server.address", "api.stripe.com").Add(static("go-ast", "", Inferred), nil)
	builder.Edge(unit.ID, external.ID, Calls, static("go-ast", "", Inferred))
	graph, err := builder.Graph()
	if err != nil {
		panic(err)
	}
	return graph
}

func labels(live Live) map[string]*Status {
	result := map[string]*Status{}
	for _, node := range live.Graph.Nodes {
		result[node.Label] = live.Nodes[node.ID]
	}
	return result
}

func server(attributes map[string]string, count, errors uint64, at int64) Observation {
	attributes["service.name"] = "api"
	return Observation{Source: "test", Environment: "production", Kind: "server", Start: at - minute, End: at, Attributes: attributes, Count: count, Errors: errors}
}

func invariant(t *testing.T, live Live) {
	t.Helper()
	for id, status := range live.Nodes {
		if status.State == "idle" && status.Observation != "quiet" {
			t.Fatalf("%s is idle while %s", id, status.Observation)
		}
		if status.State != "unknown" && status.Observation != "quiet" && status.count == 0 {
			t.Fatalf("%s claims %s with no observations", id, status.State)
		}
	}
}

func TestNoTelemetryIsUnknownNeverIdle(t *testing.T) {
	now := 100 * minute
	live := Overlay("s", overlayGraph(), nil, nil, nil, now, 15*minute)
	invariant(t, live)
	got := labels(live)
	for _, label := range []string{"api", "users", "me", "orders", "cron"} {
		if got[label].Observation != "unobserved" || got[label].State != "unknown" {
			t.Errorf("%s = %+v", label, got[label])
		}
	}
	if got["api.stripe.com"].Observation != "opaque" {
		t.Errorf("external = %+v", got["api.stripe.com"])
	}
	if live.Summary.Unobserved != 5 || live.Summary.Opaque != 1 || live.Summary.Inferred != 6 {
		t.Errorf("summary = %+v", live.Summary)
	}
}

func TestUnitGranularSourceNeverMakesRoutesIdle(t *testing.T) {
	now := 100 * minute
	coverage := []Coverage{{Source: "cloudflare", Unit: "dark-api-script", Keys: []string{"service.name"}, AsOf: now, TTL: 10 * minute}}
	observations := []Observation{{Source: "cloudflare", Kind: "server", Start: now - 2*minute, End: now - minute, Attributes: map[string]string{"service.name": "dark-api-script"}, Count: 50}}
	live := Overlay("s", overlayGraph(), observations, coverage, map[string]string{"dark-api-script": "api"}, now, 15*minute)
	invariant(t, live)
	got := labels(live)
	if got["api"].Observation != "partial" || got["api"].State != "active" {
		t.Errorf("unit = %+v, want partial and active", got["api"])
	}
	for _, label := range []string{"users", "me", "orders"} {
		if got[label].Observation != "partial" || got[label].State != "unknown" {
			t.Errorf("%s = %+v, want partial and unknown", label, got[label])
		}
	}
}

func TestRouteGranularSourceDistinguishesQuietFromActive(t *testing.T) {
	now := 100 * minute
	coverage := []Coverage{{Source: "otlp", Unit: "api", Keys: []string{"service.name", "http.route", "http.request.method", "code.function.name"}, AsOf: now, TTL: 10 * minute}}
	observations := []Observation{
		server(map[string]string{"http.request.method": "GET", "url.path": "/users/42"}, 10, 0, now),
		server(map[string]string{"http.request.method": "GET", "url.path": "/users/me"}, 8, 1, now),
		server(map[string]string{"http.request.method": "HEAD", "http.route": "/users/:id"}, 2, 2, now),
	}
	live := Overlay("s", overlayGraph(), observations, coverage, nil, now, 15*minute)
	invariant(t, live)
	got := labels(live)
	if got["users"].Observation != "observed" || got["users"].State != "degraded" {
		t.Errorf("users = %+v (concrete path and HEAD bind to the template)", got["users"])
	}
	if got["me"].Observation != "observed" || got["me"].State != "degraded" {
		t.Errorf("me = %+v (static segments outrank parameters)", got["me"])
	}
	if got["orders"].Observation != "partial" || got["orders"].State != "unknown" {
		t.Errorf("never-seen orders = %+v: a route the source never matched may be a prefix the code hid, so it is never claimed idle", got["orders"])
	}
	// Seen earlier and silent now: that silence is a claim the source can make.
	earlier := server(map[string]string{"http.request.method": "POST", "http.route": "/orders"}, 1, 0, now-30*minute)
	again := labels(Overlay("s", overlayGraph(), append(observations, earlier), coverage, nil, now, 15*minute))
	if again["orders"].Observation != "quiet" || again["orders"].State != "idle" {
		t.Errorf("orders seen before = %+v, want quiet and idle", again["orders"])
	}
	if got["cron"].Observation != "partial" || got["cron"].State != "unknown" {
		t.Errorf("silent timer = %+v, must never read idle", got["cron"])
	}
	if got["api"].Observation != "partial" || got["api"].State != "degraded" {
		t.Errorf("unit = %+v (3 errors in 20)", got["api"])
	}
}

func TestStaleCoverage(t *testing.T) {
	now := 100 * minute
	coverage := []Coverage{{Source: "otlp", Unit: "api", Keys: []string{"service.name", "http.route", "http.request.method"}, AsOf: now - 30*minute, TTL: 5 * minute}}
	live := Overlay("s", overlayGraph(), nil, coverage, nil, now, 15*minute)
	invariant(t, live)
	if got := labels(live)["orders"]; got.Observation != "stale" || got.State != "unknown" {
		t.Errorf("orders = %+v", got)
	}
}

func TestUnmatchedActivityIsBoundedAndShown(t *testing.T) {
	now := 100 * minute
	var observations []Observation
	for index := 0; index < 50; index++ {
		observations = append(observations, server(map[string]string{"http.request.method": "GET", "url.path": fmt.Sprintf("/scan%d/x/%d", index, index)}, 1, 0, now))
	}
	observations = append(observations, server(map[string]string{"url.path": "/scan1/other"}, 1, 0, now))
	live := Overlay("s", overlayGraph(), observations, nil, nil, now, 15*minute)
	invariant(t, live)
	unknown := 0
	for _, node := range live.Graph.Nodes {
		if node.Kind == Unknown {
			unknown++
			if State(node.Evidence) != "runtime" {
				t.Errorf("%s has evidence state %s", node.Label, State(node.Evidence))
			}
		}
	}
	if unknown != maxUnknownPerUnit+1 || labels(live)["other unmapped activity"] == nil {
		t.Errorf("%d unknown nodes, want %d plus other", unknown, maxUnknownPerUnit)
	}
	if live.Summary.RuntimeOnly != unknown {
		t.Errorf("summary = %+v", live.Summary)
	}
}

func TestOutboundPeerAndContradiction(t *testing.T) {
	now := 100 * minute
	builder := NewBuilder("s")
	builder.Node(Processor, "", "a", "billing").Select("service.name", "billing").Add(static("x", "", Declared), nil)
	graph := overlayGraph()
	billing, _ := builder.Graph()
	graph.Nodes = append(graph.Nodes, billing.Nodes...)
	observations := []Observation{
		{Source: "otlp", Kind: "client", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "api"}, Peer: map[string]string{"server.address": "api.stripe.com"}, Count: 4, Errors: 3},
		{Source: "otlp", Kind: "client", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "api"}, Peer: map[string]string{"server.address": "hooks.slack.com"}, Count: 1},
		{Source: "otlp", Kind: "client", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "api"}, Peer: map[string]string{"process.executable.name": "billing"}, Count: 1},
		{Source: "otlp", Kind: "server", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "billing", "http.route": "/users/me", "http.request.method": "GET"}, Count: 1},
		{Source: "otlp", Kind: "server", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "billing", "http.route": "/orders", "http.request.method": "POST"}, Count: 1},
	}
	coverage := []Coverage{{Source: "otlp", Unit: "api", Keys: []string{"service.name"}, Peers: true, AsOf: now, TTL: minute}}
	live := Overlay("s", graph, observations, coverage, nil, now, 15*minute)
	invariant(t, live)
	got := labels(live)
	if got["api.stripe.com"].Observation != "opaque" || got["api.stripe.com"].State != "failing" {
		t.Errorf("stripe = %+v", got["api.stripe.com"])
	}
	if got["hooks.slack.com"] == nil || got["hooks.slack.com"].Observation != "observed" {
		t.Errorf("runtime-only party = %+v", got["hooks.slack.com"])
	}
	if got := live.Edges[[3]string{ID("s", "", "wrangler:api"), ID("s", "", "a"), string(Calls)}]; got == nil || got.Observation != "observed" {
		t.Errorf("launch of a unit's executable = %+v, want an observed call to that unit", got)
	}
	for _, node := range live.Graph.Nodes {
		if node.Label == "me" && State(node.Evidence) != "contradicted" {
			t.Errorf("/users/me served by another unit is %s", State(node.Evidence))
		}
		if node.Label == "billing" && node.Kind == External {
			t.Error("a launched unit was minted as an external party")
		}
		// A one-segment route proves nothing about who serves it.
		if node.Label == "orders" && State(node.Evidence) == "contradicted" {
			t.Error("a generic route was contradicted")
		}
	}
	if live.Summary.Contradicted != 1 {
		t.Errorf("summary = %+v", live.Summary)
	}
}

func TestRuntimeRecordBoundsAndDropsUnknownKeys(t *testing.T) {
	runtime := NewRuntime(time.Hour)
	for index := 0; index < 3; index++ {
		runtime.Record(Observation{Source: "s", Kind: "server", Start: 10 * minute, End: 10*minute + 5, Count: 1,
			Attributes: map[string]string{"service.name": "api", "http.route": "/x", "user.email": "secret@example.com"}})
	}
	observations, _ := runtime.Snapshot(11 * minute)
	if len(observations) != 1 || observations[0].Count != 3 || observations[0].Attributes["user.email"] != "" {
		t.Fatalf("observations = %+v", observations)
	}
	if observations, _ = runtime.Snapshot(200 * minute); len(observations) != 0 {
		t.Fatal("expired aggregates retained")
	}
}

func TestDeployIsAChangeoverNotTraffic(t *testing.T) {
	now := 100 * minute
	coverage := []Coverage{{Source: "otlp", Unit: "api", Keys: []string{"service.name", "http.route", "http.request.method"}, AsOf: now, TTL: minute}}
	deploy := Observation{Source: "factoryd", Kind: "deploy", Start: now - minute, End: now - 30_000, Attributes: map[string]string{"service.name": "api"}, Count: 1, Version: "v2"}
	live := Overlay("s", overlayGraph(), []Observation{deploy}, coverage, nil, now, 15*minute)
	invariant(t, live)
	got := labels(live)["api"]
	if got.DeployedAt != now-30_000 || got.Rate != 0 {
		t.Fatalf("unit = %+v", got)
	}
}

func TestListenersPeersAndAmbiguousNames(t *testing.T) {
	builder := NewBuilder("s")
	web := builder.Node(Processor, "", "compose:web", "web").Select("service.name", "web").Add(static("compose", "", Declared), nil)
	listener := builder.Node(Ingress, web.ID, "listen:tcp:8000", "listener").Select("network.transport", "tcp").Select("server.port", "8000").Add(static("compose", "", Declared), nil)
	route := builder.Node(Ingress, web.ID, "http:/orders/", "orders").Select("http.route", "/orders/").Add(static("pattern", "", Inferred), nil)
	api := builder.Node(Processor, "", "wrangler:api", "api").Select("service.name", "api").Add(static("wrangler", "", Declared), nil)
	host := builder.Node(Ingress, api.ID, "route:b.dev/*", "b.dev").Select("server.address", "b.dev").Add(static("wrangler", "", Declared), nil)
	for _, id := range []string{"one", "two"} {
		builder.Node(Processor, "", "dup:"+id, id).Select("service.name", "shared").Add(static("x", "", Declared), nil)
	}
	graph, _ := builder.Graph()
	now := 100 * minute
	observations := []Observation{
		{Source: "otlp", Kind: "server", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "web", "http.route": "/orders/", "server.port": "8000"}, Count: 50},
		{Source: "otlp", Kind: "client", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "web"}, Peer: map[string]string{"server.address": "b.dev"}, Count: 5},
		{Source: "otlp", Kind: "server", Start: now - minute, End: now, Attributes: map[string]string{"service.name": "shared", "http.route": "/x/y"}, Count: 5},
	}
	coverage := []Coverage{{Source: "otlp", Unit: "web", Keys: []string{"service.name", "http.route", "server.port", "network.transport"}, Peers: true, AsOf: now, TTL: minute}}
	live := Overlay("s", graph, observations, coverage, nil, now, 15*minute)
	invariant(t, live)
	if got := live.Nodes[route.ID]; got.Observation != "observed" || got.State != "active" {
		t.Errorf("route = %+v: a listener absorbed its spans", got)
	}
	if live.Nodes[listener.ID].State == "active" {
		t.Error("the listener took the route's traffic")
	}
	if got := live.Edges[[3]string{web.ID, host.ID, string(Calls)}]; got == nil || got.Observation != "observed" {
		t.Errorf("call to another unit's host = %+v, want an observed edge to its ingress", got)
	}
	for _, node := range live.Graph.Nodes {
		if node.Label == "b.dev" && node.Kind == External {
			t.Error("a unit in this system was minted as an external party")
		}
		if node.Kind == Unknown || State(node.Evidence) == "runtime" && node.Kind == Processor {
			t.Errorf("ambiguous service name bound to %+v", node)
		}
	}
}

func TestMethodsOnASocketThatNamesNoneAreItsWork(t *testing.T) {
	builder := NewBuilder("s")
	daemon := builder.Node(Processor, "", "go:d", "daemon").Select("service.name", "daemon").Add(static("go-ast", "", Declared), nil)
	socket := builder.Node(Ingress, daemon.ID, "listen:unix", "unix socket").Select("network.transport", "unix").Add(static("go-ast", "", Declared), nil)
	graph, _ := builder.Graph()
	now := 100 * minute
	observations := []Observation{{Source: "factoryd", Kind: "server", Start: now - minute, End: now, Count: 9,
		Attributes: map[string]string{"service.name": "daemon", "network.transport": "unix", "rpc.method": "snapshot"}}}
	coverage := []Coverage{{Source: "factoryd", Unit: "daemon", Keys: []string{"service.name", "network.transport", "rpc.method"}, AsOf: now, TTL: minute}}
	live := Overlay("s", graph, observations, coverage, nil, now, 15*minute)
	invariant(t, live)
	if got := live.Nodes[socket.ID]; got.Observation != "observed" || got.State != "active" {
		t.Fatalf("socket = %+v", got)
	}
	if live.Summary.RuntimeOnly != 0 {
		t.Fatalf("socket methods were quarantined: %+v", live.Summary)
	}
}

func TestOnlyMethodsFallToABareListener(t *testing.T) {
	builder := NewBuilder("s")
	unit := builder.Node(Processor, "", "go:u", "u").Select("service.name", "u").Add(static("go-ast", "", Declared), nil)
	listener := builder.Node(Ingress, unit.ID, "listen:tcp", "tcp listener").Select("network.transport", "tcp").Add(static("go-ast", "", Declared), nil)
	graph, _ := builder.Graph()
	now := 100 * minute
	var observations []Observation
	for _, attributes := range []map[string]string{
		{"service.name": "u", "messaging.destination.name": "unknown-queue"},
		{"service.name": "u", "code.function.name": "pkg.Unmatched"},
		{"service.name": "u", "url.path": "/unmatched"},
	} {
		observations = append(observations, Observation{Source: "otlp", Kind: "server", Start: now - minute, End: now, Count: 3, Attributes: attributes})
	}
	live := Overlay("s", graph, observations, nil, nil, now, 15*minute)
	invariant(t, live)
	if live.Summary.RuntimeOnly != 3 || live.Nodes[listener.ID].State != "unknown" {
		t.Fatalf("unmatched work was explained away by the listener: %+v, listener %+v", live.Summary, live.Nodes[listener.ID])
	}
}

func TestSilentOutsideHostIsQuietUnlessNotConnected(t *testing.T) {
	now := 100 * minute
	cover := func(peers bool, keys ...string) []Coverage {
		return []Coverage{{Source: "factoryd", Unit: "api", Keys: keys, Peers: peers, AsOf: now, TTL: minute}}
	}
	edge := [3]string{ID("s", "", "wrangler:api"), ID("s", "", "host:api.stripe.com"), string(Calls)}
	graph := overlayGraph()
	storeID, unitID := ID("s", "", "store:db"), ID("s", "", "wrangler:api")
	graph.Nodes = append(graph.Nodes, Node{ID: storeID, Kind: Store, Label: "db", Evidence: []Evidence{static("go-ast", "", Inferred)}})
	graph.Edges = append(graph.Edges, Edge{From: unitID, To: storeID, Kind: Uses, Evidence: []Evidence{static("go-ast", "", Inferred)}})
	live := Overlay("s", graph, nil, cover(true, "service.name", "server.address"), nil, now, 15*minute)
	invariant(t, live)
	// Peer reporting sees outbound calls only: an unseen store is not a silent one.
	if got := live.Edges[[3]string{unitID, storeID, string(Uses)}]; got == nil || got.Observation != "unobserved" {
		t.Errorf("uses edge to a store under a peer-reporting caller = %+v", got)
	}
	if got := labels(live)["api.stripe.com"]; got.Observation != "quiet" || got.State != "idle" || live.Edges[edge].Observation != "quiet" {
		t.Errorf("host the only caller reports on = %+v, edge %+v", got, live.Edges[edge])
	}
	live.NotConnected("api.stripe.com")
	if got := labels(live)["api.stripe.com"]; got.Observation != "unobserved" || got.State != "unknown" || live.Edges[edge].Observation != "unobserved" || live.Summary.Quiet != 0 || live.Summary.Unobserved == 0 {
		t.Errorf("unconnected host = %+v, edge %+v, summary %+v", got, live.Edges[edge], live.Summary)
	}
	// A caller listing server.address without reporting peers (an OTLP server's own address) leaves the host opaque.
	if got := labels(Overlay("s", overlayGraph(), nil, cover(false, "service.name", "server.address"), nil, now, 15*minute))["api.stripe.com"]; got.Observation != "opaque" {
		t.Errorf("host with an unreporting caller = %+v", got)
	}
}
