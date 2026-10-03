package docextractjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/duplicate"
)

// RecordCompletion learns from a saved document: it diffs the saved values
// against the extraction's result into document_extraction_feedback rows and
// upserts the party and item aliases the user's corrections imply. The caller
// (the controller) has already verified the record exists, was created by the
// caller and matches the doc type; this layer assumes that. The alias upserts
// only point at live customers/items, so a forged uuid learns nothing.
func RecordCompletion(ctx context.Context, pool *pgxpool.Pool, extractionID, ownerIdentityID, recordUUID string, saved SavedValues) (err error) {
	if !validUUID(extractionID) {
		return ErrNotFound
	}
	if !validUUID(recordUUID) {
		return fmt.Errorf("record completion: invalid record uuid")
	}
	if !validUUID(saved.CustomerUUID) {
		saved.CustomerUUID = "" // never let a malformed client id reach a ::uuid cast
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin record completion: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("rollback record completion: %w", rbErr)
		}
	}()

	var raw json.RawMessage
	err = tx.QueryRow(ctx,
		`SELECT result FROM document_extractions WHERE id = $1::uuid AND owner_identity_id = $2`,
		extractionID, ownerIdentityID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load extraction result: %w", err)
	}
	if len(raw) == 0 {
		return ErrConflict
	}
	var res ResultDoc
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decode extraction result: %w", err)
	}

	if err := insertFeedback(ctx, tx, extractionID, res, DiffFeedback(res, saved)); err != nil {
		return err
	}
	if learnCustomer(res, saved) {
		if err := upsertPartyAlias(ctx, tx, duplicate.Key(res.Extracted.Header.CustomerName.Value), saved.CustomerUUID, ownerIdentityID); err != nil {
			return err
		}
	}
	if validUUID(saved.CustomerUUID) {
		if err := upsertItemAliases(ctx, tx, saved.CustomerUUID, learnItems(res, saved), ownerIdentityID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit record completion: %w", err)
	}
	return nil
}

// insertFeedback writes the correction rows in one statement.
func insertFeedback(ctx context.Context, tx pgx.Tx, extractionID string, res ResultDoc, rows []FeedbackRow) error {
	if len(rows) == 0 {
		return nil
	}
	fields, extracted, final := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	for i, r := range rows {
		fields[i], extracted[i], final[i] = r.Field, r.Extracted, r.Final
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_extraction_feedback (extraction_id, party_uuid, field, extracted, final, layout_fingerprint)
		SELECT $1::uuid, NULLIF($2, '')::uuid, t.f, t.e, t.fi, $6
		FROM unnest($3::text[], $4::text[], $5::text[]) AS t(f, e, fi)`,
		extractionID, rows[0].PartyUUID, fields, extracted, final, res.Extracted.LayoutFingerprint); err != nil {
		return fmt.Errorf("insert extraction feedback: %w", err)
	}
	return nil
}

// upsertPartyAlias remembers that aliasKey means the customer partyUUID. Hits
// restart at 1 when the alias is re-pointed at a different customer.
func upsertPartyAlias(ctx context.Context, tx pgx.Tx, aliasKey, partyUUID, createdBy string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_party_alias (party_kind, alias_key, party_uuid, created_by)
		SELECT $1, $2, c.customer_uuid, $4
		FROM customer c WHERE c.customer_uuid = $3::uuid AND c.customer_deleted_at IS NULL
		ON CONFLICT (party_kind, alias_key) DO UPDATE
		SET hits = CASE WHEN document_party_alias.party_uuid = EXCLUDED.party_uuid THEN document_party_alias.hits + 1 ELSE 1 END,
		    party_uuid = EXCLUDED.party_uuid,
		    last_used_at = NOW()`,
		partyKindCustomer, aliasKey, partyUUID, createdBy); err != nil {
		return fmt.Errorf("upsert party alias: %w", err)
	}
	return nil
}

// upsertItemAliases remembers each alias for this party, pointing only at live items.
func upsertItemAliases(ctx context.Context, tx pgx.Tx, partyUUID string, entries []aliasEntry, createdBy string) error {
	if len(entries) == 0 {
		return nil
	}
	keys, items := make([]string, len(entries)), make([]string, len(entries))
	for i, e := range entries {
		keys[i], items[i] = e.key, e.itemUUID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO document_item_alias (party_uuid, alias_key, item_uuid, created_by)
		SELECT $1::uuid, t.k, i.inventory_item_uuid, $4
		FROM unnest($2::text[], $3::uuid[]) AS t(k, iu)
		JOIN inventory_item i ON i.inventory_item_uuid = t.iu AND i.inventory_item_deleted_at IS NULL
		ON CONFLICT (party_uuid, alias_key) DO UPDATE
		SET hits = CASE WHEN document_item_alias.item_uuid = EXCLUDED.item_uuid THEN document_item_alias.hits + 1 ELSE 1 END,
		    item_uuid = EXCLUDED.item_uuid,
		    last_used_at = NOW()`,
		partyUUID, keys, items, createdBy); err != nil {
		return fmt.Errorf("upsert item aliases: %w", err)
	}
	return nil
}
