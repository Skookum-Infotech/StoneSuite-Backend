-- =====================================================================
-- Tenant migration 018: lkp_price_level — the 12th CRM lookup table.
-- Source: StonSuite_DBSchema.xlsx (Look Up Tables sheet, Price Levels).
-- Created before customer (019), which FKs customer_price_level here.
-- Idempotent: CREATE TABLE IF NOT EXISTS + INSERT ... ON CONFLICT DO NOTHING.
-- =====================================================================

CREATE TABLE IF NOT EXISTS lkp_price_level (
    price_level_id          SERIAL       PRIMARY KEY,
    price_level_name        VARCHAR(50)  NOT NULL,
    price_level_code        VARCHAR(10)  NOT NULL,
    price_level_discount    DECIMAL(5,2) NOT NULL DEFAULT 0,
    price_level_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    price_level_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    price_level_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    price_level_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    price_level_deleted_at  TIMESTAMP        NULL,
    price_level_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    price_level_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_price_level_code UNIQUE (price_level_code)
);

INSERT INTO lkp_price_level (price_level_name, price_level_code, price_level_discount, price_level_is_active, price_level_is_system, price_level_created_by) VALUES
    ('Base Price',     'PL0',  0.00,  TRUE, TRUE, 1),
    ('Price Level 1',  'PL1',  5.00,  TRUE, TRUE, 1),
    ('Price Level 2',  'PL2',  10.00, TRUE, TRUE, 1),
    ('Price Level 3',  'PL3',  15.00, TRUE, TRUE, 1),
    ('Price Level 4',  'PL4',  20.00, TRUE, TRUE, 1),
    ('Wholesale',      'PLWS', 25.00, TRUE, TRUE, 1)
ON CONFLICT (price_level_code) DO NOTHING;
