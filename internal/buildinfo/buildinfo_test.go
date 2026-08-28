package buildinfo

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryVersionIsOneReleaseValue(t *testing.T) {
	body, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	value := strings.TrimSuffix(string(body), "\n")
	if string(body) != value+"\n" || !validVersion(value) || value == "development" {
		t.Fatalf("VERSION = %q", body)
	}
}

func TestCurrentBuildIdentityIsDeterministicAndDevelopmentFailsClosed(t *testing.T) {
	got := Current()
	if got.Version() != "development" || got.Source() != "development" || got.Target() != runtime.GOOS+"/"+runtime.GOARCH || got.Release() {
		t.Fatalf("development identity = %+v", got)
	}
	if got.BuildID() != "development" {
		t.Fatalf("build ID = %q", got.BuildID())
	}
}

func TestReleaseIdentityRequiresExactClosedFields(t *testing.T) {
	valid := Identity{version: "1.2.3-rc.1", source: "1234567890abcdef1234567890abcdef12345678", target: "darwin/arm64"}
	valid.buildID = digest(valid.version, valid.source, valid.target)
	if !valid.Release() {
		t.Fatal("valid release identity rejected")
	}
	parsed, ok := parseReceipt(valid.Receipt())
	if !ok || parsed != valid {
		t.Fatalf("receipt round trip = %+v, %v", parsed, ok)
	}
	constructed, ok := Expected(valid.version, valid.source, valid.target)
	if !ok || constructed != valid {
		t.Fatalf("Expected = %+v, %v", constructed, ok)
	}
	tests := []Identity{
		{},
		withIdentity(valid, func(value *Identity) { value.version = "development" }),
		withIdentity(valid, func(value *Identity) { value.version = "v1.2.3" }),
		withIdentity(valid, func(value *Identity) { value.source = "1234567890ABCDEF1234567890abcdef12345678" }),
		withIdentity(valid, func(value *Identity) { value.source = "0000000000000000000000000000000000000000" }),
		withIdentity(valid, func(value *Identity) { value.target = "linux/amd64" }),
		withIdentity(valid, func(value *Identity) {
			value.buildID = "0000000000000000000000000000000000000000000000000000000000000000"
		}),
		withIdentity(valid, func(value *Identity) { value.buildID = digest("1.2.4", value.source, value.target) }),
	}
	for index, value := range tests {
		if value.Release() {
			t.Fatalf("invalid identity %d accepted: %+v", index, value)
		}
	}
}

func TestIdentityJSONIsExactAndDevelopmentFailsClosed(t *testing.T) {
	identity, ok := Expected("1.2.3", "1234567890abcdef1234567890abcdef12345678", "darwin/amd64")
	if !ok {
		t.Fatal("valid identity rejected")
	}
	var output bytes.Buffer
	if err := identity.WriteJSON(&output); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Version string `json:"version"`
		Source  string `json:"source"`
		Target  string `json:"target"`
		BuildID string `json:"build_id"`
		Release bool   `json:"release"`
	}
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Version != identity.Version() || value.Source != identity.Source() || value.Target != identity.Target() || value.BuildID != identity.BuildID() || !value.Release {
		t.Fatalf("identity JSON = %+v", value)
	}
}

func withIdentity(value Identity, mutate func(*Identity)) Identity {
	mutate(&value)
	return value
}
