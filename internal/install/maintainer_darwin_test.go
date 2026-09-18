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
	path := filepath.Join(installTempDir(t), "home")
	if _, err := Init(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := home.ReadMaintainerCredential(); err != nil || data != nil {
		t.Fatal("new home has credential")
	}
	first := []byte(`{"credential":"test-only"}`)
	if err := home.WriteMaintainerCredential(first); err != nil {
		t.Fatal(err)
	}
	if data, err := home.ReadMaintainerCredential(); err != nil || !bytes.Equal(data, first) {
		t.Fatal("credential roundtrip failed")
	}
	if err := os.WriteFile(filepath.Join(path, maintainerCredentialStage), []byte("interrupted"), 0600); err != nil {
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
	if data, err := home.ReadMaintainerCredential(); err != nil || !bytes.Equal(data, first) {
		t.Fatal("staging overwrote committed credential")
	}
	if err := home.WriteMaintainerCredential([]byte(`{"disabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(path, maintainerCredentialStage)); !os.IsNotExist(err) {
		t.Fatal("staging not consumed")
	}
	if err := os.Chmod(filepath.Join(path, maintainerCredentialName), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := home.ReadMaintainerCredential(); err == nil {
		t.Fatal("read public credential")
	}
	if err := home.WriteMaintainerCredential(first); err == nil {
		t.Fatal("overwrote public credential")
	}
	if err := os.Remove(filepath.Join(path, maintainerCredentialName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(path, tokenName), filepath.Join(path, maintainerCredentialName)); err != nil {
		t.Fatal(err)
	}
	if _, err := home.ReadMaintainerCredential(); err == nil {
		t.Fatal("followed credential symlink")
	}
	if err := home.WriteMaintainerCredential(first); err == nil {
		t.Fatal("replaced credential symlink")
	}
}
