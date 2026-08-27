package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const browserSubscriptionQueue = 8

// browserStateSubscription has exactly one producer goroutine. Cancel is
// nonblocking; Done is the join proof; Err becomes immutable before Done
// closes. The WebSocket connection owns Cancel/Done during ordinary service,
// while browserBackend.Close is the final composition-root backstop.
type browserStateSubscription struct {
	backend  *browserBackend
	clientID [browserprotocol.ClientIDSize]byte
	after    kernel.EventSequence

	ctx     context.Context
	cancel  context.CancelFunc
	updates chan browser.StateUpdate
	done    chan struct{}
	once    sync.Once

	errMu sync.Mutex
	err   error
}

func (backend *browserBackend) SubscribeState(ctx context.Context, rawClient [browserprotocol.ClientIDSize]byte, after browserprotocol.Decimal) (browser.StateSubscription, error) {
	if uint64(after) > browserprotocol.MaxSQLiteInteger {
		return nil, browser.ErrStale
	}
	sequence, err := kernel.NewEventSequence(int64(after))
	if err != nil {
		return nil, browser.ErrStale
	}
	_, release, _, err := backend.authorize(ctx, rawClient, kernel.BrowserCapabilityObserve)
	if err != nil {
		return nil, err
	}
	release()

	backend.subMu.Lock()
	if backend.closing {
		backend.subMu.Unlock()
		return nil, browser.ErrUnauthorized
	}
	ownerContext, cancel := context.WithCancel(context.Background())
	subscription := &browserStateSubscription{
		backend: backend, clientID: rawClient, after: sequence,
		ctx: ownerContext, cancel: cancel, updates: make(chan browser.StateUpdate, browserSubscriptionQueue), done: make(chan struct{}),
	}
	backend.subs[subscription] = struct{}{}
	backend.subMu.Unlock()
	go subscription.run()
	return subscription, nil
}

func (subscription *browserStateSubscription) Updates() <-chan browser.StateUpdate {
	if subscription == nil {
		return nil
	}
	return subscription.updates
}

func (subscription *browserStateSubscription) Cancel() {
	if subscription != nil {
		subscription.once.Do(subscription.cancel)
	}
}

func (subscription *browserStateSubscription) Done() <-chan struct{} {
	if subscription == nil {
		return nil
	}
	return subscription.done
}

func (subscription *browserStateSubscription) Err() error {
	if subscription == nil {
		return browser.ErrSubscriptionUnresolved
	}
	subscription.errMu.Lock()
	defer subscription.errMu.Unlock()
	return subscription.err
}

func (subscription *browserStateSubscription) run() {
	var result error
	defer func() {
		subscription.errMu.Lock()
		subscription.err = result
		subscription.errMu.Unlock()
		close(subscription.updates)
		subscription.backend.removeSubscription(subscription)
		close(subscription.done)
	}()

	for {
		batch, err := subscription.readBatch()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			var restart *browser.RestartError
			if errors.As(err, &restart) {
				if !subscription.send(browser.StateUpdate{Restart: &restart.State}) {
					return
				}
				return
			}
			result = err
			return
		}
		if len(batch.Invalidations) == 0 {
			if !subscription.waitForChange() {
				return
			}
			continue
		}
		for _, invalidation := range batch.Invalidations {
			update, restart, err := projectInvalidation(batch, invalidation)
			if err != nil {
				result = err
				return
			}
			if restart != nil {
				if !subscription.send(browser.StateUpdate{Restart: restart}) {
					return
				}
				return
			}
			if !subscription.send(browser.StateUpdate{Event: &update, Floor: decimalSequence(batch.Floor)}) {
				return
			}
			subscription.after = invalidation.Sequence
		}
		// A full batch may have more retained records. Drain it immediately;
		// otherwise wait for a mutation signal or the bounded poll fallback.
		if len(batch.Invalidations) < kernel.WatchBatchLimit && subscription.after == batch.Head {
			if !subscription.waitForChange() {
				return
			}
		}
	}
}

func (subscription *browserStateSubscription) readBatch() (kernel.WatchBatch, error) {
	_, release, _, err := subscription.backend.authorize(subscription.ctx, subscription.clientID, kernel.BrowserCapabilityObserve)
	if err != nil {
		return kernel.WatchBatch{}, err
	}
	defer release()
	batch, err := subscription.backend.store.WatchAfter(subscription.ctx, subscription.after)
	if err == nil {
		return batch, nil
	}
	var pruned *kernel.ResyncRequiredError
	if errors.As(err, &pruned) {
		return kernel.WatchBatch{}, &browser.RestartError{State: browserprotocol.StateRestart{
			Head: decimalSequence(pruned.Head), Floor: decimalSequence(pruned.Floor), Reason: browserprotocol.RestartPruned,
		}}
	}
	var restart *kernel.WatchRestartError
	if errors.As(err, &restart) {
		reason := browserprotocol.RestartGap
		if restart.Reason == kernel.WatchRestartHiddenDependency {
			reason = browserprotocol.RestartHiddenDependency
		} else if restart.Reason != kernel.WatchRestartGap {
			return kernel.WatchBatch{}, fmt.Errorf("unknown kernel watch restart")
		}
		return kernel.WatchBatch{}, &browser.RestartError{State: browserprotocol.StateRestart{
			Head: decimalSequence(restart.Head), Floor: decimalSequence(restart.Floor), Reason: reason,
		}}
	}
	if errors.Is(err, kernel.ErrFutureCursor) {
		state, stateErr := subscription.backend.store.Factory(subscription.ctx)
		if stateErr != nil {
			return kernel.WatchBatch{}, stateErr
		}
		return kernel.WatchBatch{}, &browser.RestartError{State: browserprotocol.StateRestart{
			Head: decimalSequence(state.Head), Floor: decimalSequence(state.Floor), Reason: browserprotocol.RestartGap,
		}}
	}
	return kernel.WatchBatch{}, mapBrowserError(err)
}

func (subscription *browserStateSubscription) send(update browser.StateUpdate) bool {
	select {
	case subscription.updates <- update:
		return true
	case <-subscription.ctx.Done():
		return false
	}
}

func (subscription *browserStateSubscription) waitForChange() bool {
	timer := time.NewTimer(browserStatePollInterval)
	defer timer.Stop()
	subscription.backend.subMu.Lock()
	signal := subscription.backend.stateSignal
	subscription.backend.subMu.Unlock()
	select {
	case <-subscription.ctx.Done():
		return false
	case <-signal:
		return true
	case <-timer.C:
		return true
	}
}

func (backend *browserBackend) signalStateChanged() {
	backend.subMu.Lock()
	if !backend.closing {
		close(backend.stateSignal)
		backend.stateSignal = make(chan struct{})
	}
	backend.subMu.Unlock()
}

func (backend *browserBackend) removeSubscription(subscription *browserStateSubscription) {
	backend.subMu.Lock()
	delete(backend.subs, subscription)
	backend.subMu.Unlock()
}

func (backend *browserBackend) close() error {
	if backend == nil {
		return nil
	}
	backend.subMu.Lock()
	if backend.closing {
		subscriptions := make([]*browserStateSubscription, 0, len(backend.subs))
		for subscription := range backend.subs {
			subscriptions = append(subscriptions, subscription)
		}
		backend.subMu.Unlock()
		for _, subscription := range subscriptions {
			<-subscription.done
		}
		return nil
	}
	backend.closing = true
	close(backend.stateSignal)
	subscriptions := make([]*browserStateSubscription, 0, len(backend.subs))
	for subscription := range backend.subs {
		subscriptions = append(subscriptions, subscription)
	}
	backend.subMu.Unlock()
	for _, subscription := range subscriptions {
		subscription.Cancel()
	}
	for _, subscription := range subscriptions {
		<-subscription.done
	}
	return nil
}

func projectInvalidation(batch kernel.WatchBatch, invalidation kernel.Invalidation) (browserprotocol.StateEvent, *browserprotocol.StateRestart, error) {
	sequence := decimalSequence(invalidation.Sequence)
	head := decimalSequence(batch.Head)
	switch invalidation.EntityKind {
	case "factory":
		if invalidation.EntityID != "00000000000000000000000000000000" || invalidation.Deleted {
			return browserprotocol.StateEvent{}, nil, fmt.Errorf("invalid factory invalidation")
		}
		return browserprotocol.EntityChangedEvent(browserprotocol.EntityChanged{
			Sequence: sequence, Head: head, EntityKind: browserprotocol.StateFactory, EntityID: "factory",
			Revision: decimalRevision(invalidation.Revision), Deleted: false,
		}), nil, nil
	case "project", "agent", "task", "human_request":
		kind, err := parseInvalidationKind(invalidation.EntityKind)
		if err != nil {
			return browserprotocol.StateEvent{}, nil, err
		}
		if _, err := parseID(invalidation.EntityID); err != nil {
			return browserprotocol.StateEvent{}, nil, fmt.Errorf("invalid public invalidation identity")
		}
		return browserprotocol.EntityChangedEvent(browserprotocol.EntityChanged{
			Sequence: sequence, Head: head, EntityKind: kind, EntityID: invalidation.EntityID,
			Revision: decimalRevision(invalidation.Revision), Deleted: browserprotocol.Bool(invalidation.Deleted),
		}), nil, nil
	case "change":
		return browserprotocol.HiddenAdvanceEvent(browserprotocol.HiddenAdvance{Sequence: sequence, Head: head}), nil, nil
	case "run":
		restart := browserprotocol.StateRestart{Head: head, Floor: decimalSequence(batch.Floor), Reason: browserprotocol.RestartHiddenDependency}
		return browserprotocol.StateEvent{}, &restart, nil
	default:
		return browserprotocol.StateEvent{}, nil, fmt.Errorf("unknown kernel invalidation kind")
	}
}

func parseInvalidationKind(value string) (browserprotocol.StateKind, error) {
	switch value {
	case "project":
		return browserprotocol.StateProject, nil
	case "agent":
		return browserprotocol.StateAgent, nil
	case "task":
		return browserprotocol.StateTask, nil
	case "human_request":
		return browserprotocol.StateHumanRequest, nil
	default:
		return "", fmt.Errorf("unknown public invalidation kind")
	}
}
