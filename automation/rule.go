package automation

// RuleKeyARDunning is the rule_key of the seeded accounts-receivable dunning rule.
const RuleKeyARDunning = "ar_dunning"

// ARCadenceStage is one reminder stage, as days relative to the invoice due date.
type ARCadenceStage struct {
	OffsetDays int `json:"offsetDays"`
}

// ARRuleConfig is the JSON shape stored in automation_rules.rule for ar_dunning.
type ARRuleConfig struct {
	Cadence []ARCadenceStage `json:"cadence"`
}
