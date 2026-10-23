package proposedentity

import "encoding/json"

// parseCustomer strictly decodes, validates and cleans a customer payload.
func parseCustomer(raw json.RawMessage) (CustomerPayload, error) {
	var p CustomerPayload
	if err := strictDecode(raw, &p); err != nil {
		return p, err
	}
	if err := p.validate(); err != nil {
		return p, err
	}
	p.clean()
	return p, nil
}

// clean applies cleanText to every field.
func (p *CustomerPayload) clean() {
	p.Name = cleanText(p.Name, maxCustomerName)
	p.ContactEmail = cleanText(p.ContactEmail, maxEmail)
	p.TaxID = cleanText(p.TaxID, maxTaxID)
	p.Address = cleanText(p.Address, maxAddress)
}

// validate is pure: it checks shape and limits without touching the database.
func (p CustomerPayload) validate() error {
	if err := checkSourceRef(p.SourceRef); err != nil {
		return err
	}
	if err := checkText("name", p.Name, maxCustomerName, true); err != nil {
		return err
	}
	if err := checkEmail("contactEmail", p.ContactEmail, true); err != nil {
		return err
	}
	if err := checkText("taxId", p.TaxID, maxTaxID, false); err != nil {
		return err
	}
	return checkText("address", p.Address, maxAddress, false)
}

// parseVendor strictly decodes, validates and cleans a vendor payload.
func parseVendor(raw json.RawMessage) (VendorPayload, error) {
	var p VendorPayload
	if err := strictDecode(raw, &p); err != nil {
		return p, err
	}
	if err := p.validate(); err != nil {
		return p, err
	}
	p.clean()
	return p, nil
}

// clean applies cleanText to every field.
func (p *VendorPayload) clean() {
	p.Name = cleanText(p.Name, maxVendorName)
	p.Email = cleanText(p.Email, maxEmail)
	p.Address = cleanText(p.Address, maxAddress)
	p.TaxID = cleanText(p.TaxID, maxTaxID)
}

// validate is pure: it checks shape and limits without touching the database.
func (p VendorPayload) validate() error {
	if err := checkSourceRef(p.SourceRef); err != nil {
		return err
	}
	if err := checkText("name", p.Name, maxVendorName, true); err != nil {
		return err
	}
	if err := checkEmail("email", p.Email, false); err != nil {
		return err
	}
	if err := checkText("address", p.Address, maxAddress, false); err != nil {
		return err
	}
	return checkText("taxId", p.TaxID, maxTaxID, false)
}

// parseItem strictly decodes, validates and cleans an item payload.
func parseItem(raw json.RawMessage) (ItemPayload, error) {
	var p ItemPayload
	if err := strictDecode(raw, &p); err != nil {
		return p, err
	}
	if err := p.validate(); err != nil {
		return p, err
	}
	p.clean()
	return p, nil
}

// clean applies cleanText to every field.
func (p *ItemPayload) clean() {
	p.SKU = cleanText(p.SKU, maxSKU)
	p.Name = cleanText(p.Name, maxItemName)
	p.Description = cleanText(p.Description, maxDescription)
	p.UnitCode = cleanText(p.UnitCode, maxUnitCode)
	p.DocUoM = cleanText(p.DocUoM, maxDocUoM)
}

// validate is pure: it checks shape and limits without touching the database.
func (p ItemPayload) validate() error {
	if err := checkSourceRef(p.SourceRef); err != nil {
		return err
	}
	if err := checkIdent("sku", p.SKU, maxSKU, true); err != nil {
		return err
	}
	if err := checkText("name", p.Name, maxItemName, true); err != nil {
		return err
	}
	if err := checkText("description", p.Description, maxDescription, false); err != nil {
		return err
	}
	if err := checkIdent("unitCode", p.UnitCode, maxUnitCode, false); err != nil {
		return err
	}
	if err := checkIdent("docUoM", p.DocUoM, maxDocUoM, false); err != nil {
		return err
	}
	if p.UnitID <= 0 {
		return invalid("unitId is required")
	}
	if p.UnitPriceCents < 0 || p.UnitPriceCents > maxPriceCents {
		return invalid("unitPriceCents is out of range")
	}
	if p.PurchaseUnitPriceCents < 0 || p.PurchaseUnitPriceCents > maxPriceCents {
		return invalid("purchaseUnitPriceCents is out of range")
	}
	return nil
}
