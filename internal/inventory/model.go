package inventory

import "time"

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
	LocationID string    `json:"location_id"`
	SKU        string    `json:"sku"`
	Found      bool      `json:"found"`
	OnHand     int64     `json:"on_hand"`
	Reserved   int64     `json:"reserved"`
	Available  int64     `json:"available"`
	InTransit  int64     `json:"in_transit"`
	AsOf       time.Time `json:"as_of"`
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
