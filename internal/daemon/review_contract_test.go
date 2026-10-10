package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestObservePullRequestMergeMatchesSharedContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "protocol", "maintainer", "observe_pull_request_merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Request  contractShape `json:"request"`
		Response contractShape `json:"response"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	assertContractShape(t, contract.Request, observePullRequestMergeArguments("repo", 1, "sha", "main"))
	assertContractShape(t, contract.Response, observePullRequestMergeResponse{
		PullNumber: 1, Head: "sha", Base: "main", PullState: "open", State: "NOT_QUEUED",
	})
}

type contractShape struct {
	Required   []string          `json:"required"`
	Properties map[string]string `json:"properties"`
}

func assertContractShape(t *testing.T, shape contractShape, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, name := range shape.Required {
		want[name] = true
	}
	got := map[string]bool{}
	for name := range object {
		got[name] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contract fields = %v, want %v", got, want)
	}
	for name := range shape.Properties {
		if !got[name] {
			t.Fatalf("contract property %q is not carried by caller", name)
		}
	}
}
