package buildinfo

import (
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
	if got.BuildID() != digest("development", "development", got.Target()) {
		t.Fatalf("build ID = %q", got.BuildID())
	}
}

func TestReleaseIdentityRequiresExactClosedFields(t *testing.T) {
	valid := Identity{version: "1.2.3-rc.1", source: "1234567890abcdef1234567890abcdef12345678", target: "darwin/arm64"}
	valid.buildID = digest(valid.version, valid.source, valid.target)
	if !valid.Release() {
		t.Fatal("valid release identity rejected")
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

func withIdentity(value Identity, mutate func(*Identity)) Identity {
	mutate(&value)
	return value
}
