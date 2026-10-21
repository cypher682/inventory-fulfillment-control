package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func validateLotCode(lotCode string) (string, error) {
	lotCode = strings.TrimSpace(lotCode)
	if lotCode == "" {
		return "", nil
	}
	if len(lotCode) > 64 {
		return "", &ValidationError{Field: "lot_code", Message: "must be at most 64 characters"}
	}
	return lotCode, nil
}

func validateSerials(serials []string, quantity int64) ([]string, error) {
	if len(serials) == 0 {
		return nil, nil
	}
	if int64(len(serials)) != quantity {
		return nil, &ValidationError{Field: "serial_numbers", Message: "must match quantity"}
	}
	seen := make(map[string]struct{}, len(serials))
	out := make([]string, 0, len(serials))
	for _, serial := range serials {
		serial = strings.TrimSpace(serial)
		if serial == "" {
			return nil, &ValidationError{Field: "serial_numbers", Message: "must not contain empty values"}
		}
		if len(serial) > 128 {
			return nil, &ValidationError{Field: "serial_numbers", Message: "must be at most 128 characters each"}
		}
		if _, ok := seen[serial]; ok {
			return nil, &ValidationError{Field: "serial_numbers", Message: "must be unique within a receipt"}
		}
		seen[serial] = struct{}{}
		out = append(out, serial)
	}
	return out, nil
}

// ensureLot creates the lot if needed and returns its current hold state. The
// caller holds the position row lock, which serializes lot creation and holds
// for the same position.
func (s *Service) ensureLot(
	ctx context.Context,
	tx pgx.Tx,
	tenant, locationID, sku, lotCode string,
	expiresAt *time.Time,
) (string, *time.Time, error) {
	if lotCode == "" {
		return "", nil, nil
	}
	var status string
	var existingExpiry *time.Time
	err := tx.QueryRow(ctx,
		`SELECT status, expires_at FROM lots
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3 AND lot_code = $4
		 FOR UPDATE`,
		tenant, locationID, sku, lotCode,
	).Scan(&status, &existingExpiry)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`INSERT INTO lots (tenant_id, location_id, sku, lot_code, expires_at)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING status, expires_at`,
			tenant, locationID, sku, lotCode, expiresAt,
		).Scan(&status, &existingExpiry)
		return status, existingExpiry, err
	}
	if err != nil {
		return "", nil, err
	}
	if expiresAt != nil && existingExpiry != nil {
		if !expiresAt.Equal(*existingExpiry) {
			return "", nil, &ValidationError{Field: "expires_at", Message: "does not match the existing lot"}
		}
	} else if expiresAt != nil && existingExpiry == nil {
		if _, err := tx.Exec(ctx,
			`UPDATE lots SET expires_at = $5, updated_at = now()
			 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3 AND lot_code = $4`,
			tenant, locationID, sku, lotCode, expiresAt,
		); err != nil {
			return "", nil, err
		}
		existingExpiry = expiresAt
	}
	return status, existingExpiry, nil
}

func (s *Service) recordSerials(
	ctx context.Context,
	tx pgx.Tx,
	tenant, locationID, sku, lotCode string,
	layerID int64,
	serials []string,
) error {
	for _, serial := range serials {
		if _, err := tx.Exec(ctx,
			`INSERT INTO inventory_serials
			 (tenant_id, serial, location_id, sku, lot_code, layer_id)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			tenant, serial, locationID, sku, lotCode, layerID,
		); err != nil {
			return err
		}
	}
	return nil
}

// claimLayerSerials selects the in-stock serials for a layer use. Untracked
// layers have no serials; a tracked layer must have exactly the consumed
// quantity available.
func (s *Service) claimLayerSerials(
	ctx context.Context,
	tx pgx.Tx,
	tenant string,
	layerID, quantity int64,
) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT serial FROM inventory_serials
		 WHERE tenant_id = $1 AND layer_id = $2 AND status = 'in_stock'
		 ORDER BY serial
		 LIMIT $3
		 FOR UPDATE`,
		tenant, layerID, quantity,
	)
	if err != nil {
		return nil, err
	}
	serials := make([]string, 0, quantity)
	for rows.Next() {
		var serial string
		if err := rows.Scan(&serial); err != nil {
			rows.Close()
			return nil, err
		}
		serials = append(serials, serial)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(serials) == 0 {
		return nil, nil
	}
	if int64(len(serials)) != quantity {
		return nil, fmt.Errorf("inconsistent serials for layer %d: available=%d consumed=%d", layerID, len(serials), quantity)
	}
	return serials, nil
}

func (s *Service) setSerialState(
	ctx context.Context,
	tx pgx.Tx,
	tenant string,
	serials []string,
	status string,
	transferID *string,
	transferLayerID *int64,
) error {
	if len(serials) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`UPDATE inventory_serials
		 SET status = $3,
		     transfer_id = $4,
		     transfer_layer_id = $5,
		     updated_at = now()
		 WHERE tenant_id = $1 AND serial = ANY($2)`,
		tenant, serials, status, transferID, transferLayerID,
	)
	return err
}

func (s *Service) HoldLot(ctx context.Context, tenant, actor, key string, in HoldLotInput) (Lot, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, in, 200, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.holdLotTx(ctx, tx, tenant, actor, in)
	})
	if err != nil {
		return Lot{}, false, err
	}
	var lot Lot
	if err := json.Unmarshal(raw, &lot); err != nil {
		return Lot{}, false, err
	}
	return lot, replay, nil
}

func (s *Service) holdLotTx(ctx context.Context, tx pgx.Tx, tenant, actor string, in HoldLotInput) (Lot, error) {
	if err := validateID("location_id", in.LocationID); err != nil {
		return Lot{}, err
	}
	if err := validateID("sku", in.SKU); err != nil {
		return Lot{}, err
	}
	lotCode, err := validateLotCode(in.LotCode)
	if err != nil {
		return Lot{}, err
	}
	if lotCode == "" {
		return Lot{}, &ValidationError{Field: "lot_code", Message: "is required"}
	}

	var onHand, reserved, held int64
	if err := tx.QueryRow(ctx,
		`SELECT on_hand, reserved, held FROM positions
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3
		 FOR UPDATE`,
		tenant, in.LocationID, in.SKU,
	).Scan(&onHand, &reserved, &held); errors.Is(err, pgx.ErrNoRows) {
		return Lot{}, fmt.Errorf("%w: position %s/%s", ErrNotFound, in.LocationID, in.SKU)
	} else if err != nil {
		return Lot{}, err
	}

	var lot Lot
	if err := tx.QueryRow(ctx,
		`SELECT location_id, sku, lot_code, expires_at, status, created_at, updated_at
		 FROM lots
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3 AND lot_code = $4
		 FOR UPDATE`,
		tenant, in.LocationID, in.SKU, lotCode,
	).Scan(&lot.LocationID, &lot.SKU, &lot.LotCode, &lot.ExpiresAt, &lot.Status, &lot.CreatedAt, &lot.UpdatedAt); errors.Is(err, pgx.ErrNoRows) {
		return Lot{}, fmt.Errorf("%w: lot %s", ErrNotFound, lotCode)
	} else if err != nil {
		return Lot{}, err
	}

	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(remaining_quantity), 0) FROM cost_layers
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3 AND lot_code = $4`,
		tenant, in.LocationID, in.SKU, lotCode,
	).Scan(&lot.Remaining); err != nil {
		return Lot{}, err
	}

	eventType := "inventory.lot.released"
	if in.Hold {
		eventType = "inventory.lot.held"
		if lot.Status != "hold" {
			if held+lot.Remaining > onHand-reserved {
				return Lot{}, fmt.Errorf("%w: lot=%d held=%d available=%d",
					ErrInsufficientStock, lot.Remaining, held, onHand-reserved)
			}
			held += lot.Remaining
			lot.Status = "hold"
		}
	} else if lot.Status == "hold" {
		held -= lot.Remaining
		if held < 0 {
			held = 0
		}
		lot.Status = "active"
	}

	if _, err := tx.Exec(ctx,
		`UPDATE positions SET held = $4, version = version + 1, updated_at = now()
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3`,
		tenant, in.LocationID, in.SKU, held,
	); err != nil {
		return Lot{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE lots SET status = $5, updated_at = now()
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3 AND lot_code = $4`,
		tenant, in.LocationID, in.SKU, lotCode, lot.Status,
	); err != nil {
		return Lot{}, err
	}
	if err := s.emit(ctx, tx, tenant, "lot", positionKey(in.LocationID, in.SKU)+"/"+lotCode, eventType, actor, map[string]any{
		"location_id": in.LocationID,
		"sku":         in.SKU,
		"lot_code":    lotCode,
		"hold":        in.Hold,
		"quantity":    lot.Remaining,
		"held":        held,
	}); err != nil {
		return Lot{}, err
	}
	return lot, nil
}

func (s *Service) ListLots(ctx context.Context, tenant, locationID, sku string, limit int) ([]Lot, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx,
		`SELECT l.location_id, l.sku, l.lot_code, l.expires_at, l.status, l.created_at, l.updated_at,
		        COALESCE(SUM(cl.remaining_quantity), 0)
		 FROM lots l
		 LEFT JOIN cost_layers cl
		   ON cl.tenant_id = l.tenant_id
		  AND cl.location_id = l.location_id
		  AND cl.sku = l.sku
		  AND cl.lot_code = l.lot_code
		 WHERE l.tenant_id = $1
		   AND ($2 = '' OR l.location_id = $2)
		   AND ($3 = '' OR l.sku = $3)
		 GROUP BY l.location_id, l.sku, l.lot_code, l.expires_at, l.status, l.created_at, l.updated_at
		 ORDER BY l.location_id, l.sku, l.lot_code
		 LIMIT $4`,
		tenant, locationID, sku, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lots := make([]Lot, 0)
	for rows.Next() {
		var lot Lot
		if err := rows.Scan(&lot.LocationID, &lot.SKU, &lot.LotCode, &lot.ExpiresAt, &lot.Status,
			&lot.CreatedAt, &lot.UpdatedAt, &lot.Remaining); err != nil {
			return nil, err
		}
		lots = append(lots, lot)
	}
	return lots, rows.Err()
}

func (s *Service) ListSerials(ctx context.Context, tenant, locationID, sku, lotCode, status string, limit int) ([]Serial, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx,
		`SELECT serial, location_id, sku, lot_code, status, layer_id,
		        COALESCE(transfer_id, ''), created_at, updated_at
		 FROM inventory_serials
		 WHERE tenant_id = $1
		   AND ($2 = '' OR location_id = $2)
		   AND ($3 = '' OR sku = $3)
		   AND ($4 = '' OR lot_code = $4)
		   AND ($5 = '' OR status = $5)
		 ORDER BY serial
		 LIMIT $6`,
		tenant, locationID, sku, lotCode, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	serials := make([]Serial, 0)
	for rows.Next() {
		var serial Serial
		if err := rows.Scan(&serial.Serial, &serial.LocationID, &serial.SKU, &serial.LotCode,
			&serial.Status, &serial.LayerID, &serial.TransferID, &serial.CreatedAt, &serial.UpdatedAt); err != nil {
			return nil, err
		}
		serials = append(serials, serial)
	}
	return serials, rows.Err()
}
