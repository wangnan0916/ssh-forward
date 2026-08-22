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

type hostActorOptions struct {
	host          HostAlias
	connector     HostConnector
	dialer        *currentDialer
	publish       func(hostView)
	ctx           context.Context
	onObservation func()
	retryDelay    func(int) time.Duration
	retryWait     func(context.Context, time.Duration) bool
}

// hostActor owns one Development Host's Forwarding Session, Discovery, and reconnect.
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

func newHostActor(options hostActorOptions) *hostActor {
	if options.retryDelay == nil {
		options.retryDelay = exponentialJitterDelay
	}
	if options.retryWait == nil {
		options.retryWait = waitForRetry
	}
	return &hostActor{
		host:       options.host,
		connector:  options.connector,
		dialer:     options.dialer,
		publish:    options.publish,
		onObserve:  options.onObservation,
		retryDelay: options.retryDelay,
		retryWait:  options.retryWait,
		ctx:        options.ctx,
		state:      emptyHostView(options.host),
		done:       make(chan struct{}),
	}
}

func (a *hostActor) startIfNeeded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active.Load() || a.ctx.Err() != nil {
		return
	}
	a.active.Store(true)
	a.state.Connection = ConnectionConnecting
	a.state.ConnectionDiagnostic = ""
	a.done = make(chan struct{})
	a.publishLocked()
	go a.run()
}

func (a *hostActor) isActive() bool {
	return a.active.Load()
}

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
			disposition, reason := sessionEnd(err)
			if disposition != SessionRetry {
				a.publishConnectionFailure(reason)
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
		a.state.ConnectionDiagnostic = ""
		a.state.Discovery = startingDiscovery()
		a.lastObservationSequence = 0
		a.publishLocked()
		a.mu.Unlock()

		err = a.consumeSession(session)
		closeHostSession(session)
		disposition, reason := sessionEnd(err)

		a.mu.Lock()
		a.session = nil
		a.dialer.Set(nil)
		if disposition == SessionRetry {
			a.state.Connection = ConnectionConnecting
			a.state.ConnectionDiagnostic = ""
		} else {
			a.state.Connection = ConnectionDisconnected
			a.state.ConnectionDiagnostic = connectionDiagnostic(reason)
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
		a.mu.Lock()
		a.failDiscoveryLocked(ReasonSessionInvalid)
		a.mu.Unlock()
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
	observations, truncated := boundListenerObservations(canonicalListenerObservations(set.Observations))
	degradeTruncatedCapability(&capability, truncated)
	complete := capability.RemoteListeners == CapabilityFull
	if !complete {
		observations, truncated = mergeBoundedListenerObservations(a.state.ListenerObservations, observations)
		degradeTruncatedCapability(&capability, truncated)
	}
	state := discoveryStateForCapability(capability)
	if gapped {
		state = DiscoveryDegraded
	}
	discovery := DiscoverySnapshot{
		State:               state,
		Capability:          capability,
		BaselineEstablished: complete || a.state.Discovery.BaselineEstablished,
		Diagnostic:          discoveryDiagnostic(gapped, capability, ""),
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

func (a *hostActor) failDiscoveryLocked(reason DiscoveryReason) {
	diagnostic := discoveryFailureDiagnostic(reason)
	discovery := a.state.Discovery
	discovery.State = DiscoveryFailed
	discovery.Diagnostic = diagnostic
	a.state.Discovery = discovery
	a.publishLocked()
}

func (a *hostActor) publishConnectionFailure(reason SessionReason) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active.Store(false)
	a.state.Connection = ConnectionDisconnected
	a.state.ConnectionDiagnostic = connectionDiagnostic(reason)
	a.state.Discovery = stoppedDiscovery()
	a.publishLocked()
}

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

func sessionEnd(err error) (SessionDisposition, SessionReason) {
	if errors.Is(err, context.Canceled) {
		return SessionClosed, SessionReasonClosed
	}
	var sessionError *SessionError
	if errors.As(err, &sessionError) {
		return sessionError.Disposition, sessionError.Reason
	}
	return SessionRetry, SessionReasonTransport
}

func closeHostSession(session HostSession) {
	closeWithTimeout(session.Close, 5*time.Second)
}

var errTransportUnavailable = errors.New("Development Host transport is unavailable")

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
