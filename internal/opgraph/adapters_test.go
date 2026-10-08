package opgraph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const otlpExport = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},
 "scopeSpans":[{"spans":[
  {"kind":2,"startTimeUnixNano":"1000000","endTimeUnixNano":"41000000","attributes":[{"key":"http.method","value":{"stringValue":"GET"}},{"key":"http.route","value":{"stringValue":"/users/{id}"}},{"key":"user.email","value":{"stringValue":"secret@example.com"}}],"status":{"code":2}},
  {"kind":3,"attributes":[{"key":"net.peer.name","value":{"stringValue":"api.stripe.com"}},{"key":"server.port","value":{"intValue":"443"}}]}
 ]}]}]}`

func TestOTLPExportBecomesObservationsAndKeyCoverage(t *testing.T) {
	observations, coverage, err := DecodeOTLP([]byte(otlpExport), 10*minute, false)
	if err != nil || len(observations) != 2 || len(coverage) != 1 {
		t.Fatalf("observations=%+v coverage=%+v err=%v", observations, coverage, err)
	}
	server := observations[0]
	if server.Kind != "server" || server.Attributes["http.request.method"] != "GET" || server.Errors != 1 || server.LatencyP95 != 40 {
		t.Fatalf("server span = %+v", server)
	}
	if client := observations[1]; client.Kind != "client" || client.Peer["server.address"] != "api.stripe.com" || client.Peer["server.port"] != "443" {
		t.Fatalf("client span = %+v", client)
	}
	// The service is covered only for keys its spans carried: never peers, never extras.
	keys := strings.Join(coverage[0].Keys, ",")
	if coverage[0].Unit != "api" || !strings.Contains(keys, "http.route") || strings.Contains(keys, "server.address") || coverage[0].Peers {
		t.Fatalf("coverage = %+v", coverage[0])
	}
	runtime := NewRuntime(time.Hour)
	for _, item := range observations {
		runtime.Record(item)
	}
	held, _ := runtime.Snapshot(10 * minute)
	for _, item := range held {
		if item.Attributes["user.email"] != "" {
			t.Fatal("payload attribute retained")
		}
	}
	if _, _, err := DecodeOTLP([]byte("not json"), 0, false); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestOTLPEnvironmentComesFromTheResourceAndDefaultsToLocal(t *testing.T) {
	for key, want := range map[string]string{"deployment.environment.name": "staging", "deployment.environment": "production", "service.namespace": "local"} {
		export := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}},{"key":"` + key + `","value":{"stringValue":"` + want + `"}}]},"scopeSpans":[{"spans":[{"kind":2}]}]}]}`
		observations, coverage, err := DecodeOTLP([]byte(export), 0, false)
		if err != nil || len(observations) != 1 || len(coverage) != 1 {
			t.Fatalf("%s: observations=%+v coverage=%+v err=%v", key, observations, coverage, err)
		}
		if observations[0].Environment != want || coverage[0].Environment != want || observations[0].Source != "otlp" || coverage[0].Source != "otlp" {
			t.Fatalf("%s: observation=%+v coverage=%+v, want environment %q from otlp", key, observations[0], coverage[0], want)
		}
	}
}

// A relayed export has its own source and never passes as local, even when
// it claims to be.
func TestRemoteOTLPIsNeverLocal(t *testing.T) {
	for claim, want := range map[string]string{"": "remote", "local": "remote", "production": "production"} {
		export := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"site"}},{"key":"deployment.environment.name","value":{"stringValue":"` + claim + `"}}]},"scopeSpans":[{"spans":[{"kind":2}]}]}]}`
		observations, coverage, err := DecodeOTLP([]byte(export), 0, true)
		if err != nil || len(observations) != 1 || observations[0].Environment != want || observations[0].Source != "otlp-remote" ||
			coverage[0].Environment != want || coverage[0].Source != "otlp-remote" {
			t.Fatalf("claim %q: observations=%+v coverage=%+v err=%v", claim, observations, coverage, err)
		}
	}
}

func TestCloudflareCoversEveryNamedScriptAtScriptGranularity(t *testing.T) {
	var asked map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &asked)
		_, _ = writer.Write([]byte(`{"data":{"viewer":{"accounts":[{"workersInvocationsAdaptive":[
			{"sum":{"requests":120,"errors":3},"quantiles":{"wallTimeP99":25000},"dimensions":{"scriptName":"relay"}},
			{"sum":{"requests":9,"errors":0},"quantiles":{"wallTimeP99":1000},"dimensions":{"scriptName":"someone-else"}}]}]}}}`))
	}))
	defer server.Close()
	previous := CloudflareEndpoint
	CloudflareEndpoint = server.URL
	defer func() { CloudflareEndpoint = previous }()
	now := time.UnixMilli(100 * minute)
	observations, coverage, err := PullCloudflare(context.Background(), server.Client(), "acct", "token", []string{"relay", "quiet-worker"}, "production", now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || observations[0].Attributes["service.name"] != "relay" || observations[0].Count != 120 || observations[0].LatencyP95 != 25 {
		t.Fatalf("observations = %+v (unnamed scripts are not this system's)", observations)
	}
	if len(coverage) != 2 || coverage[1].Unit != "quiet-worker" || strings.Join(coverage[1].Keys, ",") != "service.name" {
		t.Fatalf("coverage = %+v", coverage)
	}
	if asked["variables"].(map[string]any)["account"] != "acct" {
		t.Fatalf("query = %+v", asked)
	}
	if _, _, err := PullCloudflare(context.Background(), server.Client(), "acct", "wrong", nil, "production", now, time.Minute); err == nil {
		t.Fatal("refused token reported success")
	}
}

func TestHostileInputsStayBounded(t *testing.T) {
	// A system past the node bound draws what fits instead of panicking.
	var routes strings.Builder
	routes.WriteString("package main\nimport \"net/http\"\nfunc main() {\n")
	for index := 0; index < MaxNodes+200; index++ {
		routes.WriteString(`http.HandleFunc("/r` + strconv.Itoa(index) + `", nil)` + "\n")
	}
	routes.WriteString("}\n")
	graph, err := Infer("s", []Repository{{ID: "r", Name: "r", Files: map[string][]byte{"go.mod": []byte("module x\n"), "cmd/x/main.go": []byte(routes.String())}}})
	if err != ErrBounds || len(graph.Nodes) != MaxNodes {
		t.Fatalf("bounded graph: %d nodes, %v", len(graph.Nodes), err)
	}
	// A megabyte of address-named constants is read in one pass.
	var constants strings.Builder
	for constants.Len() < 1<<20 {
		constants.WriteString("const u" + strconv.Itoa(constants.Len()) + " = 'https://a.io/'\n")
	}
	started := time.Now()
	if _, err := Infer("s", []Repository{{ID: "r", Name: "r", Files: map[string][]byte{"package.json": []byte(`{"name":"x","dependencies":{"express":"4"}}`), "big.ts": []byte(constants.String())}}}); err != nil || time.Since(started) > 5*time.Second {
		t.Fatalf("large source took %v: %v", time.Since(started), err)
	}
	// Runtime floods neither panic nor push out the newest evidence.
	runtime := NewRuntime(time.Hour)
	for index := 0; index < maxObservations+100; index++ {
		runtime.Record(Observation{Source: "s", Kind: "server", Start: int64(index/1000) * minute, End: int64(index/1000)*minute + 1, Count: 1,
			Attributes: map[string]string{"service.name": "api", "http.route": "/r" + strconv.Itoa(index)}})
		runtime.Cover(Coverage{Source: "s", Unit: strings.Repeat("u", 2000) + strconv.Itoa(index), AsOf: int64(index), TTL: minute})
	}
	observations, coverage := runtime.Snapshot(10 * minute)
	newest := false
	for _, item := range observations {
		newest = newest || item.Attributes["http.route"] == "/r"+strconv.Itoa(maxObservations+99)
	}
	if !newest || len(observations) > maxObservations || len(coverage) > maxCoverage || len(coverage[0].Unit) > maxText {
		t.Fatalf("flood: newest kept=%v, %d observations, %d coverage", newest, len(observations), len(coverage))
	}
	big := NewBuilder("s")
	for index := 0; index < MaxNodes+10; index++ {
		big.Node(External, "", "host:"+strconv.Itoa(index), "x").Select("server.address", "x")
	}
	// Recent traffic with lapsed coverage is still an observation, never a malformed stale rate.
	live := Overlay("s", overlayGraph(), []Observation{server(map[string]string{"http.request.method": "GET", "http.route": "/users/{id}"}, 3, 0, 100*minute)}, []Coverage{{Source: "otlp", Unit: "api", Keys: []string{"service.name", "http.route"}, AsOf: 80 * minute, TTL: minute}}, nil, 100*minute, 15*minute)
	if got := labels(live)["users"]; got.Observation != "observed" || got.Rate == 0 {
		t.Fatalf("users = %+v", got)
	}
}
