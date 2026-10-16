package automation

import "context"

// SystemEmployeeID is the tenant schema's seeded system employee
// (employee_id = 1, employee_is_system = TRUE). Employee-keyed columns written
// by automation use it; user-keyed actor fields stay empty.
const SystemEmployeeID = 1

type systemActorKey struct{}

// WithSystemActor marks ctx as running on behalf of the automation engine, so
// the automation audit observer ignores writes it caused (loop guard).
func WithSystemActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemActorKey{}, true)
}

// IsSystemActor reports whether ctx was marked by WithSystemActor.
func IsSystemActor(ctx context.Context) bool {
	v, _ := ctx.Value(systemActorKey{}).(bool)
	return v
}
