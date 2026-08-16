package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	defaultDrainTimeout     = 30 * time.Second
	defaultHandshakeTimeout = 10 * time.Second
)

var ErrLocalPortConflict = errors.New("local port fallback range is occupied")

type HalfCloseConn interface {
	net.Conn
	CloseWrite() error
}

type Dialer interface {
	DialContext(context.Context, netip.AddrPort) (HalfCloseConn, error)
}

type EndpointOptions struct {
	PreferredPort    uint16
	Remote           netip.AddrPort
	Dialer           Dialer
	DrainTimeout     time.Duration
	HandshakeTimeout time.Duration
}

type Endpoint struct {
	localPort uint16
	listeners []net.Listener
	remote    netip.AddrPort
	dialer    Dialer
	drain     time.Duration
	handshake time.Duration
	ctx       context.Context
	cancel    context.CancelFunc

	workers sync.WaitGroup
	done    chan struct{}

	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}

	closeOnce sync.Once
	closeErr  error
}

// fallbackPortRoom is the ADR-0008 bounded fallback width: allocation tries
// the Preferred Local Port, then each successor up to +fallbackPortRoom.
const fallbackPortRoom = 100

func OpenEndpoint(options EndpointOptions) (*Endpoint, error) {
	lastCandidate := min(int(options.PreferredPort)+fallbackPortRoom, 65535)
	for candidate := int(options.PreferredPort); candidate <= lastCandidate; candidate++ {
		listeners, err := listenOnLoopback(uint16(candidate))
		if err == nil {
			ctx, cancel := context.WithCancel(context.Background())
			drainTimeout := options.DrainTimeout
			if drainTimeout <= 0 {
				drainTimeout = defaultDrainTimeout
			}
			handshakeTimeout := options.HandshakeTimeout
			if handshakeTimeout <= 0 {
				handshakeTimeout = defaultHandshakeTimeout
			}
			endpoint := &Endpoint{
				localPort:   uint16(candidate),
				listeners:   listeners,
				remote:      options.Remote,
				dialer:      options.Dialer,
				drain:       drainTimeout,
				handshake:   handshakeTimeout,
				ctx:         ctx,
				cancel:      cancel,
				done:        make(chan struct{}),
				connections: make(map[net.Conn]struct{}),
			}
			endpoint.workers.Add(len(listeners))
			for _, listener := range listeners {
				go endpoint.accept(listener)
			}
			return endpoint, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, ErrLocalPortConflict
}

func listenOnLoopback(portNumber uint16) ([]net.Listener, error) {
	port := strconv.Itoa(int(portNumber))
	ipv4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return nil, err
	}
	ipv6, err := net.Listen("tcp6", net.JoinHostPort("::1", port))
	if err != nil {
		_ = ipv4.Close()
		return nil, err
	}
	return []net.Listener{ipv4, ipv6}, nil
}

func (e *Endpoint) LocalPort() uint16 {
	return e.localPort
}

func (e *Endpoint) Close(ctx context.Context) error {
	e.closeOnce.Do(func() {
		e.cancel()
		var errs []error
		for _, listener := range e.listeners {
			errs = append(errs, listener.Close())
		}
		e.closeConnections()
		e.closeErr = errors.Join(errs...)
		go func() {
			e.workers.Wait()
			close(e.done)
		}()
	})
	select {
	case <-e.done:
		return e.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// accept serves one listener. Every Accept error is terminal by design:
// the only shutdown is Close closing the listener, and a failed or refused
// connection must not leave a half-accepted state. The Endpoint itself is
// disposable — the Manager rebuilds it on demand — so there is no recovery
// path here. workers.Add(1) runs after the connection is tracked in
// connections; Close cancels the ctx before workers.Wait, so a proxy
// goroutine the Wait may miss observes only the canceled ctx and exits
// without touching the tracked set.
func (e *Endpoint) accept(listener net.Listener) {
	defer e.workers.Done()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		if !e.addConnection(connection) {
			return
		}
		e.workers.Add(1)
		go e.proxy(connection)
	}
}

func (e *Endpoint) proxy(local net.Conn) {
	defer e.workers.Done()
	defer e.removeConnection(local)
	defer local.Close()
	dialContext, cancelDial := context.WithTimeout(e.ctx, e.handshake)
	remote, err := e.dialer.DialContext(dialContext, e.remote)
	cancelDial()
	if err != nil {
		return
	}
	if !e.addConnection(remote) {
		return
	}
	defer e.removeConnection(remote)
	defer remote.Close()
	localTCP := local.(*net.TCPConn)

	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, localTCP)
		_ = remote.CloseWrite()
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(localTCP, remote)
		_ = localTCP.CloseWrite()
		finished <- struct{}{}
	}()
	<-finished
	timer := time.NewTimer(e.drain)
	defer timer.Stop()
	select {
	case <-finished:
		return
	case <-timer.C:
		_ = localTCP.Close()
		_ = remote.Close()
		<-finished
	}
}

func (e *Endpoint) addConnection(connection net.Conn) bool {
	e.connectionsMu.Lock()
	defer e.connectionsMu.Unlock()
	if e.ctx.Err() != nil {
		_ = connection.Close()
		return false
	}
	e.connections[connection] = struct{}{}
	return true
}

func (e *Endpoint) removeConnection(connection net.Conn) {
	e.connectionsMu.Lock()
	defer e.connectionsMu.Unlock()
	delete(e.connections, connection)
}

func (e *Endpoint) closeConnections() {
	e.connectionsMu.Lock()
	defer e.connectionsMu.Unlock()
	for connection := range e.connections {
		_ = connection.Close()
	}
}
