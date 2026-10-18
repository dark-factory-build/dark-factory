package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// pushSubscriptionsFileName sits in the relay directory beside the node key:
// a push subscription is only ever registered by a device that reached this
// factory through the relay, and the relay directory is the one home member
// whose contents the home census leaves alone.
const pushSubscriptionsFileName = "push-subscriptions.json"
const pushAlertsFileName = "push-alerts.json"

// pushStore is the durable set of device subscriptions, one per paired
// browser client. It is not authority: a device that loses it re-registers on
// its next connection, and a client the kernel has revoked is dropped the
// next time a push would have gone to it.
type pushStore struct {
	path string
}

// pushFileMu serialises every read-modify-write of a subscriptions file in
// this process. A relay closed and dialled again on the same home makes a
// second pushStore for the same file, and a notification still in flight on
// the first must not save its stale snapshot over what the second stored.
//
// ponytail: one process-wide lock; a per-path lock table if a daemon ever
// serves more than one home at once.
var pushFileMu sync.Mutex

func newPushStore(relayDirectory string) *pushStore {
	return &pushStore{path: filepath.Join(relayDirectory, pushSubscriptionsFileName)}
}

type pushAlertState struct {
	SentDay string `json:"sent_day,omitempty"`
}

func (store *pushStore) alertPath() string {
	return filepath.Join(filepath.Dir(store.path), pushAlertsFileName)
}

// healthAlert records the level-triggered condition and returns whether this
// transition may notify. The day stamp is the durable daily rate limit;
// Active is cleared by recovery so a healthy period is visible after restart.
func (store *pushStore) healthAlert(condition, day string, active bool) (bool, error) {
	if store == nil {
		return false, nil
	}
	pushFileMu.Lock()
	defer pushFileMu.Unlock()
	states := map[string]pushAlertState{}
	data, err := os.ReadFile(store.alertPath())
	if err == nil {
		if err := json.Unmarshal(data, &states); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	state := states[condition]
	alert := active && state.SentDay != day
	if alert {
		state.SentDay = day
	}
	states[condition] = state
	data, err = json.Marshal(states)
	if err != nil {
		return false, err
	}
	temporary := store.alertPath() + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return false, err
	}
	return alert, os.Rename(temporary, store.alertPath())
}

func (store *pushStore) load() (map[string]browserprotocol.PushSubscribe, error) {
	subscriptions := map[string]browserprotocol.PushSubscribe{}
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return subscriptions, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &subscriptions); err != nil {
		return nil, err
	}
	return subscriptions, nil
}

func (store *pushStore) save(subscriptions map[string]browserprotocol.PushSubscribe) error {
	data, err := json.Marshal(subscriptions)
	if err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, store.path)
}

// update applies one change under the lock, so two devices registering at
// once cannot lose each other's subscription.
func (store *pushStore) update(change func(map[string]browserprotocol.PushSubscribe)) error {
	if store == nil {
		return fmt.Errorf("%w: push is not enabled", kernel.ErrNotFound)
	}
	pushFileMu.Lock()
	defer pushFileMu.Unlock()
	subscriptions, err := store.load()
	if err != nil {
		return err
	}
	change(subscriptions)
	return store.save(subscriptions)
}

// parsePushKeys checks that the private key the device handed over is the
// P-256 key behind the public key its subscription was made with, so a
// subscription that could never be signed for is refused at registration.
func parsePushKeys(subscription browserprotocol.PushSubscribe) (*ecdsa.PrivateKey, error) {
	der, err := base64.RawURLEncoding.DecodeString(subscription.PrivateKey)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("push key is not P-256")
	}
	if base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y)) != subscription.PublicKey {
		return nil, fmt.Errorf("push public key does not match its private key")
	}
	return key, nil
}

// pushAuthorization mints the VAPID header for one push service (RFC 8292):
// an ES256 JWT over the service origin, presented with the public key the
// subscription was created against.
func pushAuthorization(endpoint string, key *ecdsa.PrivateKey, publicKey string, now time.Time) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	audience := parsed.Scheme + "://" + parsed.Host
	claims, err := json.Marshal(map[string]any{"aud": audience, "exp": now.Add(12 * time.Hour).Unix(), "sub": "https://darkfactory.build"})
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + signing + "." + base64.RawURLEncoding.EncodeToString(signature) + ", k=" + publicKey, nil
}

// sendPush delivers one empty "needs you" push. It reports whether the push
// service says the subscription is gone for good, which is the only outcome
// that changes what this factory stores.
//
// ponytail: no payload, so no RFC 8291 encryption; the phone shows a fixed
// notice and opens the console. Add aes128gcm here when the alert must name
// the factory or the question.
func sendPush(ctx context.Context, client *http.Client, subscription browserprotocol.PushSubscribe, now time.Time) (gone bool, err error) {
	key, err := parsePushKeys(subscription)
	if err != nil {
		return true, err
	}
	authorization, err := pushAuthorization(subscription.Endpoint, key, subscription.PublicKey, now)
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, subscription.Endpoint, http.NoBody)
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("TTL", "86400")
	request.Header.Set("Urgency", "high")
	// One topic per device: a burst of questions collapses into one alert.
	request.Header.Set("Topic", "needs-you")
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return true, nil
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return false, fmt.Errorf("push service answered %d", response.StatusCode)
	}
	return false, nil
}

// notifyPush wakes every subscribed device that is still a live client of
// this factory. It runs after the question is durable and never holds the
// caller: a push service that is slow or down costs the operator nothing but
// the alert.
func (daemon *Daemon) notifyPush(ctx context.Context, client *http.Client) {
	daemon.browserMu.Lock()
	store := daemon.push
	daemon.browserMu.Unlock()
	if store == nil {
		return
	}
	var drop []string
	_ = store.update(func(subscriptions map[string]browserprotocol.PushSubscribe) {
		for id, subscription := range subscriptions {
			raw, err := hex.DecodeString(id)
			clientID, idErr := kernel.BrowserClientIDFromBytes(raw)
			if err != nil || idErr != nil {
				drop = append(drop, id)
				continue
			}
			known, found, err := daemon.store.BrowserClient(ctx, clientID)
			if err == nil && (!found || known.RevokedAt != nil) {
				drop = append(drop, id)
				continue
			}
			if gone, _ := sendPush(ctx, client, subscription, daemon.now()); gone {
				drop = append(drop, id)
			}
		}
		for _, id := range drop {
			delete(subscriptions, id)
		}
	})
}

func (daemon *Daemon) checkFactoryAlerts(ctx context.Context) {
	now := daemon.now()
	at, err := kernel.NewUnixMillis(now.UnixMilli())
	if err != nil {
		return
	}
	signal, err := daemon.store.AlertSignals(ctx, at.Int64())
	if err != nil {
		return
	}
	daemon.browserMu.Lock()
	store := daemon.push
	daemon.browserMu.Unlock()
	if store == nil {
		return
	}
	conditions := factoryAlertConditions(signal, daemon.intakeReadyWork(), now.UnixMilli())
	day := now.UTC().Format("2006-01-02")
	for condition, active := range conditions {
		alert, err := store.healthAlert(condition, day, active)
		if err == nil && alert {
			go daemon.notifyPush(context.WithoutCancel(ctx), daemon.observed(pushClient))
		}
	}
}

func factoryAlertConditions(signal kernel.AlertSignals, intakeReady bool, nowMS int64) map[string]bool {
	return map[string]bool{
		"merge-stall": (signal.ReadyWork || intakeReady) && (signal.LastMergeAtMS == 0 || nowMS-signal.LastMergeAtMS >= 6*time.Hour.Milliseconds()),
		"run-failure": signal.RunsLastHour >= 10 && signal.FailuresLastHour*2 > signal.RunsLastHour,
	}
}

// pushClient bounds one push delivery; every service answers within a second
// when it answers at all.
var pushClient = &http.Client{Timeout: 10 * time.Second}
