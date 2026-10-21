package inventory

import (
	"context"
	"fmt"
)

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

func (s *Service) ListEvents(ctx context.Context, tenant string, f EventFilter) (EventPage, error) {
	if err := validateID("tenant", tenant); err != nil {
		return EventPage{}, err
	}
	if err := validateAuditFilter(f.AggregateType, f.AggregateID, f.EventType); err != nil {
		return EventPage{}, err
	}
	limit := auditLimit(f.Limit)
	args := []any{tenant, auditAfter(f.After)}
	where := "tenant_id = $1 AND id > $2"
	addFilter(&where, &args, "aggregate_type", f.AggregateType)
	addFilter(&where, &args, "aggregate_id", f.AggregateID)
	addFilter(&where, &args, "event_type", f.EventType)
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx,
		`SELECT id, aggregate_type, aggregate_id, event_type, payload, actor, occurred_at
		 FROM inventory_events
		 WHERE `+where+`
		 ORDER BY id
		 LIMIT $`+fmt.Sprint(len(args)),
		args...,
	)
	if err != nil {
		return EventPage{}, err
	}
	defer rows.Close()

	events := make([]Event, 0, limit+1)
	for rows.Next() {
		var e Event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.AggregateType, &e.AggregateID, &e.EventType,
			&payload, &e.Actor, &e.OccurredAt); err != nil {
			return EventPage{}, err
		}
		e.Payload = payload
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, err
	}

	page := EventPage{Events: events}
	if len(events) > limit {
		page.HasMore = true
		page.NextAfter = events[limit-1].ID
		page.Events = events[:limit]
	}
	s.metrics.auditReads.Add(1)
	return page, nil
}

func (s *Service) ListMovements(ctx context.Context, tenant string, f MovementFilter) (MovementPage, error) {
	if err := validateID("tenant", tenant); err != nil {
		return MovementPage{}, err
	}
	if err := validateAuditFilter(f.LocationID, f.SKU, f.MovementType); err != nil {
		return MovementPage{}, err
	}
	limit := auditLimit(f.Limit)
	args := []any{tenant, auditAfter(f.After)}
	where := "tenant_id = $1 AND id > $2"
	addFilter(&where, &args, "location_id", f.LocationID)
	addFilter(&where, &args, "sku", f.SKU)
	addFilter(&where, &args, "movement_type", f.MovementType)
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx,
		`SELECT id, location_id, sku, movement_type, quantity_delta, value_delta_cents,
		        ref_type, ref_id, created_at
		 FROM inventory_movements
		 WHERE `+where+`
		 ORDER BY id
		 LIMIT $`+fmt.Sprint(len(args)),
		args...,
	)
	if err != nil {
		return MovementPage{}, err
	}
	defer rows.Close()

	movements := make([]Movement, 0, limit+1)
	for rows.Next() {
		var m Movement
		if err := rows.Scan(&m.ID, &m.LocationID, &m.SKU, &m.MovementType,
			&m.QuantityDelta, &m.ValueDelta, &m.RefType, &m.RefID, &m.CreatedAt); err != nil {
			return MovementPage{}, err
		}
		movements = append(movements, m)
	}
	if err := rows.Err(); err != nil {
		return MovementPage{}, err
	}

	page := MovementPage{Movements: movements}
	if len(movements) > limit {
		page.HasMore = true
		page.NextAfter = movements[limit-1].ID
		page.Movements = movements[:limit]
	}
	s.metrics.auditReads.Add(1)
	return page, nil
}

func (s *Service) ListLedger(ctx context.Context, tenant string, f LedgerFilter) (LedgerPage, error) {
	if err := validateID("tenant", tenant); err != nil {
		return LedgerPage{}, err
	}
	if err := validateAuditFilter(f.Account, f.LocationID, f.SKU); err != nil {
		return LedgerPage{}, err
	}
	if err := validateAuditFilter(f.RefType, f.RefID, ""); err != nil {
		return LedgerPage{}, err
	}
	limit := auditLimit(f.Limit)
	args := []any{tenant, auditAfter(f.After)}
	where := "tenant_id = $1 AND id > $2"
	addFilter(&where, &args, "account", f.Account)
	addFilter(&where, &args, "location_id", f.LocationID)
	addFilter(&where, &args, "sku", f.SKU)
	addFilter(&where, &args, "ref_type", f.RefType)
	addFilter(&where, &args, "ref_id", f.RefID)
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx,
		`SELECT id, entry_group, account, location_id, sku, debit_cents, credit_cents,
		        ref_type, ref_id, created_at
		 FROM ledger_entries
		 WHERE `+where+`
		 ORDER BY id
		 LIMIT $`+fmt.Sprint(len(args)),
		args...,
	)
	if err != nil {
		return LedgerPage{}, err
	}
	defer rows.Close()

	entries := make([]LedgerEntry, 0, limit+1)
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.EntryGroup, &e.Account, &e.LocationID, &e.SKU,
			&e.DebitCents, &e.CreditCents, &e.RefType, &e.RefID, &e.CreatedAt); err != nil {
			return LedgerPage{}, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return LedgerPage{}, err
	}

	page := LedgerPage{Entries: entries}
	if len(entries) > limit {
		page.HasMore = true
		page.NextAfter = entries[limit-1].ID
		page.Entries = entries[:limit]
	}
	s.metrics.auditReads.Add(1)
	return page, nil
}

func (s *Service) ListCostLayers(ctx context.Context, tenant string, f CostLayerFilter) (CostLayerPage, error) {
	if err := validateID("tenant", tenant); err != nil {
		return CostLayerPage{}, err
	}
	if err := validateAuditFilter(f.LocationID, f.SKU); err != nil {
		return CostLayerPage{}, err
	}
	limit := auditLimit(f.Limit)
	args := []any{tenant, auditAfter(f.After)}
	where := "tenant_id = $1 AND id > $2"
	addFilter(&where, &args, "location_id", f.LocationID)
	addFilter(&where, &args, "sku", f.SKU)
	if !f.IncludeEmpty {
		where += " AND remaining_quantity > 0"
	}
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx,
		`SELECT id, location_id, sku, original_quantity, remaining_quantity,
		        original_value_cents, remaining_value_cents, received_at, source_type, source_id
		 FROM cost_layers
		 WHERE `+where+`
		 ORDER BY id
		 LIMIT $`+fmt.Sprint(len(args)),
		args...,
	)
	if err != nil {
		return CostLayerPage{}, err
	}
	defer rows.Close()

	layers := make([]CostLayer, 0, limit+1)
	for rows.Next() {
		var l CostLayer
		if err := rows.Scan(&l.ID, &l.LocationID, &l.SKU, &l.OriginalQuantity, &l.RemainingQuantity,
			&l.OriginalValueCents, &l.RemainingValueCents, &l.ReceivedAt, &l.SourceType, &l.SourceID); err != nil {
			return CostLayerPage{}, err
		}
		layers = append(layers, l)
	}
	if err := rows.Err(); err != nil {
		return CostLayerPage{}, err
	}

	page := CostLayerPage{Layers: layers}
	if len(layers) > limit {
		page.HasMore = true
		page.NextAfter = layers[limit-1].ID
		page.Layers = layers[:limit]
	}
	s.metrics.auditReads.Add(1)
	return page, nil
}

func (s *Service) ListCostConsumptions(ctx context.Context, tenant string, f CostConsumptionFilter) (CostConsumptionPage, error) {
	if err := validateID("tenant", tenant); err != nil {
		return CostConsumptionPage{}, err
	}
	if f.LayerID < 0 {
		return CostConsumptionPage{}, &ValidationError{Field: "layer_id", Message: "must be positive"}
	}
	if err := validateAuditFilter(f.LocationID, f.SKU, f.RefType, f.RefID); err != nil {
		return CostConsumptionPage{}, err
	}
	limit := auditLimit(f.Limit)
	args := []any{tenant, auditAfter(f.After)}
	where := "tenant_id = $1 AND id > $2"
	addFilter(&where, &args, "location_id", f.LocationID)
	addFilter(&where, &args, "sku", f.SKU)
	addFilter(&where, &args, "ref_type", f.RefType)
	addFilter(&where, &args, "ref_id", f.RefID)
	if f.LayerID > 0 {
		args = append(args, f.LayerID)
		where += fmt.Sprintf(" AND layer_id = $%d", len(args))
	}
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx,
		`SELECT id, layer_id, location_id, sku, quantity, value_cents, ref_type, ref_id, created_at
		 FROM cost_consumptions
		 WHERE `+where+`
		 ORDER BY id
		 LIMIT $`+fmt.Sprint(len(args)),
		args...,
	)
	if err != nil {
		return CostConsumptionPage{}, err
	}
	defer rows.Close()

	consumptions := make([]CostConsumption, 0, limit+1)
	for rows.Next() {
		var c CostConsumption
		if err := rows.Scan(&c.ID, &c.LayerID, &c.LocationID, &c.SKU, &c.Quantity,
			&c.ValueCents, &c.RefType, &c.RefID, &c.CreatedAt); err != nil {
			return CostConsumptionPage{}, err
		}
		consumptions = append(consumptions, c)
	}
	if err := rows.Err(); err != nil {
		return CostConsumptionPage{}, err
	}

	page := CostConsumptionPage{Consumptions: consumptions}
	if len(consumptions) > limit {
		page.HasMore = true
		page.NextAfter = consumptions[limit-1].ID
		page.Consumptions = consumptions[:limit]
	}
	s.metrics.auditReads.Add(1)
	return page, nil
}

func addFilter(where *string, args *[]any, column, value string) {
	if value == "" {
		return
	}
	*args = append(*args, value)
	*where += fmt.Sprintf(" AND %s = $%d", column, len(*args))
}

func auditLimit(limit int) int {
	if limit <= 0 {
		return defaultAuditLimit
	}
	if limit > maxAuditLimit {
		return maxAuditLimit
	}
	return limit
}

func auditAfter(after int64) int64 {
	if after < 0 {
		return 0
	}
	return after
}

func validateAuditFilter(values ...string) error {
	for _, value := range values {
		if len(value) > 64 {
			return &ValidationError{Field: "filter", Message: "must be at most 64 characters"}
		}
	}
	return nil
}
