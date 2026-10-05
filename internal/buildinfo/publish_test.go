package buildinfo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const publishTag = "v1.2.3-rc.1"

// fakeGitHub models one release. failures maps a verb (view, create, upload,
// edit) to queued stderr; a "commit:" prefix applies the write before failing.
type fakeGitHub struct {
	tagSHA   string
	release  *releaseState
	failures map[string][]string
	calls    map[string]int
}

func (f *fakeGitHub) run(arguments ...string) ([]byte, string, error) {
	verb := arguments[0]
	if verb == "release" {
		verb = arguments[1]
	}
	f.calls[verb]++
	if verb == "api" {
		return []byte(`{"object":{"type":"commit","sha":"` + f.tagSHA + `"}}`), "", nil
	}
	failure := ""
	if queue := f.failures[verb]; len(queue) > 0 {
		failure, f.failures[verb] = queue[0], queue[1:]
	}
	committed := strings.HasPrefix(failure, "commit:")
	if failure != "" && !committed {
		return nil, failure, errors.New("exit status 1")
	}
	switch verb {
	case "view":
		if f.release == nil {
			return nil, "release not found", errors.New("exit status 1")
		}
		out, _ := json.Marshal(f.release)
		return out, "", nil
	case "create":
		f.release = &releaseState{Draft: true, Prerelease: strings.Contains(strings.Join(arguments, " "), "--prerelease")}
	case "upload":
		f.addAsset(arguments[3])
	case "edit":
		f.release.Draft = false
	}
	if committed {
		return nil, strings.TrimPrefix(failure, "commit:"), errors.New("exit status 1")
	}
	return nil, "", nil
}

func (f *fakeGitHub) addAsset(path string) {
	digest, _ := fileDigest(path)
	f.release.Assets = append(f.release.Assets, struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}{filepath.Base(path), digest})
}

func publishFixture(t *testing.T) (*fakeGitHub, []string) {
	t.Helper()
	publishRetryDelay = 0
	directory := t.TempDir()
	var paths []string
	for _, name := range []string{"archive.tar.gz", "SHA256SUMS", "dark-factory.rb"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("fixture "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return &fakeGitHub{tagSHA: fixtureSource, failures: map[string][]string{}, calls: map[string]int{}}, paths
}

func publish(f *fakeGitHub, paths []string) error {
	return PublishRelease(append([]string{publishTag, fixtureSource, "example/project"}, paths...), f.run)
}

func expectCalls(t *testing.T, f *fakeGitHub, create, upload, edit int) {
	t.Helper()
	if f.calls["create"] != create || f.calls["upload"] != upload || f.calls["edit"] != edit {
		t.Fatalf("writes create=%d upload=%d edit=%d; want %d %d %d",
			f.calls["create"], f.calls["upload"], f.calls["edit"], create, upload, edit)
	}
}

func expectRefusal(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("error = %v; want %q", err, message)
	}
}

func TestPublishCreatesThenRerunIsReadOnly(t *testing.T) {
	f, paths := publishFixture(t)
	if err := publish(f, paths); err != nil {
		t.Fatal(err)
	}
	expectCalls(t, f, 1, 3, 1)
	if f.release.Draft || !f.release.Prerelease || len(f.release.Assets) != 3 {
		t.Fatalf("release = %+v", f.release)
	}
	f.calls = map[string]int{}
	if err := publish(f, paths); err != nil {
		t.Fatal(err)
	}
	expectCalls(t, f, 0, 0, 0)
	if f.calls["api"] != 2 {
		t.Fatalf("rerun tag checks = %d; want 2", f.calls["api"])
	}
}

func TestPublishResumesWhenDigestsMatch(t *testing.T) {
	f, paths := publishFixture(t)
	f.release = &releaseState{Draft: true, Prerelease: true}
	f.addAsset(paths[0])
	if err := publish(f, paths); err != nil {
		t.Fatal(err)
	}
	expectCalls(t, f, 0, 2, 1)
}

func TestPublishRefusesDigestCollision(t *testing.T) {
	f, paths := publishFixture(t)
	f.release = &releaseState{Draft: true, Prerelease: true}
	f.addAsset(paths[0])
	f.release.Assets[0].Digest = "sha256:other-bytes"
	expectRefusal(t, publish(f, paths), "already exists with a different SHA-256 digest")
	expectCalls(t, f, 0, 0, 0)
}

func TestPublishRefusesUnexpectedAsset(t *testing.T) {
	f, paths := publishFixture(t)
	f.release = &releaseState{Prerelease: true}
	for _, path := range paths {
		f.addAsset(path)
	}
	f.addAsset(filepath.Join(t.TempDir(), "wrong-build.tar.gz"))
	expectRefusal(t, publish(f, paths), "has unexpected asset: wrong-build.tar.gz")
	expectCalls(t, f, 0, 0, 0)
}

func TestPublishRefusesMovedTag(t *testing.T) {
	f, paths := publishFixture(t)
	f.tagSHA = strings.Repeat("c", 40)
	expectRefusal(t, publish(f, paths), "points to "+f.tagSHA)
	expectCalls(t, f, 0, 0, 0)
	if f.calls["view"] != 0 {
		t.Fatal("moved tag read the release")
	}
}

func TestPublishRetriesTransientErrors(t *testing.T) {
	f, paths := publishFixture(t)
	f.failures["create"] = []string{"HTTP 503: unavailable", "HTTP 502", "error connecting to api.github.com: unexpected EOF"}
	if err := publish(f, paths); err != nil {
		t.Fatal(err)
	}
	expectCalls(t, f, 4, 3, 1)

	f, paths = publishFixture(t)
	f.failures["view"] = []string{"HTTP 503", "HTTP 503", "HTTP 503", "HTTP 503"}
	expectRefusal(t, publish(f, paths), "release creation failed after 4 attempts")

	f, paths = publishFixture(t)
	f.failures["view"] = []string{"HTTP 403: forbidden"}
	expectRefusal(t, publish(f, paths), "release creation failed (attempt 1/4)")
	if f.calls["view"] != 1 {
		t.Fatalf("403 was retried: %d views", f.calls["view"])
	}
}

func TestPublishAcceptsWriteThatCommittedBefore5xx(t *testing.T) {
	// A 5xx is retried after a fresh read; a non-retryable failure is
	// reconciled by the read that follows it. Neither repeats the write.
	for _, verb := range []string{"create", "upload", "edit"} {
		for _, failure := range []string{"commit:HTTP 502: bad gateway", "commit:HTTP 422: Validation Failed"} {
			f, paths := publishFixture(t)
			f.failures[verb] = []string{failure}
			if err := publish(f, paths); err != nil {
				t.Fatalf("%s %s: %v", verb, failure, err)
			}
			expectCalls(t, f, 1, 3, 1)
		}
	}
	// A rejected write that did not commit is not retried.
	f, paths := publishFixture(t)
	f.failures["upload"] = []string{"HTTP 422: Validation Failed"}
	expectRefusal(t, publish(f, paths), "upload of archive.tar.gz failed (attempt 1/4)")
	expectCalls(t, f, 1, 1, 0)
}
