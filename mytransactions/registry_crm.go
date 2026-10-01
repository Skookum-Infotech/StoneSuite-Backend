package mytransactions

import "stonesuite-backend/authz"

// crmStage is one CRM stage: Lead, Prospect and Customer are views of the
// single customer table, discriminated by lkp_record_type.record_type_code
// (see crmstore's crmKeyToCode).
type crmStage struct {
	key, label, typeCode string
	resource             authz.Resource
}

var crmStages = []crmStage{
	{"lead", "Lead", "LEAD", authz.ResourceLead},
	{"prospect", "Prospect", "PROS", authz.ResourceProspect},
	{"customer", "Customer", "CUST", authz.ResourceCustomer},
}

// crmSources builds the three CRM sources. The customer table records
// customer_created_by but no updated_by, so "updated by me" comes from the audit
// trail (AuditUpdated); own-scope narrows on the CRM owner, as crmstore does.
func crmSources() []Source {
	out := make([]Source, 0, len(crmStages))
	for _, st := range crmStages {
		out = append(out, Source{
			Key: st.key, Label: st.label, Resource: st.resource, Domain: "crm", Module: st.key,
			Table: "customer",
			Joins: "JOIN lkp_record_type rt ON rt.record_type_id = t.record_type " +
				"LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = t.customer_crm_status",
			Where:      "rt.record_type_code = '" + st.typeCode + "'",
			ID:         "t.customer_uuid",
			Number:     "t.customer_doc_num",
			Name:       "t.customer_name",
			StatusName: "cs.crm_status_name",
			StatusCode: "cs.crm_status_code",
			CreatedBy:  "customer_created_by",
			CreatedAt:  "customer_created_at",
			UpdatedAt:  "customer_updated_at",
			DeletedAt:  "customer_deleted_at",
			Owner:      "customer_crm_owner_user_id",

			AuditUpdated: true,
		})
	}
	return out
}
