package automation

// ARDunning is the accounts-receivable payment-reminder job.
type ARDunning struct{ deps Deps }

// Name identifies the job in logs.
func (*ARDunning) Name() string { return RuleKeyARDunning }
