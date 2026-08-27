//go:build darwin || linux

package api

import "testing"

func TestWebStatusAndClientPageValidationIsStrict(t *testing.T) {
	stopped := WebStatus{State: "stopped", ProtocolVersion: 1}
	if !validWebStatus(stopped) {
		t.Fatal("canonical stopped status rejected")
	}
	for _, invalid := range []WebStatus{
		{State: "stopped", Path: "/browser/v1", ProtocolVersion: 1},
		{State: "stopped", ActiveClients: 1, ProtocolVersion: 1},
		{State: "ready", Ready: true, Address: "127.0.0.1:1", Path: "/browser/v1", Origins: []string{"*"}, ProtocolVersion: 1},
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
}
