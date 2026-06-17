-- =====================================================================
-- Widen tenants.db_connection_ref to TEXT.
--
-- The column previously was VARCHAR(255), which is too small for an
-- encrypted DSN: base64(AES-256-GCM(nonce || dsn || tag)) of a typical
-- ~120-char Neon connection string lands well past 255 chars, causing
-- "value too long for type character varying(255)" on provisioning.
-- TEXT removes the bound with no practical downside.
-- =====================================================================

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'tenants'
      AND column_name = 'db_connection_ref'
      AND data_type != 'text'
  ) THEN
    ALTER TABLE tenants ALTER COLUMN db_connection_ref TYPE TEXT;
  END IF;
END $$;
