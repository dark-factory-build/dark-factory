//go:build factory_test

package maintainer

import (
	"net/http"

	"github.com/dark-factory-build/dark-factory/internal/install"
)

// NewClientForFactoryTest is a test-build-only transport seam. Production
// callers continue to use NewClient, which pins the broker origin and its
// no-redirect, Proxy:nil transport.
func NewClientForFactoryTest(origin string, transport http.RoundTripper) *Client {
	client := NewClient()
	client.origin = origin
	client.http.Transport = transport
	return client
}

// OpenHostForFactoryTest preserves OpenHost credential validation while
// replacing only the test broker transport.
func OpenHostForFactoryTest(home *install.OperationalHome, client *Client) (*Host, error) {
	host, err := OpenHost(home)
	if err != nil {
		return nil, err
	}
	host.client = client
	return host, nil
}
