package ui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
)

// NewToken returns an unguessable secret for the loopback URL.
func NewToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// ListenLoopback binds an ephemeral TCP port on 127.0.0.1 only.
func ListenLoopback() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// PageURL is the operator-facing address: loopback, token in the query.
func PageURL(addr net.Addr, token string) string {
	port := addr.(*net.TCPAddr).Port
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, token)
}
