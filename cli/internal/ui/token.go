package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
)

// TokenCookie is the host-only cookie that carries the same secret as
// the capability URL. SameSite=Strict so other sites cannot send it.
const TokenCookie = "ssh_forward_token"

// NewToken returns an unguessable secret for the loopback URL.
func NewToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func tokenEqual(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func tokenFromRequest(r *http.Request) string {
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}
	if cookie, err := r.Cookie(TokenCookie); err == nil {
		return cookie.Value
	}
	return ""
}

func grantTokenCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     TokenCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
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
