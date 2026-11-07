package main

import (
	"net/http"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/api"
)

// TestHTTPServerTimeouts: every timeout is set (issue #83), and a response
// may take well longer than the bulk import's exchange-rate budget.
func TestHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	for name, d := range map[string]int64{
		"ReadHeaderTimeout": int64(srv.ReadHeaderTimeout),
		"ReadTimeout":       int64(srv.ReadTimeout),
		"WriteTimeout":      int64(srv.WriteTimeout),
		"IdleTimeout":       int64(srv.IdleTimeout),
	} {
		if d <= 0 {
			t.Errorf("%s is not set", name)
		}
	}
	if srv.ReadHeaderTimeout > srv.ReadTimeout {
		t.Errorf("ReadHeaderTimeout %v > ReadTimeout %v", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
	if srv.WriteTimeout < 2*api.DefaultBulkFXTimeout {
		t.Errorf("WriteTimeout %v leaves too little room after the bulk FX budget %v", srv.WriteTimeout, api.DefaultBulkFXTimeout)
	}
}
