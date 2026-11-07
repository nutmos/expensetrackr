// Command server runs the expense-logging web service.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nutmos/expensetrackr/pkg/api"
	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/web"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// shutdownTimeout bounds how long in-flight requests get to finish on SIGINT/SIGTERM.
const shutdownTimeout = 5 * time.Second

// HTTP server timeouts (issue #83), so slow or idle clients can't hold
// connections and goroutines forever.
const (
	// readHeaderTimeout: time to send the request headers.
	readHeaderTimeout = 10 * time.Second
	// readTimeout: time to send the whole request, including a bulk import
	// body (up to 2 MiB).
	readTimeout = time.Minute
	// writeTimeout: time from the end of the request headers to the end of
	// the response. It must stay well above api.DefaultBulkFXTimeout, since a
	// bulk import first fetches rates and then writes up to 500 rows.
	writeTimeout = 2 * time.Minute
	// idleTimeout: how long a keep-alive connection may wait for its next request.
	idleTimeout = 2 * time.Minute
)

// newHTTPServer returns the server for addr with the timeouts above.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

func main() {
	addr := flag.String("addr", envOr("EXPENSE_ADDR", "127.0.0.1:8080"), "listen address (env EXPENSE_ADDR); use :8080 to listen on all interfaces")
	dbURL := flag.String("database-url", envOr("EXPENSE_DATABASE_URL", ""), "Postgres connection URL, e.g. postgres://user:pass@host:5432/db?sslmode=disable (env EXPENSE_DATABASE_URL; required)")
	fxURL := flag.String("fx-url", envOr("EXPENSE_FX_URL", fx.DefaultBaseURL), "Frankfurter exchange-rate API base URL (env EXPENSE_FX_URL)")
	flag.Parse()

	if *dbURL == "" {
		log.Fatal("no database: set -database-url or EXPENSE_DATABASE_URL (make db-up starts a local Postgres; see README)")
	}
	st, err := store.Open(*dbURL)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	srv := newHTTPServer(*addr, (&api.Server{Store: st, Static: web.Static(), FX: &fx.Client{BaseURL: *fxURL, Cache: st}}).Router())

	go func() {
		log.Printf("expense-service listening on http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
