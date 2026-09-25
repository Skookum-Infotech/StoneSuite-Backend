package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// crmRecordsListPath is GET /api/tenant/crm/{workflowKey}/records. It does
// not accept limit/cursor query params (controllers.CRMOps.ListRecords
// ignores the query string entirely and returns every record the caller's
// scope permits) — the ?limit= below is sent for forward compatibility only
// and has no effect today; nextCursor, if this endpoint ever grows
// pagination, is followed defensively by fetchAllRecords.
const crmRecordsListPathFmt = "/api/tenant/crm/%s/records?limit=100"

// crmRecordsListResponse is the subset of ListRecords'/SearchRecords' JSON
// this tool reads.
type crmRecordsListResponse struct {
	Success    bool            `json:"success"`
	Records    []crmRecordJSON `json:"records"`
	NextCursor string          `json:"nextCursor"`
}

// crmRecordJSON is the subset of workflow.Record this tool reads.
type crmRecordJSON struct {
	RecordNumber string         `json:"recordNumber"`
	CoreFields   map[string]any `json:"coreFields"`
}

// maxRecordPages bounds the defensive pagination loop in fetchAllRecords —
// today's ListRecords never sets nextCursor, so this only guards against a
// future pagination bug looping forever.
const maxRecordPages = 200

// fetchAllRecords pages GET /api/tenant/crm/{workflowKey}/records to
// completion via nextCursor and returns every record. Read-only, no RBAC
// mutation.
func (c *apiClient) fetchAllRecords(ctx context.Context, workflowKey string) ([]crmRecordJSON, error) {
	var all []crmRecordJSON
	path := fmt.Sprintf(crmRecordsListPathFmt, workflowKey)
	for page := 0; page < maxRecordPages; page++ {
		req, err := c.authedRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list %s records: %w", workflowKey, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxStatusBodyBytes*16))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read %s records body: %w", workflowKey, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list %s records: status %d: %s", workflowKey, resp.StatusCode, raw)
		}
		var parsed crmRecordsListResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("decode %s records: %w", workflowKey, err)
		}
		all = append(all, parsed.Records...)
		if parsed.NextCursor == "" {
			break
		}
		path = fmt.Sprintf(crmRecordsListPathFmt, workflowKey) + "&cursor=" + parsed.NextCursor
	}
	return all, nil
}

// leadSample is a single lead record's placeholder-relevant fields.
type leadSample struct {
	Number string
	City   string
	Name   string
}

// fetchLeadSample returns the first lead record's number, city
// (customer_addr_city), and name (customer_name) for the {{lead.*}}
// placeholders. Errors if the tenant has no lead records — an e2e run needs
// at least one to exercise the lookup cases.
func (c *apiClient) fetchLeadSample(ctx context.Context) (leadSample, error) {
	records, err := c.fetchAllRecords(ctx, "lead")
	if err != nil {
		return leadSample{}, fmt.Errorf("fetch lead sample: %w", err)
	}
	if len(records) == 0 {
		return leadSample{}, fmt.Errorf("fetch lead sample: tenant has no lead records")
	}
	rec := records[0]
	city, _ := rec.CoreFields["customer_addr_city"].(string)
	name, _ := rec.CoreFields["customer_name"].(string)
	return leadSample{Number: rec.RecordNumber, City: city, Name: name}, nil
}

// fetchCount returns how many records of workflowKey the tenant has.
func (c *apiClient) fetchCount(ctx context.Context, workflowKey string) (int, error) {
	records, err := c.fetchAllRecords(ctx, workflowKey)
	if err != nil {
		return 0, fmt.Errorf("fetch %s count: %w", workflowKey, err)
	}
	return len(records), nil
}

// fetchPlaceholders resolves every {{...}} token cases.json may reference,
// in one pass, for resolveCase to substitute at run time.
func (c *apiClient) fetchPlaceholders(ctx context.Context) (map[string]string, error) {
	lead, err := c.fetchLeadSample(ctx)
	if err != nil {
		return nil, err
	}
	ph := map[string]string{
		placeholderLeadNumber: lead.Number,
		placeholderLeadCity:   lead.City,
		placeholderLeadName:   lead.Name,
	}
	for token, key := range map[string]string{
		placeholderCountLead:     "lead",
		placeholderCountCustomer: "customer",
		placeholderCountProspect: "prospect",
	} {
		n, err := c.fetchCount(ctx, key)
		if err != nil {
			return nil, err
		}
		ph[token] = fmt.Sprintf("%d", n)
	}
	return ph, nil
}
