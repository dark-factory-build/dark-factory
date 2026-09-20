//go:build darwin || linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestBrowserProductionIsPrivateAndProjectScoped(t *testing.T) {
	f := newAdapterFixture(t, kernel.BrowserCapabilityObserve|kernel.BrowserCapabilityPrivateHumanRequestDetail)
	conn := f.pair(t)
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()
	project, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x61}, 16))
	other, _ := kernel.ProjectIDFromBytes(bytes.Repeat([]byte{0x62}, 16))
	for _, id := range []kernel.ProjectID{project, other} {
		if _, err := f.store.CreateProject(ctx, kernel.NewProject{ID: id, Name: "production", Root: contentRepositoryFixture(t)}, adapterTime(t, 10)); err != nil {
			t.Fatal(err)
		}
	}
	obs := kernel.ProductionObservation{Repository: "example/factory", ObservedAt: 10, PullRequests: []kernel.ProductionPullRequest{{Number: 1, Title: "Durable work", State: "open", Review: kernel.ProductionReview{State: "unknown"}}}}
	if err := f.store.RecordProductionObservation(ctx, project, obs, adapterTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	for index, id := range []kernel.ProjectID{project, other} {
		input, _ := json.Marshal(map[string]any{"project_id": id.String(), "limit": 8})
		request := browserprotocol.ProjectContent{Operation: "production", Input: input}
		wire, err := browserprotocol.EncodeProjectContent(fmt.Sprintf("production-%d", index), request)
		if err != nil {
			t.Fatal(err)
		}
		adapterWrite(t, conn, wire)
		frame := adapterRead(t, conn)
		if frame.Type != browserprotocol.TypeProjectContentResult {
			t.Fatalf("frame=%+v", frame)
		}
		var page kernel.ProductionPage
		if err := json.Unmarshal(frame.Body.(browserprotocol.ProjectContentResult).Output, &page); err != nil {
			t.Fatal(err)
		}
		if index == 0 && page.Total != 2 || index == 1 && page.Total != 0 {
			t.Fatalf("wrong project result=%+v", page)
		}
	}
	noPrivate := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	input, _ := json.Marshal(map[string]any{"project_id": project.String(), "limit": 8})
	if _, err := noPrivate.backend.ProjectContent(ctx, rawBrowserClient(noPrivate.client.ID), browserprotocol.ProjectContent{Operation: "production", Input: input}); !errors.Is(err, browser.ErrUnauthorized) {
		t.Fatalf("public read=%v", err)
	}
}
