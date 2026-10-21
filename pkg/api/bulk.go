package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

const (
	// MaxBulkItems is the most transactions one POST /api/transactions/bulk accepts.
	MaxBulkItems = 500
	// maxBulkBodyBytes caps the bulk request body.
	maxBulkBodyBytes = 2 << 20
)

type bulkRequest struct {
	Transactions []json.RawMessage `json:"transactions"`
	Atomic       *bool             `json:"atomic"`  // default true
	DryRun       bool              `json:"dry_run"` // default false
}

type bulkItemError struct {
	Message string            `json:"message"`
	Code    string            `json:"code"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type bulkItemResult struct {
	Index       int                      `json:"index"`
	Status      store.BulkStatus         `json:"status"`
	Transaction *transaction.Transaction `json:"transaction,omitempty"`
	ExistingUID string                   `json:"existing_uid,omitempty"`
	Error       *bulkItemError           `json:"error,omitempty"`
}

type bulkResponse struct {
	Created  int               `json:"created"`
	Skipped  int               `json:"skipped"`
	Failed   int               `json:"failed"`
	Valid    int               `json:"valid"`
	DryRun   bool              `json:"dry_run"`
	Atomic   bool              `json:"atomic"`
	Written  bool              `json:"written"`
	Results  []bulkItemResult  `json:"results"`
	Balances []balance.Balance `json:"balances"`
}

// bulkError maps an item failure to its JSON error. ok is false for an
// unexpected (internal) error, which aborts the whole request with 500.
func bulkError(err error) (bulkItemError, bool) {
	var ve *validate.ValidationError
	var re *validate.RequestError
	if errors.As(err, &ve) {
		return bulkItemError{Message: "validation failed", Code: "validation_failed", Fields: ve.Fields}, true
	} else if errors.As(err, &re) {
		return bulkItemError{Message: re.Msg, Code: "invalid_item"}, true
	} else if errors.Is(err, fx.ErrUnsupported) {
		return bulkItemError{Message: fxMessage(err), Code: "exchange_rate_unsupported",
			Fields: map[string]string{"to_amount": "no exchange rate is available for this currency pair; provide to_amount manually"}}, true
	} else if errors.Is(err, fx.ErrUnavailable) || errors.Is(err, fx.ErrBadUpstream) {
		return bulkItemError{Message: fxMessage(err), Code: "exchange_rate_unavailable"}, true
	} else if errors.Is(err, store.ErrReadOnly) {
		return bulkItemError{Message: err.Error(), Code: "balance_adjustment_readonly",
			Fields: map[string]string{"type": transaction.ErrAdjustmentType}}, true
	}
	return bulkItemError{}, false
}

// decodeItem strictly decodes one array element into a CreateInput.
func decodeItem(raw json.RawMessage) (transaction.CreateInput, error) {
	var in transaction.CreateInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, &validate.RequestError{Msg: describeJSONError(err)}
	}
	if in.Version != nil || in.IgnoredUID != "" {
		return in, &validate.RequestError{Msg: "uid and version are not accepted in bulk items"}
	}
	return in, nil
}

// bulkCreateTransactions handles POST /api/transactions/bulk: many creates
// with the exact single-create validation (Validate + attachPaymentBalance,
// which also resolves FX rates) done up front, then every insert and balance
// adjustment in one SQL transaction (store.CreateBulk). See docs/bulk-import.md.
func (s *Server) bulkCreateTransactions(c *gin.Context) {
	var req bulkRequest
	if !decodeBodyLimit(c, &req, true, maxBulkBodyBytes) {
		return
	}
	if len(req.Transactions) == 0 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "transactions must be a non-empty array", Code: "empty_batch"})
		return
	}
	if len(req.Transactions) > MaxBulkItems {
		c.JSON(http.StatusRequestEntityTooLarge, errorBody{Error: fmt.Sprintf("too many transactions: %d (max %d per request); split the import into batches", len(req.Transactions), MaxBulkItems),
			Code: "too_many_items"})
		return
	}
	atomic := req.Atomic == nil || *req.Atomic

	// Phase 1: validate and resolve balances, categories and FX rates. Any
	// network access happens here, before the write transaction opens.
	c.Set(fxMemoKey, map[string]fxMemoEntry{})
	items := make([]store.BulkItem, len(req.Transactions))
	for i, raw := range req.Transactions {
		if strings.TrimSpace(string(raw)) == "null" {
			items[i].Err = &validate.RequestError{Msg: "item must be a JSON object"}
			continue
		}
		in, err := decodeItem(raw)
		if err != nil {
			items[i].Err = err
			continue
		}
		e, err := in.Validate()
		if err == nil {
			err = s.attachPaymentBalance(c, &e, in, nil)
		}
		if err != nil {
			if _, ok := bulkError(err); !ok {
				internalError(c, err)
				return
			}
			items[i].Err = err
			continue
		}
		items[i].Tx = e
	}

	// Phase 2: one SQL transaction (rolled back for dry runs and failed atomic batches).
	out, err := s.Store.CreateBulk(c.Request.Context(), items, store.BulkOptions{Atomic: atomic, DryRun: req.DryRun})
	if err != nil {
		internalError(c, err)
		return
	}
	resp := bulkResponse{DryRun: req.DryRun, Atomic: atomic, Written: out.Committed, Results: make([]bulkItemResult, len(out.Results)), Balances: out.Balances}
	if resp.Balances == nil {
		resp.Balances = []balance.Balance{}
	}
	fxDown, fxBadUpstream := false, false
	for i, r := range out.Results {
		res := bulkItemResult{Index: i, Status: r.Status}
		switch r.Status {
		case store.BulkCreated, store.BulkValid:
			t := r.Tx
			if !out.Committed {
				// Not written (dry run, or an atomic batch that failed): show what would be stored.
				res.Status = store.BulkValid
				t.ID, t.UID, t.CreatedAt, t.Version = 0, "", "", 0
			}
			res.Transaction = &t
		case store.BulkSkipped:
			res.ExistingUID = r.ExistingUID
		case store.BulkFailed:
			be, ok := bulkError(r.Err)
			if !ok {
				internalError(c, r.Err)
				return
			}
			fxDown = fxDown || errors.Is(r.Err, fx.ErrUnavailable)
			fxBadUpstream = fxBadUpstream || errors.Is(r.Err, fx.ErrBadUpstream)
			res.Error = &be
		}
		switch res.Status {
		case store.BulkCreated:
			resp.Created++
		case store.BulkValid:
			resp.Valid++
		case store.BulkSkipped:
			resp.Skipped++
		case store.BulkFailed:
			resp.Failed++
		}
		resp.Results[i] = res
	}

	status := http.StatusOK
	if atomic && resp.Failed > 0 && fxDown {
		status = http.StatusServiceUnavailable
	} else if atomic && resp.Failed > 0 && fxBadUpstream {
		status = http.StatusBadGateway
	} else if atomic && resp.Failed > 0 {
		status = http.StatusUnprocessableEntity
	} else if req.DryRun {
		status = http.StatusOK
	} else if atomic && resp.Created > 0 {
		status = http.StatusCreated
	}
	c.JSON(status, resp)
}
