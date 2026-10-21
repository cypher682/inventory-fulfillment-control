ALTER TABLE reservations DROP CONSTRAINT IF EXISTS reservations_status_check;
ALTER TABLE reservations
    ADD CONSTRAINT reservations_status_check
    CHECK (status IN ('active', 'confirmed', 'released', 'expired'));

CREATE INDEX reservations_expiry_idx
    ON reservations (expires_at, tenant_id)
    WHERE status = 'active';

ALTER TABLE inventory_movements
    DROP CONSTRAINT IF EXISTS inventory_movements_movement_type_check;
ALTER TABLE inventory_movements
    ADD CONSTRAINT inventory_movements_movement_type_check
    CHECK (movement_type IN ('receive', 'ship', 'transfer_out', 'transfer_in', 'adjustment_in', 'adjustment_out'));

CREATE TABLE adjustments (
    tenant_id text NOT NULL,
    id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    quantity_delta bigint NOT NULL CHECK (quantity_delta <> 0),
    value_delta_cents bigint NOT NULL,
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 200),
    actor text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, location_id, sku)
        REFERENCES positions (tenant_id, location_id, sku)
);

CREATE INDEX adjustments_created_idx
    ON adjustments (tenant_id, created_at, id);

ALTER TABLE outbox ADD COLUMN attempts integer NOT NULL DEFAULT 0;
ALTER TABLE outbox ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE outbox ADD COLUMN last_error text;

DROP INDEX IF EXISTS outbox_unpublished_idx;
CREATE INDEX outbox_ready_idx
    ON outbox (next_attempt_at, id)
    WHERE published_at IS NULL;
