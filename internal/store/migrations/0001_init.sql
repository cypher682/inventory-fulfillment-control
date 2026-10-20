CREATE TABLE tenants (
    id text PRIMARY KEY,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE locations (
    tenant_id text NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    id text NOT NULL,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE items (
    tenant_id text NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    sku text NOT NULL,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, sku)
);

CREATE TABLE positions (
    tenant_id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    on_hand bigint NOT NULL DEFAULT 0 CHECK (on_hand >= 0),
    reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    inventory_value_cents bigint NOT NULL DEFAULT 0 CHECK (inventory_value_cents >= 0),
    version bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, location_id, sku),
    FOREIGN KEY (tenant_id, location_id) REFERENCES locations (tenant_id, id),
    FOREIGN KEY (tenant_id, sku) REFERENCES items (tenant_id, sku),
    CHECK (reserved <= on_hand)
);

CREATE TABLE reservations (
    tenant_id text NOT NULL,
    id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    quantity bigint NOT NULL CHECK (quantity > 0),
    status text NOT NULL CHECK (status IN ('active', 'confirmed', 'released')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, location_id, sku)
        REFERENCES positions (tenant_id, location_id, sku)
);

CREATE INDEX reservations_active_idx
    ON reservations (tenant_id, location_id, sku, status);

CREATE TABLE transfers (
    tenant_id text NOT NULL,
    id text NOT NULL,
    from_location_id text NOT NULL,
    to_location_id text NOT NULL,
    sku text NOT NULL,
    quantity bigint NOT NULL CHECK (quantity > 0),
    value_cents bigint NOT NULL CHECK (value_cents >= 0),
    status text NOT NULL CHECK (status IN ('in_transit', 'received')),
    created_at timestamptz NOT NULL DEFAULT now(),
    received_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, from_location_id) REFERENCES locations (tenant_id, id),
    FOREIGN KEY (tenant_id, to_location_id) REFERENCES locations (tenant_id, id),
    FOREIGN KEY (tenant_id, sku) REFERENCES items (tenant_id, sku),
    CHECK (from_location_id <> to_location_id)
);

CREATE INDEX transfers_incoming_idx
    ON transfers (tenant_id, to_location_id, sku, status);

CREATE TABLE inventory_movements (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    movement_type text NOT NULL CHECK (movement_type IN ('receive', 'ship', 'transfer_out', 'transfer_in')),
    quantity_delta bigint NOT NULL,
    value_delta_cents bigint NOT NULL,
    ref_type text NOT NULL,
    ref_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inventory_movements_position_idx
    ON inventory_movements (tenant_id, location_id, sku);

CREATE TABLE ledger_entries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    entry_group text NOT NULL,
    account text NOT NULL,
    location_id text NOT NULL,
    sku text NOT NULL,
    debit_cents bigint NOT NULL DEFAULT 0 CHECK (debit_cents >= 0),
    credit_cents bigint NOT NULL DEFAULT 0 CHECK (credit_cents >= 0),
    ref_type text NOT NULL,
    ref_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((debit_cents > 0 AND credit_cents = 0) OR (credit_cents > 0 AND debit_cents = 0))
);

CREATE INDEX ledger_entries_group_idx ON ledger_entries (tenant_id, entry_group);
CREATE INDEX ledger_entries_position_idx ON ledger_entries (tenant_id, account, location_id, sku);

CREATE TABLE inventory_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    actor text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inventory_events_aggregate_idx
    ON inventory_events (tenant_id, aggregate_type, aggregate_id);

CREATE TABLE outbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    topic text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;

CREATE TABLE idempotency_keys (
    tenant_id text NOT NULL,
    key text NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key)
);
