package browserprotocol

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxIngestURLBytes bounds the telemetry ingest URL the factory names.
const maxIngestURLBytes = 8192

// TelemetryIngest reads the telemetry ingest endpoint ("status"), mints a new
// secret for it ("mint", which rotates), or revokes it ("revoke").
type TelemetryIngest struct {
	Action string `json:"action"`
}

// TelemetryIngestResult names the ingest URL. Secret is the bearer, non-empty
// only in the answer to a mint: the factory keeps only its digest.
type TelemetryIngestResult struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
	Active Bool   `json:"active"`
}

func EncodeTelemetryIngestResult(id string, value TelemetryIngestResult) ([]byte, error) {
	return encodeControl(TypeTelemetryIngestResult, id, value)
}

// MaxPushEndpointBytes bounds a push service URL; the services in use issue
// URLs a few hundred bytes long.
const MaxPushEndpointBytes = 2048

// pushServiceHosts are the only origins a factory will ever POST a push to.
// A subscription names the endpoint, and any observing client may register
// one, so without this list the verb would be a daemon-side request to an
// address of the client's choosing. These are the push services behind every
// browser that can install the console; Edge's is per-tenant under one
// suffix.
var pushServiceHosts = map[string]bool{
	"web.push.apple.com":                true,
	"fcm.googleapis.com":                true,
	"updates.push.services.mozilla.com": true,
}

// pushServiceEndpoint reports whether a subscription endpoint belongs to a
// known push service: https, one of the listed hosts or an Edge tenant, no
// credentials, no explicit port.
func pushServiceEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Host != parsed.Hostname() {
		return false
	}
	host := parsed.Hostname()
	return pushServiceHosts[host] || strings.HasSuffix(host, ".notify.windows.com") && len(host) > len(".notify.windows.com")
}

// PushSubscribe hands the factory one device's Web Push subscription and the
// VAPID key pair the device generated for it. The device owns the key so one
// subscription can serve every factory it pairs with: a push service binds a
// subscription to exactly one application server key, and the daemons cannot
// share one. Holding the private key only lets a factory send this device
// empty "needs you" pushes, which is the whole point of handing it over.
type PushSubscribe struct {
	Endpoint   string `json:"endpoint"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// PushSubscribeResult carries nothing: the subscription is stored or the
// request is refused.
type PushSubscribeResult struct{}

func EncodePushSubscribeResult(id string, value PushSubscribeResult) ([]byte, error) {
	return encodeControl(TypePushSubscribeResult, id, value)
}

// base64url without padding, as PushManager and WebCrypto exports are carried.
func base64URL(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

func validRemoteControl(kind MessageType, body any) error {
	body = indirect(body)
	bad := func() error { return fmt.Errorf("%w: invalid %s", ErrMalformed, kind) }
	printable := func(value string) bool {
		if !utf8.ValidString(value) {
			return false
		}
		for _, character := range value {
			if character < 0x20 || character == 0x7f {
				return false
			}
		}
		return true
	}
	switch value := body.(type) {
	case PushSubscribeResult:
	case TelemetryIngest:
		if value.Action != "status" && value.Action != "mint" && value.Action != "revoke" {
			return bad()
		}
	case TelemetryIngestResult:
		// The secret is 32 bytes as unpadded base64url, the only bearer the relay admits.
		if len(value.URL) > maxIngestURLBytes || !printable(value.URL) || !strings.HasSuffix(value.URL, "/v1/traces") ||
			!strings.HasPrefix(value.URL, "https://") && !strings.HasPrefix(value.URL, "http://") ||
			value.Secret != "" && (!base64URL(value.Secret, 43, 43) || !bool(value.Active)) {
			return bad()
		}
	case PushSubscribe:
		// 87 characters is exactly one uncompressed P-256 point; a PKCS#8
		// P-256 private key exports to 138 bytes, bounded loosely.
		if len(value.Endpoint) > MaxPushEndpointBytes || !printable(value.Endpoint) || !pushServiceEndpoint(value.Endpoint) ||
			!base64URL(value.PublicKey, 87, 87) || !base64URL(value.PrivateKey, 1, 512) {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
