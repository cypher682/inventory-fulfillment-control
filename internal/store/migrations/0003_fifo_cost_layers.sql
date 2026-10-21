CREATE TABLE cost_layers (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    original_quantity bigint NOT NULL CHECK (original_quantity > 0),
    remaining_quantity bigint NOT NULL
        CHECK (remaining_quantity >= 0 AND remaining_quantity <= original_quantity),
    original_value_cents bigint NOT NULL CHECK (original_value_cents >= 0),
    remaining_value_cents bigint NOT NULL
        CHECK (remaining_value_cents >= 0 AND remaining_value_cents <= original_value_cents),
    received_at timestamptz NOT NULL,
    source_type text NOT NULL,
    source_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, location_id, sku)
        REFERENCES positions (tenant_id, location_id, sku),
    CHECK (remaining_quantity > 0 OR remaining_value_cents = 0)
);

CREATE INDEX cost_layers_fifo_idx
    ON cost_layers (tenant_id, location_id, sku, received_at, id)
    WHERE remaining_quantity > 0;

CREATE INDEX cost_layers_source_idx
    ON cost_layers (tenant_id, source_type, source_id);

CREATE TABLE transfer_layers (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    transfer_id text NOT NULL,
    sku text NOT NULL,
    quantity bigint NOT NULL CHECK (quantity > 0),
    value_cents bigint NOT NULL CHECK (value_cents >= 0),
    received_at timestamptz NOT NULL,
    source_layer_id bigint REFERENCES cost_layers (id),
    settled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, transfer_id)
        REFERENCES transfers (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX transfer_layers_pending_idx
    ON transfer_layers (tenant_id, transfer_id)
    WHERE settled_at IS NULL;

CREATE TABLE cost_consumptions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    layer_id bigint NOT NULL REFERENCES cost_layers (id),
    quantity bigint NOT NULL CHECK (quantity > 0),
    value_cents bigint NOT NULL CHECK (value_cents >= 0),
    ref_type text NOT NULL,
    ref_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX cost_consumptions_ref_idx
    ON cost_consumptions (tenant_id, ref_type, ref_id);

CREATE INDEX cost_consumptions_layer_idx
    ON cost_consumptions (tenant_id, layer_id);

INSERT INTO cost_layers
    (tenant_id, location_id, sku, original_quantity, remaining_quantity,
     original_value_cents, remaining_value_cents, received_at, source_type, source_id)
SELECT tenant_id, location_id, sku, on_hand, on_hand,
       inventory_value_cents, inventory_value_cents, updated_at, 'migration', '0003'
FROM positions
WHERE on_hand > 0;

INSERT INTO transfer_layers
    (tenant_id, transfer_id, sku, quantity, value_cents, received_at)
SELECT tenant_id, id, sku, quantity, value_cents, created_at
FROM transfers
WHERE status = 'in_transit';
