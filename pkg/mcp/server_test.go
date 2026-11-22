package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rpc sends the given JSON-RPC lines to s and returns the decoded responses.
func rpc(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	dec := json.NewDecoder(&out)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("bad output %q: %v", out.String(), err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestProtocol(t *testing.T) {
	s := &Server{Tools: Tools(&Client{BaseURL: "http://127.0.0.1:1", Token: "x"}, false)}
	resps := rpc(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_transaction","arguments":{"uid":"1"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_transactions","arguments":{"categry":"x"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
	)
	if len(resps) != 9 { // the notification gets no answer
		t.Fatalf("got %d responses: %v", len(resps), resps)
	}
	initRes := resps[0]["result"].(map[string]any)
	if initRes["protocolVersion"] != "2025-06-18" || initRes["serverInfo"].(map[string]any)["name"] != ServerName {
		t.Fatalf("initialize: %v", initRes)
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if got := strings.Join(names, ","); got != "list_balances,get_balance,create_balance,update_balance,list_categories,list_transactions,get_transaction,create_transaction,update_transaction,delete_transaction" {
		t.Fatalf("tools: %s", got)
	}
	if resps[1]["id"] != "two" {
		t.Fatalf("id not echoed: %v", resps[1])
	}
	if resps[2]["error"] != nil {
		t.Fatalf("ping: %v", resps[2])
	}
	if e := resps[3]["error"].(map[string]any); e["code"].(float64) != codeMethodNotFound {
		t.Fatalf("unknown method: %v", resps[3])
	}
	if e := resps[4]["error"].(map[string]any); e["code"].(float64) != codeParseError || resps[4]["id"] != nil {
		t.Fatalf("parse error: %v", resps[4])
	}
	if e := resps[5]["error"].(map[string]any); e["code"].(float64) != codeInvalidParams {
		t.Fatalf("unknown tool: %v", resps[5])
	}
	for i, want := range map[int]string{6: "must be a UUID", 7: `unknown field "categry"`} {
		r := resps[i]["result"].(map[string]any)
		text := r["content"].([]any)[0].(map[string]any)["text"].(string)
		if r["isError"] != true || !strings.Contains(text, want) {
			t.Fatalf("response %d: %v", i, r)
		}
	}
	if v := resps[8]["result"].(map[string]any)["protocolVersion"]; v != supportedVersions[0] {
		t.Fatalf("unsupported version answered with %v", v)
	}
}

func TestReadOnlyTools(t *testing.T) {
	for _, tl := range Tools(&Client{}, true) {
		if !tl.ReadOnly {
			t.Fatalf("%s in read-only set", tl.Name)
		}
	}
	if n := len(Tools(&Client{}, true)); n != 5 {
		t.Fatalf("read-only tools: %d", n)
	}
}

// TestClientRequests checks headers, query and error mapping against a fake API.
func TestClientRequests(t *testing.T) {
	var got *http.Request
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"validation failed","fields":{"amount":"must be greater than 0"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"transactions":[],"count":0,"next_cursor":null}`))
	}))
	defer api.Close()
	s := &Server{Tools: Tools(&Client{BaseURL: api.URL + "/", Token: "ext_secret"}, false)}

	resps := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_transactions","arguments":{"type":"expense","from":"2026-10-01T00:00:00+08:00"}}}`)
	if got.Header.Get("Authorization") != "Bearer ext_secret" || got.URL.Path != "/api/transactions" ||
		got.URL.Query().Get("from") != "2026-10-01T00:00:00+08:00" || got.URL.Query().Get("limit") != "50" || got.URL.Query().Get("type") != "expense" {
		t.Fatalf("request: %v %v", got.URL, got.Header)
	}
	if r := resps[0]["result"].(map[string]any); r["isError"] != false {
		t.Fatalf("list: %v", r)
	}

	resps = rpc(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_transaction","arguments":{"type":"expense","amount":0,"currency":"THB","balance_uid":"x","spent_at":"2026-10-01T00:00:00+08:00","note":""}}}`)
	if got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type: %v", got.Header)
	}
	r := resps[0]["result"].(map[string]any)
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if r["isError"] != true || !strings.Contains(text, "HTTP 422") || !strings.Contains(text, "must be greater than 0") || strings.Contains(text, "ext_secret") {
		t.Fatalf("error result: %v", text)
	}
}

func TestMergeUpdate(t *testing.T) {
	transfer := map[string]any{
		"type": "transfer", "amount": "100.00", "currency": "USD", "balance_uid": "a", "to_balance_uid": "b",
		"category_uid": nil, "spent_at": "2026-10-12T10:00:00+08:00", "note": "fx", "rate_source": "manual",
		"to_amount": "3361.50", "to_currency": "THB", "version": json.Number("2"),
	}
	repayCharges := map[string]any{"type": "repay_debt", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "l", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00", "note": "", "interest_amount": "12.50", "fee_amount": "3.00"}
	assetSale := map[string]any{"type": "asset_sale", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "g", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00", "note": "", "gain_loss": "-12.50", "gain_loss_minor": json.Number("-1250"), "entire_asset_sale": true}
	foreignExpense := map[string]any{
		"type": "expense", "amount": "850.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": nil, "category_uid": "c",
		"spent_at": "2026-10-12T10:00:00+08:00", "note": "", "foreign_amount": "25.00", "foreign_amount_minor": json.Number("2500"), "foreign_currency": "USD",
	}
	cases := []struct {
		name    string
		cur     map[string]any
		changes map[string]string
		want    map[string]string
		wantErr string
	}{
		{
			name:    "note only, manual rate kept by the server",
			cur:     transfer,
			changes: map[string]string{"note": "new"},
			want:    map[string]string{"type": "transfer", "amount": "100.00", "currency": "USD", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00", "note": "new"},
		},
		{
			name:    "same-day time change is fine",
			cur:     transfer,
			changes: map[string]string{"spent_at": "2026-10-12T18:00:00+08:00"},
			want:    map[string]string{"type": "transfer", "amount": "100.00", "currency": "USD", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T18:00:00+08:00", "note": "fx"},
		},
		{
			name:    "amount change on a manual rate needs to_amount",
			cur:     transfer,
			changes: map[string]string{"amount": "200"},
			wantErr: "also send to_amount",
		},
		{
			name:    `to_amount "" switches to ECB`,
			cur:     transfer,
			changes: map[string]string{"amount": "200", "to_amount": ""},
			want:    map[string]string{"type": "transfer", "amount": "200", "currency": "USD", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00", "note": "fx"},
		},
		{
			name:    "transfer to expense drops to_balance_uid",
			cur:     transfer,
			changes: map[string]string{"type": "expense", "category_uid": "c", "note": ""},
			want:    map[string]string{"type": "expense", "amount": "100.00", "currency": "USD", "balance_uid": "a", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "expense to transfer drops category_uid",
			cur:     map[string]any{"type": "expense", "amount": "5", "currency": "THB", "balance_uid": "a", "to_balance_uid": nil, "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00", "note": ""},
			changes: map[string]string{"type": "transfer", "to_balance_uid": "b", "import_ref": ""},
			want:    map[string]string{"type": "transfer", "amount": "5", "currency": "THB", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00", "import_ref": ""},
		},
		{
			name:    "foreign amount is carried over on a note change",
			cur:     foreignExpense,
			changes: map[string]string{"note": "dinner"},
			want:    map[string]string{"type": "expense", "amount": "850.00", "currency": "THB", "balance_uid": "a", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00", "note": "dinner", "foreign_amount": "25.00", "foreign_currency": "USD"},
		},
		{
			name:    `foreign_amount "" clears both halves`,
			cur:     foreignExpense,
			changes: map[string]string{"foreign_amount": ""},
			want:    map[string]string{"type": "expense", "amount": "850.00", "currency": "THB", "balance_uid": "a", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "expense to transfer drops the foreign amount",
			cur:     foreignExpense,
			changes: map[string]string{"type": "transfer", "to_balance_uid": "b"},
			want:    map[string]string{"type": "transfer", "amount": "850.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "repay_debt keeps its charges and category on a note change",
			cur:     repayCharges,
			changes: map[string]string{"note": "Nov"},
			want:    map[string]string{"type": "repay_debt", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "l", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00", "note": "Nov", "interest_amount": "12.50", "fee_amount": "3.00"},
		},
		{
			name:    "clearing the last charge drops the repay_debt category",
			cur:     repayCharges,
			changes: map[string]string{"interest_amount": "", "fee_amount": ""},
			want:    map[string]string{"type": "repay_debt", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "l", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "repay_debt to transfer drops charges and category",
			cur:     repayCharges,
			changes: map[string]string{"type": "transfer"},
			want:    map[string]string{"type": "transfer", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "l", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "asset_sale keeps its result, flag and category on a note change",
			cur:     assetSale,
			changes: map[string]string{"note": "sold"},
			want:    map[string]string{"type": "asset_sale", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "g", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00", "note": "sold", "gain_loss": "-12.50", "entire_asset_sale": "true"},
		},
		{
			name:    "clearing the gain_loss drops the asset_sale category",
			cur:     assetSale,
			changes: map[string]string{"gain_loss": "", "entire_asset_sale": "false"},
			want:    map[string]string{"type": "asset_sale", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "g", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "asset_sale to asset_purchase drops the sale fields",
			cur:     assetSale,
			changes: map[string]string{"type": "asset_purchase"},
			want:    map[string]string{"type": "asset_purchase", "amount": "100.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "g", "category_uid": "c", "spent_at": "2026-10-12T10:00:00+08:00"},
		},
		{
			name:    "balance adjustment keeps its direction",
			cur:     map[string]any{"type": "balance_adjustment", "amount": "10.00", "currency": "THB", "balance_uid": "a", "spent_at": "2026-10-12T10:00:00+08:00", "adjustment_direction": "decrease", "note": "Manual edit"},
			changes: map[string]string{"note": "x", "amount": "12"},
			want:    map[string]string{"type": "balance_adjustment", "amount": "12", "currency": "THB", "balance_uid": "a", "spent_at": "2026-10-12T10:00:00+08:00", "adjustment_direction": "decrease", "note": "x"},
		},
		{
			name:    "balance adjustment direction and balance change",
			cur:     map[string]any{"type": "balance_adjustment", "amount": "10.00", "currency": "THB", "balance_uid": "a", "spent_at": "2026-10-12T10:00:00+08:00", "adjustment_direction": "decrease"},
			changes: map[string]string{"adjustment_direction": "increase", "balance_uid": "b"},
			want:    map[string]string{"type": "balance_adjustment", "amount": "10.00", "currency": "THB", "balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00", "adjustment_direction": "increase"},
		},
		{
			name:    "an explicit direction on another type is passed on (the API rejects it)",
			cur:     map[string]any{"type": "transfer", "amount": "10.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00", "adjustment_direction": nil},
			changes: map[string]string{"adjustment_direction": "increase"},
			want:    map[string]string{"type": "transfer", "amount": "10.00", "currency": "THB", "balance_uid": "a", "to_balance_uid": "b", "spent_at": "2026-10-12T10:00:00+08:00", "adjustment_direction": "increase"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeUpdate(tc.cur, tc.changes)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(tc.want)
			if string(g) != string(w) {
				t.Fatalf("got  %s\nwant %s", g, w)
			}
		})
	}
}

func TestDecimalArg(t *testing.T) {
	var a struct {
		A decimal `json:"a"`
		B decimal `json:"b"`
	}
	if err := decodeArgs([]byte(`{"a":"120.50","b":120.50}`), &a); err != nil || a.A != "120.50" || a.B != "120.50" {
		t.Fatalf("%v %+v", err, a)
	}
	if err := decodeArgs([]byte(`{"a":true}`), &a); err == nil {
		t.Fatal("bool accepted as decimal")
	}
}

func TestMergeBalanceUpdate(t *testing.T) {
	card := map[string]any{
		"uid": "u", "name": "Card", "type": "credit_card", "kind": "liability", "currency": "THB", "description": "main",
		"balance": nil, "debt": "100.00", "limit": "50000.00", "available": "49900.00", "version": json.Number("3"),
	}
	got := mergeBalanceUpdate(card, map[string]string{"limit": "60000", "description": ""})
	want := map[string]string{"name": "Card", "type": "credit_card", "currency": "THB", "description": "", "debt": "100.00", "limit": "60000"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q (%v)", k, got[k], v, got)
		}
	}
}

// TestUpdateBalanceRequests checks the read-modify-write against a fake API:
// PUT with If-Match and the merged body, then hide with the new version.
func TestUpdateBalanceRequests(t *testing.T) {
	const uid = "11111111-2222-4333-8444-555555555555"
	var calls []string
	var putBody map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("If-Match"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"4"`)
			_, _ = w.Write([]byte(`{"uid":"` + uid + `","name":"Cash","type":"payment_account","currency":"THB","description":"","balance":"10.00","debt":null,"limit":null,"hidden":false,"version":4}`))
		case http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.Header().Set("ETag", `"5"`)
			_, _ = w.Write([]byte(`{"uid":"` + uid + `","name":"Old cash","type":"payment_account","currency":"THB","balance":"10.00","hidden":false,"version":5}`))
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"uid":"` + uid + `","name":"Old cash","hidden":true,"version":6}`))
		}
	}))
	defer api.Close()
	s := &Server{Tools: Tools(&Client{BaseURL: api.URL, Token: "t"}, false)}

	out := mustCall(t, s, "update_balance", map[string]any{"uid": uid, "name": "Old cash", "hidden": true})
	if out["hidden"] != true || out["version"].(float64) != 6 {
		t.Fatalf("result: %v", out)
	}
	want := []string{"GET /api/balances/" + uid + " ", "PUT /api/balances/" + uid + ` "4"`, "POST /api/balances/" + uid + `/hide "5"`}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls: %q", calls)
	}
	if putBody["name"] != "Old cash" || putBody["balance"] != "10.00" || putBody["type"] != "payment_account" || putBody["debt"] != nil {
		t.Fatalf("put body: %v", putBody)
	}

	// hidden alone: no PUT
	calls = nil
	mustCall(t, s, "update_balance", map[string]any{"uid": uid, "hidden": true})
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "POST /api/balances/"+uid+`/hide "4"`) {
		t.Fatalf("hide only: %q", calls)
	}

	// type, stale version and empty updates write nothing
	calls = nil
	for _, args := range []map[string]any{
		{"uid": uid, "type": "credit_card"},
		{"uid": uid, "version": 3, "name": "x"},
		{"uid": uid},
	} {
		if text, isErr := callTool(t, s, "update_balance", args); !isErr {
			t.Fatalf("%v: no error: %s", args, text)
		}
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("wrote: %q", calls)
		}
	}
}

func TestLocalDayBounds(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	time.Local = ny
	for _, tc := range []struct{ in, start, end string }{
		{"2026-10-01", "2026-10-01T00:00:00-04:00", "2026-10-01T23:59:59.999999999-04:00"},
		// DST ends: the day starts at -04:00 and ends at -05:00.
		{"2026-11-01", "2026-11-01T00:00:00-04:00", "2026-11-01T23:59:59.999999999-05:00"},
		// Exact times and junk pass through for the server to judge.
		{"2026-10-01T09:00:00+08:00", "2026-10-01T09:00:00+08:00", "2026-10-01T09:00:00+08:00"},
		{"yesterday", "yesterday", "yesterday"},
		{"", "", ""},
	} {
		if got := localDayStart(tc.in); got != tc.start {
			t.Errorf("localDayStart(%q) = %q, want %q", tc.in, got, tc.start)
		}
		if got := localDayEnd(tc.in); got != tc.end {
			t.Errorf("localDayEnd(%q) = %q, want %q", tc.in, got, tc.end)
		}
	}
}
