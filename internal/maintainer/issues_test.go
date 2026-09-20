package maintainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssueReaderExactIdentityAndUnavailablePages(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	payload := `{"result":{"structuredContent":{"repository_id":7,"issues":[{"id":42,"node_id":"I_fixture","number":9,"title":"reviewed","body":"exact snapshot","state":"closed"}],"next_page":null}}}`
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/mcp") {
			var body struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Params.Name != "list_issues" || body.Params.Arguments["issue_number"] != float64(9) || body.Params.Arguments["label"] != nil {
				t.Error("exact lookup was replaced with discovery")
			}
			_, _ = out.Write([]byte(payload))
			return
		}
		_ = json.NewEncoder(out).Encode(Status{State: "connected", User: &User{ID: 123, Login: "operator"}, ConnectionID: id, Repositories: []Delegation{{Repository: "team/repo", RepositoryID: 7, InstallationID: 1}}})
	}))
	defer server.Close()
	host := &Host{client: NewClient(), connection: connectionRecord{ID: id, Secret: secret, Author: gitauthor.Identity{ID: 123, Login: "operator"}}}
	host.client.origin = server.URL
	page, err := host.Issues(context.Background(), "team/repo", 7, 1, "", 9)
	if err != nil || len(page.Issues) != 1 || page.Issues[0].Body != "exact snapshot" || page.Issues[0].State != "closed" {
		t.Fatalf("exact issue: %+v %v", page, err)
	}
	for _, invalid := range []string{
		`{"result":{"isError":true}}`, `{"result":{"structuredContent":{"repository_id":8,"issues":[]}}}`,
		`{"result":{"structuredContent":{"repository_id":7,"issues":[]}}}`, `{"result":{"structuredContent":{"repository_id":7,"issues":null}}}`,
	} {
		payload = invalid
		if _, err := host.Issues(context.Background(), "team/repo", 7, 1, "", 9); err == nil {
			t.Fatal("missing/foreign/unavailable exact issue accepted")
		}
	}
}
