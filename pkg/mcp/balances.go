package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var balanceTypes = []string{"payment_account", "credit_card", "other_asset", "other_liability"}

const (
	descBalName     = "Name of the balance, at most 100 characters, e.g. \"KBank savings\"."
	descBalType     = "Balance type: payment_account (bank account, wallet, cash: can pay expenses), credit_card (can pay expenses, has a debt and a limit), other_asset (investment, gold, property, a car...), other_liability (loan, mortgage...). Cannot be changed later."
	descBalCurrency = "ISO 4217 code, e.g. THB. Cannot be changed once a transaction uses the balance."
	descBalDesc     = "Optional free text, at most 1000 characters."
	descBalBalance  = `payment_account and other_asset only (required there): the current amount, a decimal string with at most the currency's minor digits, e.g. "1200.50"; may be negative (overdraft).`
	descBalDebt     = `credit_card and other_liability only (required there): the amount owed, e.g. "3500"; "0" for none; negative means overpaid (in credit).`
	descBalLimit    = `credit_card and other_liability only (required there): the credit limit, >= 0, e.g. "50000"; "0" when there is none (e.g. a loan).`
)

func balanceProperties() map[string]any {
	return map[string]any{
		"name":        str(descBalName),
		"type":        enum(descBalType, balanceTypes...),
		"currency":    str(descBalCurrency),
		"description": str(descBalDesc),
		"balance":     decimalSchema(descBalBalance),
		"debt":        decimalSchema(descBalDebt),
		"limit":       decimalSchema(descBalLimit),
	}
}

// balanceFields are the writable balance fields, as the API names them. nil
// means "not given".
type balanceFields struct {
	Name        *string  `json:"name"`
	Type        *string  `json:"type"`
	Currency    *string  `json:"currency"`
	Description *string  `json:"description"`
	Balance     *decimal `json:"balance"`
	Debt        *decimal `json:"debt"`
	Limit       *decimal `json:"limit"`
}

// given returns the fields that were set, keyed by API name.
func (f balanceFields) given() map[string]string {
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
	put("name", f.Name)
	put("type", f.Type)
	put("currency", f.Currency)
	put("description", f.Description)
	putD("balance", f.Balance)
	putD("debt", f.Debt)
	putD("limit", f.Limit)
	return m
}

// ---- get_balance ------------------------------------------------------------

func getBalanceTool(c *Client) Tool {
	return Tool{
		Name:        "get_balance",
		Title:       "Get a balance",
		Description: "Get one balance by uid (hidden ones too), with its current amount and version.",
		InputSchema: obj(map[string]any{"uid": str("The balance uid, from list_balances.")}, "uid"),
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
			v, _, err := c.getJSON(ctx, "/api/balances/"+uid, nil)
			return v, err
		},
	}
}

// ---- create_balance ---------------------------------------------------------

func createBalanceTool(c *Client) Tool {
	return Tool{
		Name:  "create_balance",
		Title: "Create a balance",
		Description: "Add a balance (bank account, wallet, credit card, asset or loan). " +
			"Only use values the user gave: never invent the name, currency or amounts; ask when one is missing. " +
			"Check list_balances (include_hidden: true) first: if a balance with that name already exists, ask the user whether they mean it instead. " +
			"Assets (payment_account, other_asset) need balance; liabilities (credit_card, other_liability) need debt and limit. " +
			"Returns the stored balance with its uid and version.",
		InputSchema: obj(balanceProperties(), "name", "type", "currency"),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a balanceFields
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			body := map[string]string{}
			for k, v := range a.given() {
				if strings.TrimSpace(v) != "" {
					body[k] = v
				}
			}
			res, err := c.do(ctx, http.MethodPost, "/api/balances", nil, body, "")
			if err != nil {
				return nil, err
			}
			return decode(res.Body)
		},
	}
}

// ---- update_balance ---------------------------------------------------------

func updateBalanceTool(c *Client) Tool {
	props := balanceProperties()
	delete(props, "type") // fixed at creation
	props["uid"] = str("uid of the balance to change, from list_balances.")
	props["version"] = map[string]any{"type": "integer", "minimum": 1, "description": "Optional: the version you last showed the user. If the balance has changed since (e.g. a transaction moved it), nothing is written and the current record is returned in the error."}
	props["description"] = str(descBalDesc + ` Send "" to clear it.`)
	props["balance"] = decimalSchema(descBalBalance + " Setting it overrides what transactions did and records a balance_adjustment transaction for the difference; only when the user gives the new amount (e.g. to match a statement).")
	props["debt"] = decimalSchema(descBalDebt + " Setting it overrides what transactions did and records a balance_adjustment transaction for the difference; only when the user gives the new amount.")
	props["currency"] = str(descBalCurrency + " When the currency has other minor digits, send the amounts again too.")
	props["hidden"] = map[string]any{"type": "boolean", "description": "true hides the balance (a closed account or paid-off loan: it leaves list_balances but keeps its transactions); false shows it again."}
	return Tool{
		Name:  "update_balance",
		Title: "Update a balance",
		Description: "Change some fields of a balance; fields you leave out keep their current values. The type cannot change. " +
			"Renaming or changing the description or limit moves no money. Setting balance (assets) or debt (liabilities) replaces the current amount, " +
			"and the server records the difference as a balance_adjustment transaction (which can later be edited or deleted with update_transaction / delete_transaction). " +
			"Normally balances follow their transactions: prefer recording the missing transaction over overriding the amount, and confirm an override with the user. " +
			"Returns the updated balance.",
		InputSchema: obj(props, "uid"),
		Idempotent:  true,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				UID     string `json:"uid"`
				Version *int64 `json:"version"`
				Hidden  *bool  `json:"hidden"`
				balanceFields
			}
			if err := decodeArgs(raw, &a); err != nil {
				return nil, err
			}
			if a.Type != nil {
				return nil, errors.New("type cannot be changed after a balance is created")
			}
			uid, err := pathUID("uid", a.UID)
			if err != nil {
				return nil, err
			}
			changes := a.given()
			if len(changes) == 0 && a.Hidden == nil {
				return nil, errors.New("nothing to change: pass at least one field to update")
			}
			v, res, err := c.getJSON(ctx, "/api/balances/"+uid, nil)
			if err != nil {
				return nil, err
			}
			cur, ok := v.(map[string]any)
			if !ok {
				return nil, errNotObject
			}
			curVersion, _ := strconv.ParseInt(fmt.Sprint(cur["version"]), 10, 64)
			if a.Version != nil && *a.Version != curVersion {
				b, _ := json.Marshal(map[string]any{"error": "version conflict: the balance changed since you read it; nothing was written", "code": "version_conflict", "current": cur})
				return nil, &APIError{Status: http.StatusConflict, Body: b}
			}
			ifMatch := res.ETag
			if ifMatch == "" {
				ifMatch = etag(curVersion)
			}
			out := any(cur)
			if len(changes) > 0 {
				put, err := c.do(ctx, http.MethodPut, "/api/balances/"+uid, nil, mergeBalanceUpdate(cur, changes), ifMatch)
				if err != nil {
					return nil, err
				}
				if out, err = decode(put.Body); err != nil {
					return nil, err
				}
				ifMatch = put.ETag
				if m, ok := out.(map[string]any); ok && ifMatch == "" {
					n, _ := strconv.ParseInt(fmt.Sprint(m["version"]), 10, 64)
					ifMatch = etag(n)
				}
			}
			if a.Hidden != nil {
				if m, ok := out.(map[string]any); !ok || m["hidden"] != *a.Hidden {
					action := "unhide"
					if *a.Hidden {
						action = "hide"
					}
					hres, err := c.do(ctx, http.MethodPost, "/api/balances/"+uid+"/"+action, nil, nil, ifMatch)
					if err != nil {
						if len(changes) > 0 {
							return nil, fmt.Errorf("the other changes were saved, but %s failed: %w", action, err)
						}
						return nil, err
					}
					return decode(hres.Body)
				}
			}
			return out, nil
		},
	}
}

// mergeBalanceUpdate builds the PUT body (a full replace): the current
// record's editable fields with changes applied. Amount fields that do not
// apply to the type are null in the record and so left out.
func mergeBalanceUpdate(cur map[string]any, changes map[string]string) map[string]string {
	body := map[string]string{}
	for _, k := range []string{"name", "type", "currency", "description", "balance", "debt", "limit"} {
		if v, ok := cur[k]; ok && v != nil {
			body[k] = fmt.Sprint(v)
		}
	}
	for k, v := range changes {
		body[k] = v
	}
	return body
}
