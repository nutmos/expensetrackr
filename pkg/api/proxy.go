package api

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

// ParseTrustedProxies parses a comma- or space-separated list of IP
// addresses and CIDR ranges (EXPENSE_TRUSTED_PROXIES) for
// Server.TrustedProxies. An empty string gives an empty list.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", f, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not an IP address or CIDR range", f)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// configureProxies makes Gin read the client address from the headers the
// reverse proxy sets, but only for connections from s.TrustedProxies.
//
// X-Real-IP comes first: the deployment's Caddy sets it to the client
// address it resolved itself (from Cloudflare's CF-Connecting-IP when the
// request came through Cloudflare), overwriting anything the client sent.
// X-Forwarded-For is the fallback for other proxies.
func (s *Server) configureProxies(r *gin.Engine) {
	if len(s.TrustedProxies) == 0 {
		_ = r.SetTrustedProxies(nil)
		return
	}
	cidrs := make([]string, len(s.TrustedProxies))
	for i, p := range s.TrustedProxies {
		cidrs[i] = p.String()
	}
	if err := r.SetTrustedProxies(cidrs); err != nil {
		panic(err) // prefixes are already parsed, so this cannot happen
	}
	r.RemoteIPHeaders = []string{"X-Real-IP", "X-Forwarded-For"}
}

// fromTrustedProxy reports whether the connection comes from one of
// s.TrustedProxies.
func (s *Server) fromTrustedProxy(c *gin.Context) bool {
	if len(s.TrustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(c.Request.RemoteAddr))
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range s.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// isHTTPS reports whether the browser reached the service over HTTPS:
// directly, or through a trusted proxy that says so in X-Forwarded-Proto
// (Caddy terminating TLS, possibly behind Cloudflare). It decides the
// session cookie's Secure flag.
func (s *Server) isHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return s.fromTrustedProxy(c) && strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}
