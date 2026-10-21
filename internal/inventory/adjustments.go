package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Service) Adjust(ctx context.Context, tenant, actor, key string, in AdjustmentInput) (Adjustment, bool, error) {
	raw, replay, err := s.mutate(ctx, tenant, key, in, 201, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return s.adjustTx(ctx, tx, tenant, actor, in)
	})
	if err != nil {
		return Adjustment{}, false, err
	}

	var adjustment Adjustment
	if err := json.Unmarshal(raw, &adjustment); err != nil {
		return Adjustment{}, false, err
	}
	if !replay {
		s.metrics.adjustments.Add(1)
	}
	return adjustment, replay, nil
}

func (s *Service) adjustTx(ctx context.Context, tx pgx.Tx, tenant, actor string, in AdjustmentInput) (Adjustment, error) {
	if err := validateID("location_id", in.LocationID); err != nil {
		return Adjustment{}, err
	}
	if err := validateID("sku", in.SKU); err != nil {
		return Adjustment{}, err
	}
	if in.QuantityDelta == 0 {
		return Adjustment{}, &ValidationError{Field: "quantity_delta", Message: "must not be zero"}
	}
	if in.QuantityDelta == math.MinInt64 {
		return Adjustment{}, &ValidationError{Field: "quantity_delta", Message: "is too large"}
	}
	quantity := in.QuantityDelta
	if quantity < 0 {
		quantity = -quantity
	}
	if err := validateQuantity(quantity); err != nil {
		return Adjustment{}, err
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return Adjustment{}, &ValidationError{Field: "reason", Message: "is required"}
	}
	if len(reason) > 200 {
		return Adjustment{}, &ValidationError{Field: "reason", Message: "must be at most 200 characters"}
	}
	if in.UnitCostCents < 0 {
		return Adjustment{}, &ValidationError{Field: "unit_cost_cents", Message: "must be >= 0"}
	}
	if in.QuantityDelta < 0 && in.UnitCostCents != 0 {
		return Adjustment{}, &ValidationError{Field: "unit_cost_cents", Message: "is only valid for a positive adjustment"}
	}
	if in.QuantityDelta > 0 && in.UnitCostCents > 0 && in.QuantityDelta > math.MaxInt64/in.UnitCostCents {
		return Adjustment{}, &ValidationError{Field: "quantity_delta", Message: "value overflow"}
	}

	if err := s.ensureMasterData(ctx, tx, tenant, []string{in.LocationID}, in.SKU); err != nil {
		return Adjustment{}, err
	}

	id, err := newID()
	if err != nil {
		return Adjustment{}, err
	}

	var (
		position Position
		value    int64
		costUses []layerUse
	)
	if in.QuantityDelta > 0 {
		value = in.QuantityDelta * in.UnitCostCents
		if err := tx.QueryRow(ctx,
			`INSERT INTO positions (tenant_id, location_id, sku, on_hand, reserved, inventory_value_cents)
			 VALUES ($1, $2, $3, $4, 0, $5)
			 ON CONFLICT (tenant_id, location_id, sku) DO UPDATE
			 SET on_hand = positions.on_hand + EXCLUDED.on_hand,
				 inventory_value_cents = positions.inventory_value_cents + EXCLUDED.inventory_value_cents,
				 version = positions.version + 1,
				 updated_at = now()
			 RETURNING location_id, sku, on_hand, reserved, inventory_value_cents, version, updated_at`,
			tenant, in.LocationID, in.SKU, in.QuantityDelta, value,
		).Scan(&position.LocationID, &position.SKU, &position.OnHand, &position.Reserved,
			&position.InventoryValueCents, &position.Version, &position.UpdatedAt); err != nil {
			return Adjustment{}, err
		}
		if err := s.createCostLayer(ctx, tx, tenant, in.LocationID, in.SKU, in.QuantityDelta, value, s.clock(), "adjustment", id); err != nil {
			return Adjustment{}, err
		}
	} else {
		locked, err := s.lockPosition(ctx, tx, tenant, in.LocationID, in.SKU)
		if err != nil {
			return Adjustment{}, err
		}
		if locked.Available < quantity {
			return Adjustment{}, fmt.Errorf("%w: available=%d requested=%d", ErrInsufficientStock, locked.Available, quantity)
		}

		costUses, value, err = s.consumeFIFO(ctx, tx, tenant, in.LocationID, in.SKU, quantity)
		if err != nil {
			return Adjustment{}, err
		}
		if err := s.recordConsumptions(ctx, tx, tenant, in.LocationID, in.SKU, costUses, "adjustment", id); err != nil {
			return Adjustment{}, err
		}

		if err := tx.QueryRow(ctx,
			`UPDATE positions
			 SET on_hand = on_hand - $4,
				 inventory_value_cents = inventory_value_cents - $5,
				 version = version + 1,
				 updated_at = now()
			 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3
			 RETURNING location_id, sku, on_hand, reserved, inventory_value_cents, version, updated_at`,
			tenant, in.LocationID, in.SKU, quantity, value,
		).Scan(&position.LocationID, &position.SKU, &position.OnHand, &position.Reserved,
			&position.InventoryValueCents, &position.Version, &position.UpdatedAt); err != nil {
			return Adjustment{}, err
		}
	}
	position.Available = position.OnHand - position.Reserved

	signedValue := value
	if in.QuantityDelta < 0 {
		signedValue = -value
	}

	var createdAt = position.UpdatedAt
	if err := tx.QueryRow(ctx,
		`INSERT INTO adjustments
		 (tenant_id, id, location_id, sku, quantity_delta, value_delta_cents, reason, actor)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING created_at`,
		tenant, id, in.LocationID, in.SKU, in.QuantityDelta, signedValue, reason, actor,
	).Scan(&createdAt); err != nil {
		return Adjustment{}, err
	}

	movementType := "adjustment_in"
	debitAccount, creditAccount := "inventory", "inventory_adjustment"
	if in.QuantityDelta < 0 {
		movementType = "adjustment_out"
		debitAccount, creditAccount = "inventory_adjustment", "inventory"
	}

	if err := s.recordMovement(ctx, tx, tenant, in.LocationID, in.SKU, movementType,
		in.QuantityDelta, signedValue, "adjustment", id); err != nil {
		return Adjustment{}, err
	}
	if err := s.postLedger(ctx, tx, tenant, in.LocationID, in.SKU, "adjustment", id,
		debitAccount, creditAccount, value); err != nil {
		return Adjustment{}, err
	}
	if err := s.emit(ctx, tx, tenant, "adjustment", id, "inventory.adjusted", actor, map[string]any{
		"adjustment_id":     id,
		"location_id":       in.LocationID,
		"sku":               in.SKU,
		"quantity_delta":    in.QuantityDelta,
		"value_delta_cents": signedValue,
		"reason":            reason,
		"on_hand":           position.OnHand,
		"inventory_value":   position.InventoryValueCents,
		"cost_layers":       costLayerPayload(costUses),
	}); err != nil {
		return Adjustment{}, err
	}

	return Adjustment{
		ID:              id,
		LocationID:      in.LocationID,
		SKU:             in.SKU,
		QuantityDelta:   in.QuantityDelta,
		ValueDeltaCents: signedValue,
		Reason:          reason,
		Actor:           actor,
		CreatedAt:       createdAt,
		Position:        position,
	}, nil
}
