//go:build darwin

package api

import (
	"testing"
	"unsafe"
)

func TestDarwinPeerCredentialLayoutMatchesXucred(t *testing.T) {
	if size := unsafe.Sizeof(darwinPeerCredential{}); size != 76 {
		t.Fatalf("xucred size = %d, want 76", size)
	}
}
