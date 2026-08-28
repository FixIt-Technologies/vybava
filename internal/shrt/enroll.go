package shrt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ParseEnrollCIDRs parses the comma-separated LUKO_ENROLL_CIDRS value.
// An empty input returns nil — enrollment disabled, fail closed.
func ParseEnrollCIDRs(value string) ([]netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, part := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("enroll CIDR %q: %w", part, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// clientAddr resolves the originating client IP. X-Real-IP is honored ONLY
// when the direct TCP peer is a configured trusted proxy (the deployik edge
// overwrites the header, but a client that reaches the app directly could
// forge it). Otherwise the peer address itself is the client. X-Forwarded-For
// is NEVER consulted — its leftmost value is attacker-controlled.
func clientAddr(r *http.Request, trustedProxies []netip.Prefix) (netip.Addr, bool) {
	peer, ok := peerAddr(r)
	if !ok {
		return netip.Addr{}, false
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" && inAny(peer, trustedProxies) {
		if addr, err := netip.ParseAddr(xri); err == nil {
			return addr, true
		}
	}
	return peer, true
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

func inAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// handleEnroll is self-service token issuance, authorized by network
// position alone: the client's real IP must fall inside a configured
// WireGuard range. No CIDRs configured → the endpoint does not exist (403).
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if len(s.EnrollCIDRs) == 0 || s.Tokens == nil {
		http.Error(w, "enrollment disabled", http.StatusForbidden)
		return
	}
	addr, ok := clientAddr(r, s.TrustedProxies)
	if !ok {
		http.Error(w, "enrollment disabled", http.StatusForbidden)
		return
	}
	if !inAny(addr, s.EnrollCIDRs) {
		s.logf("enroll refused from %s (outside mesh)", addr)
		http.Error(w, "enrollment disabled", http.StatusForbidden)
		return
	}
	var req tokenIssueRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	value, err := s.Tokens.Issue(name)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.logf("token enrolled: %s (from %s)", name, addr)
	writeAPIJSON(w, tokenIssueResponse{Name: name, Token: value})
}

// Enroll self-issues a named token over the mesh. via is the mesh gateway IP
// to dial; the TLS handshake still verifies against the origin's hostname,
// so a wrong gateway fails closed rather than talking to an impostor.
func (c Client) Enroll(name, via string) (TokenIssue, error) {
	origin := c.base()
	host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	client := c.HTTP
	if client == nil && via != "" {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		client = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, net.JoinHostPort(via, "443"))
				},
				TLSClientConfig: &tls.Config{ServerName: host},
			},
		}
	}
	body, err := json.Marshal(tokenIssueRequest{Name: name})
	if err != nil {
		return TokenIssue{}, err
	}
	req, err := http.NewRequest(http.MethodPost, origin+"/api/enroll", strings.NewReader(string(body)))
	if err != nil {
		return TokenIssue{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return TokenIssue{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return TokenIssue{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return TokenIssue{}, fmt.Errorf("enroll: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var out TokenIssue
	if err := json.Unmarshal(payload, &out); err != nil {
		return TokenIssue{}, err
	}
	if out.Token == "" {
		return TokenIssue{}, fmt.Errorf("enroll: empty token in response")
	}
	return out, nil
}
