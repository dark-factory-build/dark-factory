//go:build darwin || linux

package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMaintainerUsesOnlyAttemptDomain(t *testing.T) {
	body, err := json.Marshal(requestEnvelope{Method: "attempt_maintainer", Params: MaintainerInput{Request: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	call, code := decodeCall(attemptDomain, testCredential('M'), body)
	if code != "" || call.Kind() != CallMaintainer {
		t.Fatalf("attempt request: %v", code)
	}
	if _, ok := call.AttemptDigest(); !ok {
		t.Fatal("attempt credential lost")
	}
	input, ok := call.MaintainerInput()
	if !ok || string(input.Request) != `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` {
		t.Fatalf("typed request lost: %+v", input)
	}
	if _, code := decodeCall(operatorDomain, testCredential('M'), body); code != RemoteForbidden {
		t.Fatal("operator crossed attempt domain")
	}
}

func TestMaintainerTransportPreservesMCPCamelCase(t *testing.T) {
	initialize := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"factory","version":"1"}}}`)
	request, err := json.Marshal(requestEnvelope{Method: "attempt_maintainer", Params: MaintainerInput{Request: initialize}})
	if err != nil {
		t.Fatal(err)
	}
	call, code := decodeCall(attemptDomain, testCredential('M'), request)
	if code != "" {
		t.Fatalf("initialize rejected: %s", code)
	}
	input, ok := call.MaintainerInput()
	if !ok || !bytes.Equal(input.Request, initialize) {
		t.Fatalf("initialize changed: %s", input.Request)
	}

	ok = true
	upstream := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"issueNumber":9},"isError":false}}`)
	data, err := json.Marshal(MaintainerResult{State: "ok", Response: upstream})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(responseEnvelope{OK: &ok, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	var result MaintainerResult
	if err := decodeResponse(encoded, &result); err != nil || result.State != "ok" || !bytes.Equal(result.Response, upstream) {
		t.Fatalf("camel-case response: %+v %v", result, err)
	}
}
