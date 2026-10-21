ALTER TABLE positions
    ADD COLUMN held bigint NOT NULL DEFAULT 0 CHECK (held >= 0);

ALTER TABLE positions
    ADD CONSTRAINT positions_reserved_held_check CHECK (reserved + held <= on_hand);

CREATE TABLE lots (
    tenant_id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    lot_code text NOT NULL,
    expires_at timestamptz,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'hold')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, location_id, sku, lot_code),
    FOREIGN KEY (tenant_id, location_id, sku)
        REFERENCES positions (tenant_id, location_id, sku)
);

CREATE INDEX lots_status_idx
    ON lots (tenant_id, location_id, sku, status);

ALTER TABLE cost_layers
    ADD COLUMN lot_code text;

ALTER TABLE cost_layers
    ADD CONSTRAINT cost_layers_lot_fk
    FOREIGN KEY (tenant_id, location_id, sku, lot_code)
    REFERENCES lots (tenant_id, location_id, sku, lot_code);

CREATE INDEX cost_layers_lot_idx
    ON cost_layers (tenant_id, location_id, sku, lot_code)
    WHERE lot_code IS NOT NULL;

ALTER TABLE transfer_layers
    ADD COLUMN lot_code text;

CREATE TABLE inventory_serials (
    tenant_id text NOT NULL,
    serial text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    lot_code text NOT NULL,
    layer_id bigint NOT NULL REFERENCES cost_layers (id),
    status text NOT NULL DEFAULT 'in_stock'
        CHECK (status IN ('in_stock', 'in_transit', 'shipped', 'adjusted')),
    transfer_id text,
    transfer_layer_id bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, serial),
    FOREIGN KEY (tenant_id, location_id, sku, lot_code)
        REFERENCES lots (tenant_id, location_id, sku, lot_code)
);

CREATE INDEX inventory_serials_layer_idx
    ON inventory_serials (tenant_id, layer_id, status);

CREATE INDEX inventory_serials_transfer_layer_idx
    ON inventory_serials (tenant_id, transfer_layer_id)
    WHERE transfer_layer_id IS NOT NULL;
