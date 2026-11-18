// Package mcp is a Model Context Protocol (MCP) server that lets an LLM
// client (Claude Desktop, Claude Code, Cursor, ...) read and update
// transactions in expensetrackr.
//
// It speaks MCP over stdio (newline-delimited JSON-RPC 2.0) and is a thin
// client of the HTTP API: every tool call becomes one or two requests to
// /api/... with a personal API token, so authentication, per-user ownership,
// validation, balance effects and optimistic locking are exactly the API's.
// See docs/mcp.md.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ServerName and ServerVersion identify this server in the initialize result.
const (
	ServerName    = "expensetrackr"
	ServerVersion = "0.1.0"
)

// supportedVersions are the MCP protocol revisions this server can speak,
// newest first. The tools-only subset used here is the same in all of them.
var supportedVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// maxMessageSize bounds one incoming JSON-RPC line.
const maxMessageSize = 4 << 20

// instructions is sent to the client in the initialize result; clients
// usually add it to the model's context.
const instructions = `Tools for the user's expensetrackr ledger (expenses, income, transfers, asset purchases, returns on investment, debt repayments).
Rules:
- Never invent amounts, accounts, categories or dates; ask the user when one is missing or ambiguous.
- Resolve names to uids with list_balances and list_categories; talk to the user in names, not uids.
- Amounts are decimal strings (e.g. "120.50"); currency must equal the source balance's currency.
- spent_at and the from/to filters are RFC 3339 with a UTC offset (e.g. 2026-10-06T12:30:00+08:00); ask for the user's offset if unknown.
- category_uid is required for every type except transfer and repay_debt, where it is not allowed (expense/asset_purchase: an expense category; income/return_on_investment: an income category).
- balance_adjustment rows are written by the server when a balance is corrected; they cannot be created, edited or deleted here.
- Confirm with the user before deleting a transaction.`

// Server is an MCP server exposing Tools. Serve handles one client.
type Server struct {
	Tools []Tool

	mu  sync.Mutex // serializes writes to out
	out io.Writer
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads JSON-RPC messages from r, one per line, and writes responses
// to w until r is exhausted or ctx is cancelled. Requests are handled in
// order, one at a time.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.out = w
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 {
			s.handleLine(ctx, line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readLine reads one newline-terminated message. A line longer than
// maxMessageSize is discarded and reported as a parse error.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) <= maxMessageSize {
			buf = append(buf, chunk...)
		} else if buf != nil {
			buf = []byte("\x00too long") // invalid JSON → parse error
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(nil, nil, &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()})
		return
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if req.JSONRPC != "2.0" || req.Method == "" {
		if !isNotification {
			s.reply(req.ID, nil, &rpcError{Code: codeInvalidRequest, Message: `invalid request: need "jsonrpc":"2.0" and a method`})
		}
		return
	}
	result, rerr := s.dispatch(ctx, req)
	if isNotification { // notifications (e.g. notifications/initialized) get no response
		return
	}
	s.reply(req.ID, result, rerr)
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := supportedVersions[0]
		for _, v := range supportedVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": ServerName, "title": "expensetrackr", "version": ServerVersion},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			list = append(list, t.descriptor())
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs params {name, arguments}"}
		}
		for _, t := range s.Tools {
			if t.Name == p.Name {
				return s.call(ctx, t, p.Arguments), nil
			}
		}
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool %q", p.Name)}
	}
	if len(req.Method) > 14 && req.Method[:14] == "notifications/" {
		return nil, nil // nothing to do for client notifications
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method %q not found", req.Method)}
}

// call runs a tool. Errors are reported in the result (isError) so the model
// can read them and correct itself, as the MCP spec recommends.
func (s *Server) call(ctx context.Context, t Tool, args json.RawMessage) map[string]any {
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	out, err := t.Handler(ctx, args)
	if err != nil {
		return textResult(err.Error(), true)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return textResult("encode result: "+err.Error(), true)
	}
	return textResult(string(b), false)
}

func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) reply(id json.RawMessage, result any, rerr *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rerr}
	if rerr == nil && result == nil {
		resp.Result = map[string]any{}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: codeInternalError, Message: err.Error()}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}
