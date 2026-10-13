package fx

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFormatRate(t *testing.T) {
	for in, want := range map[string]string{
		"33.615": "33.615", "1/3": "0.3333333333", "2/3": "0.6666666667", "0.0000123456789012": "0.0000123456789",
		"12345678901.5": "12345678902", "100": "100", "0.02975": "0.02975",
	} {
		r, _ := new(big.Rat).SetString(in)
		if got := FormatRate(r); got != want {
			t.Errorf("FormatRate(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestConvertHalfUp(t *testing.T) {
	cases := []struct {
		minor   int64
		from    int
		rate    string
		to      int
		want    int64
		wantErr bool
	}{
		{10000, 2, "33.615", 2, 336150, false},
		{101, 2, "149.567", 0, 151, false},
		{50, 2, "1.005", 2, 50, false},         // 0.5025 -> 0.50
		{150, 2, "0.003", 2, 0, true},          // 0.0045 -> 0.00: zero
		{1, 2, "0.5", 2, 1, false},             // 0.005 -> 0.01 (half up)
		{12345, 0, "0.02975", 2, 36726, false}, // 367.26375 -> 367.26
	}
	for _, c := range cases {
		got, err := Convert(c.minor, c.from, c.rate, c.to)
		if (err != nil) != c.wantErr || (!c.wantErr && got != c.want) {
			t.Errorf("Convert(%d,%d,%s,%d) = %d, %v; want %d", c.minor, c.from, c.rate, c.to, got, err, c.want)
		}
	}
	if r := ManualRate(10000, 2, 3361, 0); r != "33.61" {
		t.Errorf("ManualRate = %s", r)
	}
}

type memCache struct {
	rate, date string
	at         time.Time
	ok         bool
	puts       int
}

func (m *memCache) FXLookup(context.Context, string, string, string) (string, string, time.Time, bool, error) {
	return m.rate, m.date, m.at, m.ok, nil
}
func (m *memCache) FXStore(_ context.Context, _, _, _, d, r string, at time.Time) error {
	m.rate, m.date, m.at, m.ok = r, d, at, true
	m.puts++
	return nil
}

func TestRateCacheTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"amount":1.0,"base":"USD","date":"2026-10-12","rates":{"THB":33.615}}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c := &Client{BaseURL: srv.URL, Cache: &memCache{}, Now: func() time.Time { return now }}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		q, err := c.Rate(ctx, "usd", "thb", "", nil)
		if err != nil || q.Rate != "33.615" || q.Date != "2026-10-12" || q.From != "USD" {
			t.Fatalf("%v %v", q, err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("hits %d", hits.Load())
	}
	now = now.Add(LatestTTL + time.Minute) // 'latest' expires
	if _, err := c.Rate(ctx, "USD", "THB", "", nil); err != nil || hits.Load() != 2 {
		t.Errorf("after TTL: hits %d err %v", hits.Load(), err)
	}
	// A settled historical date that resolved to an earlier working day stays cached.
	c.Cache = &memCache{rate: "31.39", date: "2026-01-02", at: now.Add(-30 * 24 * time.Hour), ok: true}
	if q, _ := c.Rate(ctx, "USD", "THB", "2026-01-04", nil); q.Rate != "31.39" || hits.Load() != 2 {
		t.Errorf("settled: %v hits %d", q, hits.Load())
	}
	// A recent date that mapped to an earlier day expires like 'latest'.
	c.Cache = &memCache{rate: "1", date: "2026-10-09", at: now.Add(-4 * time.Hour), ok: true}
	if q, _ := c.Rate(ctx, "USD", "THB", "2026-10-12", nil); q.Rate != "33.615" || hits.Load() != 3 {
		t.Errorf("recent: %v hits %d", q, hits.Load())
	}
}
