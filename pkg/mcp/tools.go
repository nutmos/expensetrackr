package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Tool is one MCP tool.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	// ReadOnly tools only read; the others change data (Destructive: delete).
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	Handler     func(ctx context.Context, args json.RawMessage) (any, error)
}

func (t Tool) descriptor() map[string]any {
	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": t.Description,
		"inputSchema": t.InputSchema,
		"annotations": map[string]any{
			"title":           t.Title,
			"readOnlyHint":    t.ReadOnly,
			"destructiveHint": t.Destructive,
			"idempotentHint":  t.Idempotent,
			"openWorldHint":   false,
		},
	}
}

// Tools returns the tools backed by c. With readOnly, only the tools that
// never write are returned.
func Tools(c *Client, readOnly bool) []Tool {
	all := []Tool{
		listBalancesTool(c),
		listCategoriesTool(c),
		listTransactionsTool(c),
		getTransactionTool(c),
		createTransactionTool(c),
		updateTransactionTool(c),
		deleteTransactionTool(c),
	}
	if !readOnly {
		return all
	}
	var ro []Tool
	for _, t := range all {
		if t.ReadOnly {
			ro = append(ro, t)
		}
	}
	return ro
}

// ---- argument helpers -------------------------------------------------------

// decodeArgs decodes tool arguments strictly: an unknown argument is an
// error, so a misspelt field (e.g. "category" for "category_uid") is
// reported instead of silently ignored.
func decodeArgs(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// decimal accepts a JSON string or number and keeps its exact text, so
// "120.50" and 120.50 both reach the API as "120.50"-ish text (the API
// parses it exactly; no float rounding happens here).
type decimal string

func (d *decimal) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = decimal(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New("must be a decimal string such as \"120.50\"")
	}
	*d = decimal(n.String())
	return nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// pathUID checks a uid before it is put into a URL path.
func pathUID(name, uid string) (string, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if !uuidRe.MatchString(uid) {
		return "", fmt.Errorf("%s must be a UUID (the uid from list_transactions), got %q", name, uid)
	}
	return strings.ToLower(uid), nil
}

func setIf(q url.Values, key, val string) {
	if v := strings.TrimSpace(val); v != "" {
		q.Set(key, v)
	}
}

// schema helpers
func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}

// decimalSchema is a decimal amount, preferably a string.
func decimalSchema(desc string) map[string]any {
	return map[string]any{"type": []string{"string", "number"}, "description": desc}
}

var writableTypes = []string{"expense", "income", "transfer", "asset_purchase", "return_on_investment", "repay_debt"}

const (
	descAmount     = `Amount as a decimal string, > 0, with at most the currency's minor digits, e.g. "120.50" (THB), "1500" (JPY). Never negative: the type gives the direction.`
	descCurrency   = "ISO 4217 code, e.g. THB. Must equal the currency of the source balance (balance_uid)."
	descBalance    = "uid of the source balance: the paying balance (expense, asset_purchase), receiving balance (income, return_on_investment) or the balance money leaves (transfer; repay_debt: a payment_account). From list_balances."
	descToBalance  = "uid of the destination balance. Required for transfer (any other balance), asset_purchase (an other_asset) and return_on_investment (the other_asset the return came from; its value is not changed) and repay_debt (the credit_card or other_liability being paid down). Not allowed otherwise."
	descCategory   = "uid of the category, from list_categories. Required for every type except transfer (not allowed) and repay_debt: an expense category for expense and asset_purchase, an income category for income and return_on_investment. For repay_debt it is optional and only allowed with interest_amount or fee_amount: the expense category of those charges."
	descSpentAt    = "When it happened: RFC 3339 with a UTC offset, e.g. 2026-10-06T12:30:00+08:00. Use the user's offset; ask if unknown."
	descNote       = "Optional free text, at most 1000 characters."
	descToAmount   = "Transfer only: the amount received by the destination, in its currency. Same currency: omit (or equal amount). Cross-currency: the amount the user says was received (manual rate); omit to convert at the ECB rate of the spent_at date. Never invent it."
	descToCurrency = "Transfer only, optional: must equal the destination balance's currency."
	descForeignAmt = "Optional, expense / income / asset_purchase / return_on_investment only: the amount in another currency, for reference (e.g. 25.00 when a USD purchase was charged as THB to the account). Never affects balances; amount stays the amount in the balance currency. Send with foreign_currency. Only when the user gives it."
	descForeignCur = "ISO 4217 code of foreign_amount, e.g. USD. Required with foreign_amount; must differ from currency."
	descInterest   = "Optional, repay_debt only: interest paid together with the repayment, in currency. amount is then the principal only (what the debt goes down by); the payment account pays amount + interest_amount + fee_amount. Counted as an expense in reports. Only when the user separates it."
	descFee        = "Optional, repay_debt only: other fees paid together with the repayment (late fee, service charge), in currency. Paid from the account like interest_amount; the debt is not reduced by it."
	descImportRef  = "Optional idempotency key (at most 200 characters, unique per balance), e.g. a bank statement line id. Usually omit."
	descType       = "Transaction type. expense (spending), income (money in), transfer (between the user's own balances, incl. paying a credit card), asset_purchase (buying a tracked asset), return_on_investment (dividend/interest from a tracked asset), repay_debt (paying down a credit card or loan from a payment account)."
)

// ---- list_balances ----------------------------------------------------------

func listBalancesTool(c *Client) Tool {
	return Tool{
		Name:  "list_balances",
		Title: "List balances",
		Description: "List the user's balances (bank accounts, wallets, credit cards, assets, loans) with uid, name, type, currency and current amount, plus per-currency totals. " +
			"Use it to turn an account name into the balance_uid a transaction needs. Names can repeat: if a name matches more than one balance, ask the user which.",
		InputSchema: obj(map[string]any{
			"type":    enum("Only balances of this type.", "payment_account", "credit_card", "other_asset", "other_liability"),
			"payable": map[string]any{"type": "boolean", "description": "Only balances that can pay for an expense (payment_account and credit_card). Cannot be combined with type."},
		}),
		ReadOnly:   true,
		Idempotent: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Type    string `json:"type"`
				Payable bool   `json:"payable"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			q := url.Values{}
			setIf(q, "type", a.Type)
			if a.Payable {
				q.Set("payable", "true")
			}
			v, _, err := c.getJSON(ctx, "/api/balances", q)
			return v, err
		},
	}
}

// ---- list_categories --------------------------------------------------------

func listCategoriesTool(c *Client) Tool {
	return Tool{
		Name:        "list_categories",
		Title:       "List categories",
		Description: "List the user's categories (uid, name, type expense|income, description). Use it to turn a category name into a category_uid. Do not create categories; if none fits, ask the user.",
		InputSchema: obj(map[string]any{
			"type": enum("Only categories of this type.", "expense", "income"),
		}),
		ReadOnly:   true,
		Idempotent: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Type string `json:"type"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			q := url.Values{}
			setIf(q, "type", a.Type)
			v, _, err := c.getJSON(ctx, "/api/categories", q)
			return v, err
		},
	}
}

// ---- list_transactions ------------------------------------------------------

const (
	defaultListLimit = 50
	maxListLimit     = 500
)

func listTransactionsTool(c *Client) Tool {
	return Tool{
		Name:  "list_transactions",
		Title: "List transactions",
		Description: "List the user's transactions, newest first by spent_at, with optional filters. " +
			"Each row has uid, type, amount, currency, balance_uid/account (name), to_balance_uid/to_account, category_uid, spent_at, note and version. " +
			"Category names are not included: map category_uid with list_categories. " +
			"Paged: while next_cursor is not null, call again with cursor=next_cursor and the same filters to get more.",
		InputSchema: obj(map[string]any{
			"from":         str("Only transactions at or after this time, RFC 3339 with offset, e.g. 2026-10-01T00:00:00+08:00."),
			"to":           str("Only transactions at or before this time, RFC 3339 with offset, e.g. 2026-10-31T23:59:59+08:00."),
			"type":         enum("Only this type.", append(append([]string{}, writableTypes...), "balance_adjustment")...),
			"category_uid": str("Only transactions in this category (uid from list_categories)."),
			"balance_uid":  str("Only transactions from or to this balance (uid from list_balances)."),
			"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": maxListLimit, "description": fmt.Sprintf("Page size, default %d, at most %d.", defaultListLimit, maxListLimit)},
			"cursor":       str("next_cursor from the previous page (keep the other filters the same)."),
		}),
		ReadOnly:   true,
		Idempotent: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				From        string `json:"from"`
				To          string `json:"to"`
				Type        string `json:"type"`
				CategoryUID string `json:"category_uid"`
				BalanceUID  string `json:"balance_uid"`
				Limit       *int   `json:"limit"`
				Cursor      string `json:"cursor"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			limit := defaultListLimit
			if a.Limit != nil {
				if *a.Limit < 1 || *a.Limit > maxListLimit {
					return nil, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
				}
				limit = *a.Limit
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			setIf(q, "from", a.From)
			setIf(q, "to", a.To)
			setIf(q, "type", a.Type)
			setIf(q, "category_uid", a.CategoryUID)
			setIf(q, "balance_uid", a.BalanceUID)
			setIf(q, "cursor", a.Cursor)
			v, _, err := c.getJSON(ctx, "/api/transactions", q)
			return v, err
		},
	}
}

// ---- get_transaction --------------------------------------------------------

func getTransactionTool(c *Client) Tool {
	return Tool{
		Name:        "get_transaction",
		Title:       "Get a transaction",
		Description: "Get one transaction by uid, with its current version.",
		InputSchema: obj(map[string]any{"uid": str("The transaction uid.")}, "uid"),
		ReadOnly:    true,
		Idempotent:  true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				UID string `json:"uid"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			uid, err := pathUID("uid", a.UID)
			if err != nil {
				return nil, err
			}
			v, _, err := c.getJSON(ctx, "/api/transactions/"+uid, nil)
			return v, err
		},
	}
}

// ---- create_transaction -----------------------------------------------------

// txFields are the writable transaction fields, as the API names them. nil
// means "not given".
type txFields struct {
	Type         *string  `json:"type"`
	Amount       *decimal `json:"amount"`
	Currency     *string  `json:"currency"`
	BalanceUID   *string  `json:"balance_uid"`
	ToBalanceUID *string  `json:"to_balance_uid"`
	CategoryUID  *string  `json:"category_uid"`
	SpentAt      *string  `json:"spent_at"`
	Note         *string  `json:"note"`
	ToAmount     *decimal `json:"to_amount"`
	ToCurrency   *string  `json:"to_currency"`
	ImportRef    *string  `json:"import_ref"`
	// Optional foreign-currency amount (reference only).
	ForeignAmount   *decimal `json:"foreign_amount"`
	ForeignCurrency *string  `json:"foreign_currency"`
	// Optional interest and fees of a debt repayment.
	InterestAmount *decimal `json:"interest_amount"`
	FeeAmount      *decimal `json:"fee_amount"`
}

// given returns the fields that were set, keyed by API name.
func (f txFields) given() map[string]string {
	m := map[string]string{}
	put := func(k string, v *string) {
		if v != nil {
			m[k] = *v
		}
	}
	putD := func(k string, v *decimal) {
		if v != nil {
			m[k] = string(*v)
		}
	}
	put("type", f.Type)
	putD("amount", f.Amount)
	put("currency", f.Currency)
	put("balance_uid", f.BalanceUID)
	put("to_balance_uid", f.ToBalanceUID)
	put("category_uid", f.CategoryUID)
	put("spent_at", f.SpentAt)
	put("note", f.Note)
	putD("to_amount", f.ToAmount)
	put("to_currency", f.ToCurrency)
	put("import_ref", f.ImportRef)
	putD("foreign_amount", f.ForeignAmount)
	put("foreign_currency", f.ForeignCurrency)
	putD("interest_amount", f.InterestAmount)
	putD("fee_amount", f.FeeAmount)
	return m
}

func txProperties() map[string]any {
	return map[string]any{
		"type":             enum(descType, writableTypes...),
		"amount":           decimalSchema(descAmount),
		"currency":         str(descCurrency),
		"balance_uid":      str(descBalance),
		"to_balance_uid":   str(descToBalance),
		"category_uid":     str(descCategory),
		"spent_at":         str(descSpentAt),
		"note":             str(descNote),
		"to_amount":        decimalSchema(descToAmount),
		"to_currency":      str(descToCurrency),
		"import_ref":       str(descImportRef),
		"foreign_amount":   decimalSchema(descForeignAmt),
		"foreign_currency": str(descForeignCur),
		"interest_amount":  decimalSchema(descInterest),
		"fee_amount":       decimalSchema(descFee),
	}
}

func createTransactionTool(c *Client) Tool {
	return Tool{
		Name:  "create_transaction",
		Title: "Create a transaction",
		Description: "Record one transaction; the affected balances move at once. " +
			"Only use values the user gave: never invent the amount, account, category or date. " +
			"Resolve balance and category names to uids first (list_balances, list_categories). " +
			"Returns the stored transaction (uid, version, account names; for a cross-currency transfer also to_amount and the exchange rate).",
		InputSchema: obj(txProperties(), "type", "amount", "currency", "balance_uid", "spent_at"),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a txFields
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			body := map[string]string{}
			for k, v := range a.given() {
				if strings.TrimSpace(v) != "" {
					body[k] = v
				}
			}
			res, err := c.do(ctx, http.MethodPost, "/api/transactions", nil, body, "")
			if err != nil {
				return nil, err
			}
			return decode(res.Body)
		},
	}
}

// ---- update_transaction -----------------------------------------------------

func updateTransactionTool(c *Client) Tool {
	props := txProperties()
	props["uid"] = str("uid of the transaction to change.")
	props["version"] = map[string]any{"type": "integer", "minimum": 1, "description": "Optional: the version you last showed the user. If the transaction has changed since, nothing is written and the current record is returned in the error."}
	props["note"] = str(descNote + ` Send "" to clear it.`)
	props["to_amount"] = decimalSchema(descToAmount + ` On a cross-currency transfer with a manual rate, send it again when changing amount, currency, balances or the date (or "" to switch to the ECB rate).`)
	props["import_ref"] = str(descImportRef + ` Send "" to clear it.`)
	props["foreign_amount"] = decimalSchema(descForeignAmt + ` Send "" (and foreign_currency "") to remove it.`)
	props["interest_amount"] = decimalSchema(descInterest + ` Send "" to remove it.`)
	props["fee_amount"] = decimalSchema(descFee + ` Send "" to remove it.`)
	props["adjustment_direction"] = enum("Balance adjustments only: whether the adjustment raised (increase) or lowered (decrease) the balance (for an asset) or the debt (for a credit card or other liability).", "increase", "decrease")
	return Tool{
		Name:  "update_transaction",
		Title: "Update a transaction",
		Description: "Change some fields of a transaction; fields you leave out keep their current values. " +
			"Balances are corrected automatically (the old effect is reversed, the new one applied). " +
			"When the type changes, send the fields the new type needs (e.g. category_uid when changing a transfer to an expense, to_balance_uid when changing to a transfer); " +
			"to_balance_uid is dropped automatically for types without a destination, category_uid for a transfer (and for a repay_debt left without interest or fees), foreign_amount/foreign_currency and interest_amount/fee_amount for types that do not allow them. " +
			"A balance_adjustment can be edited too (amount, adjustment_direction, balance_uid — any balance type —, spent_at, note) but stays a balance adjustment; its balance moves by the difference. Returns the updated transaction.",
		InputSchema: obj(props, "uid"),
		Idempotent:  true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				UID                 string  `json:"uid"`
				Version             *int64  `json:"version"`
				AdjustmentDirection *string `json:"adjustment_direction"`
				txFields
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			uid, err := pathUID("uid", a.UID)
			if err != nil {
				return nil, err
			}
			changes := a.given()
			if a.AdjustmentDirection != nil {
				changes["adjustment_direction"] = *a.AdjustmentDirection
			}
			if len(changes) == 0 {
				return nil, errors.New("nothing to change: pass at least one field to update")
			}
			v, res, err := c.getJSON(ctx, "/api/transactions/"+uid, nil)
			if err != nil {
				return nil, err
			}
			cur, ok := v.(map[string]any)
			if !ok {
				return nil, errNotObject
			}
			curVersion, _ := strconv.ParseInt(fmt.Sprint(cur["version"]), 10, 64)
			if a.Version != nil && *a.Version != curVersion {
				b, _ := json.Marshal(map[string]any{"error": "version conflict: the transaction changed since you read it; nothing was written", "code": "version_conflict", "current": cur})
				return nil, &APIError{Status: http.StatusConflict, Body: b}
			}
			body, err := mergeUpdate(cur, changes)
			if err != nil {
				return nil, err
			}
			ifMatch := res.ETag
			if ifMatch == "" {
				ifMatch = etag(curVersion)
			}
			put, err := c.do(ctx, http.MethodPut, "/api/transactions/"+uid, nil, body, ifMatch)
			if err != nil {
				return nil, err
			}
			return decode(put.Body)
		},
	}
}

// editable are the stored fields a PUT body carries over from the current
// record (to_amount, to_currency and import_ref are handled separately;
// adjustment_direction is set only on balance adjustments).
var editable = []string{"type", "amount", "currency", "balance_uid", "to_balance_uid", "category_uid", "spent_at", "note", "foreign_amount", "foreign_currency", "interest_amount", "fee_amount", "adjustment_direction"}

// mergeUpdate builds the PUT body: the current record's editable fields with
// changes applied, cleaned up for the resulting type.
func mergeUpdate(cur map[string]any, changes map[string]string) (map[string]string, error) {
	curStr := func(k string) string {
		if v, ok := cur[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	body := map[string]string{}
	for _, k := range editable {
		if v := curStr(k); v != "" {
			body[k] = v
		}
	}
	for k, v := range changes {
		body[k] = v
	}
	typ := strings.ToLower(strings.TrimSpace(body["type"]))
	if typ != "balance_adjustment" {
		delete(body, "adjustment_direction") // let the API reject an explicit one
		if v, ok := changes["adjustment_direction"]; ok {
			body["adjustment_direction"] = v
		}
	}
	_, explicitTo := changes["to_balance_uid"]
	_, explicitCat := changes["category_uid"]
	if typ != "transfer" && typ != "asset_purchase" && typ != "return_on_investment" && typ != "repay_debt" && !explicitTo {
		delete(body, "to_balance_uid")
	}
	if typ != "repay_debt" {
		for _, k := range []string{"interest_amount", "fee_amount"} {
			if _, explicit := changes[k]; !explicit {
				delete(body, k)
			}
		}
	}
	for _, k := range []string{"interest_amount", "fee_amount"} {
		if strings.TrimSpace(body[k]) == "" {
			delete(body, k)
		}
	}
	// A repay_debt may keep its category only while it has interest or fees.
	_, hasCharges := body["interest_amount"]
	if _, ok := body["fee_amount"]; ok {
		hasCharges = true
	}
	if (typ == "transfer" || (typ == "repay_debt" && !hasCharges)) && !explicitCat {
		delete(body, "category_uid")
	}
	_, explicitFA := changes["foreign_amount"]
	_, explicitFC := changes["foreign_currency"]
	if typ != "expense" && typ != "income" && typ != "asset_purchase" && typ != "return_on_investment" && !explicitFA && !explicitFC {
		delete(body, "foreign_amount")
		delete(body, "foreign_currency")
	}
	// Clearing either half of the foreign amount clears both.
	if (explicitFA && strings.TrimSpace(changes["foreign_amount"]) == "") || (explicitFC && strings.TrimSpace(changes["foreign_currency"]) == "") {
		delete(body, "foreign_amount")
		delete(body, "foreign_currency")
	}

	// A cross-currency transfer with a manual rate: the server keeps the
	// stored conversion only while nothing it depends on changes; otherwise
	// it would silently switch to the ECB rate. Make the caller decide.
	if _, given := changes["to_amount"]; !given && typ == "transfer" && curStr("rate_source") == "manual" {
		dateOf := func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		}
		var changed []string
		for _, k := range []string{"type", "amount", "currency", "balance_uid", "to_balance_uid"} {
			if v, ok := changes[k]; ok && !strings.EqualFold(strings.TrimSpace(v), curStr(k)) {
				changed = append(changed, k)
			}
		}
		if v, ok := changes["spent_at"]; ok && dateOf(strings.TrimSpace(v)) != dateOf(curStr("spent_at")) {
			changed = append(changed, "spent_at (date)")
		}
		if len(changed) > 0 {
			return nil, fmt.Errorf("this cross-currency transfer uses a manual rate (to_amount %s %s) and you changed %s: "+
				"also send to_amount (the amount received, from the user), or to_amount \"\" to use the ECB rate instead",
				curStr("to_amount"), curStr("to_currency"), strings.Join(changed, ", "))
		}
	}
	for k, v := range body { // "" means "clear": omit it (the API treats omitted as empty)
		if strings.TrimSpace(v) == "" && k != "import_ref" {
			delete(body, k)
		}
	}
	return body, nil
}

// ---- delete_transaction -----------------------------------------------------

func deleteTransactionTool(c *Client) Tool {
	return Tool{
		Name:  "delete_transaction",
		Title: "Delete a transaction",
		Description: "Permanently delete a transaction; its effect on the balances is reversed. " +
			"Confirm with the user first (show amount, account, date and note). Deleting a balance_adjustment reverts the balance correction it recorded.",
		InputSchema: obj(map[string]any{
			"uid":     str("uid of the transaction to delete."),
			"version": map[string]any{"type": "integer", "minimum": 1, "description": "Optional: the version you showed the user; if the transaction changed since, nothing is deleted."},
		}, "uid"),
		Destructive: true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				UID     string `json:"uid"`
				Version *int64 `json:"version"`
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			uid, err := pathUID("uid", a.UID)
			if err != nil {
				return nil, err
			}
			ifMatch := ""
			if a.Version != nil {
				if *a.Version < 1 {
					return nil, errors.New("version must be a positive integer")
				}
				ifMatch = etag(*a.Version)
			}
			if _, err := c.do(ctx, http.MethodDelete, "/api/transactions/"+uid, nil, nil, ifMatch); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true, "uid": uid}, nil
		},
	}
}
