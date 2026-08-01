package accountingperiod

import (
	"fmt"
	"sort"
	"time"
)

// PeriodState is the minimum a sequencing rule needs to know about a period.
// Keeping the rules over this struct rather than over *Period is what makes
// them pure and table-driven testable with no database.
type PeriodState struct {
	ID           string
	Name         string
	Start        time.Time
	End          time.Time
	Status       string
	APStatus     string
	ARStatus     string
	GLStatus     string
	IsBeforeBase bool // ends before the go-live boundary; never reopenable
}

// LockField identifies one of a period's three independent sub-ledger
// locks -- AP, AR, GL -- each sequenced by the same chronological rule
// PlanClose/PlanReopen already apply to the derived overall Status.
type LockField int

const (
	LockAP LockField = iota
	LockAR
	LockGL
)

// get returns the value of this lock's status field on the given period.
func (f LockField) get(p PeriodState) string {
	switch f {
	case LockAP:
		return p.APStatus
	case LockAR:
		return p.ARStatus
	case LockGL:
		return p.GLStatus
	}
	return ""
}

// label returns the human-readable name of this lock, used only in error
// message text.
func (f LockField) label() string {
	switch f {
	case LockAP:
		return "AP"
	case LockAR:
		return "AR"
	case LockGL:
		return "GL"
	}
	return ""
}

// PlanClose validates closing every period named in ids against the full
// period set and returns them in the order they must be applied.
//
// Periods are closed oldest-first, and the effect of each close is threaded
// onto the next, so closing Jan, Feb and Mar in one call succeeds while
// closing Mar alone with Jan still open does not. That threading is why this
// is one function over the whole batch rather than a per-period predicate: a
// batch must be judged as the state it produces, not one row at a time.
func PlanClose(ids []string, all []PeriodState) ([]PeriodState, error) {
	return plan(ids, all, func(p PeriodState) string { return p.Status }, "", true)
}

// PlanReopen is PlanClose's mirror: newest-first, and blocked by any later
// period that is still closed or by the base-period boundary.
func PlanReopen(ids []string, all []PeriodState) ([]PeriodState, error) {
	return plan(ids, all, func(p PeriodState) string { return p.Status }, "", false)
}

// PlanCloseLock is PlanClose scoped to one of a period's three independent
// sub-ledger locks (see LockField) instead of the derived overall Status --
// same chronological-order rule, applied to that lock's own history.
func PlanCloseLock(ids []string, all []PeriodState, lock LockField) ([]PeriodState, error) {
	return plan(ids, all, lock.get, lock.label(), true)
}

// PlanReopenLock is PlanReopen scoped to one lock. IsBeforeBase still
// blocks reopening on every dimension -- a pre-go-live period can never be
// reopened on any lock, not just the derived overall status.
func PlanReopenLock(ids []string, all []PeriodState, lock LockField) ([]PeriodState, error) {
	return plan(ids, all, lock.get, lock.label(), false)
}

// plan implements both directions. closing selects the direction; the two
// share selection, dedup, ordering and the working-state thread, and differ
// only in the per-period rule. get extracts the status field being planned
// over (Status, or one of the three lock fields) and label names it for
// error messages ("" for the derived overall Status).
func plan(ids []string, all []PeriodState, get func(PeriodState) string, label string, closing bool) ([]PeriodState, error) {
	if len(ids) == 0 {
		return nil, ClientError{Msg: "periodIds must contain at least one period."}
	}

	byID := make(map[string]PeriodState, len(all))
	for _, p := range all {
		byID[p.ID] = p
	}

	selected := make([]PeriodState, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue // a duplicate id is a no-op, not an error
		}
		p, ok := byID[id]
		if !ok {
			return nil, ErrNotFound
		}
		seen[id] = true
		selected = append(selected, p)
	}

	// Close oldest-first; reopen newest-first.
	sort.Slice(selected, func(i, j int) bool {
		if closing {
			return selected[i].Start.Before(selected[j].Start)
		}
		return selected[j].Start.Before(selected[i].Start)
	})

	// working carries the effect of each step onto the next.
	working := make(map[string]string, len(all))
	for _, p := range all {
		working[p.ID] = get(p)
	}

	for _, target := range selected {
		var err error
		if closing {
			err = checkClose(target, all, working, label)
		} else {
			err = checkReopen(target, all, working, label)
		}
		if err != nil {
			return nil, err
		}
		if closing {
			working[target.ID] = StatusClosed
		} else {
			working[target.ID] = StatusOpen
		}
	}
	return selected, nil
}

// checkClose enforces: the target is open, and every earlier period is
// closed. label prefixes the error text ("AP for", "AR for", "GL for") when
// checking a sub-ledger lock rather than the derived overall Status; "" for
// the latter reproduces today's unprefixed messages exactly.
func checkClose(target PeriodState, all []PeriodState, working map[string]string, label string) error {
	prefix := ""
	if label != "" {
		prefix = label + " for "
	}
	if working[target.ID] == StatusClosed {
		return conflict(ErrAlreadyClosed, fmt.Sprintf("%s%s is already closed.", prefix, target.Name))
	}
	for _, p := range all {
		if p.Start.Before(target.Start) && working[p.ID] == StatusOpen {
			return conflict(ErrPriorPeriodOpen, fmt.Sprintf(
				"%s%s cannot be closed while the earlier period %s%s is still open. "+
					"Close periods in chronological order.", prefix, target.Name, prefix, p.Name))
		}
	}
	return nil
}

// checkReopen enforces: the target is closed, is not before the base period,
// and every later period is open. label prefixes the error text ("AP for",
// "AR for", "GL for") when checking a sub-ledger lock rather than the derived
// overall Status; "" for the latter reproduces today's unprefixed messages
// exactly.
func checkReopen(target PeriodState, all []PeriodState, working map[string]string, label string) error {
	prefix := ""
	if label != "" {
		prefix = label + " for "
	}
	if working[target.ID] == StatusOpen {
		return conflict(ErrAlreadyOpen, fmt.Sprintf("%s%s is already open.", prefix, target.Name))
	}
	if target.IsBeforeBase {
		return conflict(ErrBeforeBasePeriod, fmt.Sprintf(
			"%s precedes the base period and cannot be reopened.", target.Name))
	}
	for _, p := range all {
		if target.Start.Before(p.Start) && working[p.ID] == StatusClosed {
			return conflict(ErrLaterPeriodClosed, fmt.Sprintf(
				"%s%s cannot be reopened while the later period %s%s is closed. "+
					"Reopen periods in reverse chronological order.", prefix, target.Name, prefix, p.Name))
		}
	}
	return nil
}

// DeriveYearStatus reports the status a fiscal year should carry given its
// periods: closed only when every one of them is closed. A year with no
// periods is open — it cannot be "fully closed" on the strength of an empty
// set, which is the answer a bare `NOT EXISTS(... open ...)` would give.
func DeriveYearStatus(periods []PeriodState) string {
	if len(periods) == 0 {
		return StatusOpen
	}
	for _, p := range periods {
		if p.Status != StatusClosed {
			return StatusOpen
		}
	}
	return StatusClosed
}

// ClosedThrough returns the end of the contiguous closed prefix — the value
// books_closed_through must carry so the legacy single-date period concept
// stays truthful. nil when the earliest period is open.
//
// It stops at the first open period rather than taking max(end) over all
// closed periods: with a hole in the sequence the latter would claim months
// are closed that are not. The sequencing rules make holes unreachable, but
// this function is the one that would silently lie if they ever failed.
func ClosedThrough(periods []PeriodState) *time.Time {
	ordered := make([]PeriodState, len(periods))
	copy(ordered, periods)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start.Before(ordered[j].Start) })

	var out *time.Time
	for _, p := range ordered {
		if p.Status != StatusClosed {
			break
		}
		end := p.End
		out = &end
	}
	return out
}
