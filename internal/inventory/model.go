package inventory

import (
	"encoding/json"
	"time"
)

type Position struct {
	LocationID          string    `json:"location_id"`
	SKU                 string    `json:"sku"`
	OnHand              int64     `json:"on_hand"`
	Reserved            int64     `json:"reserved"`
	Available           int64     `json:"available"`
	InventoryValueCents int64     `json:"inventory_value_cents"`
	Version             int64     `json:"version"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type Reservation struct {
	ID         string    `json:"id"`
	LocationID string    `json:"location_id"`
	SKU        string    `json:"sku"`
	Quantity   int64     `json:"quantity"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Transfer struct {
	ID             string     `json:"id"`
	FromLocationID string     `json:"from_location_id"`
	ToLocationID   string     `json:"to_location_id"`
	SKU            string     `json:"sku"`
	Quantity       int64      `json:"quantity"`
	ValueCents     int64      `json:"value_cents"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	ReceivedAt     *time.Time `json:"received_at,omitempty"`
}

type ATP struct {
	LocationID      string    `json:"location_id"`
	SKU             string    `json:"sku"`
	Found           bool      `json:"found"`
	OnHand          int64     `json:"on_hand"`
	Reserved        int64     `json:"reserved"`
	ExpiredReserved int64     `json:"expired_reserved"`
	Available       int64     `json:"available"`
	InTransit       int64     `json:"in_transit"`
	AsOf            time.Time `json:"as_of"`
}

type Reconciliation struct {
	OK        bool      `json:"ok"`
	Issues    []string  `json:"issues"`
	CheckedAt time.Time `json:"checked_at"`
}

type ReceiveInput struct {
	LocationID    string `json:"location_id"`
	SKU           string `json:"sku"`
	Quantity      int64  `json:"quantity"`
	UnitCostCents int64  `json:"unit_cost_cents"`
}

type ReserveInput struct {
	LocationID string `json:"location_id"`
	SKU        string `json:"sku"`
	Quantity   int64  `json:"quantity"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
}

type TransferInput struct {
	FromLocationID string `json:"from_location_id"`
	ToLocationID   string `json:"to_location_id"`
	SKU            string `json:"sku"`
	Quantity       int64  `json:"quantity"`
}

type AdjustmentInput struct {
	LocationID    string `json:"location_id"`
	SKU           string `json:"sku"`
	QuantityDelta int64  `json:"quantity_delta"`
	UnitCostCents int64  `json:"unit_cost_cents,omitempty"`
	Reason        string `json:"reason"`
}

type Adjustment struct {
	ID              string    `json:"id"`
	LocationID      string    `json:"location_id"`
	SKU             string    `json:"sku"`
	QuantityDelta   int64     `json:"quantity_delta"`
	ValueDeltaCents int64     `json:"value_delta_cents"`
	Reason          string    `json:"reason"`
	Actor           string    `json:"actor"`
	CreatedAt       time.Time `json:"created_at"`
	Position        Position  `json:"position"`
}

type Event struct {
	ID            int64           `json:"id"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	Actor         string          `json:"actor"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

type Movement struct {
	ID            int64     `json:"id"`
	LocationID    string    `json:"location_id"`
	SKU           string    `json:"sku"`
	MovementType  string    `json:"movement_type"`
	QuantityDelta int64     `json:"quantity_delta"`
	ValueDelta    int64     `json:"value_delta_cents"`
	RefType       string    `json:"ref_type"`
	RefID         string    `json:"ref_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type LedgerEntry struct {
	ID          int64     `json:"id"`
	EntryGroup  string    `json:"entry_group"`
	Account     string    `json:"account"`
	LocationID  string    `json:"location_id"`
	SKU         string    `json:"sku"`
	DebitCents  int64     `json:"debit_cents"`
	CreditCents int64     `json:"credit_cents"`
	RefType     string    `json:"ref_type"`
	RefID       string    `json:"ref_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type EventFilter struct {
	AggregateType string
	AggregateID   string
	EventType     string
	After         int64
	Limit         int
}

type MovementFilter struct {
	LocationID   string
	SKU          string
	MovementType string
	After        int64
	Limit        int
}

type LedgerFilter struct {
	Account    string
	LocationID string
	SKU        string
	RefType    string
	RefID      string
	After      int64
	Limit      int
}

type EventPage struct {
	Events    []Event `json:"events"`
	NextAfter int64   `json:"next_after"`
	HasMore   bool    `json:"has_more"`
}

type MovementPage struct {
	Movements []Movement `json:"movements"`
	NextAfter int64      `json:"next_after"`
	HasMore   bool       `json:"has_more"`
}

type LedgerPage struct {
	Entries   []LedgerEntry `json:"entries"`
	NextAfter int64         `json:"next_after"`
	HasMore   bool          `json:"has_more"`
}
