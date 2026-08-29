package main

import "testing"

func TestDirectBuildHasNoWrapperReceipt(t *testing.T) {
	if wrapperReceipt != "" {
		t.Fatalf("direct build unexpectedly carried wrapper receipt %q", wrapperReceipt)
	}
	if requiredWrapperReceipt == "" {
		t.Fatal("wrapper receipt contract is empty")
	}
}
