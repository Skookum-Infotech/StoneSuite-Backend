package dashboard

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WidgetConfigOverrides returns the tenant's widget_key -> enabled overrides.
// A key absent from the result is enabled -- the table stores overrides
// only, never a full mirror of the catalog.
func WidgetConfigOverrides(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT widget_key, enabled FROM dashboard_widget_config`)
	if err != nil {
		return nil, fmt.Errorf("query dashboard widget config: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		var enabled bool
		if err := rows.Scan(&key, &enabled); err != nil {
			return nil, fmt.Errorf("scan dashboard widget config: %w", err)
		}
		out[key] = enabled
	}
	return out, rows.Err()
}

// SetWidgetConfig upserts the tenant-wide enabled flag for each given widget.
// Partial: widgets not named in updates keep whatever state they already
// had (enabled, by default).
func SetWidgetConfig(ctx context.Context, pool *pgxpool.Pool, updates []ConfigInput) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set widget config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, u := range updates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dashboard_widget_config (widget_key, enabled, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (widget_key) DO UPDATE SET enabled = $2, updated_at = NOW()`,
			u.WidgetKey, u.Enabled); err != nil {
			return fmt.Errorf("upsert dashboard widget config %q: %w", u.WidgetKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit set widget config: %w", err)
	}
	return nil
}

// UserPrefs returns userID's saved widget_key -> preference map.
func UserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string) (map[string]UserPref, error) {
	rows, err := pool.Query(ctx, `
		SELECT widget_key, visible, position, width, height
		FROM dashboard_user_widget WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query dashboard user widgets: %w", err)
	}
	defer rows.Close()
	out := map[string]UserPref{}
	for rows.Next() {
		var p UserPref
		if err := rows.Scan(&p.WidgetKey, &p.Visible, &p.Position, &p.Width, &p.Height); err != nil {
			return nil, fmt.Errorf("scan dashboard user widget: %w", err)
		}
		out[p.WidgetKey] = p
	}
	return out, rows.Err()
}

// SaveUserPrefs upserts userID's visibility/layout for each given widget.
// Partial: widgets not named in prefs keep whatever the caller last saved
// (or the catalog default, if never saved).
func SaveUserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string, prefs []UserPref) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin save dashboard prefs: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, p := range prefs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dashboard_user_widget (user_id, widget_key, visible, position, width, height, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, NOW())
			ON CONFLICT (user_id, widget_key) DO UPDATE
				SET visible = $3, position = $4, width = $5, height = $6, updated_at = NOW()`,
			userID, p.WidgetKey, p.Visible, p.Position, p.Width, p.Height); err != nil {
			return fmt.Errorf("upsert dashboard user widget %q: %w", p.WidgetKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit save dashboard prefs: %w", err)
	}
	return nil
}

// ClearUserPrefs deletes all of userID's saved preferences, reverting every
// widget to its catalog default.
func ClearUserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_user_widget WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clear dashboard user widgets: %w", err)
	}
	return nil
}
