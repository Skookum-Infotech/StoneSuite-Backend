package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/services"
)

const (
	statusClaimed = "claimed"
	statusSent    = "sent"
	statusFailed  = "failed"
	statusDead    = "dead"
	statusUnknown = "unknown"

	// claimBatchSize bounds the rows one drain claims.
	claimBatchSize = 50
	// claimLease is how long a claim is held before another drain may reclaim it.
	claimLease = 2 * time.Minute
)

// Delivery is one claimed automation_deliveries row.
type Delivery struct {
	ID, RuleID, Resource, RecordID, OccurrenceKey, DigestKey, RecipientEmail string
	Anchor                                                                   time.Time
	Attempts                                                                 int
}

// Group is one outbound email: all claimed deliveries sharing a digest_key.
type Group struct {
	DigestKey, RecipientEmail string
	Items                     []Delivery
}

// Renderer builds the email and live line items for a group; items it drops are
// returned (keyed by occurrence key) with a skip reason. It must not send.
type Renderer interface {
	Render(ctx context.Context, env JobEnv, g Group) (req services.NotificationRequest, kept []Delivery, dropped map[string]string, err error)
}

// drain claims the rule's due deliveries, applies the guards, sends one email
// per digest group and records every outcome. Send timeouts leave rows
// 'unknown' (neither Sent nor Failed in the Result). Result.Sent counts emails
// delivered; Result.Failed counts delivery rows that ended failed or dead;
// Result.Skipped counts delivery rows skipped. Render errors are logged and
// returned joined; their rows are released back to 'planned'.
func drain(ctx context.Context, pool *pgxpool.Pool, env JobEnv, send Sender, r Renderer, ruleID string) (Result, error) {
	ctx = WithSystemActor(ctx)
	var res Result
	if !InSendWindow(env.Now, env.Loc) {
		return res, nil
	}
	if err := sweepSuperseded(ctx, pool, ruleID, env.Now); err != nil {
		return res, err
	}
	if err := sweepStale(ctx, pool, ruleID, env.Now); err != nil {
		return res, err
	}
	claimed, err := claimDue(ctx, pool, ruleID, env.Now)
	if err != nil {
		return res, err
	}
	var errs error
	for _, g := range groupDeliveries(claimed) {
		if gerr := drainGroup(ctx, pool, env, send, r, g, &res); gerr != nil {
			errs = errors.Join(errs, gerr)
		}
	}
	return res, errs
}

// groupDeliveries buckets deliveries by digest_key (a row without one is its own
// group), ordered by first appearance, items sorted by occurrence key.
func groupDeliveries(ds []Delivery) []Group {
	idx := map[string]int{}
	var groups []Group
	for _, d := range ds {
		key := d.DigestKey
		if key == "" {
			key = "single:" + d.ID
		}
		i, ok := idx[key]
		if !ok {
			i = len(groups)
			idx[key] = i
			groups = append(groups, Group{DigestKey: d.DigestKey, RecipientEmail: d.RecipientEmail})
		}
		groups[i].Items = append(groups[i].Items, d)
	}
	for i := range groups {
		items := groups[i].Items
		sort.Slice(items, func(a, b int) bool { return items[a].OccurrenceKey < items[b].OccurrenceKey })
	}
	return groups
}

func deliveryIDs(ds []Delivery) []string {
	ids := make([]string, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	return ids
}

func occurrenceKeys(ds []Delivery) []string {
	keys := make([]string, len(ds))
	for i, d := range ds {
		keys[i] = d.OccurrenceKey
	}
	return keys
}

// drainGroup guards, renders, sends and records one group on one pool connection.
func drainGroup(ctx context.Context, pool *pgxpool.Pool, env JobEnv, send Sender, r Renderer, g Group, res *Result) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("drain acquire: %w", err)
	}
	defer conn.Release()

	reason, err := guardExternal(ctx, conn, g.RecipientEmail, env.Now)
	if err == nil && reason == "" {
		reason, err = guardDailyCap(ctx, conn, localDayStart(env.Today, env.Loc))
	}
	if err != nil {
		return releaseAfter(ctx, conn, g.Items, env.Now, err, "guard")
	}
	if reason != "" {
		n, serr := markSkipped(ctx, conn, deliveryIDs(g.Items), reason, env.Now)
		res.Skipped += n
		return serr
	}

	req, kept, dropped, rerr := r.Render(ctx, env, g)
	if rerr != nil {
		slog.Error("automation drain: render failed", "digest", g.DigestKey, "err", rerr)
		return releaseAfter(ctx, conn, g.Items, env.Now, rerr, "render")
	}
	kept, serr := settleDropped(ctx, conn, g, kept, dropped, env.Now, res)
	if serr != nil || len(kept) == 0 {
		return serr
	}

	req.IdempotencyKey = NotifyKey(occurrenceKeys(kept))
	out, sendErr := send.Send(ctx, req)
	ids := deliveryIDs(kept)
	switch {
	case sendErr == nil:
		res.Sent++
		if _, err := markSent(ctx, conn, ids, out.NotificationIDs, env.Now); err != nil {
			return err
		}
	case isTimeoutErr(sendErr):
		slog.Warn("automation drain: send outcome unknown", "digest", g.DigestKey, "err", sendErr)
		if _, err := markUnknown(ctx, conn, ids, sendErr.Error(), env.Now); err != nil {
			return err
		}
	default:
		slog.Warn("automation drain: send failed", "digest", g.DigestKey, "err", sendErr)
		return markSendFailed(ctx, conn, kept, sendErr.Error(), env.Now, res)
	}
	return nil
}

// settleDropped skips every group item the renderer did not keep, using the
// renderer's reason (reasonNotRender when it gave none), and returns the kept
// items that belong to the group.
func settleDropped(ctx context.Context, q execer, g Group, kept []Delivery, dropped map[string]string, now time.Time, res *Result) ([]Delivery, error) {
	keptKeys := map[string]bool{}
	for _, k := range kept {
		keptKeys[k.OccurrenceKey] = true
	}
	var live []Delivery
	byReason := map[string][]string{}
	for _, it := range g.Items {
		if keptKeys[it.OccurrenceKey] {
			live = append(live, it)
			continue
		}
		reason := dropped[it.OccurrenceKey]
		if reason == "" {
			reason = reasonNotRender
		}
		byReason[reason] = append(byReason[reason], it.ID)
	}
	var errs error
	for reason, ids := range byReason {
		n, err := markSkipped(ctx, q, ids, reason, now)
		res.Skipped += n
		errs = errors.Join(errs, err)
	}
	return live, errs
}
