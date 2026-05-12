package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// When metrics are enabled, mountHTTPHandlers wires /metrics to the gathered
// registry; /healthz and /jobs are always present regardless.
func TestMountHTTPHandlers_MetricsEnabled(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics.Register(reg, nil, nil) // mirrors Run's `if IsEnabled() { Register(...) }`

	mux := http.NewServeMux()
	mountHTTPHandlers(mux, nil, nil, nil, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// request_duration_seconds is a plain Histogram, so it emits zero-value
	// samples right after Register (unlike the CounterVecs, which stay absent
	// until a series is touched) — a reliable "endpoint is wired to our
	// registry" signal.
	if !strings.Contains(string(body), "agentdock_request_duration_seconds_count") {
		t.Errorf("/metrics body missing agentdock_request_duration_seconds:\n%s", body)
	}

	healthz, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer healthz.Body.Close()
	if healthz.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", healthz.StatusCode)
	}
}

// When metrics are disabled (Run passes a nil handler and skips Register),
// /metrics is not mounted — the mux 404s it — while the other endpoints stay.
func TestMountHTTPHandlers_MetricsDisabled(t *testing.T) {
	mux := http.NewServeMux()
	mountHTTPHandlers(mux, nil, nil, nil, nil) // nil metricsHandler → /metrics not mounted
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics status = %d, want 404 (handler must not be mounted when disabled)", resp.StatusCode)
	}

	healthz, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer healthz.Body.Close()
	if healthz.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200 (unrelated endpoints unaffected)", healthz.StatusCode)
	}
}
