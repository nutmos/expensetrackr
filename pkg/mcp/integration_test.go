package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/nutmos/expensetrackr/pkg/api"
	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/store/storetest"
	"github.com/nutmos/expensetrackr/pkg/user"
)

func init() { auth.BcryptCost = 4 }

// liveAPI starts the real API on a fresh test database with one user and
// returns its URL and a token of each scope. Skipped without Postgres.
func liveAPI(t *testing.T) (baseURL, writeTok, readTok string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st := storetest.Open(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("pw-12345")
	if err != nil {
		t.Fatal(err)
	}
	name := "alice"
	u := user.User{Username: &name, DisplayName: "Alice"}
	if err := st.RegisterUser(ctx, &u, hash, true); err != nil {
		t.Fatal(err)
	}
	newTok := func(scopes ...string) string {
		plain, prefix, err := auth.NewAPIToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateAPIToken(ctx, &store.APIToken{UserUID: u.UID, Name: "mcp " + scopes[0], Prefix: prefix, TokenHash: auth.HashToken(plain), Scopes: scopes}); err != nil {
			t.Fatal(err)
		}
		return plain
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	srv := httptest.NewServer((&api.Server{Store: st, Static: static}).Router())
	t.Cleanup(srv.Close)
	return srv.URL, newTok("read", "write"), newTok("read")
}

// callTool runs one tools/call and returns the text and isError.
func callTool(t *testing.T, s *Server, name string, args any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	resps := rpc(t, s, string(b))
	r := resps[0]["result"].(map[string]any)
	return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"] == true
}

func mustCall(t *testing.T, s *Server, name string, args any) map[string]any {
	t.Helper()
	text, isErr := callTool(t, s, name, args)
	if isErr {
		t.Fatalf("%s: %s", name, text)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%s: %v in %s", name, err, text)
	}
	return m
}

// setupRecords creates two balances and an expense category over HTTP.
func setupRecords(t *testing.T, base, tok string) (wallet, card, food string) {
	t.Helper()
	post := func(path, body string) string {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(res.Body).Decode(&m)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s: %d %v", path, res.StatusCode, m)
		}
		return m["uid"].(string)
	}
	wallet = post("/api/balances", `{"name":"Wallet","type":"payment_account","currency":"THB","balance":"1000"}`)
	card = post("/api/balances", `{"name":"Card","type":"credit_card","currency":"THB","debt":"0","limit":"50000"}`)
	food = post("/api/categories", `{"name":"Food","type":"expense"}`)
	return
}

func balanceOf(t *testing.T, s *Server, uid string) string {
	t.Helper()
	list := mustCall(t, s, "list_balances", map[string]any{})
	for _, b := range list["balances"].([]any) {
		if m := b.(map[string]any); m["uid"] == uid {
			if m["balance"] != nil {
				return m["balance"].(string)
			}
			return m["debt"].(string)
		}
	}
	t.Fatalf("balance %s not listed", uid)
	return ""
}

func TestEndToEnd(t *testing.T) {
	base, writeTok, readTok := liveAPI(t)
	wallet, card, food := setupRecords(t, base, writeTok)
	s := &Server{Tools: Tools(&Client{BaseURL: base, Token: writeTok}, false)}

	// create
	tx := mustCall(t, s, "create_transaction", map[string]any{
		"type": "expense", "amount": 120.5, "currency": "THB", "balance_uid": wallet,
		"category_uid": food, "spent_at": "2026-10-06T12:30:00+08:00", "note": "Lunch",
	})
	uid := tx["uid"].(string)
	if tx["amount"] != "120.50" || tx["account"] != "Wallet" {
		t.Fatalf("created: %v", tx)
	}
	if got := balanceOf(t, s, wallet); got != "879.50" {
		t.Fatalf("wallet after create: %s", got)
	}

	// list + get
	list := mustCall(t, s, "list_transactions", map[string]any{"category_uid": food, "from": "2026-10-01T00:00:00+08:00"})
	if list["count"].(float64) != 1 {
		t.Fatalf("list: %v", list)
	}
	if got := mustCall(t, s, "get_transaction", map[string]any{"uid": strings.ToUpper(uid)}); got["note"] != "Lunch" {
		t.Fatalf("get: %v", got)
	}

	// partial update: amount only; note and category are kept
	upd := mustCall(t, s, "update_transaction", map[string]any{"uid": uid, "amount": "150"})
	if upd["amount"] != "150.00" || upd["note"] != "Lunch" || upd["category_uid"] != food || upd["version"].(float64) != 2 {
		t.Fatalf("update: %v", upd)
	}
	if got := balanceOf(t, s, wallet); got != "850.00" {
		t.Fatalf("wallet after update: %s", got)
	}

	// stale version: nothing written
	if text, isErr := callTool(t, s, "update_transaction", map[string]any{"uid": uid, "version": 1, "note": "x"}); !isErr || !strings.Contains(text, "version_conflict") {
		t.Fatalf("stale update: %v %s", isErr, text)
	}

	// expense -> transfer to the card (category dropped automatically), clear the note
	upd = mustCall(t, s, "update_transaction", map[string]any{"uid": uid, "version": 2, "type": "transfer", "to_balance_uid": card, "note": ""})
	if upd["type"] != "transfer" || upd["category_uid"] != nil || upd["to_account"] != "Card" || upd["note"] != "" {
		t.Fatalf("to transfer: %v", upd)
	}

	// validation errors come back readable
	if text, isErr := callTool(t, s, "update_transaction", map[string]any{"uid": uid, "type": "expense"}); !isErr || !strings.Contains(text, "category_uid") {
		t.Fatalf("missing category: %v %s", isErr, text)
	}

	// read-only token cannot write
	ro := &Server{Tools: Tools(&Client{BaseURL: base, Token: readTok}, false)}
	if text, isErr := callTool(t, ro, "delete_transaction", map[string]any{"uid": uid}); !isErr || !strings.Contains(text, "insufficient_scope") {
		t.Fatalf("read token delete: %v %s", isErr, text)
	}

	// delete with a stale version fails, with the current one succeeds
	if _, isErr := callTool(t, s, "delete_transaction", map[string]any{"uid": uid, "version": 1}); !isErr {
		t.Fatal("stale delete succeeded")
	}
	mustCall(t, s, "delete_transaction", map[string]any{"uid": uid, "version": 3})
	if got := balanceOf(t, s, wallet); got != "1000.00" {
		t.Fatalf("wallet after delete: %s", got)
	}
	if text, isErr := callTool(t, s, "get_transaction", map[string]any{"uid": uid}); !isErr || !strings.Contains(text, "HTTP 404") {
		t.Fatalf("get deleted: %s", text)
	}

	// bad token
	bad := &Server{Tools: Tools(&Client{BaseURL: base, Token: "ext_nope"}, false)}
	if text, isErr := callTool(t, bad, "list_transactions", map[string]any{}); !isErr || !strings.Contains(text, "HTTP 401") {
		t.Fatalf("bad token: %s", text)
	}
}
