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

// The actor publishes the published host shape directly (Forwards are filled
// in by the Manager at publication time), so the Manager's copy and the
// Snapshot never diverge by construction.
type hostActorOptions struct {
	host       HostAlias
	connector  hostConnector
	dialer     *currentDialer
	publish    func(HostSnapshot)
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
	publish    func(HostSnapshot)
	retryDelay func(int) time.Duration
	retryWait  func(context.Context, time.Duration) bool
	ctx        context.Context

	mu                      sync.Mutex
	active                  bool
	session                 hostSession
	state                   HostSnapshot
	lastObservationSequence uint64

	tracker       *lifetimeTracker
	lastPublished HostSnapshot
	done          chan struct{}
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
		state:      emptyHostSnapshot(options.host),
		tracker:    newLifetimeTracker(defaultListenerGraceCycles),
		done:       make(chan struct{}),
	}
}

// startIfNeeded launches the connect loop unless one is already running or
// the Manager is shutting down. It is idempotent and re-arms after the loop
// ends, so a later command can retry a terminally failed session. The
// Manager has already published the Connecting transition synchronously
// under its own lock, so startIfNeeded publishes nothing itself; the loop
// publishes from Connected onward.
func (a *hostActor) startIfNeeded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active || a.ctx.Err() != nil {
		return
	}
	a.active = true
	a.state.Connection = ConnectionConnecting
	a.done = make(chan struct{})
	go a.run()
}

func (a *hostActor) isActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
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
		a.active = false
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
			a.active = false
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
		observations, truncated = mergeBoundedListenerObservations(a.state.ListenerObservations, observations)
		degradeTruncatedCapability(&capability, truncated)
	}
	discovery := DiscoverySnapshot{
		State:               discoveryStateForCapability(capability),
		Capability:          capability,
		BaselineEstablished: complete || a.state.Discovery.BaselineEstablished,
		ScannerVersion:      set.ScannerVersion,
		// ScannerChecksum is evidence metadata: the scanner parser stamps
		// each ObservationSet with the embedded script's digest, so clients
		// can attribute observations to a script revision. It is not a
		// verification gate (the stamp cannot drift from the script that
		// produced it); budget drift is instead rejected in-band.
		ScannerChecksum: set.ScannerChecksum,
	}
	if gapped {
		discovery.State = DiscoveryDegraded
		discovery.Diagnostic = "observation_resync"
	}
	// The tracker always advances: absent listeners accrue grace even when
	// the observation set itself is unchanged, and only crossing the grace
	// threshold changes a verdict. publishLocked deduplicates the no-change
	// publication, so lifetime progression and publish suppression coexist.
	verdicts := a.tracker.advance(observations)
	a.state.Discovery = discovery
	a.state.ListenerObservations = observations
	a.state.ListenerLifetimes = verdicts
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
	discovery := a.state.Discovery
	discovery.State = change.State
	discovery.Capability = change.Capability
	discovery.Diagnostic = change.Diagnostic
	a.state.Discovery = discovery
	a.publishLocked()
}

func (a *hostActor) applyInvalidDiscoveryFact() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failDiscoveryLocked("invalid_session_fact")
}

func (a *hostActor) failDiscoveryLocked(diagnostic string) {
	discovery := a.state.Discovery
	discovery.State = DiscoveryFailed
	discovery.Diagnostic = diagnostic
	a.state.Discovery = discovery
	a.publishLocked()
}

func (a *hostActor) publishConnectionFailure() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = false
	a.state.Connection = ConnectionDisconnected
	a.state.Discovery = stoppedDiscovery()
	a.publishLocked()
}

// publishLocked publishes one per-host snapshot and is the single place that
// suppresses no-change publications: it compares against the last snapshot it
// handed to the Manager. Callers mutate state and publish unconditionally, so
// new state fields (for example Policy reconciliation verdicts) get dedup for
// free. The tracker's unconditional advance above is unaffected: it is a side
// effect on the actor, not a publication.
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
// down". Policy reconciliation (slice 5) will need the distinction for
// Ask-state waits and will consume the disposition directly.
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
	closeWithTimeout(session.Close, 5*time.Second)
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
