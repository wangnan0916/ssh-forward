package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// hostActorOptions carries the actor's run-time assembly. The reconnect
// policy (retryDelay/retryWait) is intentionally absent: it is configured
// through managerOptions (the Manager's single configuration surface) and
// handed to newHostActor as constructor arguments, so the pair cannot be
// configured inconsistently between the two structs.
type hostActorOptions struct {
	host      HostAlias
	connector HostConnector
	dialer    *currentDialer
	publish   func(hostView)
	ctx       context.Context
	// onObservation notifies the Manager that a new observation generation
	// was applied. It fires once per ObservationSet — not on connection
	// state or Discovery-only publications — so the reconciliation worker
	// advances its hysteresis exactly once per generation.
	onObservation func()
}

// hostActor owns one Development Host's Forwarding Session, Discovery State,
// and reconnect scheduling. Observation ingestion happens here, outside the
// Manager state lock, and blocking socket work can be scheduled from the
// ingestion path without holding either state lock.
type hostActor struct {
	host       HostAlias
	connector  HostConnector
	dialer     *currentDialer
	publish    func(hostView)
	retryDelay func(int) time.Duration
	retryWait  func(context.Context, time.Duration) bool
	onObserve  func()
	ctx        context.Context

	mu                      sync.Mutex
	active                  atomic.Bool
	session                 HostSession
	state                   hostView
	lastObservationSequence uint64

	lastPublished hostView
	done          chan struct{}
}

func newHostActor(options hostActorOptions, retryDelay func(int) time.Duration, retryWait func(context.Context, time.Duration) bool) *hostActor {
	return &hostActor{
		host:       options.host,
		connector:  options.connector,
		dialer:     options.dialer,
		publish:    options.publish,
		onObserve:  options.onObservation,
		retryDelay: retryDelay,
		retryWait:  retryWait,
		ctx:        options.ctx,
		state:      emptyHostView(options.host),
		done:       make(chan struct{}),
	}
}

// startIfNeeded launches the connect loop unless it is already running or
// the Manager is shutting down. It is idempotent. It publishes Connecting
// before the loop runs so the transition is visible as soon as arming
// returns; the loop publishes from Connected onward.
func (a *hostActor) startIfNeeded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.armed() {
		return
	}
	a.active.Store(true)
	a.state.Connection = ConnectionConnecting
	a.done = make(chan struct{})
	a.publishLocked()
	go a.run()
}

// armed is the single arming authority: the connect loop is either running
// (active) or the Manager is shutting down (ctx done).
func (a *hostActor) armed() bool {
	return a.active.Load() || a.ctx.Err() != nil
}

func (a *hostActor) isActive() bool {
	return a.active.Load()
}

// run marks the loop inactive on the way out. The connect loop itself also
// sets active false before publishing its final Disconnected transition, so
// a concurrently arriving command sees a completed session and can re-arm.
// done is captured up front: a re-arm replaces a.done with a fresh channel
// while this goroutine is still winding down, and each run must close only
// the channel it was launched with.
func (a *hostActor) run() {
	a.mu.Lock()
	done := a.done
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.active.Store(false)
		close(done)
		a.mu.Unlock()
	}()
	a.connect()
}

func (a *hostActor) connect() {
	retryAttempt := 0
	for {
		session, err := a.connector.Connect(a.ctx, a.host)
		if err != nil {
			if sessionDisposition(err) != SessionRetry {
				a.publishConnectionFailure()
				return
			}
			if !a.waitToReconnect(retryAttempt) {
				return
			}
			retryAttempt++
			continue
		}

		a.mu.Lock()
		if a.ctx.Err() != nil {
			a.mu.Unlock()
			closeHostSession(session)
			return
		}
		a.session = session
		a.dialer.Set(session)
		a.state.Connection = ConnectionConnected
		a.state.Discovery = startingDiscovery()
		a.lastObservationSequence = 0
		a.publishLocked()
		a.mu.Unlock()

		err = a.consumeSession(session)
		closeHostSession(session)
		disposition := sessionDisposition(err)

		a.mu.Lock()
		a.session = nil
		a.dialer.Set(nil)
		if disposition == SessionRetry {
			a.state.Connection = ConnectionConnecting
		} else {
			a.state.Connection = ConnectionDisconnected
			a.active.Store(false)
		}
		a.state.Discovery = stoppedDiscovery()
		a.publishLocked()
		a.mu.Unlock()
		if disposition != SessionRetry || !a.waitToReconnect(retryAttempt) {
			return
		}
		retryAttempt++
	}
}

func (a *hostActor) closeSession(ctx context.Context) error {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close(ctx)
}

func (a *hostActor) consumeSession(session HostSession) error {
	for {
		fact, err := session.Next(a.ctx)
		if err != nil {
			return err
		}
		a.applySessionFact(fact)
	}
}

func (a *hostActor) applySessionFact(fact SessionFact) {
	switch fact := fact.(type) {
	case ObservationSet:
		a.applyObservationSet(fact)
	case DiscoveryChange:
		a.applyDiscoveryChange(fact)
	default:
		a.applyInvalidDiscoveryFact()
	}
}

func (a *hostActor) applyObservationSet(set ObservationSet) {
	a.mu.Lock()
	defer a.mu.Unlock()
	gapped, ok := admitObservationSet(set, a.lastObservationSequence)
	if !ok {
		a.failDiscoveryLocked(ReasonSessionInvalid)
		return
	}
	a.lastObservationSequence = set.Sequence
	capability := set.Capability
	reason := set.CapabilityReason
	observations, truncated := boundListenerObservations(canonicalListenerObservations(set.Observations))
	degradeTruncatedCapability(&capability, truncated)
	if truncated.listeners || truncated.sockets || truncated.processes {
		reason = CapabilityReasonEvidenceTruncated
	}
	complete := capability.RemoteListeners == CapabilityFull
	if !complete {
		observations, truncated = mergeBoundedListenerObservations(a.state.ListenerObservations, observations)
		degradeTruncatedCapability(&capability, truncated)
		if truncated.listeners || truncated.sockets || truncated.processes {
			reason = CapabilityReasonEvidenceTruncated
		}
	}
	state := discoveryStateForCapability(capability)
	if gapped {
		state = DiscoveryDegraded
	}
	discovery := DiscoverySnapshot{
		State:               state,
		Capability:          capability,
		BaselineEstablished: complete || a.state.Discovery.BaselineEstablished,
		Diagnostic:          discoveryDiagnostic(gapped, capability, reason, ""),
		ScannerVersion:      set.ScannerVersion,
		ScannerChecksum:     set.ScannerChecksum,
	}
	a.state.Discovery = discovery
	a.state.ListenerObservations = observations
	a.publishLocked()
	if a.onObserve != nil {
		a.onObserve()
	}
}

func (a *hostActor) applyDiscoveryChange(change DiscoveryChange) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !admitDiscoveryChange(change) {
		a.failDiscoveryLocked(ReasonSessionInvalid)
		return
	}
	diagnostic := discoveryFailureDiagnostic(change.Reason)
	discovery := a.state.Discovery
	discovery.State = change.State
	discovery.Capability = change.Capability
	discovery.Diagnostic = diagnostic
	a.state.Discovery = discovery
	a.publishLocked()
}

func (a *hostActor) applyInvalidDiscoveryFact() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failDiscoveryLocked(ReasonSessionInvalid)
}

// failDiscoveryLocked is the actor's own failure verdict, reached through
// admission: a misbehaving adapter (stale sequence, bad capability, unknown
// budget, invalid report) must not corrupt the mirror.
func (a *hostActor) failDiscoveryLocked(reason DiscoveryReason) {
	diagnostic := discoveryFailureDiagnostic(reason)
	discovery := a.state.Discovery
	discovery.State = DiscoveryFailed
	discovery.Diagnostic = diagnostic
	a.state.Discovery = discovery
	a.publishLocked()
}

func (a *hostActor) publishConnectionFailure() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active.Store(false)
	a.state.Connection = ConnectionDisconnected
	a.state.Discovery = stoppedDiscovery()
	a.publishLocked()
}

// publishLocked publishes one host view and is the single place that
// suppresses no-change publications: it compares against the last view it
// handed to the Manager. Callers mutate state and publish unconditionally, so
// new state fields get dedup for free.
func (a *hostActor) publishLocked() {
	if reflect.DeepEqual(a.lastPublished, a.state) {
		return
	}
	a.lastPublished = a.state
	a.publish(a.state)
}

func (a *hostActor) waitToReconnect(attempt int) bool {
	return a.retryWait(a.ctx, a.retryDelay(attempt))
}

func exponentialJitterDelay(attempt int) time.Duration {
	const (
		initial = 250 * time.Millisecond
		maximum = 30 * time.Second
	)
	delay := initial
	for range min(attempt, 7) {
		delay = min(delay*2, maximum)
	}
	jitter := delay / 5
	return delay - jitter + time.Duration(rand.Int64N(int64(2*jitter)+1))
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// sessionDisposition collapses SessionSuspend and SessionClosed into one
// non-retry terminal: both end the connect loop, and reconnect policy does
// not yet distinguish "user must fix authentication" from "session shut
// down".
func sessionDisposition(err error) SessionDisposition {
	if errors.Is(err, context.Canceled) {
		return SessionClosed
	}
	var sessionError *SessionError
	if errors.As(err, &sessionError) {
		return sessionError.Disposition
	}
	return SessionRetry
}

func closeHostSession(session HostSession) {
	closeWithTimeout(session.Close, 5*time.Second)
}

var errTransportUnavailable = errors.New("Development Host transport is unavailable")

// currentDialer is the concurrency-safe holder of the live Forwarding
// Session's data path. The actor sets it exactly when it installs or removes
// the session; Forward endpoint allocation reads through it, so endpoints
// survive session replacement without holding either state lock.
type currentDialer struct {
	mu     sync.RWMutex
	dialer Dialer
}

func (d *currentDialer) Set(dialer Dialer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialer = dialer
}

func (d *currentDialer) DialContext(ctx context.Context, target netip.AddrPort) (HalfCloseConn, error) {
	d.mu.RLock()
	dialer := d.dialer
	d.mu.RUnlock()
	if dialer == nil {
		return nil, errTransportUnavailable
	}
	return dialer.DialContext(ctx, target)
}
