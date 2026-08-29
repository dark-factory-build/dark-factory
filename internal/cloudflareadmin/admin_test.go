package cloudflareadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseOperationIsFinite(t *testing.T) {
	allowed := map[string]operation{
		"dns status":      operationDNSStatus,
		"dns publish-app": operationDNSPublishApp,
	}
	for input, want := range allowed {
		got, err := parseOperation(strings.Fields(input))
		if err != nil || got != want {
			t.Fatalf("parse %q = %v, %v", input, got, err)
		}
	}
	for _, input := range []string{
		"wrangler auth token",
		"wrangler deploy",
		"wrangler secret",
		"wrangler whoami --json",
		"dns delete",
		"dns publish-app extra",
		"status",
		"",
	} {
		if _, err := parseOperation(strings.Fields(input)); err == nil || err.Error() != usage {
			t.Fatalf("parse %q returned %v", input, err)
		}
	}
}

func TestDNSStatusBindsAccountAndReportsAllExactNameRecords(t *testing.T) {
	fixture := newAPIFixture(t)
	fixture.records = []dnsRecord{fixture.exactRecord("record-one")}
	config := testConfiguration(t, fixture.client())
	var output bytes.Buffer
	if err := config.execute(context.Background(), operationDNSStatus, &output); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Outcome string         `json:"outcome"`
		Zone    string         `json:"zone"`
		Records []recordOutput `json:"records"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "observed" || got.Zone != zoneName || len(got.Records) != 1 || got.Records[0].ID != "record-one" {
		t.Fatalf("unexpected status: %#v", got)
	}
	if strings.Contains(output.String(), testToken) || strings.Contains(output.String(), testAccount) {
		t.Fatal("credential entered DNS status output")
	}
	if fixture.posts.Load() != 0 {
		t.Fatal("status mutated DNS")
	}
}

func TestDNSRefusesWrongAuthorityPaginationAndConflictsBeforeCreate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*apiFixture)
		want   string
	}{
		{"wrong account", func(f *apiFixture) { f.accountID = strings.Repeat("b", 32) }, "configured account and zone"},
		{"wrong zone", func(f *apiFixture) { f.zone = "wrong.example" }, "configured account and zone"},
		{"zone pagination", func(f *apiFixture) { f.zonePages = 2 }, "pagination metadata"},
		{"zone metadata missing", func(f *apiFixture) { f.omitZoneInfo = true }, "pagination metadata"},
		{"record pagination", func(f *apiFixture) { f.recordPages = 2 }, "pagination metadata"},
		{"record metadata missing", func(f *apiFixture) { f.omitRecordInfo = true }, "pagination metadata"},
		{"conflict", func(f *apiFixture) { f.records = []dnsRecord{f.conflictingRecord()} }, "conflicting records"},
		{"exact plus conflict", func(f *apiFixture) { f.records = []dnsRecord{f.exactRecord("one"), f.conflictingRecord()} }, "conflicting records"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAPIFixture(t)
			test.mutate(fixture)
			config := testConfiguration(t, fixture.client())
			err := config.execute(context.Background(), operationDNSPublishApp, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
			if fixture.posts.Load() != 0 {
				t.Fatal("refused state still created a record")
			}
		})
	}
}

func TestDNSPublishIsExactAndIdempotent(t *testing.T) {
	fixture := newAPIFixture(t)
	config := testConfiguration(t, fixture.client())
	var first, second bytes.Buffer
	if err := config.execute(context.Background(), operationDNSPublishApp, &first); err != nil {
		t.Fatal(err)
	}
	if err := config.execute(context.Background(), operationDNSPublishApp, &second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), `"outcome":"created"`) || !strings.Contains(second.String(), `"outcome":"unchanged"`) {
		t.Fatalf("unexpected outcomes: %s / %s", first.String(), second.String())
	}
	if strings.Contains(first.String()+second.String(), testToken) || strings.Contains(first.String()+second.String(), testAccount) {
		t.Fatal("credential entered DNS publish output")
	}
	if fixture.posts.Load() != 1 {
		t.Fatalf("POST count = %d", fixture.posts.Load())
	}
}

func TestDNSPublishLockRejectsConcurrentCallerBeforeItsAPIRequest(t *testing.T) {
	fixture := newAPIFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.onZone = func() {
		select {
		case <-entered:
		default:
			close(entered)
			<-release
		}
	}
	config := testConfiguration(t, fixture.client())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- config.execute(context.Background(), operationDNSPublishApp, io.Discard)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first publisher did not acquire the lock and enter the API")
	}
	before := fixture.requests.Load()
	err := config.execute(context.Background(), operationDNSPublishApp, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("concurrent publisher got %v", err)
	}
	if fixture.requests.Load() != before {
		t.Fatal("concurrent publisher made an API request before refusing")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if fixture.posts.Load() != 1 {
		t.Fatalf("POST count = %d", fixture.posts.Load())
	}
}

func TestDNSPublishRechecksAfterAmbiguousCreateWithoutRetryingPOST(t *testing.T) {
	fixture := newAPIFixture(t)
	var postCalls atomic.Int64
	roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			postCalls.Add(1)
			fixture.records = []dnsRecord{fixture.exactRecord("ambiguous-record")}
			return nil, errors.New("response lost")
		}
		return fixture.response(request), nil
	})
	config := testConfiguration(t, &http.Client{Transport: roundTrip})
	config.apiBaseURL = "https://fixture.invalid"
	var output bytes.Buffer
	if err := config.execute(context.Background(), operationDNSPublishApp, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"outcome":"settled_after_ambiguous_create"`) {
		t.Fatalf("unexpected outcome: %s", output.String())
	}
	if postCalls.Load() != 1 {
		t.Fatalf("POST count = %d", postCalls.Load())
	}
}

func TestDNSPublishReturnsIndeterminateAfterFailedCreateAndFreshEmptyLookup(t *testing.T) {
	fixture := newAPIFixture(t)
	var postCalls atomic.Int64
	roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			postCalls.Add(1)
			return nil, errors.New("response lost")
		}
		return fixture.response(request), nil
	})
	config := testConfiguration(t, &http.Client{Transport: roundTrip})
	err := config.execute(context.Background(), operationDNSPublishApp, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "outcome is indeterminate") {
		t.Fatalf("got %v", err)
	}
	if postCalls.Load() != 1 {
		t.Fatalf("POST count = %d", postCalls.Load())
	}
}

func TestProductionHTTPClientRejectsRedirectsAndAmbientProxies(t *testing.T) {
	client := productionHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("production client admitted an ambient proxy")
	}
	request, err := http.NewRequest(http.MethodGet, "https://api.cloudflare.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy returned %v", err)
	}
}

func testConfiguration(t *testing.T, client *http.Client) configuration {
	t.Helper()
	root := t.TempDir()
	credentialsPath := filepath.Join(root, ".env.txt")
	writeCredentialFixture(t, credentialsPath)
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	return configuration{
		credentialsPath: credentialsPath,
		commonDirectory: root,
		apiBaseURL:      "https://fixture.invalid",
		httpClient:      client,
	}
}

type apiFixture struct {
	t              *testing.T
	mu             sync.Mutex
	zone           string
	accountID      string
	records        []dnsRecord
	zonePages      int
	recordPages    int
	omitZoneInfo   bool
	omitRecordInfo bool
	onZone         func()
	requests       atomic.Int64
	posts          atomic.Int64
}

func newAPIFixture(t *testing.T) *apiFixture {
	return &apiFixture{t: t, zone: zoneName, accountID: testAccount, zonePages: 1, recordPages: 1}
}

func (fixture *apiFixture) exactRecord(id string) dnsRecord {
	proxied := false
	return dnsRecord{ID: id, Type: "A", Name: appRecordName, Content: appRecordContent, Proxied: &proxied, TTL: 1}
}

func (fixture *apiFixture) conflictingRecord() dnsRecord {
	proxied := false
	return dnsRecord{ID: "conflict", Type: "CNAME", Name: appRecordName, Content: "wrong.example", Proxied: &proxied, TTL: 1}
}

func (fixture *apiFixture) client() *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return fixture.response(request), nil
	})}
}

func (fixture *apiFixture) response(request *http.Request) *http.Response {
	fixture.requests.Add(1)
	if request.Header.Get("Authorization") != "Bearer "+testToken {
		fixture.t.Errorf("unexpected Authorization header")
	}
	var result any
	var info *resultInfo
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/zones":
		if fixture.onZone != nil {
			fixture.onZone()
		}
		if request.URL.Query().Get("name") != zoneName || request.URL.Query().Get("status") != "active" {
			fixture.t.Errorf("unexpected zone query: %s", request.URL.RawQuery)
		}
		got := zone{ID: "57fdc117443e728d79d35f230f3caa22", Name: fixture.zone, Status: "active"}
		got.Account.ID = fixture.accountID
		result = []zone{got}
		if !fixture.omitZoneInfo {
			info = &resultInfo{Count: 1, Page: 1, PerPage: 2, TotalCount: 1, TotalPages: fixture.zonePages}
		}
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/dns_records"):
		fixture.mu.Lock()
		result = append([]dnsRecord(nil), fixture.records...)
		count := len(fixture.records)
		fixture.mu.Unlock()
		if !fixture.omitRecordInfo {
			info = &resultInfo{Count: count, Page: 1, PerPage: 100, TotalCount: count, TotalPages: fixture.recordPages}
		}
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/dns_records"):
		fixture.posts.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			fixture.t.Errorf("decode create payload: %v", err)
		}
		want := map[string]any{"type": "A", "name": appRecordName, "content": appRecordContent, "ttl": float64(1), "proxied": false}
		if !mapsEqual(payload, want) {
			fixture.t.Errorf("create payload = %#v", payload)
		}
		created := fixture.exactRecord("created-record")
		fixture.mu.Lock()
		fixture.records = []dnsRecord{created}
		fixture.mu.Unlock()
		result = created
	default:
		fixture.t.Errorf("unexpected request: %s %s", request.Method, request.URL.String())
		return jsonResponse(http.StatusNotFound, map[string]any{"success": false, "errors": []apiError{{Code: 1, Message: "not found"}}})
	}
	return jsonResponse(http.StatusOK, map[string]any{"success": true, "errors": []apiError{}, "result": result, "result_info": info})
}

func jsonResponse(status int, value any) *http.Response {
	content, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(content)),
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mapsEqual(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
