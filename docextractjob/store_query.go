package docextractjob

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Duplicate kinds reported by the duplicate and reference checks.
const (
	DupSameFile     = "same_file"
	DupSamePO       = "same_po"
	DupRevision     = "revision"
	DupQuoteRef     = "quote_reference"
	DupEstimateRef  = "estimate_reference"
	reasonSameFile  = "This file was already uploaded"
	maxDupLookupRow = 5
)

// CountCreatedToday counts extractions created since 00:00 UTC today in this
// tenant, for the daily cap.
func (s *Store) CountCreatedToday(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM document_extractions
		WHERE created_at >= date_trunc('day', NOW() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count extractions today: %w", err)
	}
	return n, nil
}

// SumPendingStagingBytes sums size_bytes over this tenant's non-expired rows
// whose staging object may still exist (everything but discarded/attached).
func (s *Store) SumPendingStagingBytes(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(size_bytes), 0)::bigint FROM document_extractions
		WHERE expires_at > NOW() AND status <> ALL($1)`,
		[]string{StatusDiscarded, StatusAttached}).Scan(&n); err != nil {
		return 0, fmt.Errorf("sum staging bytes: %w", err)
	}
	return n, nil
}

// FindCacheHit finds an earlier extraction of the same file (doc type + sha)
// created within ttl, in status ready/used/attached with a stored result,
// excluding excludeID. It returns (nil, nil) when there is none.
func (s *Store) FindCacheHit(ctx context.Context, docType, sha string, ttl time.Duration, excludeID string) (*Extraction, error) {
	e, err := scanExtraction(s.pool.QueryRow(ctx, `
		SELECT `+extractionColumns+`
		FROM document_extractions
		WHERE doc_type = $1 AND sha256 = $2 AND id <> $3::uuid AND result IS NOT NULL
		  AND status = ANY($4) AND created_at > NOW() - make_interval(secs => $5)
		ORDER BY created_at DESC LIMIT 1`,
		docType, strings.ToLower(sha), excludeID,
		[]string{StatusReady, StatusUsed, StatusAttached}, ttl.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find extraction cache hit: %w", err)
	}
	return e, nil
}

// DuplicateBySHA reports earlier extractions of the same file that produced a
// record (used or attached) as same_file duplicates; an extraction that was only
// read, left unsaved or discarded is not a duplicate. No record id is returned:
// the uploader of the earlier file may be someone else, and a bare id would leak it.
func (s *Store) DuplicateBySHA(ctx context.Context, docType, sha, excludeID string) ([]Duplicate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT status FROM document_extractions
		WHERE doc_type = $1 AND sha256 = $2 AND id <> $3::uuid AND status = ANY($4)
		ORDER BY created_at DESC LIMIT $5`,
		docType, strings.ToLower(sha), excludeID,
		[]string{StatusUsed, StatusAttached}, maxDupLookupRow)
	if err != nil {
		return nil, fmt.Errorf("find duplicate file: %w", err)
	}
	defer rows.Close()
	var out []Duplicate
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			return nil, fmt.Errorf("scan duplicate file: %w", err)
		}
		out = append(out, Duplicate{Kind: DupSameFile, Status: st, Reason: reasonSameFile})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate duplicate files: %w", err)
	}
	return out, nil
}

// SweepExpired deletes up to limit expired, non-attached rows and returns
// their staging keys so the caller can delete the objects.
func (s *Store) SweepExpired(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		DELETE FROM document_extractions
		WHERE id IN (
			SELECT id FROM document_extractions
			WHERE expires_at < NOW() AND status <> $1
			ORDER BY expires_at LIMIT $2)
		RETURNING staging_key`, StatusAttached, limit)
	if err != nil {
		return nil, fmt.Errorf("sweep expired extractions: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan swept key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate swept keys: %w", err)
	}
	return keys, nil
}
