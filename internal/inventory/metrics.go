package inventory

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var httpDurationBuckets = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
}

type metrics struct {
	receipts      atomic.Int64
	reservations  atomic.Int64
	rejections    atomic.Int64
	confirmations atomic.Int64
	releases      atomic.Int64
	transfers     atomic.Int64
	receives      atomic.Int64
	expirations   atomic.Int64
	adjustments   atomic.Int64
	auditReads    atomic.Int64

	httpMu      sync.Mutex
	httpCount   int64
	httpErrors  int64
	httpSeconds float64
	httpBuckets []int64
}

func (m *metrics) observeRequest(duration time.Duration, status int) {
	seconds := duration.Seconds()
	m.httpMu.Lock()
	defer m.httpMu.Unlock()
	if m.httpBuckets == nil {
		m.httpBuckets = make([]int64, len(httpDurationBuckets))
	}
	m.httpCount++
	if status >= 500 {
		m.httpErrors++
	}
	m.httpSeconds += seconds
	for i, upper := range httpDurationBuckets {
		if seconds <= upper {
			m.httpBuckets[i]++
		}
	}
}

func (m *metrics) writePrometheus(w io.Writer) {
	fmt.Fprintf(w, "inventory_receipts_total %d\n", m.receipts.Load())
	fmt.Fprintf(w, "inventory_reservations_total %d\n", m.reservations.Load())
	fmt.Fprintf(w, "inventory_reservation_rejections_total %d\n", m.rejections.Load())
	fmt.Fprintf(w, "inventory_reservation_confirmations_total %d\n", m.confirmations.Load())
	fmt.Fprintf(w, "inventory_reservation_releases_total %d\n", m.releases.Load())
	fmt.Fprintf(w, "inventory_transfers_total %d\n", m.transfers.Load())
	fmt.Fprintf(w, "inventory_transfer_receives_total %d\n", m.receives.Load())
	fmt.Fprintf(w, "inventory_reservation_expirations_total %d\n", m.expirations.Load())
	fmt.Fprintf(w, "inventory_adjustments_total %d\n", m.adjustments.Load())
	fmt.Fprintf(w, "inventory_audit_reads_total %d\n", m.auditReads.Load())

	m.httpMu.Lock()
	defer m.httpMu.Unlock()
	if m.httpBuckets == nil {
		m.httpBuckets = make([]int64, len(httpDurationBuckets))
	}
	fmt.Fprintf(w, "# TYPE inventory_http_requests_total counter\n")
	fmt.Fprintf(w, "inventory_http_requests_total %d\n", m.httpCount)
	fmt.Fprintf(w, "inventory_http_request_errors_total %d\n", m.httpErrors)
	fmt.Fprintf(w, "# TYPE inventory_http_request_duration_seconds histogram\n")
	for i, upper := range httpDurationBuckets {
		fmt.Fprintf(w, "inventory_http_request_duration_seconds_bucket{le=%q} %d\n", formatBucket(upper), m.httpBuckets[i])
	}
	fmt.Fprintf(w, "inventory_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.httpCount)
	fmt.Fprintf(w, "inventory_http_request_duration_seconds_sum %g\n", m.httpSeconds)
	fmt.Fprintf(w, "inventory_http_request_duration_seconds_count %d\n", m.httpCount)
}

func formatBucket(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%g", value)
}
