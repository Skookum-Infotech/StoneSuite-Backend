package installschedule

import (
	"context"
	"fmt"
)

// CrewOption is one assignable employee.
type CrewOption struct {
	EmployeeID int    `json:"employeeId"`
	Name       string `json:"name"`
	HasLogin   bool   `json:"hasLogin"`
}

// CrewOptions lists active, non-system, non-deleted employees for crew pickers.
func CrewOptions(ctx context.Context, q Querier) ([]CrewOption, error) {
	rows, err := q.Query(ctx, `
		SELECT employee_id, btrim(employee_first_name || ' ' || employee_last_name), (employee_user_id IS NOT NULL)
		FROM employee
		WHERE employee_is_active AND NOT employee_is_system AND employee_deleted_at IS NULL
		ORDER BY employee_first_name, employee_last_name, employee_id`)
	if err != nil {
		return nil, fmt.Errorf("list crew options: %w", err)
	}
	defer rows.Close()
	out := []CrewOption{}
	for rows.Next() {
		var o CrewOption
		if err := rows.Scan(&o.EmployeeID, &o.Name, &o.HasLogin); err != nil {
			return nil, fmt.Errorf("scan crew option: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crew options: %w", err)
	}
	return out, nil
}
