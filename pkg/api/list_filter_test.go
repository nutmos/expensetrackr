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
	cur := store.ListCursor{SpentAtUnix: 1790000000, ID: 42}
	f, fields := listFilterFor("from=2026-10-01T00:00:00+07:00&to=2026-10-31T23:59:59Z&type=Income&category_uid=AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA&balance_uid=BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB&cursor=" + cur.String() + "&limit=20")
	if len(fields) != 0 {
		t.Fatalf("fields: %v", fields)
	}
	if f.From == nil || !f.From.Equal(time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)) || f.To == nil ||
		f.Type != transaction.Income || f.CategoryUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || f.Limit != 20 ||
		f.BalanceUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || f.After == nil || *f.After != cur {
		t.Errorf("filter: %+v", f)
	}

	if f, fields := listFilterFor(""); len(fields) != 0 || f.Limit != defaultLimit || f.From != nil || f.To != nil || f.Type != "" || f.CategoryUID != "" || f.BalanceUID != "" || f.After != nil {
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
		"balance_uid=x":    "balance_uid",
		"cursor=abc":       "cursor",
	} {
		if _, fields := listFilterFor(query); fields[field] == "" {
			t.Errorf("%s: fields %v, want %q", query, fields, field)
		}
	}
}

func TestParseListFilterDates(t *testing.T) {
	bkk, _ := time.LoadLocation("Asia/Bangkok")
	ny, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		query    string
		from, to time.Time
	}{
		// A date alone is the whole day in UTC.
		{"from=2026-10-01&to=2026-10-31",
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 23, 59, 59, 999999999, time.UTC)},
		{"from=2026-10-01&to=2026-10-01&tz=Asia/Bangkok",
			time.Date(2026, 10, 1, 0, 0, 0, 0, bkk), time.Date(2026, 10, 1, 23, 59, 59, 999999999, bkk)},
		// Unencoded "+" arrives as a space and is repaired.
		{"from=2026-10-01&to=2026-10-01&tz=+07:00",
			time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 16, 59, 59, 999999999, time.UTC)},
		{"from=2026-10-01&tz=-0530",
			time.Date(2026, 10, 1, 5, 30, 0, 0, time.UTC), time.Time{}},
		// A 25-hour day (DST ends in New York on 2026-11-01).
		{"from=2026-11-01&to=2026-11-01&tz=America/New_York",
			time.Date(2026, 11, 1, 0, 0, 0, 0, ny), time.Date(2026, 11, 1, 23, 59, 59, 999999999, ny)},
		// tz does not change full date-times; the two forms can be mixed.
		{"from=2026-10-01T00:00:00Z&to=2026-10-01&tz=Asia/Bangkok",
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 23, 59, 59, 999999999, bkk)},
	}
	for _, tc := range cases {
		f, fields := listFilterFor(tc.query)
		if len(fields) != 0 {
			t.Errorf("%s: fields %v", tc.query, fields)
			continue
		}
		if f.From == nil || !f.From.Equal(tc.from) {
			t.Errorf("%s: from %v, want %v", tc.query, f.From, tc.from)
		}
		if tc.to.IsZero() != (f.To == nil) || (f.To != nil && !f.To.Equal(tc.to)) {
			t.Errorf("%s: to %v, want %v", tc.query, f.To, tc.to)
		}
	}

	for query, field := range map[string]string{
		"from=2026-10-01&tz=Mars/Olympus":         "tz",
		"from=2026-10-01&tz=Local":                "tz",
		"from=2026-10-01&tz=%2B25:00":             "tz",
		"from=2026-13-01":                         "from",
		"from=2026-10-01T00:00:00":                "from",
		"from=2026-10-02&to=2026-10-01":           "to",
		"from=2026-10-02&to=2026-10-01T23:59:59Z": "to",
	} {
		if _, fields := listFilterFor(query); fields[field] == "" {
			t.Errorf("%s: fields %v, want %q", query, fields, field)
		}
	}
}
