package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

type stubRow struct {
	sym string
	err error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.sym
	return nil
}

type stubQuerier struct{ row stubRow }

func (q stubQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return q.row }

func TestLookupCurrencySymbol(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		row     stubRow
		want    string
		wantErr bool
	}{
		{"known currency", stubRow{sym: "€"}, "€", false},
		{"unknown id falls back to empty", stubRow{err: pgx.ErrNoRows}, "", false},
		{"query error is wrapped", stubRow{err: boom}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lookupCurrencySymbol(context.Background(), stubQuerier{tc.row}, 2)
			assert.Equal(t, tc.want, got)
			if tc.wantErr {
				assert.ErrorIs(t, err, boom)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Loaders that report no currency (or an error) must pass through untouched
// without ever reaching the database -- the pool here is nil.
func TestWithCurrencySymbol_PassThrough(t *testing.T) {
	loadErr := errors.New("load failed")
	eur := 2
	tests := []struct {
		name    string
		meta    DocMeta
		doc     docpdf.PrintableDoc
		err     error
		wantSym string
		wantErr error
	}{
		{"no currency keeps default", DocMeta{}, docpdf.PrintableDoc{}, nil, "", nil},
		{"loader error is returned", DocMeta{CurrencyID: &eur}, docpdf.PrintableDoc{}, loadErr, "", loadErr},
		{"symbol already set is kept", DocMeta{CurrencyID: &eur}, docpdf.PrintableDoc{CurrencySymbol: "£"}, nil, "£", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			load := func(context.Context, *pgxpool.Pool, string, docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error) {
				return tc.doc, tc.meta, tc.err
			}
			doc, _, err := withCurrencySymbol(load)(context.Background(), nil, "id", docpdf.Seller{})
			assert.Equal(t, tc.wantSym, doc.CurrencySymbol)
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}
