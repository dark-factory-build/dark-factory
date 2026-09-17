//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func browserContentProjectFixture(t *testing.T, f *adapterFixture, id kernel.ProjectID, name string) string {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-browser-content-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, args := range [][]string{{"init", root}, {"-C", root, "config", "user.name", "Dark Factory Test"}, {"-C", root, "config", "user.email", "test@invalid"}} {
		if output, runErr := exec.Command(change.TrustedGitExecutable, args...).CombinedOutput(); runErr != nil {
			t.Fatalf("git %v: %v: %s", args, runErr, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("content source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", root, "add", "README.md"}, {"-C", root, "commit", "-m", "initial"}} {
		if output, runErr := exec.Command(change.TrustedGitExecutable, args...).CombinedOutput(); runErr != nil {
			t.Fatalf("git %v: %v: %s", args, runErr, output)
		}
	}
	if _, err := f.store.CreateProject(context.Background(), kernel.NewProject{ID: id, Name: name, Root: root}, adapterTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	return root
}

func seedBrowserContent(t *testing.T, f *adapterFixture, spec kernel.NewContent, at kernel.UnixMillis) kernel.ContentRevision {
	t.Helper()
	f.daemon.operationMu.Lock()
	defer f.daemon.operationMu.Unlock()
	pinned, err := f.daemon.writeContentSource(context.Background(), spec, 1)
	if err != nil {
		t.Fatal(err)
	}
	content, err := f.store.CreateContent(context.Background(), pinned, at)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestBrowserProjectContentUsesExactProjectAndRevisionAuthority(t *testing.T) {
	f := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail|kernel.BrowserCapabilityHumanActions)
	connection := f.pair(t)
	if connection == nil {
		t.Fatal("pairing failed")
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	projectID, _ := kernel.ProjectIDFromBytes(bytesOf(0x21))
	contentID, _ := kernel.ContentIDFromBytes(bytesOf(0x22))
	browserContentProjectFixture(t, f, projectID, "library")
	content := seedBrowserContent(t, f, kernel.NewContent{ID: contentID, ProjectID: projectID, Kind: kernel.ContentProcedure, Title: "Procedure", Body: "abcdef", Author: "seed"}, adapterTime(t, 11))
	input := func(project string, revision uint64) []byte {
		b, _ := json.Marshal(map[string]any{"project_id": project, "id": contentID.String(), "revision": revision, "offset": 0, "limit": 4})
		return b
	}
	result, err := f.backend.ProjectContent(context.Background(), rawBrowserClient(f.client.ID), browserprotocol.ProjectContent{Operation: "body", Input: input(projectID.String(), uint64(content.Revision.Int64()))})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Body     string `json:"body"`
		Complete bool   `json:"complete"`
	}
	if err := json.Unmarshal(result.Output, &body); err != nil || body.Body != "abcd" || body.Complete {
		t.Fatalf("body result = %s, %v", result.Output, err)
	}
	otherID, _ := kernel.ProjectIDFromBytes(bytesOf(0x23))
	browserContentProjectFixture(t, f, otherID, "other")
	_, err = f.backend.ProjectContent(context.Background(), rawBrowserClient(f.client.ID), browserprotocol.ProjectContent{Operation: "body", Input: input(otherID.String(), 1)})
	if !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("cross-project body = %v", err)
	}
	_, err = f.backend.ProjectContent(context.Background(), rawBrowserClient(f.client.ID), browserprotocol.ProjectContent{Operation: "create", Input: []byte(`{"project_id":"` + projectID.String() + `","id":"` + contentID.String() + `","kind":"procedure","title":"x"}`)})
	if err == nil {
		t.Fatal("duplicate create accepted")
	}
}

func bytesOf(value byte) []byte {
	return []byte{value, value, value, value, value, value, value, value, value, value, value, value, value, value, value, value}
}

func TestBrowserLibraryRealWireAndCapabilityBoundaries(t *testing.T) {
	for _, caps := range []kernel.BrowserCapabilityMask{
		kernel.BrowserCapabilityObserve,
		kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityHumanActions,
		kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityPrivateHumanRequestDetail,
		kernel.BrowserCapabilityObserve | kernel.BrowserCapabilityPrivateHumanRequestDetail | kernel.BrowserCapabilityHumanActions,
	} {
		t.Run(fmt.Sprint(caps), func(t *testing.T) {
			f := newAdapterFixture(t, caps)
			conn := f.pair(t)
			defer conn.Close(websocket.StatusNormalClosure, "")
			ctx := context.Background()
			project, _ := kernel.ProjectIDFromBytes(bytesOf(0x31))
			other, _ := kernel.ProjectIDFromBytes(bytesOf(0x32))
			for i, p := range []kernel.ProjectID{project, other} {
				browserContentProjectFixture(t, f, p, fmt.Sprint(i))
			}
			content, _ := kernel.ContentIDFromBytes(bytesOf(0x33))
			spec := kernel.NewContent{ID: content, ProjectID: project, Kind: kernel.ContentProcedure, Title: "guide", Body: "old", Author: "seed"}
			first := seedBrowserContent(t, f, spec, adapterTime(t, 11))
			spec.Body = "new"
			f.daemon.operationMu.Lock()
			pinned, err := f.daemon.writeContentSource(ctx, spec, 2)
			if err == nil {
				_, err = f.store.ReviseContent(ctx, first.Revision, pinned, adapterTime(t, 12))
			}
			f.daemon.operationMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			sequence := 0
			send := func(operation string, input map[string]any) browserprotocol.ControlFrame {
				t.Helper()
				sequence++
				raw, _ := json.Marshal(input)
				wire, e := browserprotocol.EncodeProjectContent(fmt.Sprintf("library-%d", sequence), browserprotocol.ProjectContent{Operation: operation, Input: raw})
				if e != nil {
					t.Fatal(e)
				}
				adapterWrite(t, conn, wire)
				return adapterRead(t, conn)
			}
			frame := send("read", map[string]any{"project_id": project.String(), "id": content.String(), "revision": 1})
			if !caps.Has(kernel.BrowserCapabilityPrivateHumanRequestDetail) {
				if frame.Type != browserprotocol.TypeError {
					t.Fatalf("private read admitted: %+v", frame)
				}
				return
			}
			if frame.Type != browserprotocol.TypeProjectContentResult {
				t.Fatalf("read = %+v", frame)
			}
			var meta api.Content
			if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &meta); err != nil || meta.ID != content.String() || meta.Revision != 1 || meta.LatestRevision != 2 || meta.Body != "" {
				t.Fatalf("metadata = %+v %v", meta, err)
			}
			frame = send("body", map[string]any{"project_id": project.String(), "id": content.String(), "revision": 1, "limit": 8192})
			var body api.ContentBody
			if frame.Type != browserprotocol.TypeProjectContentResult {
				t.Fatalf("body = %+v", frame)
			}
			if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &body); err != nil || body.Body != "old" {
				t.Fatalf("pinned body = %+v %v", body, err)
			}
			mismatch, _ := json.Marshal(map[string]any{"project_id": other.String(), "id": content.String(), "revision": 1, "limit": 8192})
			if _, err := f.backend.ProjectContent(ctx, rawBrowserClient(f.client.ID), browserprotocol.ProjectContent{Operation: "body", Input: mismatch}); !errors.Is(err, browser.ErrUnauthorized) {
				t.Fatalf("cross-project body=%v", err)
			}
			hugeID, _ := kernel.ContentIDFromBytes(bytesOf(0x35))
			seedBrowserContent(t, f, kernel.NewContent{ID: hugeID, ProjectID: project, Kind: kernel.ContentProcedure, Title: "large metadata", Body: "large", Author: "seed", SourceReferences: strings.Repeat("\x01", 32768)}, adapterTime(t, 13))
			frame = send("read", map[string]any{"project_id": project.String(), "id": hugeID.String(), "revision": 1})
			if frame.Type != browserprotocol.TypeError || frame.Body.(browserprotocol.Error).Code != browserprotocol.ErrorTooLarge {
				t.Fatalf("oversized response=%+v", frame)
			}
			frame = send("read", map[string]any{"project_id": project.String(), "id": content.String(), "revision": 1})
			if frame.Type != browserprotocol.TypeProjectContentResult {
				t.Fatalf("connection lost after too_large: %+v", frame)
			}

			newID, _ := kernel.ContentIDFromBytes(bytesOf(0x34))
			frame = send("create", map[string]any{"project_id": project.String(), "id": newID.String(), "kind": "procedure", "title": "browser draft", "body": "human contribution"})
			if !caps.Has(kernel.BrowserCapabilityHumanActions) {
				if frame.Type != browserprotocol.TypeError {
					t.Fatal("read-only write admitted")
				}
				return
			}
			if frame.Type != browserprotocol.TypeProjectContentResult {
				t.Fatalf("create = %+v", frame)
			}
			if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &meta); err != nil || meta.Author != "browser:"+f.client.ID.String() || meta.ID != newID.String() || meta.Commit == "" || meta.Path == "" {
				t.Fatalf("browser provenance = %+v %v", meta, err)
			}
			frame = send("body", map[string]any{"project_id": project.String(), "id": newID.String(), "revision": 1, "limit": 8192})
			if frame.Type != browserprotocol.TypeProjectContentResult || json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &body) != nil || body.Body != "human contribution" {
				t.Fatalf("browser-created body = %+v, body=%+v", frame, body)
			}
			frame = send("revise", map[string]any{"project_id": project.String(), "id": newID.String(), "kind": "procedure", "title": "browser revision", "body": "human revision", "expected_revision": 1})
			if frame.Type != browserprotocol.TypeProjectContentResult || json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &meta) != nil || meta.Revision != 2 || meta.Author != "browser:"+f.client.ID.String() {
				t.Fatalf("browser revision = %+v, metadata=%+v", frame, meta)
			}
			frame = send("body", map[string]any{"project_id": project.String(), "id": newID.String(), "revision": 2, "limit": 8192})
			if frame.Type != browserprotocol.TypeProjectContentResult || json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &body) != nil || body.Body != "human revision" {
				t.Fatalf("browser-revised body = %+v, body=%+v", frame, body)
			}
			if _, err := f.store.RevokeBrowserClient(ctx, f.client.ID, f.client.Revision, adapterTime(t, 2100)); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"project_id": project.String(), "limit": 1})
			if _, err := f.backend.ProjectContent(ctx, rawBrowserClient(f.client.ID), browserprotocol.ProjectContent{Operation: "list", Input: raw}); !errors.Is(err, browser.ErrUnauthorized) {
				t.Fatalf("revoked read = %v", err)
			}
		})
	}
}
