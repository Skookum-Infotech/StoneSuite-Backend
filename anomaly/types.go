// Package anomaly detects records and events that look wrong (an invariant
// broken, a separation-of-duties gap, a probable duplicate), stores each one
// once, and tells the people who can act on it.
//
// Detection is a set of Rules, each a read-only SQL scan over one tenant
// database. Scan stores the findings; Alert sends the bell/email notification
// for any finding not yet announced; review.go serves the Anomalies page.
// The automation tick (automation/anomaly_job.go) drives Scan and Alert, so
// the platform automation switch gates this package like every other job.
package anomaly

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Severity ranks how urgent a finding is. It also picks the alert channel:
// critical findings are emailed as well as shown in the bell.
type Severity string

// Severity levels, mirrored by chk_anomaly_severity in tenant/schema.sql.
const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Status is where a finding stands in review, mirrored by chk_anomaly_status.
type Status string

// Review statuses.
const (
	StatusOpen      Status = "open"
	StatusReviewed  Status = "reviewed"
	StatusDismissed Status = "dismissed"
	StatusConfirmed Status = "confirmed"
)

// Valid reports whether s is one of the known review statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusReviewed, StatusDismissed, StatusConfirmed:
		return true
	}
	return false
}

// Rule keys, stored in anomaly.anomaly_rule_key.
const (
	RuleSelfApproval        = "self_approval"
	RuleUnbalancedJournal   = "unbalanced_journal"
	RuleDuplicateVendorBill = "duplicate_vendor_bill"
)

const (
	// MaxFindingsPerRule bounds how many new findings one rule may raise in a
	// single scan, so a bad import can't flood the table or the bell.
	MaxFindingsPerRule = 200

	maxRuleKeyLen    = 60
	maxSubjectKeyLen = 120
	maxTitleLen      = 200
	ellipsis         = "…"
)

// Audience says who a finding is addressed to, before the person who caused it
// is removed. SuperAdmins adds every active super admin; IdentityIDs are
// control-plane identity ids (users.identity_id) the rule resolved itself,
// e.g. the record owner or the module's approvers.
type Audience struct {
	SuperAdmins bool
	IdentityIDs []string
}

// Finding is one anomaly a rule detected.
type Finding struct {
	RuleKey string
	// SubjectKey is unique per (RuleKey, condition): a rescan that finds the
	// same subject again is a no-op, and a dismissed finding never reopens.
	SubjectKey string
	Severity   Severity
	// Resource is the RBAC resource of the affected record. The notify bell
	// only shows a recipient entries for resources they can read, so it must
	// be the record's own resource, not a made-up "anomaly" one.
	Resource   string
	RecordUUID string // "" when the finding is not about one record
	Title      string
	Detail     string
	// InvolvedIdentityID is the person whose action tripped the rule. They are
	// never alerted: telling a fraudster about their own finding helps no one.
	InvolvedIdentityID string
	Audience           Audience
}

var (
	errFindingRuleKey    = errors.New("rule key is empty or too long")
	errFindingSubjectKey = errors.New("subject key is empty or too long")
	errFindingTitle      = errors.New("title is empty")
	errFindingResource   = errors.New("resource is empty")
	errFindingSeverity   = errors.New("unknown severity")
)

// validate rejects a finding the table could not store, so a rule bug is
// reported as an error instead of a failed INSERT.
func (f Finding) validate() error {
	switch {
	case f.RuleKey == "" || len(f.RuleKey) > maxRuleKeyLen:
		return errFindingRuleKey
	case f.SubjectKey == "" || len(f.SubjectKey) > maxSubjectKeyLen:
		return errFindingSubjectKey
	case strings.TrimSpace(f.Title) == "":
		return errFindingTitle
	case f.Resource == "":
		return errFindingResource
	}
	switch f.Severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return nil
	}
	return fmt.Errorf("%w %q", errFindingSeverity, f.Severity)
}

// clip shortens s to at most max runes, ending with an ellipsis when it cut.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + ellipsis
}
