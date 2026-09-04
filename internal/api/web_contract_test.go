//go:build darwin || linux

package api

import (
	"fmt"
	"testing"
)

func TestWebStatusAndClientPageValidationIsStrict(t *testing.T) {
	stopped := WebStatus{State: "stopped", ProtocolVersion: BrowserProtocolVersion}
	if !validWebStatus(stopped) {
		t.Fatal("canonical stopped status rejected")
	}
	for _, invalid := range []WebStatus{
		{State: "stopped", Path: "/browser/v2", ProtocolVersion: BrowserProtocolVersion},
		{State: "stopped", ActiveClients: 1, ProtocolVersion: BrowserProtocolVersion},
		{State: "ready", Ready: true, Address: "127.0.0.1:1", Path: "/browser/v2", Origins: []string{"*"}, ProtocolVersion: BrowserProtocolVersion},
	} {
		if validWebStatus(invalid) {
			t.Fatalf("invalid status accepted: %+v", invalid)
		}
	}

	first := WebClient{ID: "00000000000000000000000000000001", CapabilityMask: 1, Revision: 1, CreatedAtMs: 1, UpdatedAtMs: 1}
	second := first
	second.ID = "00000000000000000000000000000002"
	page := WebClientPage{Clients: []WebClient{first, second}}
	if !validWebClientPage(page) {
		t.Fatal("canonical client page rejected")
	}
	if validWebClientPage(WebClientPage{Clients: []WebClient{second, first}}) {
		t.Fatal("out-of-order client page accepted")
	}
	next := "00000000000000000000000000000003"
	page.NextAfter = &next
	if validWebClientPage(page) {
		t.Fatal("short page with next cursor accepted")
	}
	full := WebClientPage{Clients: make([]WebClient, 128)}
	for index := range full.Clients {
		full.Clients[index] = WebClient{ID: fmt.Sprintf("%032x", index+1), CapabilityMask: 1, Revision: 1, CreatedAtMs: uint64(index + 1), UpdatedAtMs: uint64(index + 1)}
	}
	full.NextAfter = &full.Clients[len(full.Clients)-1].ID
	if !validWebClientPage(full) {
		t.Fatal("canonical full client page rejected")
	}
	wrong := "00000000000000000000000000000001"
	full.NextAfter = &wrong
	if validWebClientPage(full) {
		t.Fatal("wrong full-page cursor accepted")
	}
}
