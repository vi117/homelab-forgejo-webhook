package webhook

import (
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"time"
)

type metrics struct {
	accepted         atomic.Uint64
	rejected         atomic.Uint64
	duplicates       atomic.Uint64
	deploySuccess    atomic.Uint64
	deployFailure    atomic.Uint64
	deployInFlight   atomic.Int64
	lastDurationBits atomic.Uint64
	lastSuccessUnix  atomic.Int64
}

func (metrics *metrics) setLastDuration(duration time.Duration) {
	metrics.lastDurationBits.Store(math.Float64bits(duration.Seconds()))
}

func (metrics *metrics) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# TYPE pages_webhook_requests_total counter\n")
	fmt.Fprintf(w, "pages_webhook_requests_total{result=\"accepted\"} %d\n", metrics.accepted.Load())
	fmt.Fprintf(w, "pages_webhook_requests_total{result=\"rejected\"} %d\n", metrics.rejected.Load())
	fmt.Fprintf(w, "pages_webhook_requests_total{result=\"duplicate\"} %d\n", metrics.duplicates.Load())
	fmt.Fprintf(w, "# TYPE pages_deployments_total counter\n")
	fmt.Fprintf(w, "pages_deployments_total{result=\"success\"} %d\n", metrics.deploySuccess.Load())
	fmt.Fprintf(w, "pages_deployments_total{result=\"failure\"} %d\n", metrics.deployFailure.Load())
	fmt.Fprintf(w, "# TYPE pages_deployments_in_flight gauge\n")
	fmt.Fprintf(w, "pages_deployments_in_flight %d\n", metrics.deployInFlight.Load())
	fmt.Fprintf(w, "# TYPE pages_deployment_last_duration_seconds gauge\n")
	fmt.Fprintf(w, "pages_deployment_last_duration_seconds %g\n",
		math.Float64frombits(metrics.lastDurationBits.Load()))
	fmt.Fprintf(w, "# TYPE pages_deployment_last_success_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "pages_deployment_last_success_timestamp_seconds %d\n",
		metrics.lastSuccessUnix.Load())
}
