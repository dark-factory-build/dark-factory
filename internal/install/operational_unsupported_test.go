//go:build !darwin

package install

import (
	"context"
	"errors"
	"testing"
)

func TestOpenOperationalHomeUnsupportedHasNoEffect(t *testing.T) {
	home, err := OpenOperationalHome(context.Background(), "/private/not-a-home")
	if home != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported operational open = %v, %v", home, err)
	}
}

func TestOperationalHomeOpenStoreUnsupportedHasNoEffect(t *testing.T) {
	home := &OperationalHome{state: &operationalHomeState{}}
	store, err := home.OpenStore(context.Background())
	if store != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported operational Store open = %v, %v", store, err)
	}
}

func TestOperationalHomeOpenLocalAPIUnsupportedHasNoEffect(t *testing.T) {
	home := &OperationalHome{state: &operationalHomeState{}}
	authority, err := home.OpenLocalAPI(context.Background())
	if authority != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported local API open = %v, %v", authority, err)
	}
	if authority.CheckOperator(make([]byte, 32)) {
		t.Fatal("nil unsupported authority accepted operator")
	}
}
