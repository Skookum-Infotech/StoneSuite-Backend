package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docpdf"
)

// currencySymbolQuerier is the one query the symbol lookup needs; *pgxpool.Pool
// satisfies it and tests can stub it.
type currencySymbolQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// lookupCurrencySymbol returns the display symbol of a lkp_currency row, or ""
// when the id is unknown (the renderer then falls back to its default).
func lookupCurrencySymbol(ctx context.Context, q currencySymbolQuerier, currencyID int) (string, error) {
	var sym string
	err := q.QueryRow(ctx,
		`SELECT currency_symbol FROM lkp_currency WHERE currency_id = $1`, currencyID).Scan(&sym)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up currency symbol: %w", err)
	}
	return sym, nil
}

// withCurrencySymbol wraps a loader so the document prints in its own
// currency: when the loader reports meta.CurrencyID, the symbol is resolved
// from lkp_currency. A document with no currency, an unknown currency, or a
// failed lookup keeps the renderer's default symbol -- a missing symbol must
// never block the PDF.
func withCurrencySymbol(load DocumentLoader) DocumentLoader {
	return func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
		doc, meta, err := load(ctx, pool, uuid, seller)
		if err != nil || meta.CurrencyID == nil || doc.CurrencySymbol != "" {
			return doc, meta, err
		}
		sym, lerr := lookupCurrencySymbol(ctx, pool, *meta.CurrencyID)
		if lerr != nil {
			slog.Warn("document currency symbol lookup failed", "workflowKey", meta.WorkflowKey, "error", lerr)
			return doc, meta, nil
		}
		doc.CurrencySymbol = sym
		return doc, meta, nil
	}
}
