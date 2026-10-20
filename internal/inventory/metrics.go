package inventory

import (
	"fmt"
	"io"
	"sync/atomic"
)

type metrics struct {
	receipts      atomic.Int64
	reservations  atomic.Int64
	rejections    atomic.Int64
	confirmations atomic.Int64
	releases      atomic.Int64
	transfers     atomic.Int64
	receives      atomic.Int64
}

func (m *metrics) writePrometheus(w io.Writer) {
	fmt.Fprintf(w, "inventory_receipts_total %d\n", m.receipts.Load())
	fmt.Fprintf(w, "inventory_reservations_total %d\n", m.reservations.Load())
	fmt.Fprintf(w, "inventory_reservation_rejections_total %d\n", m.rejections.Load())
	fmt.Fprintf(w, "inventory_reservation_confirmations_total %d\n", m.confirmations.Load())
	fmt.Fprintf(w, "inventory_reservation_releases_total %d\n", m.releases.Load())
	fmt.Fprintf(w, "inventory_transfers_total %d\n", m.transfers.Load())
	fmt.Fprintf(w, "inventory_transfer_receives_total %d\n", m.receives.Load())
}
