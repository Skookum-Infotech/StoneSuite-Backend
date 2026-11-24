-- LOCAL DEV ONLY. Never run against a shared, staging or production database.
--
-- Seeds two ready-to-use login users in the "admin" tenant of the docker-compose
-- dev database, skipping the invite / email-verification / MFA steps, so the
-- Employee approval flow (and Expense claims) can be tested end to end:
--
--   approver@stonesuite.test    "Test Approver"   role: test_employee_approver
--   requester@stonesuite.test   "Test Requester"  role: test_employee_requester
--   password for both:          Test@12345
--
-- Each user gets: a control-plane identity (email already verified, MFA off,
-- not locked), a tenant users row, an Active employee row linked to it, and a
-- small role. Safe to re-run: it resets both users' password, lock and role
-- grants back to the values below.
--
-- Run (from the repo root, with the compose Postgres up):
--   docker cp scripts/dev-seed-test-users.sql stonesuite-postgres:/tmp/seed.sql
--   docker exec -e PGPASSWORD=stonesuite_secret stonesuite-postgres \
--     psql -U stonesuite -v ON_ERROR_STOP=1 -d stonesuite_cp -f /tmp/seed.sql
--
-- Then in the app (signed in as admin): Configure > Workflows > Employee > turn the
-- approval gate on and pick "Test Approver". Sign in as the requester, add an
-- employee (it waits as Pending Approval), then sign in as the approver and
-- approve or reject it.

\set ON_ERROR_STOP on

-- 1. Control plane: identities (login credentials). bcrypt cost 10, like the app.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

INSERT INTO identities (tenant_id, email, password_hash, full_name, email_verified,
                        email_verification_code, failed_login_attempts, is_locked, locked_until)
SELECT t.id, v.email, crypt('Test@12345', gen_salt('bf', 10)), v.full_name, TRUE, NULL, 0, FALSE, NULL
FROM tenants t
CROSS JOIN (VALUES
    ('approver@stonesuite.test',  'Test Approver'),
    ('requester@stonesuite.test', 'Test Requester')
) AS v(email, full_name)
WHERE t.slug = 'admin'
ON CONFLICT (email) DO UPDATE
SET password_hash = EXCLUDED.password_hash, full_name = EXCLUDED.full_name,
    email_verified = TRUE, email_verification_code = NULL,
    failed_login_attempts = 0, is_locked = FALSE, locked_until = NULL,
    mfa_enabled = FALSE, updated_at = NOW();

-- Hand the identity ids to the tenant database below (psql variables).
SELECT id AS approver_identity  FROM identities WHERE email = 'approver@stonesuite.test'  \gset
SELECT id AS requester_identity FROM identities WHERE email = 'requester@stonesuite.test' \gset

-- 2. Tenant database: users, employees, roles.
\c tenant_admin

INSERT INTO users (identity_id, email, full_name, status) VALUES
    (:'approver_identity',  'approver@stonesuite.test',  'Test Approver',  'active'),
    (:'requester_identity', 'requester@stonesuite.test', 'Test Requester', 'active')
ON CONFLICT (identity_id) DO UPDATE
SET email = EXCLUDED.email, full_name = EXCLUDED.full_name, status = 'active', updated_at = NOW();

-- Active employee rows linked to the users (the app makes these on signup).
INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email,
                      employee_is_active, employee_status)
SELECT u.id, split_part(u.full_name, ' ', 1), split_part(u.full_name, ' ', 2), u.email, TRUE,
       (SELECT rs.record_status_id FROM lkp_record_status rs
        JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
        WHERE rt.record_type_code = 'EMPL' AND rs.record_status_code = 'ACTV')
FROM users u
WHERE u.email IN ('approver@stonesuite.test', 'requester@stonesuite.test')
  AND NOT EXISTS (SELECT 1 FROM employee e WHERE e.employee_user_id = u.id);

UPDATE employee
SET employee_number = 'EMPL-' || LPAD(employee_id::text, 6, '0')
WHERE employee_number IS NULL AND NOT employee_is_system;

-- Two small roles. The approver can see and sign off employees (and read what the
-- approval links to); the requester can add and manage employees and file expenses.
INSERT INTO roles (key, name, description) VALUES
    ('test_employee_approver',  'Test Employee Approver',  'Dev seed: approves new employees.'),
    ('test_employee_requester', 'Test Employee Requester', 'Dev seed: adds employees and files expenses.')
ON CONFLICT (lower(key)) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description;

-- Reset both roles' grants so a re-run always lands on exactly this set.
DELETE FROM role_permissions
WHERE role_id IN (SELECT id FROM roles WHERE key IN ('test_employee_approver', 'test_employee_requester'));

INSERT INTO role_permissions (role_id, resource, action, scope)
SELECT r.id, g.resource, g.action, 'all'
FROM (VALUES
    ('test_employee_approver', 'employee',    'read'),
    ('test_employee_approver', 'employee',    'approve'),
    ('test_employee_approver', 'expense',     'read'),
    ('test_employee_approver', 'expense',     'transition'),
    ('test_employee_approver', 'vendor',      'read'),
    ('test_employee_approver', 'vendor_bill', 'read'),
    ('test_employee_requester', 'employee',    'read'),
    ('test_employee_requester', 'employee',    'create'),
    ('test_employee_requester', 'employee',    'update'),
    ('test_employee_requester', 'employee',    'transition'),
    ('test_employee_requester', 'expense',     'read'),
    ('test_employee_requester', 'expense',     'create'),
    ('test_employee_requester', 'expense',     'update'),
    ('test_employee_requester', 'expense',     'transition'),
    ('test_employee_requester', 'vendor',      'read'),
    ('test_employee_requester', 'vendor_bill', 'read')
) AS g(role_key, resource, action)
JOIN roles r ON r.key = g.role_key;

DELETE FROM user_roles
WHERE user_id IN (SELECT id FROM users WHERE email IN ('approver@stonesuite.test', 'requester@stonesuite.test'));

INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
FROM users u
JOIN roles r ON r.key = CASE u.email WHEN 'approver@stonesuite.test'  THEN 'test_employee_approver'
                                     WHEN 'requester@stonesuite.test' THEN 'test_employee_requester' END
WHERE u.email IN ('approver@stonesuite.test', 'requester@stonesuite.test');

-- Show what was loaded.
SELECT u.email, u.status, e.employee_number, r.key AS role
FROM users u
JOIN employee e ON e.employee_user_id = u.id
JOIN user_roles ur ON ur.user_id = u.id
JOIN roles r ON r.id = ur.role_id
WHERE u.email IN ('approver@stonesuite.test', 'requester@stonesuite.test')
ORDER BY u.email;
