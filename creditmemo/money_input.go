package creditmemo

// validateCreateMoney enforces that a new credit memo carries its money either
// as a single amount or as line items (kept for API clients and for memos that
// already have lines) -- never both, and never neither.
func validateCreateMoney(amount float64, lines []CreditMemoLineInput) error {
	switch {
	case amount != 0 && len(lines) > 0:
		return ClientError{Msg: "send either an amount or lines, not both."}
	case len(lines) > 0:
		return nil
	case amount < 0:
		return ClientError{Msg: "amount must be positive."}
	case amount == 0:
		return ClientError{Msg: "a credit memo needs an amount."}
	}
	return nil
}

// validateUpdateMoney is the PATCH counterpart: an omitted amount leaves the
// money as it is, so only a supplied one is checked.
func validateUpdateMoney(amount *float64, lines []CreditMemoLineInput) error {
	if amount == nil {
		return nil
	}
	if lines != nil {
		return ClientError{Msg: "send either an amount or lines, not both."}
	}
	if *amount <= 0 {
		return ClientError{Msg: "amount must be positive."}
	}
	return nil
}
