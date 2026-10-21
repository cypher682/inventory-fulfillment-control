package inventory

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
)

type layerUse struct {
	LayerID    int64
	Quantity   int64
	ValueCents int64
	ReceivedAt time.Time
}

func (s *Service) createCostLayer(
	ctx context.Context,
	tx pgx.Tx,
	tenant, locationID, sku string,
	quantity, valueCents int64,
	receivedAt time.Time,
	sourceType, sourceID string,
) error {
	if quantity <= 0 {
		return fmt.Errorf("cost layer quantity must be positive, got %d", quantity)
	}
	if valueCents < 0 {
		return fmt.Errorf("cost layer value must be non-negative, got %d", valueCents)
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO cost_layers
		 (tenant_id, location_id, sku, original_quantity, remaining_quantity,
		  original_value_cents, remaining_value_cents, received_at, source_type, source_id)
		 VALUES ($1, $2, $3, $4, $4, $5, $5, $6, $7, $8)`,
		tenant, locationID, sku, quantity, valueCents, receivedAt, sourceType, sourceID,
	)
	return err
}

// consumeFIFO takes quantity from the oldest remaining layers and returns the
// exact value consumed plus a per-layer trace. Callers must hold the position
// row lock; this serializes layer consumption for a (tenant, location, sku).
func (s *Service) consumeFIFO(
	ctx context.Context,
	tx pgx.Tx,
	tenant, locationID, sku string,
	quantity int64,
) ([]layerUse, int64, error) {
	if quantity <= 0 {
		return nil, 0, fmt.Errorf("consume quantity must be positive, got %d", quantity)
	}

	rows, err := tx.Query(ctx,
		`SELECT id, remaining_quantity, remaining_value_cents, received_at
		 FROM cost_layers
		 WHERE tenant_id = $1 AND location_id = $2 AND sku = $3
		   AND remaining_quantity > 0
		 ORDER BY received_at, id
		 FOR UPDATE`,
		tenant, locationID, sku,
	)
	if err != nil {
		return nil, 0, err
	}

	type layer struct {
		id         int64
		quantity   int64
		valueCents int64
		receivedAt time.Time
	}
	var layers []layer
	for rows.Next() {
		var l layer
		if err := rows.Scan(&l.id, &l.quantity, &l.valueCents, &l.receivedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		layers = append(layers, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	remaining := quantity
	uses := make([]layerUse, 0, len(layers))
	var totalValue int64
	for _, l := range layers {
		if remaining == 0 {
			break
		}
		take := min(l.quantity, remaining)
		takeValue := takeLayerValue(l.valueCents, l.quantity, take)
		if _, err := tx.Exec(ctx,
			`UPDATE cost_layers
			 SET remaining_quantity = remaining_quantity - $2,
			     remaining_value_cents = remaining_value_cents - $3
			 WHERE id = $1`,
			l.id, take, takeValue,
		); err != nil {
			return nil, 0, err
		}
		uses = append(uses, layerUse{
			LayerID:    l.id,
			Quantity:   take,
			ValueCents: takeValue,
			ReceivedAt: l.receivedAt,
		})
		totalValue += takeValue
		remaining -= take
	}
	if remaining != 0 {
		return nil, 0, fmt.Errorf(
			"inconsistent cost layers for %s/%s: missing %d units", locationID, sku, remaining)
	}
	return uses, totalValue, nil
}

// takeLayerValue uses floor division and takes the whole remaining value when
// the layer is fully consumed, so rounding never creates or destroys cents.
func takeLayerValue(remainingValue, remainingQuantity, take int64) int64 {
	if take >= remainingQuantity {
		return remainingValue
	}
	var value, quantity, units, product, result big.Int
	value.SetInt64(remainingValue)
	quantity.SetInt64(remainingQuantity)
	units.SetInt64(take)
	product.Mul(&value, &units)
	result.Quo(&product, &quantity)
	return result.Int64()
}

func (s *Service) recordConsumptions(
	ctx context.Context,
	tx pgx.Tx,
	tenant, locationID, sku string,
	uses []layerUse,
	refType, refID string,
) error {
	for _, use := range uses {
		if _, err := tx.Exec(ctx,
			`INSERT INTO cost_consumptions
			 (tenant_id, location_id, sku, layer_id, quantity, value_cents, ref_type, ref_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			tenant, locationID, sku, use.LayerID, use.Quantity, use.ValueCents, refType, refID,
		); err != nil {
			return err
		}
	}
	return nil
}

func costLayerPayload(uses []layerUse) []map[string]any {
	out := make([]map[string]any, 0, len(uses))
	for _, use := range uses {
		out = append(out, map[string]any{
			"layer_id":    use.LayerID,
			"quantity":    use.Quantity,
			"value_cents": use.ValueCents,
		})
	}
	return out
}
