-- Runs once, on first init of an empty postgres_data volume.
-- The control-plane DB (tenants/identities/invites) needs to exist before
-- the backend applies its migrations on startup. The "postgres" DB (set via
-- POSTGRES_DB below) is used as the admin DSN target for CREATE DATABASE
-- when provisioning per-tenant databases.
CREATE DATABASE stonesuite_cp;
