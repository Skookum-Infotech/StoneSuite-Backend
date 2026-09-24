package aisettings

// Status is the combined on/off state of the AI assistant for one tenant: the
// platform master switch, this tenant's own switch, and whether both are on.
type Status struct {
	PlatformEnabled bool `json:"platformEnabled"`
	TenantEnabled   bool `json:"tenantEnabled"`
	Available       bool `json:"available"`
}

// Available reports whether the assistant is usable: both the platform
// master switch and the tenant's own switch must be on.
func Available(platform, tenant bool) bool {
	return platform && tenant
}
