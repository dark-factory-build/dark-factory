//go:build darwin

package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMaintainerCredentialPrivateRestartAndCrash(t *testing.T) {
	for _, name := range []string{maintainerCredentialName, "linear.json"} {
		t.Run(name, func(t *testing.T) {
			stage := name + ".staging"
			if name == maintainerCredentialName {
				stage = maintainerCredentialStage
			}
			path := filepath.Join(installTempDir(t), "home")
			if _, err := Init(context.Background(), path); err != nil {
				t.Fatal(err)
			}
			home, err := OpenOperationalHome(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if data, err := home.state.readCredential(name); err != nil || data != nil {
				t.Fatal("new home has credential")
			}
			first := []byte(`{"credential":"test-only"}`)
			if err := home.state.writeCredential(name, first); err != nil {
				t.Fatal(err)
			}
			if data, err := home.state.readCredential(name); err != nil || !bytes.Equal(data, first) {
				t.Fatal("credential roundtrip failed")
			}
			if err := os.WriteFile(filepath.Join(path, stage), []byte("interrupted"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := home.Close(); err != nil {
				t.Fatal(err)
			}
			home, err = OpenOperationalHome(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer home.Close()
			if data, err := home.state.readCredential(name); err != nil || !bytes.Equal(data, first) {
				t.Fatal("staging overwrote committed credential")
			}
			if err := home.state.writeCredential(name, []byte(`{"disabled":true}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(path, stage)); !os.IsNotExist(err) {
				t.Fatal("staging not consumed")
			}
			if err := os.Chmod(filepath.Join(path, name), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := home.state.readCredential(name); err == nil {
				t.Fatal("read public credential")
			}
			if err := home.state.writeCredential(name, first); err == nil {
				t.Fatal("overwrote public credential")
			}
			if err := os.Remove(filepath.Join(path, name)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(path, tokenName), filepath.Join(path, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := home.state.readCredential(name); err == nil {
				t.Fatal("followed credential symlink")
			}
			if err := home.state.writeCredential(name, first); err == nil {
				t.Fatal("replaced credential symlink")
			}
		})
	}
}

func TestOperationalCensusIncludesEveryCredentialAndRejectsOverflow(t *testing.T) {
	path := t.TempDir()
	for _, name := range []string{formatName, databaseName, tokenName, lockName, lockAnchorName, runtimesName, changesName, databaseName + "-wal", databaseName + "-shm", RelayDirectoryName, maintainerCredentialName, maintainerCredentialStage, "linear.json", "linear.json.staging"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, overflow := range []bool{false, true} {
		if overflow {
			if err := os.WriteFile(filepath.Join(path, "unexpected"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		dir, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		names, err := readOperationalCensus(dir)
		dir.Close()
		if overflow && err == nil {
			t.Fatal("overflow entry accepted")
		}
		if !overflow && (err != nil || len(names) != 14) {
			t.Fatalf("optional members truncated: %d %v", len(names), err)
		}
	}
}
