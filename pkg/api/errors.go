package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

// Error responses (issue #65). Every handler maps errors through writeError
// with the name of its resource and the resource's own cases; the rules
// every resource shares (validation, not found, exchange rates, read-only
// adjustments, duplicate import refs) live here once.

// errorCase answers one sentinel error (matched with errors.Is) with a
// status and body. body receives the error, for messages that include it.
type errorCase struct {
	target error
	status int
	body   func(err error) errorBody
}

// fixed is an errorCase body that ignores the error.
func fixed(b errorBody) func(error) errorBody {
	return func(error) errorBody { return b }
}

// withText is an errorCase body using the error's text as the message.
func withText(code string, fields map[string]string) func(error) errorBody {
	return func(err error) errorBody { return errorBody{Error: err.Error(), Code: code, Fields: fields} }
}

// sharedErrorCases apply to every resource, after its own cases.
var sharedErrorCases = []errorCase{
	{fx.ErrUnsupported, http.StatusUnprocessableEntity, func(err error) errorBody {
		return errorBody{Error: fxMessage(err), Code: "exchange_rate_unsupported",
			Fields: map[string]string{"to_amount": "no exchange rate is available for this currency pair; provide to_amount manually"}}
	}},
	{fx.ErrUnavailable, http.StatusServiceUnavailable, func(err error) errorBody {
		return errorBody{Error: fxMessage(err), Code: "exchange_rate_unavailable"}
	}},
	{fx.ErrBadUpstream, http.StatusBadGateway, func(err error) errorBody {
		return errorBody{Error: fxMessage(err), Code: "exchange_rate_unavailable"}
	}},
	{store.ErrReadOnly, http.StatusConflict, withText("balance_adjustment_readonly", nil)},
}

// writeError answers err for a request about resource ("transaction",
// "balance", ...; "" for none), in this order:
//
//   - *validate.ValidationError: 422 with its fields;
//   - *validate.RequestError: 400 with its message;
//   - store.ErrNotFound: 404 "<resource> not found";
//   - the resource's own cases, then sharedErrorCases;
//   - *store.DuplicateImportRefError: 409 duplicate_import_ref;
//   - anything else: 500 (logged).
func writeError(c *gin.Context, err error, resource string, cases ...errorCase) {
	var ve *validate.ValidationError
	var re *validate.RequestError
	var dup *store.DuplicateImportRefError
	if errors.As(err, &ve) {
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Fields: ve.Fields})
		return
	}
	if errors.As(err, &re) {
		c.JSON(http.StatusBadRequest, errorBody{Error: re.Msg})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(c, resource)
		return
	}
	for _, list := range [][]errorCase{cases, sharedErrorCases} {
		for _, ec := range list {
			if errors.Is(err, ec.target) {
				c.JSON(ec.status, ec.body(err))
				return
			}
		}
	}
	if errors.As(err, &dup) {
		c.JSON(http.StatusConflict, errorBody{Error: dup.Error(), Code: "duplicate_import_ref",
			Fields: map[string]string{"import_ref": "is already used on this balance by transaction " + dup.ExistingUID}})
		return
	}
	internalError(c, err)
}

// writeNotFound answers 404 "<resource> not found" ("not found" without a
// resource).
func writeNotFound(c *gin.Context, resource string) {
	msg := "not found"
	if resource != "" {
		msg = resource + " " + msg
	}
	c.JSON(http.StatusNotFound, errorBody{Error: msg})
}

func internalError(c *gin.Context, err error) {
	log.Printf("internal error: %v", err)
	c.JSON(http.StatusInternalServerError, errorBody{Error: "internal server error"})
}

// ---- per-resource mappers ----------------------------------------------------

// writeTransactionError maps errors of the transaction routes.
func writeTransactionError(c *gin.Context, err error) {
	writeError(c, err, "transaction")
}

// balanceErrorCases: 409 currency change while in use, 409 duplicate name.
// (Version conflicts are answered by balanceConflict, which needs the store.)
var balanceErrorCases = []errorCase{
	{store.ErrBalanceInUse, http.StatusConflict, withText("balance_in_use",
		map[string]string{"currency": "cannot change while transactions reference this balance (no exchange-rate conversion)"})},
	{store.ErrDuplicateName, http.StatusConflict, fixed(errorBody{
		Error:  "a balance with this name already exists",
		Fields: map[string]string{"name": "is already used by another of your balances (names are case-insensitive)"},
	})},
}

func writeBalanceError(c *gin.Context, err error) {
	writeError(c, err, "balance", balanceErrorCases...)
}

// categoryErrorCases: 409 duplicate name, limit reached, or in use.
var categoryErrorCases = []errorCase{
	{store.ErrDuplicateCategory, http.StatusConflict, fixed(errorBody{
		Error:  "a category with this name and type already exists",
		Fields: map[string]string{"name": "is already used by another category of this type (names are case-insensitive)"},
	})},
	{store.ErrCategoryLimit, http.StatusConflict, withText("", nil)},
	{store.ErrCategoryInUse, http.StatusConflict, func(err error) errorBody {
		return errorBody{Error: err.Error() + "; reassign or clear category_uid on those transactions first"}
	}},
}

func writeCategoryError(c *gin.Context, err error) {
	writeError(c, err, "category", categoryErrorCases...)
}

// userErrorCases: 409 duplicate username/email or last login account, 422
// username required.
var userErrorCases = []errorCase{
	{store.ErrDuplicateUsername, http.StatusConflict, withText("",
		map[string]string{"username": "is already taken (usernames are case-insensitive)"})},
	{store.ErrDuplicateEmail, http.StatusConflict, withText("",
		map[string]string{"email": "is already used by another user"})},
	{store.ErrUsernameRequired, http.StatusUnprocessableEntity, fixed(errorBody{Error: "validation failed", Code: "username_required",
		Fields: map[string]string{"username": "cannot be removed: this user logs in with it"}})},
	{store.ErrLastPasswordUser, http.StatusConflict, withText("last_login_user", nil)},
}

func writeUserError(c *gin.Context, err error) {
	writeError(c, err, "user", userErrorCases...)
}

// writeRegistrationClosed answers 403 registration_closed (first-run
// registration after a user with a password exists).
func writeRegistrationClosed(c *gin.Context) {
	c.JSON(http.StatusForbidden, errorBody{Error: "registration is closed; log in to create more users", Code: "registration_closed"})
}
