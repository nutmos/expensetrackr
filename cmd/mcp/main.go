// Command expense-mcp is an MCP (Model Context Protocol) server over stdio
// that lets an LLM client read and update transactions and balances in expensetrackr,
// through the HTTP API and a personal API token. See docs/mcp.md.
//
//	EXPENSETRACKR_TOKEN=ext_... expense-mcp -url http://127.0.0.1:8080
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // TZ=Asia/Bangkok works in the zoneinfo-less Docker image (date filters)

	"github.com/nutmos/expensetrackr/pkg/mcp"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	// stdout carries the protocol; everything else goes to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("expense-mcp: ")
	log.SetFlags(0)

	baseURL := flag.String("url", envOr("EXPENSETRACKR_URL", "http://127.0.0.1:8080"), "expensetrackr server URL (env EXPENSETRACKR_URL)")
	readOnly := flag.Bool("read-only", os.Getenv("EXPENSETRACKR_MCP_READ_ONLY") == "1", "expose only the read tools (env EXPENSETRACKR_MCP_READ_ONLY=1)")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: EXPENSETRACKR_TOKEN=ext_... %s [flags]\n\nMCP server (stdio) for expensetrackr transactions and balances. The API token is read from\nEXPENSETRACKR_TOKEN only (never a flag, so it stays out of process lists).\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *version {
		fmt.Println(mcp.ServerName, mcp.ServerVersion)
		return
	}

	token := os.Getenv("EXPENSETRACKR_TOKEN")
	if token == "" {
		log.Fatal("EXPENSETRACKR_TOKEN is not set: create an API token in the web page (Settings → API tokens) and pass it in the MCP client's env")
	}
	u, err := url.Parse(*baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		log.Fatalf("-url must be an http(s) URL such as http://127.0.0.1:8080, got %q", *baseURL)
	}

	client := &mcp.Client{BaseURL: *baseURL, Token: token}
	srv := &mcp.Server{Tools: mcp.Tools(client, *readOnly)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
