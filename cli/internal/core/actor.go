package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/netip"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"ssh-forward/cli/internal/proxy"
)

// hostState is the per-host state the hostActor publishes to the Manager for
// canonical Snapshot publication. The actor owns the authoritative state; the
// Manager's copy changes only through publishHostState, so the two never
// diverge by convention.
type hostState struct {
	connection           ConnectionState
	discovery            DiscoverySnapshot
	listenerObservations []ListenerObservation
	listenerLifetimes    []ListenerLifetimeSnapshot
}

type hostActorOptions struct {
	host       HostAlias
	connector  hostConnector
	dialer     *currentDialer
	publish    func(hostState)
	retryDelay func(int) time.Duration
	retryWait  func(context.Context, time.Duration) bool
	ctx        context.Context
}

// hostActor owns one Development Host's Forwarding Session, Discovery State,
// and reconnect scheduling. It is the per-host seam where Listener Lifetime
// and Policy reconciliation will run: observation ingestion happens here,
// outside the Manager state lock, and blocking socket work can be scheduled
// from the ingestion path without holding either state lock.
type hostActor struct {
	host       HostAlias
	connector  hostConnector
	dialer     *currentDialer
	publish    func(hostState)
	retryDelay func(int) time.Duration
	retryWait  func(context.Context, time.Duration) bool
	ctx        context.Context

	mu                      sync.Mutex
	started                 bool
	session                 hostSession
	connection              ConnectionState
	discovery               DiscoverySnapshot
	listenerObservations    []ListenerObservation
	listenerLifetimes       []ListenerLifetimeSnapshot
	lastObservationSequence uint64

	tracker *lifetimeTracker
	done    chan struct{}
}

func newHostActor(options hostActorOptions) *hostActor {
	return &hostActor{
		host:       options.host,
		connector:  options.connector,
		dialer:     options.dialer,
		publish:    options.publish,
		retryDelay: options.retryDelay,
		retryWait:  options.retryWait,
		ctx:        options.ctx,
		connection: ConnectionDisconnected,
		discovery:  stoppedDiscovery(),
		tracker:    newLifetimeTracker(defaultListenerGraceCycles),
		done:       make(chan struct{}),
	}
}

// start launches the connect loop exactly once. The Manager has already
// published the Connecting transition synchronously under its own lock, so
// start publishes nothing itself; the loop publishes from Connected onward.
func (a *hostActor) start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return
	}
	a.started = true
	a.connection = ConnectionConnecting
	go a.run()
}

func (a *hostActor) isStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

func (a *hostActor) run() {
	defer close(a.done)
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
		a.connection = ConnectionConnected
		a.discovery = startingDiscovery()
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
			a.connection = ConnectionConnecting
		} else {
			a.connection = ConnectionDisconnected
		}
		a.discovery = stoppedDiscovery()
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

func (a *hostActor) consumeSession(session hostSession) error {
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
	if set.Sequence == 0 || set.Sequence <= a.lastObservationSequence || !validDiscoveryCapability(set.Capability) || !validObservationBudget(set.Budget) {
		a.failDiscoveryLocked("invalid_session_fact")
		return
	}
	gapped := set.Sequence != a.lastObservationSequence+1
	a.lastObservationSequence = set.Sequence
	capability := set.Capability
	observations, truncated := boundListenerObservations(canonicalListenerObservations(set.Observations))
	degradeTruncatedCapability(&capability, truncated)
	complete := capability.RemoteListeners == CapabilityFull
	if !complete {
		observations, truncated = mergeBoundedListenerObservations(a.listenerObservations, observations)
		degradeTruncatedCapability(&capability, truncated)
	}
	discovery := DiscoverySnapshot{
		State:               discoveryStateForCapability(capability),
		Capability:          capability,
		BaselineEstablished: complete || a.discovery.BaselineEstablished,
		ScannerVersion:      set.ScannerVersion,
		ScannerChecksum:     set.ScannerChecksum,
	}
	if gapped {
		discovery.State = DiscoveryDegraded
		discovery.Diagnostic = "observation_resync"
	}
	if reflect.DeepEqual(a.discovery, discovery) && reflect.DeepEqual(a.listenerObservations, observations) {
		return
	}
	a.discovery = discovery
	a.listenerObservations = observations
	a.listenerLifetimes = a.tracker.advance(observations)
	a.publishLocked()
}

func (a *hostActor) applyDiscoveryChange(change DiscoveryChange) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if (change.State != DiscoveryDegraded && change.State != DiscoveryFailed) ||
		!validDiscoveryCapability(change.Capability) || len(change.Diagnostic) > 128 || !utf8.ValidString(change.Diagnostic) {
		a.failDiscoveryLocked("invalid_session_fact")
		return
	}
	discovery := a.discovery
	discovery.State = change.State
	discovery.Capability = change.Capability
	discovery.Diagnostic = change.Diagnostic
	if reflect.DeepEqual(a.discovery, discovery) {
		return
	}
	a.discovery = discovery
	a.publishLocked()
}

func (a *hostActor) applyInvalidDiscoveryFact() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failDiscoveryLocked("invalid_session_fact")
}

func (a *hostActor) failDiscoveryLocked(diagnostic string) {
	discovery := a.discovery
	discovery.State = DiscoveryFailed
	discovery.Diagnostic = diagnostic
	if reflect.DeepEqual(a.discovery, discovery) {
		return
	}
	a.discovery = discovery
	a.publishLocked()
}

func (a *hostActor) publishConnectionFailure() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connection == ConnectionDisconnected {
		return
	}
	a.connection = ConnectionDisconnected
	a.discovery = stoppedDiscovery()
	a.publishLocked()
}

func (a *hostActor) publishLocked() {
	a.publish(hostState{
		connection:           a.connection,
		discovery:            a.discovery,
		listenerObservations: a.listenerObservations,
		listenerLifetimes:    a.listenerLifetimes,
	})
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

func closeHostSession(session hostSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = session.Close(ctx)
}

var errTransportUnavailable = errors.New("Development Host transport is unavailable")

// currentDialer is the concurrency-safe holder of the live Forwarding
// Session's data path. The actor sets it exactly when it installs or removes
// the session; Forward endpoint allocation reads through it, so endpoints
// survive session replacement without holding either state lock.
type currentDialer struct {
	mu     sync.RWMutex
	dialer proxy.Dialer
}

func (d *currentDialer) Set(dialer proxy.Dialer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialer = dialer
}

func (d *currentDialer) DialContext(ctx context.Context, target netip.AddrPort) (proxy.HalfCloseConn, error) {
	d.mu.RLock()
	dialer := d.dialer
	d.mu.RUnlock()
	if dialer == nil {
		return nil, errTransportUnavailable
	}
	return dialer.DialContext(ctx, target)
}
