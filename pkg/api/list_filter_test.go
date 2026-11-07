package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/transaction"

	"github.com/gin-gonic/gin"
)

// listFilterFor runs parseListFilter on a fresh context (Gin caches the
// parsed query per context).
func listFilterFor(query string) (store.ListFilter, map[string]string) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/transactions?"+query, nil)
	return parseListFilter(c)
}

func TestParseListFilter(t *testing.T) {
	// An unencoded "+07:00" arrives as " 07:00" and is repaired.
	f, fields := listFilterFor("from=2026-10-01T00:00:00+07:00&to=2026-10-31T23:59:59Z&type=Income&category_uid=AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA&limit=20")
	if len(fields) != 0 {
		t.Fatalf("fields: %v", fields)
	}
	if f.From == nil || !f.From.Equal(time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)) || f.To == nil ||
		f.Type != transaction.Income || f.CategoryUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || f.Limit != 20 {
		t.Errorf("filter: %+v", f)
	}

	if f, fields := listFilterFor(""); len(fields) != 0 || f.Limit != defaultLimit || f.From != nil || f.To != nil || f.Type != "" || f.CategoryUID != "" {
		t.Errorf("defaults: %+v %v", f, fields)
	}

	for query, field := range map[string]string{
		"from=yesterday": "from",
		"from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z": "to",
		"type=refund":      "type",
		"category_uid=abc": "category_uid",
		"limit=0":          "limit",
		"limit=5001":       "limit",
		"limit=ten":        "limit",
	} {
		if _, fields := listFilterFor(query); fields[field] == "" {
			t.Errorf("%s: fields %v, want %q", query, fields, field)
		}
	}
}
