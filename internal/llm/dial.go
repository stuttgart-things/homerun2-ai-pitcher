package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Dialer connects to every address of a host in turn, IPv4 first, and on
// failure reports the error of EVERY address it tried.
//
// Go's own dialer returns only the error of the first address when all of
// them fail. On a single-stack IPv4 cluster that first address is usually
// IPv6, so a run logged "dial tcp [2606:4700::…]:443: connect: network is
// unreachable" while the IPv4 attempt that actually mattered failed for a
// different reason (#11).
//
// IPv4 first rather than Happy Eyeballs: the clusters this runs on are
// IPv4-only or dual-stack, an IPv6 attempt on an IPv4-only node fails at
// once ("network is unreachable"), and one serial order keeps the errors
// readable. Network "tcp4" or "tcp6" leaves out the other family.
type Dialer struct {
	// Network: "tcp" (both families, IPv4 first), "tcp4" or "tcp6".
	Network string
	// Timeout per address.
	Timeout time.Duration

	// lookup and dial are replaced in tests.
	lookup func(ctx context.Context, network, host string) ([]net.IP, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// DialError lists every address that was tried and why it failed.
type DialError struct {
	Host   string
	Errors []error
}

func (e *DialError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		parts[i] = err.Error()
	}
	return fmt.Sprintf("dial %s: all %d addresses failed: %s", e.Host, len(e.Errors), strings.Join(parts, "; "))
}

// Unwrap lets errors.Is/As see each address's error (e.g. a timeout).
func (e *DialError) Unwrap() []error { return e.Errors }

// DialContext implements http.Transport.DialContext.
func (d *Dialer) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	network := d.network()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		lookupNet := map[string]string{"tcp": "ip", "tcp4": "ip4", "tcp6": "ip6"}[network]
		ips, err = d.lookupFn()(ctx, lookupNet, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
	}
	ips = order(ips, network)
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %s: no %s address", host, network)
	}
	var errs []error
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		actx, cancel := context.WithTimeout(ctx, d.timeout())
		conn, err := d.dialFn()(actx, network, net.JoinHostPort(ip.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", ip, dialCause(err)))
	}
	if len(errs) == 0 {
		return nil, ctx.Err()
	}
	return nil, &DialError{Host: address, Errors: errs}
}

// order puts IPv4 before IPv6 and drops the family the network excludes.
func order(ips []net.IP, network string) []net.IP {
	var v4, v6 []net.IP
	for _, ip := range ips {
		if ip.To4() != nil {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	switch network {
	case "tcp4":
		return v4
	case "tcp6":
		return v6
	}
	return append(v4, v6...)
}

// dialCause drops the "dial tcp <addr>:" prefix the address already names.
func dialCause(err error) error {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err
	}
	return err
}

func (d *Dialer) network() string {
	switch d.Network {
	case "tcp4", "tcp6":
		return d.Network
	}
	return "tcp"
}

func (d *Dialer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 10 * time.Second
}

func (d *Dialer) lookupFn() func(context.Context, string, string) ([]net.IP, error) {
	if d.lookup != nil {
		return d.lookup
	}
	return net.DefaultResolver.LookupIP
}

func (d *Dialer) dialFn() func(context.Context, string, string) (net.Conn, error) {
	if d.dial != nil {
		return d.dial
	}
	return (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext
}

// NewHTTPClient is the client for OpenRouter: the Dialer above on a copy
// of the default transport (proxy from the environment, HTTP/2, TLS
// timeout). No overall timeout: Client.Complete sets one per request.
func NewHTTPClient(network string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&Dialer{Network: network}).DialContext
	return &http.Client{Transport: tr}
}
