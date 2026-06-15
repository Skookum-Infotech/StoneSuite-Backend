-- =====================================================================
-- Tenant migration 012: employee table (v2 relational design).
--
-- The relational lkp_* and crm_record tables reference employee(employee_id)
-- for audit columns and ownership. employee_user_id links back to the
-- existing UUID users table (and through it to the control-plane identity).
-- A system employee with employee_id = 1 is seeded for system/seed rows.
-- =====================================================================

CREATE TABLE IF NOT EXISTS employee (
    employee_id             SERIAL       PRIMARY KEY,
    employee_user_id        UUID             NULL REFERENCES users(id),
    employee_first_name     VARCHAR(100) NOT NULL DEFAULT '',
    employee_last_name      VARCHAR(100) NOT NULL DEFAULT '',
    employee_email          VARCHAR(255) NOT NULL,
    -- Audit
    employee_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    employee_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    employee_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    employee_created_by     INTEGER          NULL,
    employee_deleted_at     TIMESTAMP        NULL,
    employee_deleted_by     INTEGER          NULL,
    employee_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_employee_email UNIQUE (employee_email),
    CONSTRAINT chk_employee_soft_delete CHECK (
        (employee_deleted_at IS NULL AND employee_deleted_by IS NULL) OR
        (employee_deleted_at IS NOT NULL AND employee_deleted_by IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_employee_user ON employee(employee_user_id)
    WHERE employee_user_id IS NOT NULL;

-- Seed the system employee at id = 1 (used as created_by for lkp seed rows).
INSERT INTO employee (employee_id, employee_first_name, employee_last_name, employee_email, employee_is_system)
VALUES (1, 'System', 'User', 'system@stonesuite.local', TRUE)
ON CONFLICT (employee_id) DO NOTHING;

-- Keep the SERIAL sequence ahead of the explicit id we just inserted.
SELECT setval(
    pg_get_serial_sequence('employee', 'employee_id'),
    GREATEST((SELECT MAX(employee_id) FROM employee), 1)
);
