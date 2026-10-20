package inventory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultTTL = 15 * time.Minute
	maxTTL     = 7 * 24 * time.Hour
)

type Service struct {
	pool    *pgxpool.Pool
	clock   func() time.Time
	metrics metrics
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, clock: time.Now}
}

func (s *Service) Receive(ctx context.Context, tenant, actor, key string, in ReceiveInput) (Position, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, in, 201, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.receiveTx(ctx, tx, tenant, actor, in)
	})
	if err != nil {
		return Position{}, false, err
	}

	var p Position
	if err := json.Unmarshal(raw, &p); err != nil {
		return Position{}, false, err
	}
	if !replay {
		s.metrics.receipts.Add(1)
	}
	return p, replay, nil
}

func (s *Service) Reserve(ctx context.Context, tenant, actor, key string, in ReserveInput) (Reservation, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, in, 201, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.reserveTx(ctx, tx, tenant, actor, in)
	})
	if err != nil {
		if errors.Is(err, ErrInsufficientStock) {
			s.metrics.rejections.Add(1)
		}
		return Reservation{}, false, err
	}

	var r Reservation
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reservation{}, false, err
	}
	if !replay {
		s.metrics.reservations.Add(1)
	}
	return r, replay, nil
}

func (s *Service) Confirm(ctx context.Context, tenant, actor, key, id string) (Reservation, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, map[string]string{"id": id}, 200, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.confirmTx(ctx, tx, tenant, actor, id)
	})
	if err != nil {
		return Reservation{}, false, err
	}

	var r Reservation
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reservation{}, false, err
	}
	if !replay {
		s.metrics.confirmations.Add(1)
	}
	return r, replay, nil
}

func (s *Service) Release(ctx context.Context, tenant, actor, key, id string) (Reservation, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, map[string]string{"id": id}, 200, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.releaseTx(ctx, tx, tenant, actor, id)
	})
	if err != nil {
		return Reservation{}, false, err
	}

	var r Reservation
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reservation{}, false, err
	}
	if !replay {
		s.metrics.releases.Add(1)
	}
	return r, replay, nil
}

func (s *Service) CreateTransfer(ctx context.Context, tenant, actor, key string, in TransferInput) (Transfer, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, in, 201, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.createTransferTx(ctx, tx, tenant, actor, in)
	})
	if err != nil {
		return Transfer{}, false, err
	}

	var t Transfer
	if err := json.Unmarshal(raw, &t); err != nil {
		return Transfer{}, false, err
	}
	if !replay {
		s.metrics.transfers.Add(1)
	}
	return t, replay, nil
}

func (s *Service) ReceiveTransfer(ctx context.Context, tenant, actor, key, id string) (Transfer, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, map[string]string{"id": id}, 200, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.receiveTransferTx(ctx, tx, tenant, actor, id)
	})
	if err != nil {
		return Transfer{}, false, err
	}

	var t Transfer
	if err := json.Unmarshal(raw, &t); err != nil {
		return Transfer{}, false, err
	}
	if !replay {
		s.metrics.receives.Add(1)
	}
	return t, replay, nil
}

func (s *Service) GetPosition(ctx context.Context, tenant, locationID, sku string) (Position, error) {
	p, err := s.readPosition(ctx, tenant, locationID, sku)
	if err != nil {
		return Position{}, err
	}
	return p, nil
}

func (s *Service) GetReservation(ctx context.Context, tenant, id string) (Reservation, error) {
	return s.readReservation(ctx, s.pool, tenant, id)
}

func (s *Service) GetTransfer(ctx context.Context, tenant, id string) (Transfer, error) {
	return s.readTransfer(ctx, s.pool, tenant, id)
}

func (s *Service) ATP(ctx context.Context, tenant, locationID, sku string) (ATP, error) {
	if err := validateID("location_id", locationID); err != nil {
		return ATP{}, err
	}
	if err := validateID("sku", sku); err != nil {
		return ATP{}, err
	}

	now := s.clock()
	var onHand, reserved int64
	err := s.pool.QueryRow(ctx,
		`SELECT on_hand, reserved FROM positions WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, locationID, sku,
	).Scan(&onHand, &reserved)
	found := true
	if errors.Is(err, pgx.ErrNoRows) {
		found = false
	} else if err != nil {
		return ATP{}, err
	}

	var inTransit int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(quantity), 0) FROM transfers
		 WHERE tenant_id = $1 AND to_location_id = $2 AND sku = $3 AND status = 'in_transit'`,
		tenant, locationID, sku,
	).Scan(&inTransit); err != nil {
		return ATP{}, err
	}

	return ATP{
		LocationID: locationID,
		SKU:        sku,
		Found:      found,
		OnHand:     onHand,
		Reserved:   reserved,
		Available:  onHand - reserved,
		InTransit:  inTransit,
		AsOf:       now,
	}, nil
}

func (s *Service) Reconcile(ctx context.Context, tenant string) (Reconciliation, error) {
	issues := make([]string, 0)

	movementRows, err := s.pool.Query(ctx,
		`SELECT p.location_id, p.sku, p.on_hand, COALESCE(m.qty, 0)
		 FROM positions p
		 LEFT JOIN (
			SELECT location_id, sku, SUM(quantity_delta) AS qty
			FROM inventory_movements
			WHERE tenant_id = $1
			GROUP BY location_id, sku
		 ) m ON m.location_id = p.location_id AND m.sku = p.sku
		 WHERE p.tenant_id = $1 AND p.on_hand <> COALESCE(m.qty, 0)
		 ORDER BY p.location_id, p.sku`, tenant)
	if err != nil {
		return Reconciliation{}, err
	}
	for movementRows.Next() {
		var loc, sku string
		var onHand, moved int64
		if err := movementRows.Scan(&loc, &sku, &onHand, &moved); err != nil {
			movementRows.Close()
			return Reconciliation{}, err
		}
		issues = append(issues, fmt.Sprintf("position %s/%s on_hand=%d but movements=%d", loc, sku, onHand, moved))
	}
	movementRows.Close()
	if err := movementRows.Err(); err != nil {
		return Reconciliation{}, err
	}

	reservedRows, err := s.pool.Query(ctx,
		`SELECT p.location_id, p.sku, p.reserved, COALESCE(r.qty, 0)
		 FROM positions p
		 LEFT JOIN (
			SELECT location_id, sku, SUM(quantity) AS qty
			FROM reservations
			WHERE tenant_id = $1 AND status = 'active'
			GROUP BY location_id, sku
		 ) r ON r.location_id = p.location_id AND r.sku = p.sku
		 WHERE p.tenant_id = $1 AND p.reserved <> COALESCE(r.qty, 0)
		 ORDER BY p.location_id, p.sku`, tenant)
	if err != nil {
		return Reconciliation{}, err
	}
	for reservedRows.Next() {
		var loc, sku string
		var reserved, active int64
		if err := reservedRows.Scan(&loc, &sku, &reserved, &active); err != nil {
			reservedRows.Close()
			return Reconciliation{}, err
		}
		issues = append(issues, fmt.Sprintf("position %s/%s reserved=%d but active reservations=%d", loc, sku, reserved, active))
	}
	reservedRows.Close()
	if err := reservedRows.Err(); err != nil {
		return Reconciliation{}, err
	}

	var ledgerImbalance int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(debit_cents - credit_cents), 0) FROM ledger_entries WHERE tenant_id = $1`,
		tenant,
	).Scan(&ledgerImbalance); err != nil {
		return Reconciliation{}, err
	}
	if ledgerImbalance != 0 {
		issues = append(issues, fmt.Sprintf("ledger is not balanced: sum(debit-credit)=%d", ledgerImbalance))
	}

	valueRows, err := s.pool.Query(ctx,
		`SELECT p.location_id, p.sku, p.inventory_value_cents, COALESCE(l.balance, 0)
		 FROM positions p
		 LEFT JOIN (
			SELECT location_id, sku, SUM(debit_cents - credit_cents) AS balance
			FROM ledger_entries
			WHERE tenant_id = $1 AND account = 'inventory'
			GROUP BY location_id, sku
		 ) l ON l.location_id = p.location_id AND l.sku = p.sku
		 WHERE p.tenant_id = $1 AND p.inventory_value_cents <> COALESCE(l.balance, 0)
		 ORDER BY p.location_id, p.sku`, tenant)
	if err != nil {
		return Reconciliation{}, err
	}
	for valueRows.Next() {
		var loc, sku string
		var positionValue, ledgerValue int64
		if err := valueRows.Scan(&loc, &sku, &positionValue, &ledgerValue); err != nil {
			valueRows.Close()
			return Reconciliation{}, err
		}
		issues = append(issues, fmt.Sprintf("position %s/%s value=%d but inventory ledger=%d", loc, sku, positionValue, ledgerValue))
	}
	valueRows.Close()
	if err := valueRows.Err(); err != nil {
		return Reconciliation{}, err
	}

	var transferValue, transitLedger int64
	if err := s.pool.QueryRow(ctx,
		`SELECT
			COALESCE((SELECT SUM(value_cents) FROM transfers WHERE tenant_id = $1 AND status = 'in_transit'), 0),
			COALESCE((SELECT SUM(debit_cents - credit_cents) FROM ledger_entries WHERE tenant_id = $1 AND account = 'inventory_in_transit'), 0)`,
		tenant,
	).Scan(&transferValue, &transitLedger); err != nil {
		return Reconciliation{}, err
	}
	if transferValue != transitLedger {
		issues = append(issues, fmt.Sprintf("in-transit value=%d but ledger=%d", transferValue, transitLedger))
	}

	return Reconciliation{
		OK:        len(issues) == 0,
		Issues:    issues,
		CheckedAt: s.clock(),
	}, nil
}

func (s *Service) mutate(
	ctx context.Context,
	tenant, key string,
	input any,
	status int,
	fn func(context.Context, pgx.Tx) (any, error),
) (json.RawMessage, bool, error) {
	if err := validateID("tenant", tenant); err != nil {
		return nil, false, err
	}
	requestHash, err := hashInput(input)
	if err != nil {
		return nil, false, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if key != "" {
		var inserted string
		err := tx.QueryRow(ctx,
			`INSERT INTO idempotency_keys (tenant_id, key, request_hash)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (tenant_id, key) DO NOTHING
			 RETURNING key`,
			tenant, key, requestHash,
		).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.replayIdempotent(ctx, tx, tenant, key, requestHash)
		}
		if err != nil {
			return nil, false, fmt.Errorf("claim idempotency key: %w", err)
		}
	}

	result, err := fn(ctx, tx)
	if err != nil {
		return nil, false, err
	}

	body, err := json.Marshal(result)
	if err != nil {
		return nil, false, err
	}

	if key != "" {
		if _, err := tx.Exec(ctx,
			`UPDATE idempotency_keys
			 SET response_status = $3, response_body = $4
			 WHERE tenant_id = $1 AND key = $2`,
			tenant, key, status, string(body),
		); err != nil {
			return nil, false, fmt.Errorf("store idempotent response: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return body, false, nil
}

func (s *Service) replayIdempotent(ctx context.Context, tx pgx.Tx, tenant, key, requestHash string) (json.RawMessage, bool, error) {
	var storedHash string
	var storedStatus *int
	var storedBody []byte
	if err := tx.QueryRow(ctx,
		`SELECT request_hash, response_status, response_body
		 FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`,
		tenant, key,
	).Scan(&storedHash, &storedStatus, &storedBody); err != nil {
		return nil, false, err
	}
	if storedHash != requestHash {
		return nil, false, ErrIdempotencyMismatch
	}
	if storedStatus == nil || storedBody == nil {
		return nil, false, ErrRequestInProgress
	}
	return storedBody, true, nil
}

func (s *Service) receiveTx(ctx context.Context, tx pgx.Tx, tenant, actor string, in ReceiveInput) (Position, error) {
	if err := validateID("location_id", in.LocationID); err != nil {
		return Position{}, err
	}
	if err := validateID("sku", in.SKU); err != nil {
		return Position{}, err
	}
	if err := validateQuantity(in.Quantity); err != nil {
		return Position{}, err
	}
	if in.UnitCostCents < 0 {
		return Position{}, &ValidationError{Field: "unit_cost_cents", Message: "must be >= 0"}
	}
	if in.UnitCostCents > 0 && in.Quantity > math.MaxInt64/in.UnitCostCents {
		return Position{}, &ValidationError{Field: "quantity", Message: "value overflow"}
	}

	if err := s.ensureMasterData(ctx, tx, tenant, []string{in.LocationID}, in.SKU); err != nil {
		return Position{}, err
	}

	value := in.Quantity * in.UnitCostCents
	var p Position
	if err := tx.QueryRow(ctx,
		`INSERT INTO positions (tenant_id, location_id, sku, on_hand, reserved, inventory_value_cents)
		 VALUES ($1, $2, $3, $4, 0, $5)
		 ON CONFLICT (tenant_id, location_id, sku) DO UPDATE
		 SET on_hand = positions.on_hand + EXCLUDED.on_hand,
			 inventory_value_cents = positions.inventory_value_cents + EXCLUDED.inventory_value_cents,
			 version = positions.version + 1,
			 updated_at = now()
		 RETURNING location_id, sku, on_hand, reserved, inventory_value_cents, version, updated_at`,
		tenant, in.LocationID, in.SKU, in.Quantity, value,
	).Scan(&p.LocationID, &p.SKU, &p.OnHand, &p.Reserved, &p.InventoryValueCents, &p.Version, &p.UpdatedAt); err != nil {
		return Position{}, err
	}
	p.Available = p.OnHand - p.Reserved

	receiptID, err := newID()
	if err != nil {
		return Position{}, err
	}
	if err := s.recordMovement(ctx, tx, tenant, p.LocationID, p.SKU, "receive", in.Quantity, value, "receipt", receiptID); err != nil {
		return Position{}, err
	}
	if err := s.postLedger(ctx, tx, tenant, p.LocationID, p.SKU, "receipt", receiptID, "inventory", "inventory_source", value); err != nil {
		return Position{}, err
	}
	if err := s.emit(ctx, tx, tenant, "position", positionKey(p.LocationID, p.SKU), "inventory.received", actor, map[string]any{
		"location_id":     p.LocationID,
		"sku":             p.SKU,
		"quantity":        in.Quantity,
		"unit_cost_cents": in.UnitCostCents,
		"on_hand":         p.OnHand,
		"inventory_value": p.InventoryValueCents,
		"receipt_id":      receiptID,
	}); err != nil {
		return Position{}, err
	}

	return p, nil
}

func (s *Service) reserveTx(ctx context.Context, tx pgx.Tx, tenant, actor string, in ReserveInput) (Reservation, error) {
	if err := validateID("location_id", in.LocationID); err != nil {
		return Reservation{}, err
	}
	if err := validateID("sku", in.SKU); err != nil {
		return Reservation{}, err
	}
	if err := validateQuantity(in.Quantity); err != nil {
		return Reservation{}, err
	}
	ttl := defaultTTL
	if in.TTLSeconds > 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
	}
	if ttl > maxTTL {
		return Reservation{}, &ValidationError{Field: "ttl_seconds", Message: "must be <= 604800"}
	}

	p, err := s.lockPosition(ctx, tx, tenant, in.LocationID, in.SKU)
	if err != nil {
		return Reservation{}, err
	}
	if p.Available < in.Quantity {
		return Reservation{}, fmt.Errorf("%w: available=%d requested=%d", ErrInsufficientStock, p.Available, in.Quantity)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE positions
		 SET reserved = reserved + $4, version = version + 1, updated_at = now()
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, in.LocationID, in.SKU, in.Quantity,
	); err != nil {
		return Reservation{}, err
	}

	id, err := newID()
	if err != nil {
		return Reservation{}, err
	}
	expiresAt := s.clock().Add(ttl)

	var r Reservation
	if err := tx.QueryRow(ctx,
		`INSERT INTO reservations (tenant_id, id, location_id, sku, quantity, status, expires_at)
		 VALUES ($1, $2, $3, $4, $5, 'active', $6)
		 RETURNING id, location_id, sku, quantity, status, expires_at, created_at, updated_at`,
		tenant, id, in.LocationID, in.SKU, in.Quantity, expiresAt,
	).Scan(&r.ID, &r.LocationID, &r.SKU, &r.Quantity, &r.Status, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Reservation{}, err
	}

	if err := s.emit(ctx, tx, tenant, "reservation", r.ID, "reservation.created", actor, map[string]any{
		"reservation_id": r.ID,
		"location_id":    r.LocationID,
		"sku":            r.SKU,
		"quantity":       r.Quantity,
		"expires_at":     r.ExpiresAt,
	}); err != nil {
		return Reservation{}, err
	}

	return r, nil
}

func (s *Service) confirmTx(ctx context.Context, tx pgx.Tx, tenant, actor, id string) (Reservation, error) {
	r, err := s.lockReservation(ctx, tx, tenant, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.Status == "confirmed" {
		return r, nil
	}
	if r.Status != "active" {
		return Reservation{}, fmt.Errorf("%w: reservation is %s", ErrInvalidState, r.Status)
	}

	p, err := s.lockPosition(ctx, tx, tenant, r.LocationID, r.SKU)
	if err != nil {
		return Reservation{}, err
	}
	if p.Reserved < r.Quantity || p.OnHand < r.Quantity {
		return Reservation{}, fmt.Errorf("inconsistent position %s/%s: on_hand=%d reserved=%d quantity=%d", p.LocationID, p.SKU, p.OnHand, p.Reserved, r.Quantity)
	}

	cogs := p.InventoryValueCents * r.Quantity / p.OnHand
	if r.Quantity == p.OnHand {
		cogs = p.InventoryValueCents
	}

	if _, err := tx.Exec(ctx,
		`UPDATE positions
		 SET on_hand = on_hand - $4,
			 reserved = reserved - $4,
			 inventory_value_cents = inventory_value_cents - $5,
			 version = version + 1,
			 updated_at = now()
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, r.LocationID, r.SKU, r.Quantity, cogs,
	); err != nil {
		return Reservation{}, err
	}

	if err := tx.QueryRow(ctx,
		`UPDATE reservations
		 SET status = 'confirmed', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING id, location_id, sku, quantity, status, expires_at, created_at, updated_at`,
		tenant, id,
	).Scan(&r.ID, &r.LocationID, &r.SKU, &r.Quantity, &r.Status, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Reservation{}, err
	}

	if err := s.recordMovement(ctx, tx, tenant, r.LocationID, r.SKU, "ship", -r.Quantity, -cogs, "reservation", r.ID); err != nil {
		return Reservation{}, err
	}
	if err := s.postLedger(ctx, tx, tenant, r.LocationID, r.SKU, "shipment", r.ID, "cogs", "inventory", cogs); err != nil {
		return Reservation{}, err
	}
	if err := s.emit(ctx, tx, tenant, "reservation", r.ID, "reservation.confirmed", actor, map[string]any{
		"reservation_id": r.ID,
		"location_id":    r.LocationID,
		"sku":            r.SKU,
		"quantity":       r.Quantity,
		"cogs_cents":     cogs,
	}); err != nil {
		return Reservation{}, err
	}

	return r, nil
}

func (s *Service) releaseTx(ctx context.Context, tx pgx.Tx, tenant, actor, id string) (Reservation, error) {
	r, err := s.lockReservation(ctx, tx, tenant, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.Status == "released" {
		return r, nil
	}
	if r.Status != "active" {
		return Reservation{}, fmt.Errorf("%w: reservation is %s", ErrInvalidState, r.Status)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE positions
		 SET reserved = reserved - $4, version = version + 1, updated_at = now()
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, r.LocationID, r.SKU, r.Quantity,
	); err != nil {
		return Reservation{}, err
	}

	if err := tx.QueryRow(ctx,
		`UPDATE reservations
		 SET status = 'released', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING id, location_id, sku, quantity, status, expires_at, created_at, updated_at`,
		tenant, id,
	).Scan(&r.ID, &r.LocationID, &r.SKU, &r.Quantity, &r.Status, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Reservation{}, err
	}

	if err := s.emit(ctx, tx, tenant, "reservation", r.ID, "reservation.released", actor, map[string]any{
		"reservation_id": r.ID,
		"location_id":    r.LocationID,
		"sku":            r.SKU,
		"quantity":       r.Quantity,
	}); err != nil {
		return Reservation{}, err
	}

	return r, nil
}

func (s *Service) createTransferTx(ctx context.Context, tx pgx.Tx, tenant, actor string, in TransferInput) (Transfer, error) {
	if err := validateID("from_location_id", in.FromLocationID); err != nil {
		return Transfer{}, err
	}
	if err := validateID("to_location_id", in.ToLocationID); err != nil {
		return Transfer{}, err
	}
	if in.FromLocationID == in.ToLocationID {
		return Transfer{}, &ValidationError{Field: "to_location_id", Message: "must differ from from_location_id"}
	}
	if err := validateID("sku", in.SKU); err != nil {
		return Transfer{}, err
	}
	if err := validateQuantity(in.Quantity); err != nil {
		return Transfer{}, err
	}

	if err := s.ensureMasterData(ctx, tx, tenant, []string{in.FromLocationID, in.ToLocationID}, in.SKU); err != nil {
		return Transfer{}, err
	}

	p, err := s.lockPosition(ctx, tx, tenant, in.FromLocationID, in.SKU)
	if err != nil {
		return Transfer{}, err
	}
	if p.Available < in.Quantity {
		return Transfer{}, fmt.Errorf("%w: available=%d requested=%d", ErrInsufficientStock, p.Available, in.Quantity)
	}

	value := p.InventoryValueCents * in.Quantity / p.OnHand
	if in.Quantity == p.OnHand {
		value = p.InventoryValueCents
	}

	if _, err := tx.Exec(ctx,
		`UPDATE positions
		 SET on_hand = on_hand - $4,
			 inventory_value_cents = inventory_value_cents - $5,
			 version = version + 1,
			 updated_at = now()
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, in.FromLocationID, in.SKU, in.Quantity, value,
	); err != nil {
		return Transfer{}, err
	}

	id, err := newID()
	if err != nil {
		return Transfer{}, err
	}

	var t Transfer
	if err := tx.QueryRow(ctx,
		`INSERT INTO transfers (tenant_id, id, from_location_id, to_location_id, sku, quantity, value_cents, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'in_transit')
		 RETURNING id, from_location_id, to_location_id, sku, quantity, value_cents, status, created_at, received_at`,
		tenant, id, in.FromLocationID, in.ToLocationID, in.SKU, in.Quantity, value,
	).Scan(&t.ID, &t.FromLocationID, &t.ToLocationID, &t.SKU, &t.Quantity, &t.ValueCents, &t.Status, &t.CreatedAt, &t.ReceivedAt); err != nil {
		return Transfer{}, err
	}

	if err := s.recordMovement(ctx, tx, tenant, in.FromLocationID, in.SKU, "transfer_out", -in.Quantity, -value, "transfer", t.ID); err != nil {
		return Transfer{}, err
	}
	if err := s.postLedger(ctx, tx, tenant, in.FromLocationID, in.SKU, "transfer_out", t.ID, "inventory_in_transit", "inventory", value); err != nil {
		return Transfer{}, err
	}
	if err := s.emit(ctx, tx, tenant, "transfer", t.ID, "transfer.created", actor, map[string]any{
		"transfer_id":      t.ID,
		"from_location_id": t.FromLocationID,
		"to_location_id":   t.ToLocationID,
		"sku":              t.SKU,
		"quantity":         t.Quantity,
		"value_cents":      t.ValueCents,
	}); err != nil {
		return Transfer{}, err
	}

	return t, nil
}

func (s *Service) receiveTransferTx(ctx context.Context, tx pgx.Tx, tenant, actor, id string) (Transfer, error) {
	t, err := s.lockTransfer(ctx, tx, tenant, id)
	if err != nil {
		return Transfer{}, err
	}
	if t.Status == "received" {
		return t, nil
	}

	if err := s.ensureMasterData(ctx, tx, tenant, []string{t.ToLocationID}, t.SKU); err != nil {
		return Transfer{}, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO positions (tenant_id, location_id, sku, on_hand, reserved, inventory_value_cents)
		 VALUES ($1, $2, $3, $4, 0, $5)
		 ON CONFLICT (tenant_id, location_id, sku) DO UPDATE
		 SET on_hand = positions.on_hand + EXCLUDED.on_hand,
			 inventory_value_cents = positions.inventory_value_cents + EXCLUDED.inventory_value_cents,
			 version = positions.version + 1,
			 updated_at = now()`,
		tenant, t.ToLocationID, t.SKU, t.Quantity, t.ValueCents,
	); err != nil {
		return Transfer{}, err
	}

	if err := tx.QueryRow(ctx,
		`UPDATE transfers
		 SET status = 'received', received_at = now()
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING id, from_location_id, to_location_id, sku, quantity, value_cents, status, created_at, received_at`,
		tenant, id,
	).Scan(&t.ID, &t.FromLocationID, &t.ToLocationID, &t.SKU, &t.Quantity, &t.ValueCents, &t.Status, &t.CreatedAt, &t.ReceivedAt); err != nil {
		return Transfer{}, err
	}

	if err := s.recordMovement(ctx, tx, tenant, t.ToLocationID, t.SKU, "transfer_in", t.Quantity, t.ValueCents, "transfer", t.ID); err != nil {
		return Transfer{}, err
	}
	if err := s.postLedger(ctx, tx, tenant, t.ToLocationID, t.SKU, "transfer_in", t.ID, "inventory", "inventory_in_transit", t.ValueCents); err != nil {
		return Transfer{}, err
	}
	if err := s.emit(ctx, tx, tenant, "transfer", t.ID, "transfer.received", actor, map[string]any{
		"transfer_id":    t.ID,
		"to_location_id": t.ToLocationID,
		"sku":            t.SKU,
		"quantity":       t.Quantity,
		"value_cents":    t.ValueCents,
	}); err != nil {
		return Transfer{}, err
	}

	return t, nil
}

func (s *Service) ensureMasterData(ctx context.Context, tx pgx.Tx, tenant string, locationIDs []string, sku string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, tenant,
	); err != nil {
		return err
	}
	for _, locationID := range locationIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO locations (tenant_id, id, name) VALUES ($1, $2, $2)
			 ON CONFLICT (tenant_id, id) DO NOTHING`,
			tenant, locationID,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO items (tenant_id, sku, name) VALUES ($1, $2, $2)
		 ON CONFLICT (tenant_id, sku) DO NOTHING`,
		tenant, sku,
	); err != nil {
		return err
	}
	return nil
}

func (s *Service) lockPosition(ctx context.Context, tx pgx.Tx, tenant, locationID, sku string) (Position, error) {
	var p Position
	err := tx.QueryRow(ctx,
		`SELECT location_id, sku, on_hand, reserved, inventory_value_cents, version, updated_at
		 FROM positions
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3
		 FOR UPDATE`,
		tenant, locationID, sku,
	).Scan(&p.LocationID, &p.SKU, &p.OnHand, &p.Reserved, &p.InventoryValueCents, &p.Version, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Position{}, fmt.Errorf("%w: position %s/%s", ErrNotFound, locationID, sku)
	}
	if err != nil {
		return Position{}, err
	}
	p.Available = p.OnHand - p.Reserved
	return p, nil
}

func (s *Service) readPosition(ctx context.Context, tenant, locationID, sku string) (Position, error) {
	var p Position
	err := s.pool.QueryRow(ctx,
		`SELECT location_id, sku, on_hand, reserved, inventory_value_cents, version, updated_at
		 FROM positions
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, locationID, sku,
	).Scan(&p.LocationID, &p.SKU, &p.OnHand, &p.Reserved, &p.InventoryValueCents, &p.Version, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Position{}, fmt.Errorf("%w: position %s/%s", ErrNotFound, locationID, sku)
	}
	if err != nil {
		return Position{}, err
	}
	p.Available = p.OnHand - p.Reserved
	return p, nil
}

func (s *Service) lockReservation(ctx context.Context, tx pgx.Tx, tenant, id string) (Reservation, error) {
	var r Reservation
	err := tx.QueryRow(ctx,
		`SELECT id, location_id, sku, quantity, status, expires_at, created_at, updated_at
		 FROM reservations
		 WHERE tenant_id = $1 AND id = $2
		 FOR UPDATE`,
		tenant, id,
	).Scan(&r.ID, &r.LocationID, &r.SKU, &r.Quantity, &r.Status, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, fmt.Errorf("%w: reservation %s", ErrNotFound, id)
	}
	return r, err
}

func (s *Service) readReservation(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant, id string) (Reservation, error) {
	var r Reservation
	err := q.QueryRow(ctx,
		`SELECT id, location_id, sku, quantity, status, expires_at, created_at, updated_at
		 FROM reservations WHERE tenant_id = $1 AND id = $2`,
		tenant, id,
	).Scan(&r.ID, &r.LocationID, &r.SKU, &r.Quantity, &r.Status, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, fmt.Errorf("%w: reservation %s", ErrNotFound, id)
	}
	return r, err
}

func (s *Service) lockTransfer(ctx context.Context, tx pgx.Tx, tenant, id string) (Transfer, error) {
	var t Transfer
	err := tx.QueryRow(ctx,
		`SELECT id, from_location_id, to_location_id, sku, quantity, value_cents, status, created_at, received_at
		 FROM transfers WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenant, id,
	).Scan(&t.ID, &t.FromLocationID, &t.ToLocationID, &t.SKU, &t.Quantity, &t.ValueCents, &t.Status, &t.CreatedAt, &t.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, fmt.Errorf("%w: transfer %s", ErrNotFound, id)
	}
	return t, err
}

func (s *Service) readTransfer(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant, id string) (Transfer, error) {
	var t Transfer
	err := q.QueryRow(ctx,
		`SELECT id, from_location_id, to_location_id, sku, quantity, value_cents, status, created_at, received_at
		 FROM transfers WHERE tenant_id = $1 AND id = $2`,
		tenant, id,
	).Scan(&t.ID, &t.FromLocationID, &t.ToLocationID, &t.SKU, &t.Quantity, &t.ValueCents, &t.Status, &t.CreatedAt, &t.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, fmt.Errorf("%w: transfer %s", ErrNotFound, id)
	}
	return t, err
}

func (s *Service) recordMovement(ctx context.Context, tx pgx.Tx, tenant, locationID, sku, movementType string, quantityDelta, valueDelta int64, refType, refID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO inventory_movements
		 (tenant_id, location_id, sku, movement_type, quantity_delta, value_delta_cents, ref_type, ref_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenant, locationID, sku, movementType, quantityDelta, valueDelta, refType, refID,
	)
	return err
}

func (s *Service) postLedger(ctx context.Context, tx pgx.Tx, tenant, locationID, sku, refType, refID, debitAccount, creditAccount string, amount int64) error {
	if amount < 0 {
		return fmt.Errorf("ledger amount must be non-negative, got %d", amount)
	}
	if amount == 0 {
		return nil
	}
	if debitAccount == creditAccount {
		return fmt.Errorf("ledger accounts must differ")
	}

	group := refType + ":" + refID
	if _, err := tx.Exec(ctx,
		`INSERT INTO ledger_entries
		 (tenant_id, entry_group, account, location_id, sku, debit_cents, credit_cents, ref_type, ref_id)
		 VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8), ($1, $2, $9, $4, $5, 0, $6, $7, $8)`,
		tenant, group, debitAccount, locationID, sku, amount, refType, refID, creditAccount,
	); err != nil {
		return err
	}
	return nil
}

func (s *Service) emit(ctx context.Context, tx pgx.Tx, tenant, aggregateType, aggregateID, eventType, actor string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO inventory_events (tenant_id, aggregate_type, aggregate_id, event_type, payload, actor)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		tenant, aggregateType, aggregateID, eventType, string(body), actor,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (tenant_id, topic, payload) VALUES ($1, $2, $3)`,
		tenant, "inventory.events", string(body),
	); err != nil {
		return err
	}
	return nil
}

func validateID(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return &ValidationError{Field: field, Message: "is required"}
	}
	if len(value) > 64 {
		return &ValidationError{Field: field, Message: "must be at most 64 characters"}
	}
	return nil
}

func validateQuantity(quantity int64) error {
	if quantity <= 0 {
		return &ValidationError{Field: "quantity", Message: "must be > 0"}
	}
	if quantity > 1_000_000_000_000 {
		return &ValidationError{Field: "quantity", Message: "is too large"}
	}
	return nil
}

func hashInput(input any) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func newID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func positionKey(locationID, sku string) string {
	return locationID + "/" + sku
}
