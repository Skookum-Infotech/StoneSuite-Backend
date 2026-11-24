package automation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// planDigests writes today's digest delivery row for every opted-in user whose
// send hour has arrived, in one transaction, and returns how many were newly
// inserted.
func planDigests(ctx context.Context, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned int, err error) {
	ctx = WithSystemActor(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("digest plan begin: %w", err)
	}
	planned, err = planDigestsTx(ctx, tx, env, rule)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("digest plan commit: %w", err)
	}
	return planned, nil
}

// planDigestsTx is the body of planDigests, run against an open transaction.
func planDigestsTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (int, error) {
	rcpts, err := listDigestRecipients(ctx, tx)
	if err != nil {
		return 0, err
	}
	planned, _, err := insertDeliveries(ctx, tx, rule.ID, resourceDailyDigest, recipientInternal, digestInsertsFor(rcpts, env, rule))
	if err != nil {
		return 0, err
	}
	if err := markPlanned(ctx, tx, rule.ID, env.Now); err != nil {
		return 0, err
	}
	return planned, nil
}

// digestInsertsFor is the recipient loop: a pure function of its inputs. A user
// gets at most one row per local day (the occurrence key carries the date), and a
// row only once the current local hour has reached their send hour and the
// send window is still open, so a digest is never planned for a time that has
// passed unsent or sent overnight.
func digestInsertsFor(rcpts []DigestRecipient, env JobEnv, rule ruleRow) []deliveryInsert {
	if !InSendWindow(env.Now, env.Loc) {
		return nil
	}
	hour := env.Now.In(env.Loc).Hour()
	var out []deliveryInsert
	for _, r := range rcpts {
		if !r.Prefs.Enabled || r.Email == "" || hour < r.Prefs.SendHour {
			continue
		}
		digest := DigestKey(rule.ID, r.Email, env.Today)
		uid := r.UserID
		out = append(out, deliveryInsert{
			RecordID: r.UserID, Anchor: env.Today, Stage: 0,
			Key:   OccurrenceKey(rule.ID, resourceDailyDigest, r.UserID, env.Today, 0),
			Email: r.Email, UserID: &uid, Digest: &digest, RunAt: env.Now, Status: statusPlanned,
		})
	}
	return out
}
