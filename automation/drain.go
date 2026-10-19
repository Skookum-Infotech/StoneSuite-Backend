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
	// notifySendTimeout is the longest services.SendNotificationWithResult may take.
	notifySendTimeout = 30 * time.Second
	// postWriteTimeout bounds each state write made after a send (or on an
	// abort). Those writes run on a context detached from the tick's deadline,
	// so a deadline that fires mid-send can't strand a row in 'claimed'.
	postWriteTimeout = 5 * time.Second
	// minGroupBudget is the time that must remain on the tick context before a
	// new group is started: one send plus its state write.
	minGroupBudget = notifySendTimeout + postWriteTimeout
)

// detach returns a context that survives cancellation/deadline of ctx (keeping
// its values, e.g. the system actor) but is bounded by postWriteTimeout.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), postWriteTimeout)
}

// outOfTime reports that ctx is done or too close to its deadline to safely
// start another send-and-record cycle.
func outOfTime(ctx context.Context, now func() time.Time) bool {
	if ctx.Err() != nil {
		return true
	}
	dl, ok := ctx.Deadline()
	return ok && dl.Sub(now()) < minGroupBudget
}

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
	groups := groupDeliveries(claimed)
	for i, g := range groups {
		if outOfTime(ctx, time.Now) {
			slog.Warn("automation drain: out of time, releasing unsent groups", "automation_event", "drain_out_of_time", "groups", len(groups)-i)
			var rest []Delivery
			for _, ug := range groups[i:] {
				rest = append(rest, ug.Items...)
			}
			dctx, cancel := detach(ctx)
			errs = errors.Join(errs, releaseRows(dctx, pool, rest, env.Now))
			cancel()
			break
		}
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

	settings, err := loadSettings(ctx, conn)
	var reason string
	if err == nil {
		reason, err = guardExternal(ctx, conn, settings, g.RecipientEmail, env.Now)
	}
	if err == nil && reason == "" {
		reason, err = guardDailyCap(ctx, conn, settings, localDayStart(env.Today, env.Loc))
	}
	if err != nil {
		return releaseAfter(ctx, conn, g.Items, env.Now, err, "guard")
	}
	if reason != "" && isDeferral(reason) {
		return deferGroup(ctx, conn, env, g, reason)
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
	// Post-send writes must land even if the tick context expired during Send.
	wctx, cancel := detach(ctx)
	defer cancel()
	switch {
	case sendErr == nil:
		res.Sent++
		n, err := markSent(wctx, conn, ids, out.NotificationIDs, env.Now)
		warnShortWrite("sent", g.DigestKey, n, len(ids))
		return err
	case isTimeoutErr(sendErr):
		slog.Warn("automation drain: send outcome unknown", "digest", g.DigestKey, "err", sendErr)
		n, err := markUnknown(wctx, conn, ids, sendErr.Error(), env.Now)
		warnShortWrite("unknown", g.DigestKey, n, len(ids))
		return err
	default:
		slog.Warn("automation drain: send failed", "digest", g.DigestKey, "err", sendErr)
		return markSendFailed(wctx, conn, kept, sendErr.Error(), env.Now, res)
	}
}

// deferGroup pushes a capped or kill-switched group to its next eligible send
// time instead of dropping it: next local day's window for caps, one hour past
// the next send window for the kill switch (a recheck time, not guaranteed to
// be inside the window; the drain's own window gate covers that).
func deferGroup(ctx context.Context, q execer, env JobEnv, g Group, reason string) error {
	runAt := NextSendWindow(localDayStart(env.Today, env.Loc).AddDate(0, 0, 1), env.Loc)
	if reason == reasonKillSwitch {
		runAt = NextSendWindow(env.Now, env.Loc).Add(killSwitchRecheck)
	}
	slog.Info("automation drain: group deferred", "automation_event", "drain_deferred",
		"reason", reason, "group_size", len(g.Items), "run_at", runAt)
	n, err := deferRows(ctx, q, g.Items, runAt, env.Now, env.Loc)
	warnShortWrite("deferred", g.DigestKey, n, len(g.Items))
	return err
}

// warnShortWrite logs when a status write touched fewer rows than the group had
// (some row left 'claimed' by another actor, e.g. a lease reclaimed mid-send).
func warnShortWrite(outcome, digest string, got, want int) {
	if got < want {
		slog.Warn("automation drain: short state write", "automation_event", "drain_short_write",
			"outcome", outcome, "digest", digest, "updated", got, "expected", want)
	}
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
