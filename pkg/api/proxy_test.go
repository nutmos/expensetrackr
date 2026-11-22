package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 172.18.0.0/16, 10.0.0.5 ::1\n2400:cb00::/32 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.18.0.0/16", "10.0.0.5/32", "::1/128", "2400:cb00::/32"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if got, err := ParseTrustedProxies(""); err != nil || len(got) != 0 {
		t.Errorf("empty: %v, %v", got, err)
	}
	for _, bad := range []string{"nope", "1.2.3.4/40", "example.com"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// The client address and the HTTPS flag come from the proxy's headers only
// when the connection is from a trusted proxy (Caddy, itself behind
// Cloudflare in production).
func TestTrustedProxyHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	trusted, _ := ParseTrustedProxies("172.18.0.0/16")
	type result struct {
		ip    string
		https bool
	}
	serve := func(s *Server, remote string, hdr map[string]string) result {
		r := gin.New()
		s.configureProxies(r)
		var res result
		r.GET("/", func(c *gin.Context) { res = result{c.ClientIP(), s.isHTTPS(c)} })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
		return res
	}
	proxyHeaders := map[string]string{
		"X-Real-IP":         "203.0.113.7",
		"X-Forwarded-For":   "198.51.100.1, 203.0.113.7, 162.158.1.1",
		"X-Forwarded-Proto": "https",
	}
	for _, tc := range []struct {
		name   string
		s      *Server
		remote string
		hdr    map[string]string
		want   result
	}{
		{"no trusted proxies", &Server{}, "172.18.0.3:4000", proxyHeaders, result{"172.18.0.3", false}},
		{"from trusted proxy", &Server{TrustedProxies: trusted}, "172.18.0.3:4000", proxyHeaders, result{"203.0.113.7", true}},
		{"from trusted proxy, plain http", &Server{TrustedProxies: trusted}, "172.18.0.3:4000",
			map[string]string{"X-Real-IP": "203.0.113.7", "X-Forwarded-Proto": "http"}, result{"203.0.113.7", false}},
		{"spoofed by untrusted client", &Server{TrustedProxies: trusted}, "192.0.2.9:4000", proxyHeaders, result{"192.0.2.9", false}},
	} {
		if got := serve(tc.s, tc.remote, tc.hdr); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
