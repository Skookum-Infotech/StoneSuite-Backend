package controllers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/docextractjob"
	"stonesuite-backend/proposedaction"
)

// docExtractActionStore is the slice of proposedaction.Store the extraction
// handlers use to look up and create proposals.
type docExtractActionStore interface {
	ListBySourceRef(ctx context.Context, identityID string, source proposedaction.Source, refPrefix string) ([]proposedaction.Action, error)
	FindActiveBySourceRef(ctx context.Context, identityID string, source proposedaction.Source, ref string) (proposedaction.Action, bool, error)
	Propose(ctx context.Context, q proposedaction.Querier, p proposedaction.Proposal) (string, error)
	Get(ctx context.Context, identityID, id string) (proposedaction.Action, error)
}

// sourceRefSep joins the extraction id and the entity key into a sourceRef.
const sourceRefSep = ":"

// proposedEntityResources maps a proposed entity kind to the RBAC resource
// whose create permission gates it.
var proposedEntityResources = map[string]authz.Resource{
	docextractjob.ProposedKindCustomer: authz.ResourceCustomer,
	docextractjob.ProposedKindVendor:   authz.ResourceVendor,
	docextractjob.ProposedKindItem:     authz.ResourceInventoryItem,
}

// proposedEntityNouns is the plural noun used in the denied reason.
var proposedEntityNouns = map[string]string{
	docextractjob.ProposedKindCustomer: "customers",
	docextractjob.ProposedKindVendor:   "vendors",
	docextractjob.ProposedKindItem:     "items",
}

// proposedEntityView is a proposed entity as the client sees it: the stored
// entity plus whether the caller may create it and its latest proposal.
type proposedEntityView struct {
	docextractjob.ProposedEntity
	CanCreate bool                   `json:"canCreate"`
	Reason    string                 `json:"reason,omitempty"`
	Action    *proposedaction.Action `json:"action,omitempty"`
}

// docExtractResultView is a ResultDoc whose proposedEntities are enriched. The
// outer field shadows the embedded one, so every other result field is unchanged.
type docExtractResultView struct {
	docextractjob.ResultDoc
	ProposedEntities []proposedEntityView `json:"proposedEntities"`
}

// newResultView wraps doc, enriching its proposed entities (empty when the
// feature flag is off).
func (h *DocExtractOps) newResultView(r *http.Request, c *docExtractCaller, ex *docextractjob.Extraction, doc docextractjob.ResultDoc) *docExtractResultView {
	return &docExtractResultView{ResultDoc: doc, ProposedEntities: h.enrichProposed(r, c, ex, doc.ProposedEntities)}
}

// enrichProposed adds canCreate/reason/action to each entity. It never fails
// the request: a permission lookup error denies, an action lookup error omits
// the actions.
func (h *DocExtractOps) enrichProposed(r *http.Request, c *docExtractCaller, ex *docextractjob.Extraction, entities []docextractjob.ProposedEntity) []proposedEntityView {
	out := make([]proposedEntityView, 0, len(entities))
	if !h.cfg.ProposeEntities || len(entities) == 0 {
		return out
	}
	latest := h.latestActions(r, c, ex.ID)
	allowed := map[authz.Resource]bool{}
	for _, e := range entities {
		v := proposedEntityView{ProposedEntity: e}
		res, known := proposedEntityResources[e.Kind]
		if known {
			ok, seen := allowed[res]
			if !seen {
				d, err := h.check(r.Context(), c, res, authz.ActionCreate)
				ok = err == nil && d.Allowed
				allowed[res] = ok
			}
			v.CanCreate = ok
		}
		if !v.CanCreate {
			v.Reason = "You don't have permission to create " + proposedEntityNouns[e.Kind] + "."
			if !known {
				v.Reason = "This record type can't be created from a document."
			}
		}
		if a, ok := latest[ex.ID+sourceRefSep+e.Key]; ok {
			v.Action = &a
		}
		out = append(out, v)
	}
	return out
}

// latestActions returns the caller's newest proposal per sourceRef for the
// extraction. Any failure is logged and yields no actions.
func (h *DocExtractOps) latestActions(r *http.Request, c *docExtractCaller, extractionID string) map[string]proposedaction.Action {
	if h.newActionStore == nil {
		return nil
	}
	rows, err := h.newActionStore(c.pool).ListBySourceRef(r.Context(), c.identityID, proposedaction.SourceDocExtract, extractionID+sourceRefSep)
	if err != nil {
		slog.WarnContext(r.Context(), "doc extract: list proposed actions", "extractionId", extractionID, "error", err)
		return nil
	}
	latest := make(map[string]proposedaction.Action, len(rows))
	for _, a := range rows {
		var p struct {
			SourceRef string `json:"sourceRef"`
		}
		if json.Unmarshal(a.Payload, &p) != nil || p.SourceRef == "" {
			continue
		}
		if cur, seen := latest[p.SourceRef]; !seen || a.CreatedAt.After(cur.CreatedAt) {
			latest[p.SourceRef] = a
		}
	}
	return latest
}
