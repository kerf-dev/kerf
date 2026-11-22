// Package tunnel lets the hub open the connections an agent uses to call it, for networks that allow traffic from
// the hub to a workload cluster but not back.
//
// The hub dials the agent's tunnel endpoint and upgrades the HTTP connection with a WebSocket handshake, so the
// ingresses and load balancers that pass WebSockets pass it too. After the handshake the roles swap: the agent is
// the HTTP client and the hub serves its normal API on the connection. The agent API, its bearer token and the
// hub's checks do not change.
//
// Both sides authenticate: the hub presents a per-cluster tunnel token when it connects (checked by the agent),
// and the agent presents its agent token on every request inside the tunnel (checked by the hub).
package tunnel

import (
	"crypto/sha1" // RFC 6455 handshake hash, not a security use
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

// Path is the agent endpoint the hub upgrades.
const Path = "/v1/tunnel"

// BaseURL is the hub URL the agent uses in listen mode. The host is never resolved: every connection comes from
// the hub.
const BaseURL = "http://hub.tunnel"

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// acceptKey is the Sec-WebSocket-Accept value for a Sec-WebSocket-Key (RFC 6455, section 4.2.2).
func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// TokenSource returns a token for each connection.
type TokenSource func() (string, error)

// StaticToken returns a TokenSource for a fixed token.
func StaticToken(t string) TokenSource { return func() (string, error) { return t, nil } }

// FileToken reads the token from a file on every call, so a rotated Secret is picked up without a restart. A
// missing file yields an empty token.
func FileToken(path string) TokenSource {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
}

// headerHas reports whether a comma-separated header lists token (case-insensitive), e.g. Connection: Upgrade.
func headerHas(values []string, token string) bool {
	for _, v := range values {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}
