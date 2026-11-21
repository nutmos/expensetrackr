package api

import (
	"net/http"
	"time"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/report"

	"github.com/gin-gonic/gin"
)

// registerReportRoutes mounts the read-only reports (issue #136):
//
//	GET /api/reports                     the available report types
//	GET /api/reports/income-expense      income and expenses in a time range
//	GET /api/reports/assets-liabilities  current assets and liabilities
func (s *Server) registerReportRoutes(api *gin.RouterGroup) {
	api.GET("/reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"reports": report.Catalog, "count": len(report.Catalog)})
	})
	api.GET("/reports/"+report.IncomeExpenseID, s.incomeExpenseReport)
	api.GET("/reports/"+report.AssetsLiabilitiesID, s.assetsLiabilitiesReport)
}

// incomeExpenseReport answers GET /api/reports/income-expense?from=&to=
// (both optional RFC 3339 date-times with offset, inclusive, like the
// transaction list filter).
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
	echo := func(t *time.Time, name string) *string {
		if t == nil {
			return nil
		}
		v := queryTime(c, name)
		return &v
	}
	c.JSON(http.StatusOK, report.IncomeExpense{
		Report:     report.IncomeExpenseID,
		From:       echo(from, "from"),
		To:         echo(to, "to"),
		Currencies: report.BuildIncomeExpense(rows, names),
	})
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
