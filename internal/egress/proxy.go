// Package egress is the in-repo HTTP CONNECT proxy a sandbox must use to reach the network. A host
// is let through only when the effective policy's egress allowlist covers it (contract.Policy,
// CheckEgress). Every refusal is recorded as a contract.AuditEvent. The shape follows
// stripe/smokescreen (MIT): an allowlist checked on CONNECT, then a dial that refuses private
// addresses. No code is copied.
package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	pkgegress "github.com/KaaS-team/factory/pkg/sandbox/egress"
)

// ActionConnect is the audit action of a CONNECT through the proxy.
const ActionConnect contract.Action = "egress.connect"

// Proxy is an http.Handler for CONNECT requests.
type Proxy struct {
	Policy   contract.Policy // required: the allowlist
	Audit    contract.Audit  // required: where denials are recorded
	Actor    string          // the Principal.ID the run acts as
	Resource string          // the repo, "owner/name"

	// Dial opens the upstream connection. The default resolves host, refuses any non-public
	// address (loopback, private, link-local and the cloud metadata address), then dials the
	// address it checked, so a rebinding name cannot swap it afterwards.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// ServeHTTP handles CONNECT host:port. Any other method is refused, because a plain request
// cannot be tunnelled and its host would escape the check.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if r.Method != http.MethodConnect {
		p.deny(r.Context(), host, contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted, Detail: "only CONNECT"})
		http.Error(w, "egress: only CONNECT is allowed", http.StatusMethodNotAllowed)
		return
	}
	if d := p.Policy.Evaluate(r.Context(), contract.PolicyRequest{Check: contract.CheckEgress, Value: host}); !d.Allow {
		p.deny(r.Context(), host, d)
		http.Error(w, "egress: host is not allowed by policy", http.StatusForbidden)
		return
	}
	up, err := p.dial(r.Context(), net.JoinHostPort(host, port))
	if err != nil {
		if errors.Is(err, errNonPublic) {
			p.deny(r.Context(), host, contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted, Detail: "non-public address"})
			http.Error(w, "egress: address is not allowed", http.StatusForbidden)
			return
		}
		http.Error(w, "egress: upstream unreachable", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()
		http.Error(w, "egress: cannot hijack", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	down, rw, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	_ = rw.Flush()
	pipe(down, up)
}

// deny records the refusal. A failed Record still leaves the request denied.
func (p *Proxy) deny(ctx context.Context, host string, d contract.Decision) {
	data := map[string]string{"host": host}
	if d.Detail != "" {
		data["detail"] = d.Detail
	}
	_, _ = p.Audit.Record(context.WithoutCancel(ctx), contract.AuditEvent{
		Actor: p.Actor, Action: ActionConnect, Resource: p.Resource,
		Outcome: contract.OutcomeDeny, Reason: d.Reason, Data: data,
	})
}

var errNonPublic = errors.New("egress: non-public address")

func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("egress: lookup failed")
	}
	for _, ip := range ips {
		if pkgegress.NonPublic(ip.IP) {
			return nil, errNonPublic
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), port))
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}
