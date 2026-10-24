package httpapi_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"lythbench/httpapi/goimpl"
	"lythbench/httpapi/lythimpl"
	"lythbench/internal/fixtures"
)

// BenchmarkHandler measures the handler itself through httptest.
func BenchmarkHandler(b *testing.B) {
	body := fixtures.OrderJSON(20)
	for b.Loop() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			b.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
}

// BenchmarkServerLatency sends requests through a loopback HTTP server and
// reports the latency distribution.
func BenchmarkServerLatency(b *testing.B) {
	body := fixtures.OrderJSON(20)
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()
	var lat []time.Duration
	for b.Loop() {
		start := time.Now()
		resp, err := client.Post(srv.URL+"/orders", "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		lat = append(lat, time.Since(start))
	}
	slices.Sort(lat)
	pct := func(p float64) float64 { return float64(lat[int(p*float64(len(lat)-1))].Microseconds()) }
	b.ReportMetric(pct(0.50), "p50-µs")
	b.ReportMetric(pct(0.90), "p90-µs")
	b.ReportMetric(pct(0.99), "p99-µs")
}

func TestEquivalence(t *testing.T) {
	for _, body := range [][]byte{fixtures.OrderJSON(3), []byte(`{"id":1}`), []byte(`not json`)} {
		g, n := httptest.NewRecorder(), httptest.NewRecorder()
		goimpl.HandleOrder(g, httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body)))
		lythimpl.HandleOrder(n, httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(body)))
		if g.Code != n.Code || g.Body.String() != n.Body.String() || g.Header().Get("Content-Type") != n.Header().Get("Content-Type") {
			t.Errorf("body %q: go=%d %q lyth=%d %q", body, g.Code, g.Body, n.Code, n.Body)
		}
	}
}
