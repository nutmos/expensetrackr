package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/report"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/transaction"

	"github.com/gin-gonic/gin"
)

// registerReportRoutes mounts the read-only reports (issue #136):
//
//	GET /api/reports                     the available report types
//	GET /api/reports/income-expense      income and expenses in a time range
//	GET /api/reports/assets-liabilities  current assets and liabilities
//	GET /api/reports/balance-transactions  one balance's transactions (issue #160)
func (s *Server) registerReportRoutes(api *gin.RouterGroup) {
	api.GET("/reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"reports": report.Catalog, "count": len(report.Catalog)})
	})
	api.GET("/reports/"+report.IncomeExpenseID, s.incomeExpenseReport)
	api.GET("/reports/"+report.AssetsLiabilitiesID, s.assetsLiabilitiesReport)
	api.GET("/reports/"+report.BalanceTransactionsID, s.balanceTransactionsReport)
}

// incomeExpenseReport answers GET /api/reports/income-expense?from=&to=&tz=
// (all optional; dates or RFC 3339 date-times, inclusive, like the
// transaction list filter: see parseTimeRange).
func (s *Server) incomeExpenseReport(c *gin.Context) {
	fields := map[string]string{}
	from, to := parseTimeRange(c, fields)
	if len(fields) > 0 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters", Fields: fields})
		return
	}
	ctx, user := c.Request.Context(), sessionUserUID(c)
	rows, err := s.Store.SumFlows(ctx, user, from, to)
	if err != nil {
		internalError(c, err)
		return
	}
	cats, err := s.Store.ListCategories(ctx, user, "")
	if err != nil {
		internalError(c, err)
		return
	}
	names := make(map[common_type.UID]string, len(cats))
	for _, cat := range cats {
		names[cat.UID] = cat.Name
	}
	c.JSON(http.StatusOK, report.IncomeExpense{
		Report:     report.IncomeExpenseID,
		From:       echoTime(c, from, "from"),
		To:         echoTime(c, to, "to"),
		Currencies: report.BuildIncomeExpense(rows, names),
	})
}

// echoTime is the ?name= filter as sent, or nil when t (its parsed value)
// is nil.
func echoTime(c *gin.Context, t *time.Time, name string) *string {
	if t == nil {
		return nil
	}
	v := queryTime(c, name)
	return &v
}

// balanceTransactionsReport answers
// GET /api/reports/balance-transactions?balance_uid=&from=&to=: a statement
// of one balance (hidden ones included). balance_uid is required; from and
// to (and tz) are optional, inclusive bounds like the other reports.
func (s *Server) balanceTransactionsReport(c *gin.Context) {
	fields := map[string]string{}
	uid := common_type.NormalizeUID(c.Query("balance_uid"))
	switch {
	case strings.TrimSpace(c.Query("balance_uid")) == "":
		fields["balance_uid"] = "is required"
	case !uid.Valid():
		fields["balance_uid"] = "must be a UUID (the uid of a balance)"
	}
	from, to := parseTimeRange(c, fields)
	if len(fields) > 0 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters", Fields: fields})
		return
	}
	ctx, user := c.Request.Context(), sessionUserUID(c)
	b, err := s.Store.GetBalanceByUID(ctx, user, uid)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(c, "balance")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	inRange, err := s.Store.List(ctx, user, store.ListFilter{BalanceUID: uid, From: from, To: to})
	if err != nil {
		internalError(c, err)
		return
	}
	slices.Reverse(inRange) // the store lists newest first; ties keep id order
	var after []transaction.Transaction
	if to != nil {
		// Strictly after the range. Postgres keeps microseconds, so step one
		// microsecond past the (truncated) inclusive bound.
		next := to.Truncate(time.Microsecond).Add(time.Microsecond)
		if after, err = s.Store.List(ctx, user, store.ListFilter{BalanceUID: uid, From: &next}); err != nil {
			internalError(c, err)
			return
		}
	}
	cats, err := s.Store.ListCategories(ctx, user, "")
	if err != nil {
		internalError(c, err)
		return
	}
	names := make(map[common_type.UID]string, len(cats))
	for _, cat := range cats {
		names[cat.UID] = cat.Name
	}
	out := report.BuildBalanceTransactions(b, inRange, after, names)
	out.From, out.To = echoTime(c, from, "from"), echoTime(c, to, "to")
	c.JSON(http.StatusOK, out)
}

// assetsLiabilitiesReport answers GET /api/reports/assets-liabilities: the
// current balances, per currency, by type and by account.
func (s *Server) assetsLiabilitiesReport(c *gin.Context) {
	items, err := s.Store.ListBalances(c.Request.Context(), sessionUserUID(c), "")
	if err != nil {
		internalError(c, err)
		return
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	c.JSON(http.StatusOK, report.AssetsLiabilities{
		Report:     report.AssetsLiabilitiesID,
		AsOf:       now().UTC().Format(time.RFC3339),
		Currencies: report.BuildAssetsLiabilities(items),
	})
}
