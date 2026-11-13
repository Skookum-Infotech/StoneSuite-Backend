//go:build dbtest

package controllers

import (
	"testing"

	"stonesuite-backend/tenancy"
)

// newTestRouter is tenancy.NewRouter(nil) whose tenant pools are closed when the
// test ends. A bare NewRouter leaks every pool it opens (up to maxConns idle
// connections each), and this package builds hundreds of routers in one process
// against one Postgres, so leaking them exhausts max_connections ("too many
// clients already") and the last tests to run fail with 500s.
func newTestRouter(t testing.TB) *tenancy.Router {
	t.Helper()
	router := tenancy.NewRouter(nil)
	t.Cleanup(router.Close)
	return router
}
