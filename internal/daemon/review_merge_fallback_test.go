package daemon

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/review"
)

func TestMergeFromPullSettlesWithoutReceipt(t *testing.T) {
	head := strings.Repeat("a", 40)
	op := review.Operation{Request: review.Request{Head: head}}
	cause := errors.New("indeterminate")
	read := func(state, sha string) json.RawMessage {
		return json.RawMessage(`{"pull_requests":[{"head_sha":"` + sha + `","state":"` + state + `"}]}`)
	}
	// A pull request merged or still open at another head superseded this
	// operation: it ends closed instead of retrying forever (#1407).
	other := strings.Repeat("b", 40)
	for _, c := range []struct{ state, sha, want string }{
		{"merged", head, "MERGED_AFTER_ENQUEUE_ATTEMPT"}, {"closed", head, "NOT_QUEUED"}, {"open", head, "ACTIVE_QUEUE"},
		{"merged", other, "NOT_QUEUED"}, {"open", other, "NOT_QUEUED"}, {"open", "", ""},
	} {
		got, err := mergeFromPull(read(c.state, c.sha), op, cause)
		if (c.want == "") != (err != nil) || got.State != c.want || got.Open != (c.want == "ACTIVE_QUEUE") {
			t.Fatalf("%s/%q: %+v %v", c.state, c.sha, got, err)
		}
	}
}
