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
	for _, c := range []struct{ state, sha, want string }{
		{"merged", head, "MERGED_AFTER_ENQUEUE_ATTEMPT"}, {"closed", head, "NOT_QUEUED"}, {"open", head, "ACTIVE_QUEUE"}, {"merged", strings.Repeat("b", 40), ""},
	} {
		got, err := mergeFromPull(read(c.state, c.sha), op, cause)
		if (c.want == "") != (err != nil) || got.State != c.want {
			t.Fatalf("%s/%s: %+v %v", c.state, c.sha[:1], got, err)
		}
	}
}
