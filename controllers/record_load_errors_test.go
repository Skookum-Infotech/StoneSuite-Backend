package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/purchaseorder"
)

// pgInvalidTextRepresentation is Postgres's SQLSTATE for a value that is not a
// valid representation of its type, e.g. "not-a-uuid" compared to a uuid column.
const pgInvalidTextRepresentation = "22P02"

// loadErrorCases are the lookup failures a path id can cause. Before the fix a
// malformed id fell into a blanket 500 in the *ByUUID auth helpers, which
// bypassed the 400 that poFail/irFail already map for it.
var loadErrorCases = []struct {
	name       string
	err        error
	wantStatus int
}{
	{"malformed id is a client error", &pgconn.PgError{Code: pgInvalidTextRepresentation}, http.StatusBadRequest},
	{"malformed id wrapped by the store", fmt.Errorf("load: %w", &pgconn.PgError{Code: pgInvalidTextRepresentation}), http.StatusBadRequest},
	{"unexpected database error stays a 500", errors.New("connection reset"), http.StatusInternalServerError},
}

// assertFailureBody checks the shared error envelope every handler promises.
func assertFailureBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body.Success)
	assert.NotEmpty(t, body.Message)
}

func TestLoadPOOrFail(t *testing.T) {
	cases := append([]struct {
		name       string
		err        error
		wantStatus int
	}{{"missing order is a 404", purchaseorder.ErrNotFound, http.StatusNotFound}}, loadErrorCases...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, ok := loadPOOrFail(context.Background(), rec, func(context.Context) (*purchaseorder.PurchaseOrder, error) {
				return nil, tc.err
			})
			assert.False(t, ok)
			assert.Nil(t, got)
			assert.Equal(t, tc.wantStatus, rec.Code)
			assertFailureBody(t, rec)
		})
	}

	t.Run("a loaded order is returned and nothing is written", func(t *testing.T) {
		want := &purchaseorder.PurchaseOrder{ID: "po-1"}
		rec := httptest.NewRecorder()
		got, ok := loadPOOrFail(context.Background(), rec, func(context.Context) (*purchaseorder.PurchaseOrder, error) {
			return want, nil
		})
		assert.True(t, ok)
		assert.Same(t, want, got)
		assert.Equal(t, 0, rec.Body.Len())
	})
}

func TestLoadIROrFail(t *testing.T) {
	cases := append([]struct {
		name       string
		err        error
		wantStatus int
	}{{"missing receipt is a 404", itemreceipt.ErrNotFound, http.StatusNotFound}}, loadErrorCases...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			got, ok := loadIROrFail(context.Background(), rec, func(context.Context) (*itemreceipt.ItemReceipt, error) {
				return nil, tc.err
			})
			assert.False(t, ok)
			assert.Nil(t, got)
			assert.Equal(t, tc.wantStatus, rec.Code)
			assertFailureBody(t, rec)
		})
	}

	t.Run("a loaded receipt is returned and nothing is written", func(t *testing.T) {
		want := &itemreceipt.ItemReceipt{ID: "ir-1"}
		rec := httptest.NewRecorder()
		got, ok := loadIROrFail(context.Background(), rec, func(context.Context) (*itemreceipt.ItemReceipt, error) {
			return want, nil
		})
		assert.True(t, ok)
		assert.Same(t, want, got)
		assert.Equal(t, 0, rec.Body.Len())
	})
}
