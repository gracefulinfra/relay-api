// Package health serves the ops port: /healthz (liveness), /readyz (readiness), and /metrics
// (Prometheus). The ops port is never routed through the Gateway; the kubelet and Prometheus reach it
// inside the cluster.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check is one readiness condition. It returns nil when ready.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Handler returns the ops mux. Liveness only proves the process serves HTTP; it never touches the
// database, so a database outage makes pods unready rather than restarting them.
func Handler(reg *prometheus.Registry, checks ...Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		ok := true
		for _, c := range checks {
			wg.Go(func() {
				res := "ok"
				if err := c.Fn(ctx); err != nil {
					res = err.Error()
				}
				mu.Lock()
				defer mu.Unlock()
				results[c.Name] = res
				if res != "ok" {
					ok = false
				}
			})
		}
		wg.Wait()
		status, code := "ok", http.StatusOK
		if !ok {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{"status": status, "checks": results})
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
