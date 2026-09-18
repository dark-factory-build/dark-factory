//go:build darwin || linux

package api

import "testing"

func TestMaintainerUsesOnlyAttemptDomain(t *testing.T) {
	body := []byte(`{"method":"attempt_maintainer","params":{"request":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	call, code := decodeCall(attemptDomain, testCredential('M'), body)
	if code != "" || call.Kind() != CallMaintainer {
		t.Fatalf("attempt request: %v", code)
	}
	if _, ok := call.AttemptDigest(); !ok {
		t.Fatal("attempt credential lost")
	}
	if _, ok := call.MaintainerInput(); !ok {
		t.Fatal("typed request lost")
	}
	if _, code := decodeCall(operatorDomain, testCredential('M'), body); code != RemoteForbidden {
		t.Fatal("operator crossed attempt domain")
	}
}
