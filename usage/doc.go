// Package usage meters what each tenant consumes for the Subscription page:
// LLM tokens spent on the self-hosted Ollama (recorded per call by
// MeteredLLM into a daily rollup, summed per monthly billing period — the
// count resets each period) and the overall size of the tenant's own R2
// bucket (a running total, not per period; measured on demand by
// StorageRefresher). Both live in the control-plane
// database and every query filters explicitly by tenant_id. Plan limits are
// not enforced here yet; the API leaves room for them.
package usage
