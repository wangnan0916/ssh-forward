package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type SOCKS5Dialer struct {
	server netip.AddrPort
}

func NewSOCKS5Dialer(server netip.AddrPort) *SOCKS5Dialer {
	return &SOCKS5Dialer{server: server}
}

func (d *SOCKS5Dialer) DialContext(ctx context.Context, target netip.AddrPort) (core.HalfCloseConn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.server.String())
	if err != nil {
		return nil, err
	}
	tcpConnection, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("SOCKS transport is not TCP")
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = tcpConnection.Close() })
	if err := negotiateSOCKS5(tcpConnection, target); err != nil {
		stopCancellation()
		_ = tcpConnection.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if !stopCancellation() {
		_ = tcpConnection.Close()
		return nil, ctx.Err()
	}
	return tcpConnection, nil
}

// NegotiateMethod performs the SOCKS5 no-authentication method negotiation:
// send the {5,1,0} greeting and verify the server's two-byte reply selects
// no authentication. The dial path and the OpenSSH Forwarding Session
// readiness probe share this one handshake, so the two cannot drift.
func NegotiateMethod(connection net.Conn) error {
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		return err
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		return errors.New("SOCKS server rejected no-authentication method")
	}
	return nil
}

func negotiateSOCKS5(connection net.Conn, target netip.AddrPort) error {
	if err := NegotiateMethod(connection); err != nil {
		return err
	}

	request := []byte{5, 1, 0}
	if target.Addr().Is4() {
		request = append(request, 1)
		request = append(request, target.Addr().AsSlice()...)
	} else if target.Addr().Is6() {
		request = append(request, 4)
		request = append(request, target.Addr().AsSlice()...)
	} else {
		return errors.New("SOCKS target address is invalid")
	}
	request = binary.BigEndian.AppendUint16(request, target.Port())
	if _, err := connection.Write(request); err != nil {
		return err
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return err
	}
	if header[0] != 5 || header[1] != 0 || header[2] != 0 {
		return fmt.Errorf("SOCKS CONNECT failed with reply %d", header[1])
	}
	var addressBytes int
	switch header[3] {
	case 1:
		addressBytes = 4
	case 4:
		addressBytes = 16
	default:
		return errors.New("SOCKS server returned an unsupported address type")
	}
	boundAddress := make([]byte, addressBytes+2)
	_, err := io.ReadFull(connection, boundAddress)
	return err
}
