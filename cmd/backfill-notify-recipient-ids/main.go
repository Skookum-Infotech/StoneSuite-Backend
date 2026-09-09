// Command backfill-notify-recipient-ids repairs notification rows in the
// stonesuite-notify database that were written keyed by a tenant-local
// users.id instead of the control-plane identity id.
//
// stonesuite-notify scopes every bell/feed query by the JWT's "id" claim,
// which is the control-plane identities.id (see stonesuite-notify
// middleware/auth.go and generateTenantJWT). Before the fix in
// approvalchain/notify.go and controllers/documents.go, the backend resolved
// recipients to users.id and sent that — so those rows exist but no user's
// bell can ever match them. This one-off tool translates
// recipient_user_id (and the same column on notification_deliveries, plus
// user_notification_preferences.user_id) from users.id to users.identity_id,
// per tenant, using the id mapping that lives in each tenant database.
//
// Usage:
//
//	go run ./cmd/backfill-notify-recipient-ids \
//	  --notify-db-url "postgres://…"      # stonesuite-notify's DATABASE_URL
//	  [--tenant slug] [--tenant slug] …   # default: every provisioned tenant
//	  [--dry-run]                         # report counts, change nothing
//
// Reads CONTROL_PLANE_DB_URL and (if set) SECRET_ENCRYPTION_KEY from the
// environment / .env file, same as the server. Idempotent: a second run
// matches nothing, since no users.id equals any identities.id.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/config"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

type tenantFlags []string

func (t *tenantFlags) String() string { return fmt.Sprint([]string(*t)) }
func (t *tenantFlags) Set(v string) error {
	*t = append(*t, v)
	return nil
}

func main() {
	var (
		notifyDBURL string
		tenants     tenantFlags
		dryRun      bool
	)
	flag.StringVar(&notifyDBURL, "notify-db-url", "", "stonesuite-notify DATABASE_URL (required)")
	flag.Var(&tenants, "tenant", "tenant slug to backfill; repeatable; default is every provisioned tenant")
	flag.BoolVar(&dryRun, "dry-run", false, "report the rows each UPDATE would touch, then roll back")
	flag.Parse()

	config.Load()
	if notifyDBURL == "" {
		log.Fatal("--notify-db-url is required")
	}
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	ctx := context.Background()

	cp, err := tenancy.NewControlPlane(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}

	// Same DSN-decryption seam as main.go: encrypted at rest in prod,
	// plaintext in dev.
	var dsnResolver tenancy.DSNResolver
	if config.AppConfig.SecretEncryptionKey != "" {
		cipher, cErr := secret.New(config.AppConfig.SecretEncryptionKey)
		if cErr != nil {
			log.Fatalf("invalid SECRET_ENCRYPTION_KEY: %v", cErr)
		}
		dsnResolver = func(_ context.Context, t *tenancy.Tenant) (string, error) {
			return cipher.Decrypt(t.DBConnectionRef)
		}
	}
	router := tenancy.NewRouter(dsnResolver)

	notifyPool, err := pgxpool.New(ctx, notifyDBURL)
	if err != nil {
		log.Fatalf("connect notify db: %v", err)
	}
	defer notifyPool.Close()

	targets, err := resolveTenants(ctx, cp, tenants)
	if err != nil {
		log.Fatalf("resolve tenants: %v", err)
	}
	if len(targets) == 0 {
		log.Fatal("no tenants to process")
	}

	var totalRows int64
	for _, t := range targets {
		rows, err := backfillTenant(ctx, router, notifyPool, t, dryRun)
		if err != nil {
			log.Fatalf("tenant %s (%s): %v", t.Slug, t.ID, err)
		}
		totalRows += rows
		log.Printf("tenant %s: %d row(s) %s", t.Slug, rows, verb(dryRun))
	}
	log.Printf("done: %d row(s) %s across %d tenant(s)", totalRows, verb(dryRun), len(targets))
}

func verb(dryRun bool) string {
	if dryRun {
		return "would be updated (rolled back)"
	}
	return "updated"
}

func resolveTenants(ctx context.Context, cp *tenancy.ControlPlane, slugs tenantFlags) ([]tenancy.Tenant, error) {
	if len(slugs) == 0 {
		return cp.ListTenants(ctx)
	}
	out := make([]tenancy.Tenant, 0, len(slugs))
	for _, slug := range slugs {
		t, err := cp.TenantBySlug(ctx, slug)
		if err != nil {
			return nil, fmt.Errorf("tenant %q: %w", slug, err)
		}
		out = append(out, *t)
	}
	return out, nil
}

// backfillTenant loads one tenant's users.id -> identity_id map and applies
// the three UPDATEs against the notify DB in a single transaction, scoped to
// that tenant_id. On --dry-run the transaction is rolled back after the row
// counts are read.
func backfillTenant(
	ctx context.Context, router *tenancy.Router, notifyPool *pgxpool.Pool, t tenancy.Tenant, dryRun bool,
) (int64, error) {
	tenantPool, err := router.PoolFor(ctx, &t)
	if err != nil {
		return 0, fmt.Errorf("tenant pool: %w", err)
	}

	userIDs, identityIDs, err := loadUserIdentityMap(ctx, tenantPool)
	if err != nil {
		return 0, err
	}
	if len(userIDs) == 0 {
		return 0, nil
	}

	tx, err := notifyPool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin notify tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var total int64
	for _, u := range []struct{ table, tenantCol, idCol string }{
		{"notifications", "tenant_id", "recipient_user_id"},
		{"notification_deliveries", "tenant_id", "recipient_user_id"},
		// PK is (tenant_id, user_id); translating the PK column is safe here
		// because no identity-id-keyed preference row can exist yet (the bell
		// never worked, so nobody set a preference against the new id).
		{"user_notification_preferences", "tenant_id", "user_id"},
	} {
		tag, uErr := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s x
			SET %s = m.identity_id
			FROM (SELECT unnest($2::uuid[]) AS user_id, unnest($3::uuid[]) AS identity_id) m
			WHERE x.%s = $1 AND x.%s = m.user_id`, u.table, u.idCol, u.tenantCol, u.idCol),
			t.ID, userIDs, identityIDs)
		if uErr != nil {
			return 0, fmt.Errorf("update %s: %w", u.table, uErr)
		}
		total += tag.RowsAffected()
	}

	if dryRun {
		return total, nil // deferred Rollback discards the changes
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit notify tx: %w", err)
	}
	return total, nil
}

func loadUserIdentityMap(ctx context.Context, pool *pgxpool.Pool) (userIDs, identityIDs []string, err error) {
	rows, err := pool.Query(ctx, `SELECT id::text, identity_id::text FROM users`)
	if err != nil {
		return nil, nil, fmt.Errorf("load users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, identityID string
		if err := rows.Scan(&id, &identityID); err != nil {
			return nil, nil, fmt.Errorf("scan user: %w", err)
		}
		userIDs = append(userIDs, id)
		identityIDs = append(identityIDs, identityID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate users: %w", err)
	}
	return userIDs, identityIDs, nil
}
