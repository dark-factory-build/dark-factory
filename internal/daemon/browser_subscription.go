package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// One slot is enough. An update carries only a head, and the producer always
// rereads the current durable head before sending, so a burst of mutations
// collapses into one notification carrying the greatest head.
const browserStateWatchQueue = 1

// browserStateWatch has exactly one producer goroutine. Cancel is nonblocking;
// Done is the join proof; Err becomes immutable before Done closes. The
// WebSocket connection owns Cancel/Done during ordinary service, while
// browserBackend.close is the final composition-root backstop.
type browserStateWatch struct {
	backend  *browserBackend
	clientID [browserprotocol.ClientIDSize]byte
	// notified is the greatest head already delivered to this subscriber.
	notified kernel.EventSequence

	ctx     context.Context
	cancel  context.CancelFunc
	updates chan browser.StateUpdate
	done    chan struct{}
	once    sync.Once

	errMu sync.Mutex
	err   error
}

// WatchState reads the durable head once to decide whether the requested
// after_head can ever be satisfied, and refuses a head above it as stale
// rather than installing a producer that could never notify. A store read that
// fails here fails the whole watch finitely: no watcher is registered, no
// goroutine is started, and the caller gets one mapped error.
//
// Registration still happens before the producer reads the head again, and
// that reread -- the producer's first action, not this one -- is what closes
// the snapshot-to-watch gap: a commit landing between the caller's snapshot
// and this registration is delivered immediately instead of waiting for the
// next poll.
func (backend *browserBackend) WatchState(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, afterHead browserprotocol.Decimal) (browser.StateSubscription, error) {
	if uint64(afterHead) > browserprotocol.MaxSQLiteInteger {
		return nil, browser.ErrStale
	}
	after, err := kernel.NewEventSequence(int64(afterHead))
	if err != nil {
		return nil, browser.ErrStale
	}
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return nil, err
	}
	state, headErr := backend.store.Factory(ctx)
	release()
	if headErr != nil {
		return nil, mapBrowserError(headErr)
	}
	// A watcher can only ever announce a head the durable store has reached, so
	// an after_head above the current head would install a producer that never
	// notifies. That is a stale client view, and it gets one finite answer
	// rather than a silent subscription.
	if after.Int64() > state.Head.Int64() {
		return nil, browser.ErrStale
	}

	backend.subMu.Lock()
	if backend.closing {
		backend.subMu.Unlock()
		return nil, browser.ErrUnauthorized
	}
	ownerContext, cancel := context.WithCancel(context.Background())
	watch := &browserStateWatch{
		backend: backend, clientID: rawClient, notified: after,
		ctx: ownerContext, cancel: cancel, updates: make(chan browser.StateUpdate, browserStateWatchQueue), done: make(chan struct{}),
	}
	backend.subs[watch] = struct{}{}
	backend.subMu.Unlock()
	go watch.run()
	return watch, nil
}

func (watch *browserStateWatch) Updates() <-chan browser.StateUpdate {
	if watch == nil {
		return nil
	}
	return watch.updates
}

func (watch *browserStateWatch) Cancel() {
	if watch != nil {
		watch.once.Do(watch.cancel)
	}
}

func (watch *browserStateWatch) Done() <-chan struct{} {
	if watch == nil {
		return nil
	}
	return watch.done
}

func (watch *browserStateWatch) Err() error {
	if watch == nil {
		return browser.ErrSubscriptionUnresolved
	}
	watch.errMu.Lock()
	defer watch.errMu.Unlock()
	return watch.err
}

func (watch *browserStateWatch) run() {
	watch.runReading(watch.readHead)
}

func (watch *browserStateWatch) runReading(readHead func() (kernel.EventSequence, error)) {
	var result error
	defer func() {
		watch.errMu.Lock()
		watch.err = result
		watch.errMu.Unlock()
		close(watch.updates)
		watch.backend.removeSubscription(watch)
		close(watch.done)
	}()

	for {
		if watch.ctx.Err() != nil {
			return
		}
		head, err := readHead()
		if err != nil {
			if watch.ctx.Err() != nil {
				return
			}
			result = err
			return
		}
		if head.Int64() > watch.notified.Int64() {
			watch.notified = head
			if !watch.send(browser.StateUpdate{Head: decimalSequence(head)}) {
				return
			}
			continue
		}
		if !watch.wait() {
			return
		}
	}
}

func (watch *browserStateWatch) readHead() (kernel.EventSequence, error) {
	// Authority is reloaded on every head read, so revocation terminates the
	// producer rather than leaving a watcher observing a revoked client.
	_, release, _, err := watch.backend.authorize(watch.ctx, watch.clientID, kernel.BrowserCapabilityObserve)
	if err != nil {
		return kernel.EventSequence{}, err
	}
	defer release()
	state, err := watch.backend.store.Factory(watch.ctx)
	if err != nil {
		return kernel.EventSequence{}, mapBrowserError(err)
	}
	return state.Head, nil
}

func (watch *browserStateWatch) send(update browser.StateUpdate) bool {
	select {
	case watch.updates <- update:
		return true
	case <-watch.ctx.Done():
		return false
	}
}

// wait is the bounded change poll. Cancellation is the only other way out, so
// a cancelled subscription joins within one poll interval at worst.
func (watch *browserStateWatch) wait() bool {
	timer := time.NewTimer(browserStatePollInterval)
	defer timer.Stop()
	select {
	case <-watch.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (backend *browserBackend) removeSubscription(watch *browserStateWatch) {
	backend.subMu.Lock()
	delete(backend.subs, watch)
	backend.subMu.Unlock()
}

func (backend *browserBackend) close() error {
	if backend == nil {
		return nil
	}
	backend.subMu.Lock()
	if backend.closing {
		watches := make([]*browserStateWatch, 0, len(backend.subs))
		for watch := range backend.subs {
			watches = append(watches, watch)
		}
		backend.subMu.Unlock()
		for _, watch := range watches {
			<-watch.done
		}
		return nil
	}
	backend.closing = true
	watches := make([]*browserStateWatch, 0, len(backend.subs))
	for watch := range backend.subs {
		watches = append(watches, watch)
	}
	backend.subMu.Unlock()
	for _, watch := range watches {
		watch.Cancel()
	}
	for _, watch := range watches {
		<-watch.done
	}
	return nil
}
