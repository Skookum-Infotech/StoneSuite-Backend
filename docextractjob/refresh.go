package docextractjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docextract"
)

// ResolveDoc resolves master data for extracted and runs the duplicate /
// reference checks. The worker calls it once per job; Refresh re-runs it on a
// stored result so a resumed document sees today's data.
func ResolveDoc(ctx context.Context, pool *pgxpool.Pool, ex *Extraction, sha string, extracted docextract.Result, own []string) (ResultDoc, error) {
	resolution, err := NewResolver(pool, ResolveOptions{OwnCompanyNames: own}).Resolve(ctx, extracted)
	if err != nil {
		return ResultDoc{}, fmt.Errorf("resolve: %w", err)
	}
	dups, err := NewStore(pool).DuplicateBySHA(ctx, ex.DocType, sha, ex.ID)
	if err != nil {
		return ResultDoc{}, err
	}
	in := DuplicateInput{
		CustomerUUID: resolution.Customer.UUID,
		PONumber:     extracted.Header.PONumber.Value,
		Revision:     extracted.Revision,
		QuoteRef:     extracted.QuoteRef,
	}
	if f := extracted.Header.Total; f.Found() {
		if cents, issue := docextract.ParseMoney(f.Value); docextract.NumberFlagsUsable(issue) {
			in.TotalCents, in.HasTotal = int64(cents), true
		}
	}
	found, err := NewChecker(pool).Check(ctx, in)
	if err != nil {
		return ResultDoc{}, fmt.Errorf("duplicate checks: %w", err)
	}
	// Results cached before Extract stopped emitting null lists still carry nil
	// slices; normalise here so every stored doc serialises them as [].
	if extracted.Lines == nil {
		extracted.Lines = []docextract.Line{}
	}
	if extracted.Pages == nil {
		extracted.Pages = []docextract.PageRows{}
	}
	if resolution.Lines == nil {
		resolution.Lines = []LineMatch{}
	}
	all := append([]Duplicate{}, dups...)
	return ResultDoc{Extracted: extracted, Resolution: resolution, Duplicates: append(all, found...)}, nil
}

// Refresh re-resolves a ready extraction's stored text against current master
// data — aliases learned, customers/items added or deactivated, and orders
// saved since it was extracted — and persists the result when it changed, so
// the learning diff at /complete compares against what the reviewer saw. Only
// the DB lookups re-run; the document is not re-parsed. Any other status is
// returned unchanged.
func Refresh(ctx context.Context, pool *pgxpool.Pool, ex *Extraction, stored ResultDoc) (ResultDoc, error) {
	if ex.Status != StatusReady {
		return stored, nil
	}
	own, err := loadOwnCompanyNames(ctx, pool)
	if err != nil {
		return stored, err
	}
	fresh, err := ResolveDoc(ctx, pool, ex, ex.SHA256, stored.Extracted, own)
	if err != nil {
		return stored, err
	}
	same, err := sameResolution(stored, fresh)
	if err != nil || same {
		return stored, err
	}
	if err := NewStore(pool).ReplaceReadyResult(ctx, ex.ID, fresh); err != nil {
		return stored, err
	}
	return fresh, nil
}

// sameResolution compares the resolution and duplicates by their JSON form,
// which is what is stored, so nil-vs-empty differences don't force a write.
func sameResolution(a, b ResultDoc) (bool, error) {
	ja, err := json.Marshal([]any{a.Resolution, a.Duplicates})
	if err != nil {
		return false, fmt.Errorf("marshal stored resolution: %w", err)
	}
	jb, err := json.Marshal([]any{b.Resolution, b.Duplicates})
	if err != nil {
		return false, fmt.Errorf("marshal fresh resolution: %w", err)
	}
	return bytes.Equal(ja, jb), nil
}
