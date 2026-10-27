package automation

// RuleKeyARDunning is the rule_key of the seeded accounts-receivable dunning rule.
const RuleKeyARDunning = "ar_dunning"

// RuleKeyQuoteExpiry is the rule_key of the seeded quote expiry reminder rule.
const RuleKeyQuoteExpiry = "quote_expiry"

// RuleKeyEstimateExpiry is the rule_key of the seeded estimate expiry reminder rule.
const RuleKeyEstimateExpiry = "estimate_expiry"

// RuleKeyApprovalStuck is the rule_key of the seeded stuck-approval reminder rule.
const RuleKeyApprovalStuck = "approval_stuck"

// RuleKeyAnomalyScan is the rule_key of the seeded anomaly scan switch. The rule carries no
// settings: its enabled flag is the whole workspace-level on/off control.
const RuleKeyAnomalyScan = "anomaly_scan"

// ARCadenceStage is one reminder stage, as days relative to the invoice due date.
type ARCadenceStage struct {
	OffsetDays int `json:"offsetDays"`
}

// ARRuleConfig is the JSON shape stored in automation_rules.rule for ar_dunning.
type ARRuleConfig struct {
	Cadence []ARCadenceStage `json:"cadence"`
}
