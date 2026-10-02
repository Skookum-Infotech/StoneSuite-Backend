package salesorder

// overrideAddress layers a caller-supplied partial address over a default
// (e.g. the customer's snapshot), preferring the override for any non-empty
// field.
func overrideAddress(def, override AddressInput) AddressInput {
	out := def
	if override.CustomerName != "" {
		out.CustomerName = override.CustomerName
	}
	if override.Attention != "" {
		out.Attention = override.Attention
	}
	if override.AddrLine1 != "" {
		out.AddrLine1 = override.AddrLine1
	}
	if override.AddrLine2 != "" {
		out.AddrLine2 = override.AddrLine2
	}
	if override.SuiteUnit != "" {
		out.SuiteUnit = override.SuiteUnit
	}
	if override.City != "" {
		out.City = override.City
	}
	if override.StateID != nil {
		out.StateID = override.StateID
	}
	if override.Zip != "" {
		out.Zip = override.Zip
	}
	if override.CountryID != nil {
		out.CountryID = override.CountryID
	}
	if override.Phone != "" {
		out.Phone = override.Phone
	}
	if override.Fax != "" {
		out.Fax = override.Fax
	}
	if override.Email != "" {
		out.Email = override.Email
	}
	return out
}

// orNow returns the given "yyyy-mm-dd" date string, or today when blank
// (sales_order_date has a CURRENT_DATE default, but Create always supplies an
// explicit value so the stored and returned dates agree).
func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

// nullableDate returns the given "yyyy-mm-dd" string as a nullable date arg.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}
