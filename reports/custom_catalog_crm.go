package reports

import (
	"fmt"

	"stonesuite-backend/authz"
)

// crmSource builds one CRM stage source. Leads, prospects and customers are
// all rows of the customer table told apart by record type (relational design
// only, hence RequiresV2). stage is the lkp_record_type code.
func crmSource(key, label, stage string, extra ...Field) Source {
	joins, order := buildJoins(
		joinDef{name: "crmstatus", sql: "LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status"},
		joinDef{name: "csource", sql: "LEFT JOIN lkp_crm_lead_source cls ON cls.lead_source_id = c.customer_lead_source"},
		joinDef{name: "ctype", sql: "LEFT JOIN lkp_customer_type ct ON ct.customer_type_id = c.customer_type"},
		joinDef{name: "cstate", sql: "LEFT JOIN lkp_state cst ON cst.state_id = c.customer_addr_state"},
		joinDef{name: "owner", sql: "LEFT JOIN employee ow ON ow.employee_id = c.customer_crm_owner_user_id"},
		joinDef{name: "arstatus", sql: "LEFT JOIN lkp_customer_ar_status ars ON ars.customer_ar_status_id = c.customer_ar_status"},
	)
	own := []Field{
		text("name", "Name", "c.customer_name", ""),
		text("number", "Number", "c.customer_doc_num", ""),
		status("status", "Status", "cs.crm_status_name", "crmstatus", stage),
		text("lead_source", "Lead source", "cls.lead_source_name", "csource"),
		text("type", "Type", "ct.customer_type_name", "ctype"),
		text("city", "City", "c.customer_addr_city", ""),
		text("state", "State", "cst.state_name", "cstate"),
		text("email", "Email", "c.customer_contact_email", ""),
		text("owner", "Owner", personName("ow"), "owner"),
		date("created", "Created", "c.customer_created_at::date", ""),
	}
	return Source{
		Key: key, Label: label, Module: moduleCRM, Resource: authz.ResourceReportCRM, RequiresV2: true,
		from: "customer c JOIN lkp_record_type rt ON rt.record_type_id = c.record_type AND rt.record_type_code = " +
			fmt.Sprintf("'%s'", stage),
		where:     []string{"c.customer_deleted_at IS NULL"},
		ownerExpr: "c.customer_crm_owner_user_id",
		joins:     joins, joinOrder: order,
		Fields: concat(own, extra),
	}
}

func crmSources() []Source {
	return []Source{
		crmSource("leads", "Leads", "LEAD"),
		crmSource("prospects", "Prospects", "PROS"),
		crmSource("customers", "Customers", "CUST",
			text("ar_status", "AR status", "ars.customer_ar_status_name", "arstatus"),
			money("credit_limit", "Credit limit", "c.customer_credit_limit", ""),
		),
	}
}
