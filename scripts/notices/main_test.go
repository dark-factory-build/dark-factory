package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestCommittedNoticesAreCurrent(t *testing.T) {
	want, err := exec.Command("go", "run", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../THIRD_PARTY_NOTICES")
	if err != nil || string(got) != string(want) {
		t.Fatalf("THIRD_PARTY_NOTICES is stale (%v); run: go run ./scripts/notices > THIRD_PARTY_NOTICES", err)
	}
}
