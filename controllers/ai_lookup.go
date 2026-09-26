package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// recordNumberRe matches a StoneSuite record number like LEAD-000012: two or
// more uppercase letters, a hyphen, three or more digits. Loose by design —
// a tenant's numbering prefix/digit width is admin-configurable
// (workflow.NumberingConfig), so this matches any of them rather than one
// hardcoded format.
var recordNumberRe = regexp.MustCompile(`\b[A-Z]{2,}-\d{3,}\b`)

// lookupFieldAliases maps a plain-English field word to the CoreFields key it
// resolves to on a CRM record — see crmstore/relational_store.go's
// customerFields registry (customer_addr_city, customer_primary_phonenum,
// customer_contact_email, customer_addr_line1, customer_name) and
// scanRecord's crm_status_name. "owner" is deliberately not aliased: CoreFields
// only carries the raw owner_user_id, and echoing an opaque id back as an
// "answer" would be worse than not answering — a name lookup would need a
// second store call this pass doesn't add.
var lookupFieldAliases = map[string]string{
	"city":    "customer_addr_city",
	"phone":   "customer_primary_phonenum",
	"email":   "customer_contact_email",
	"address": "customer_addr_line1",
	"status":  "crm_status_name",
	"company": "customer_name",
	"name":    "customer_name",
}

// lookupFieldWordRe finds the first lookupFieldAliases word a question names.
var lookupFieldWordRe = regexp.MustCompile(`(?i)\b(city|phone|email|address|status|company|name)\b`)

// lookupNotFoundAnswer is returned for both "no record with this number
// exists" and "exists but outside the caller's scope" — identically, the same
// IDOR-safe convention as recordInScope's 404 (controllers/scope.go):
// existence must never be distinguishable from "not yours".
const lookupNotFoundAnswer = "I couldn't find a record numbered %s that you have access to."

// extractRecordNumber returns the first record number question names, if any.
func extractRecordNumber(question string) (string, bool) {
	m := recordNumberRe.FindString(question)
	return m, m != ""
}

// lookupTemplateMatch reports, without touching the store, whether question
// has the record-number fast path's shape: a record number (matched), and
// whether it also names a recognized field (hasField). needsModel uses this
// to decide whether a model slot is worth acquiring at all — a number plus a
// recognized field is guaranteed to resolve to a zero-LLM template answer.
func lookupTemplateMatch(question string) (number string, hasField, matched bool) {
	number, matched = extractRecordNumber(question)
	if !matched {
		return "", false, false
	}
	_, _, hasField = lookupField(question)
	return number, hasField, true
}

// lookupField returns the alias word and the CoreFields key it resolves to,
// if question names one of lookupFieldAliases.
func lookupField(question string) (alias, coreKey string, ok bool) {
	m := lookupFieldWordRe.FindString(strings.ToLower(question))
	if m == "" {
		return "", "", false
	}
	coreKey, ok = lookupFieldAliases[m]
	return m, coreKey, ok
}

// lookupValueText renders a CoreFields value for the template answer, "" +
// false for a nil or blank one (nothing on file).
func lookupValueText(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, strings.TrimSpace(x) != ""
	default:
		return fmt.Sprintf("%v", x), true
	}
}

// findRecordByNumber searches every CRM type the caller can read, under that
// type's own scope, for a record with this number — the same scope-safe
// query.Request + Store.SearchRecords every list endpoint uses, so this fast
// path can never reach a record outside the caller's grants. Stops at the
// first match; record numbers are per-workflow sequences (see
// workflow.GenerateRecordNumber), so a collision across types would only
// happen under an admin-chosen prefix collision, not by default.
func findRecordByNumber(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, identityID, number string) (workflow.Record, bool) {
	req := query.Request{Filters: []query.Clause{{Field: "record_number", Op: query.OpEq, Value: number}}, Limit: 1}
	for _, key := range grants.Types() {
		page, err := store.SearchRecords(ctx, pool, key, grants[key], identityID, req)
		if err != nil {
			slog.Warn("ai record lookup: search failed", "key", key, "err", err)
			continue
		}
		if len(page.Records) > 0 {
			return page.Records[0], true
		}
	}
	return workflow.Record{}, false
}

// lookupRecordAnswer implements the record-number fast path (routeLookup):
// a question naming a specific record number AND a recognized field
// (lookupFieldAliases) is answered from a template, zero LLM calls, citing
// the record. matched=false means "not this path" — the caller falls
// through to the normal count/RAG dispatch.
//
// A record number with no recognized field (e.g. "tell me about
// LEAD-000012") also returns matched=false rather than restricting RAG to
// just that one record: the library has no ready-made single-record corpus,
// and building one is a bigger change than this pass covers. It falls
// through to the caller's normal scoped RAG instead, which still retrieves
// this record via the records corpus (classifyIntent's record-number cue
// biases that path toward IntentData).
func lookupRecordAnswer(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, identityID, question string) (ragcore.AskResult, bool) {
	number, ok := extractRecordNumber(question)
	if !ok {
		return ragcore.AskResult{}, false
	}
	alias, coreKey, hasField := lookupField(question)
	if !hasField {
		return ragcore.AskResult{}, false
	}
	if len(grants.Types()) == 0 {
		return ragcore.AskResult{Answer: fmt.Sprintf(lookupNotFoundAnswer, number), Citations: []ragcore.Citation{}}, true
	}
	rec, found := findRecordByNumber(ctx, store, pool, grants, identityID, number)
	if !found {
		return ragcore.AskResult{Answer: fmt.Sprintf(lookupNotFoundAnswer, number), Citations: []ragcore.Citation{}}, true
	}
	text, hasValue := lookupValueText(rec.CoreFields[coreKey])
	if !hasValue {
		return ragcore.AskResult{
			Answer:    fmt.Sprintf("%s doesn't have a %s on file.", number, alias),
			Citations: []ragcore.Citation{{SourceType: ai.CorpusRecords, SourceID: rec.ID, Snippet: number}},
		}, true
	}
	return ragcore.AskResult{
		Answer: fmt.Sprintf("%s's %s is %s.", number, alias, text),
		Citations: []ragcore.Citation{
			{SourceType: ai.CorpusRecords, SourceID: rec.ID, Snippet: fmt.Sprintf("%s: %s", alias, text)},
		},
	}, true
}
