package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const testTeam = "11111111-1111-4111-8111-111111111111"
const testIssue = `{"id":"22222222-2222-4222-8222-222222222222","number":7,"title":"Reviewed","description":"Exact body","url":"https://linear.app/acme/issue/ENG-7/test","team":{"id":"11111111-1111-4111-8111-111111111111"},"state":{"type":"started"},"labels":{"nodes":[{"name":"ready"}],"pageInfo":{"hasNextPage":false}}}`

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testHost(t *testing.T, response func(map[string]any) (int, string)) *Host {
	t.Helper()
	return &Host{key: "private-test-key", client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.linear.app/graphql" || r.Header.Get("Authorization") != "private-test-key" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body["query"].(string), "mutation") {
			t.Fatal("intake wrote to Linear")
		}
		status, text := response(body)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: make(http.Header)}, nil
	})}}
}
func TestIssuesUsesTeamLabelAndCursorAndRereadsExactIssue(t *testing.T) {
	calls := 0
	h := testHost(t, func(body map[string]any) (int, string) {
		calls++
		vars := body["variables"].(map[string]any)
		filter := vars["filter"].(map[string]any)
		if filter["team"].(map[string]any)["id"].(map[string]any)["eq"] != testTeam {
			t.Fatal("team not bound")
		}
		if calls <= 2 {
			if filter["labels"].(map[string]any)["name"].(map[string]any)["eq"] != "ready" || filter["state"] == nil {
				t.Fatal("discovery lost filters")
			}
			if calls == 1 {
				if vars["after"] != nil {
					t.Fatal("first cursor")
				}
				return 200, `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"cursor"}}}}`
			}
			if vars["after"] != "cursor" {
				t.Fatal("cursor not followed")
			}
		} else if filter["number"].(map[string]any)["eq"] != float64(7) || filter["state"] != nil || filter["labels"] != nil {
			t.Fatal("exact reread filtered out current metadata")
		}
		return 200, `{"data":{"issues":{"nodes":[` + testIssue + `],"pageInfo":{"hasNextPage":false}}}}`
	})
	for _, args := range []struct {
		page   uint32
		label  string
		number uint64
	}{{2, "ready", 0}, {1, "", 7}} {
		page, err := h.Issues(context.Background(), testTeam, args.page, args.label, args.number)
		if err != nil || len(page.Issues) != 1 || page.Issues[0].NodeID != "22222222-2222-4222-8222-222222222222" || page.Issues[0].Body != "Exact body" || page.NextPage != nil {
			t.Fatalf("issue page: %+v, %v", page, err)
		}
	}
	if calls != 3 {
		t.Fatalf("requests=%d", calls)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", h, h), h.key) {
		t.Fatal("key in diagnostics")
	}
}
func TestIssuesFailClosedOnPartialErrorsAndInvalidIdentity(t *testing.T) {
	for name, response := range map[string]string{
		"partial":          `{"data":{"issues":{"nodes":[` + testIssue + `]}},"errors":[{"message":"private-test-key"}]}`,
		"null":             `{"data":null}`,
		"wrong-team":       `{"data":{"issues":{"nodes":[` + strings.Replace(testIssue, testTeam, "33333333-3333-4333-8333-333333333333", 1) + `]}}}`,
		"wrong-number":     `{"data":{"issues":{"nodes":[` + strings.Replace(testIssue, `"number":7`, `"number":8`, 1) + `]}}}`,
		"unsafe-url":       `{"data":{"issues":{"nodes":[` + strings.Replace(testIssue, "https://linear.app/", "https://attacker.example/", 1) + `]}}}`,
		"truncated-labels": `{"data":{"issues":{"nodes":[` + strings.Replace(testIssue, `"hasNextPage":false`, `"hasNextPage":true`, 1) + `]}}}`,
		"empty-exact":      `{"data":{"issues":{"nodes":[]}}}`,
		"oversize":         strings.Repeat("x", (2<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			h := testHost(t, func(map[string]any) (int, string) { return 200, response })
			if _, err := h.Issues(context.Background(), testTeam, 1, "", 7); err != ErrUnavailable {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, status := range []int{301, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := testHost(t, func(map[string]any) (int, string) { return status, `{"data":{"teams":{"nodes":[]}}}` })
			if _, err := h.Teams(context.Background()); err != ErrUnavailable {
				t.Fatal(err)
			}
		})
	}
}
