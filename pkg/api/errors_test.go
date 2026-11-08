package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

func respond(t *testing.T, write func(*gin.Context, error), err error) (int, errorBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	write(c, err)
	var b errorBody
	if jerr := json.Unmarshal(rec.Body.Bytes(), &b); jerr != nil {
		t.Fatalf("body %q: %v", rec.Body, jerr)
	}
	return rec.Code, b
}

// TestErrorMapping (issue #65) pins the response for every mapped error, so
// the shared table answers exactly what the per-resource if/else chains did.
func TestErrorMapping(t *testing.T) {
	if store.ErrNotFound.Error() != "not found" {
		t.Errorf("store.ErrNotFound = %q, want a message that names no entity", store.ErrNotFound)
	}
	wrapped := func(err error) error { return fmt.Errorf("context: %w", err) }
	cases := []struct {
		name   string
		write  func(*gin.Context, error)
		err    error
		status int
		msg    string // "" = don't check
		code   string
		field  string
	}{
		// not found names the resource of the route
		{"tx not found", writeTransactionError, store.ErrNotFound, 404, "transaction not found", "", ""},
		{"balance not found", writeBalanceError, wrapped(store.ErrNotFound), 404, "balance not found", "", ""},
		{"category not found", writeCategoryError, store.ErrNotFound, 404, "category not found", "", ""},
		{"user not found", writeUserError, store.ErrNotFound, 404, "user not found", "", ""},
		{"no resource", func(c *gin.Context, err error) { writeError(c, err, "") }, store.ErrNotFound, 404, "not found", "", ""},

		// shared rules, from every mapper
		{"validation", writeBalanceError, &validate.ValidationError{Fields: map[string]string{"name": "is required"}}, 422, "validation failed", "", "name"},
		{"request", writeUserError, &validate.RequestError{Msg: "unknown field \"x\""}, 400, "unknown field \"x\"", "", ""},
		{"fx unsupported", writeTransactionError, wrapped(fx.ErrUnsupported), 422, "", "exchange_rate_unsupported", "to_amount"},
		{"fx unavailable", writeTransactionError, fx.ErrUnavailable, 503, "", "exchange_rate_unavailable", ""},
		{"fx bad upstream", writeTransactionError, fx.ErrBadUpstream, 502, "", "exchange_rate_unavailable", ""},
		{"read-only", writeTransactionError, store.ErrReadOnly, 409, store.ErrReadOnly.Error(), "balance_adjustment_readonly", ""},
		{"duplicate import_ref", writeTransactionError, &store.DuplicateImportRefError{ImportRef: "r1", ExistingUID: "u1"}, 409, "", "duplicate_import_ref", "import_ref"},
		{"internal", writeCategoryError, errors.New("disk on fire"), 500, "internal server error", "", ""},

		// balances
		{"balance in use", writeBalanceError, fmt.Errorf("%w (2 transaction(s)); its currency cannot change", store.ErrBalanceInUse), 409,
			"balance is used by transactions (2 transaction(s)); its currency cannot change", "balance_in_use", "currency"},
		// categories
		{"category duplicate", writeCategoryError, store.ErrDuplicateCategory, 409, "a category with this name and type already exists", "", "name"},
		{"category limit", writeCategoryError, store.ErrCategoryLimit, 409, store.ErrCategoryLimit.Error(), "", ""},
		{"category in use", writeCategoryError, fmt.Errorf("%w (1 transaction(s)); it cannot be deleted", store.ErrCategoryInUse), 409,
			"category is used by transactions (1 transaction(s)); it cannot be deleted; reassign or clear category_uid on those transactions first", "", ""},
		// users
		{"username taken", writeUserError, store.ErrDuplicateUsername, 409, store.ErrDuplicateUsername.Error(), "", "username"},
		{"email taken", writeUserError, store.ErrDuplicateEmail, 409, store.ErrDuplicateEmail.Error(), "", "email"},
		{"username required", writeUserError, store.ErrUsernameRequired, 422, "validation failed", "username_required", "username"},
		{"last login user", writeUserError, store.ErrLastPasswordUser, 409, store.ErrLastPasswordUser.Error(), "last_login_user", ""},

		// a resource's own sentinel means nothing to another resource
		{"balance error on users route", writeUserError, store.ErrBalanceInUse, 500, "internal server error", "", ""},
	}
	for _, c := range cases {
		status, b := respond(t, c.write, c.err)
		if status != c.status || (c.msg != "" && b.Error != c.msg) || b.Code != c.code || (c.field != "" && b.Fields[c.field] == "") {
			t.Errorf("%s: %d %+v; want %d %q code %q field %q", c.name, status, b, c.status, c.msg, c.code, c.field)
		}
	}
}

// TestRegistrationClosedBody keeps both registration_closed paths identical.
func TestRegistrationClosedBody(t *testing.T) {
	status, b := respond(t, func(c *gin.Context, _ error) { writeRegistrationClosed(c) }, nil)
	if status != http.StatusForbidden || b.Code != "registration_closed" || b.Error != "registration is closed; log in to create more users" {
		t.Errorf("%d %+v", status, b)
	}
}
