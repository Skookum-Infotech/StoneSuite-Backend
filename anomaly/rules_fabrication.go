package anomaly

// Rule keys for the fabrication rules, stored in anomaly_rule_key.
const (
	RuleJobStuck            = "fabrication_job_stuck"
	RuleInstallOverdue      = "install_overdue"
	RuleScheduleOverdue     = "schedule_activity_overdue"
	RuleManyTemplateRevs    = "many_template_revisions"
	RuleMultipleJobsPerSale = "multiple_jobs_per_order"
)

// jobIdleStatuses are job statuses that are not expected to move: not started,
// finished or cancelled.
var jobIdleStatuses = []string{"DRFT", "COMP", "CANC"}

func fabricationRules() []Rule {
	return []Rule{
		// A job that has sat in one status (or on hold) too long.
		sqlRule{
			key: RuleJobStuck, severity: SeverityWarning, title: "%s has not moved for a while",
			audience: audience{owner: true, approvers: "installation"},
			args:     []any{jobIdleStatuses, jobStuckDays, jobHoldStuckDays, longLookbackDays},
			query: `
				SELECT j.fabrication_job_uuid::text || ':' || st.record_status_code, j.fabrication_job_uuid::text, 'installation',
				       'Job ' || COALESCE(j.fabrication_job_number, ''),
				       'In "' || st.record_status_name || '" since ' || to_char(a.last_at, 'YYYY-MM-DD') || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, a.last_at
				FROM fabrication_job j
				JOIN lkp_record_status st ON st.record_status_id = j.fabrication_job_status
				JOIN LATERAL (
				    SELECT COALESCE(MAX(h.at), j.fabrication_job_created_at) AS last_at
				    FROM fabrication_job_history h WHERE h.fabrication_job_id = j.fabrication_job_id) a ON TRUE
				` + ownerJoin("j", "job_owner_id") + `
				WHERE j.fabrication_job_deleted_at IS NULL AND ` + stNotIn("$4") + `
				  AND a.last_at < $3::date - (CASE WHEN st.record_status_code = 'HOLD' THEN $6::int ELSE $5::int END)
				  AND a.last_at >= $3::date - $7::int`,
		},

		// A job past its promised install date and not finished.
		sqlRule{
			key: RuleInstallOverdue, severity: SeverityWarning, title: "%s is past its promised install date",
			audience: audience{owner: true, approvers: "installation"},
			args:     []any{jobIdleStatuses, longLookbackDays},
			query: `
				SELECT j.fabrication_job_uuid::text || ':' || j.job_promised_install_date::text, j.fabrication_job_uuid::text, 'installation',
				       'Job ' || COALESCE(j.fabrication_job_number, ''),
				       'Promised for ' || to_char(j.job_promised_install_date, 'YYYY-MM-DD') || ' ('
				         || ($3::date - j.job_promised_install_date)::text || ' days ago) and not completed.',
				       ` + ownerIdentity + `, ` + involvedNone + `, j.job_promised_install_date::timestamp
				FROM fabrication_job j
				JOIN lkp_record_status st ON st.record_status_id = j.fabrication_job_status
				` + ownerJoin("j", "job_owner_id") + `
				WHERE j.fabrication_job_deleted_at IS NULL AND ` + stNotIn("$4") + `
				  AND j.job_actual_install_date IS NULL AND j.job_promised_install_date < $3::date
				  AND j.job_promised_install_date >= $3::date - $5::int`,
		},

		// Schedule activities on an order that are late and unfinished, at most once a month.
		sqlRule{
			key: RuleScheduleOverdue, severity: SeverityWarning, title: "%s has late scheduled activities",
			audience: audience{owner: true, approvers: "sales_order"},
			args:     []any{scheduleOverdueDays, longLookbackDays},
			query: `
				SELECT so.sales_order_uuid::text || ':' || to_char($3::date, 'YYYY-MM'), so.sales_order_uuid::text, 'sales_order',
				       'Sales Order ' || COALESCE(so.sales_order_number, ''),
				       g.n::text || ' scheduled activit' || CASE WHEN g.n = 1 THEN 'y is' ELSE 'ies are' END
				         || ' more than ' || $4::int::text || ' days late; the earliest was due ' || to_char(g.first_at, 'YYYY-MM-DD') || '.',
				       ` + ownerIdentity + `, ` + involvedNone + `, g.first_at
				FROM (
				    SELECT s.sales_order_id, COUNT(*) AS n, MIN(s.scheduled_at) AS first_at
				    FROM sales_order_schedule s
				    WHERE s.schedule_status IN ('pending', 'in_progress')
				      AND s.scheduled_at < $3::date - $4::int AND s.scheduled_at >= $3::date - $5::int
				    GROUP BY s.sales_order_id) g
				JOIN sales_order so ON so.sales_order_id = g.sales_order_id AND so.sales_order_deleted_at IS NULL
				JOIN lkp_record_status st ON st.record_status_id = so.sales_order_status AND st.record_status_code <> 'CANC'
				` + ownerJoin("so", "sales_order_owner_id"),
		},

		// A job whose template has been redone many times.
		sqlRule{
			key: RuleManyTemplateRevs, severity: SeverityWarning, title: "%s has had its template redone many times",
			audience: audience{owner: true, superAdmins: true},
			args:     []any{templateRevisionCount},
			query: `
				SELECT j.fabrication_job_uuid::text, j.fabrication_job_uuid::text, 'installation',
				       'Job ' || COALESCE(j.fabrication_job_number, ''),
				       g.n::text || ' template revisions so far.',
				       ` + ownerIdentity + `, ` + involvedNone + `, g.latest
				FROM (
				    SELECT t.fabrication_job_id, COUNT(*) AS n, MAX(t.created_at) AS latest
				    FROM fabrication_template_revision t GROUP BY t.fabrication_job_id HAVING COUNT(*) >= $4::int) g
				JOIN fabrication_job j ON j.fabrication_job_id = g.fabrication_job_id AND j.fabrication_job_deleted_at IS NULL
				` + ownerJoin("j", "job_owner_id"),
		},

		// More than one fabrication job on one order. Allowed by design, so information only.
		sqlRule{
			key: RuleMultipleJobsPerSale, severity: SeverityInfo, title: "%s has more than one fabrication job",
			audience: audience{superAdmins: true},
			args:     []any{jobsPerOrderCount},
			query: `
				SELECT so.sales_order_uuid::text || ':' || g.n::text, so.sales_order_uuid::text, 'sales_order',
				       'Sales Order ' || COALESCE(so.sales_order_number, ''),
				       g.n::text || ' fabrication jobs exist for this order.',
				       ` + involvedNone + `, ` + involvedNone + `, g.latest
				FROM (
				    SELECT j.sales_order_id, COUNT(*) AS n, MAX(j.fabrication_job_created_at) AS latest
				    FROM fabrication_job j JOIN lkp_record_status st ON st.record_status_id = j.fabrication_job_status
				    WHERE j.fabrication_job_deleted_at IS NULL AND st.record_status_code <> 'CANC'
				    GROUP BY j.sales_order_id HAVING COUNT(*) >= $4::int) g
				JOIN sales_order so ON so.sales_order_id = g.sales_order_id AND so.sales_order_deleted_at IS NULL`,
		},
	}
}
