package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/proposedaction"
)

// getEntities runs GET /document-extractions/{id} and decodes the proposed entities.
func (e *entEnv) getEntities(t *testing.T) []proposedEntityView {
	t.Helper()
	rec := e.do(e.h.Get, http.MethodGet, "/x", "", entExtractionID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Extraction struct {
			Result struct {
				ProposedEntities []proposedEntityView `json:"proposedEntities"`
				Extracted        json.RawMessage      `json:"extracted"`
			} `json:"result"`
		} `json:"extraction"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp.Extraction.Result.Extracted, "other result fields stay in place")
	return resp.Extraction.Result.ProposedEntities
}

func TestGet_FlagOffOmitsProposedEntities(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	e.h.cfg.ProposeEntities = false
	e.actions.listErr = assert.AnError
	got := e.getEntities(t)
	assert.Empty(t, got)
	assert.Equal(t, []authz.Resource{authz.ResourceSalesOrder}, e.checked, "only the doc-type check runs when the flag is off")
}

func TestGet_EnrichesCanCreateReasonAndAction(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	e.denied[authz.ResourceCustomer] = true
	older := time.Now().Add(-time.Hour)
	payload := func(key string) json.RawMessage {
		return json.RawMessage(`{"sourceRef":"` + entExtractionID + ":" + key + `"}`)
	}
	e.actions.rows = []proposedaction.Action{
		{ID: "act-new", Status: proposedaction.StatusExpired, Payload: payload(entKeyItem), CreatedAt: time.Now()},
		{ID: "act-old", Status: proposedaction.StatusCancelled, Payload: payload(entKeyItem), CreatedAt: older},
	}
	got := e.getEntities(t)
	require.Len(t, got, 2)
	assert.False(t, got[0].CanCreate)
	assert.Equal(t, "You don't have permission to create customers.", got[0].Reason)
	assert.Nil(t, got[0].Action)
	assert.True(t, got[1].CanCreate)
	assert.Empty(t, got[1].Reason)
	require.NotNil(t, got[1].Action)
	assert.Equal(t, "act-new", got[1].Action.ID)
	assert.Equal(t, "Acme Stone", got[0].Name, "entity fields stay flattened")
}

func TestGet_ActionListErrorStillServes(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	e.actions.listErr = assert.AnError
	got := e.getEntities(t)
	require.Len(t, got, 2)
	assert.Nil(t, got[0].Action)
	assert.True(t, got[0].CanCreate)
}

func TestGet_PermissionLookupErrorFailsClosed(t *testing.T) {
	e := newEntEnv(t)
	e.seedReady(t)
	base := e.h.check
	e.h.check = func(ctx context.Context, c *docExtractCaller, res authz.Resource, act authz.Action) (authz.Decision, error) {
		if res == authz.ResourceInventoryItem {
			return authz.Decision{}, assert.AnError
		}
		return base(ctx, c, res, act)
	}
	got := e.getEntities(t)
	require.Len(t, got, 2)
	assert.False(t, got[1].CanCreate)
	assert.NotEmpty(t, got[1].Reason)
}
