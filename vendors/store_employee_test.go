package vendors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmployeeProfile_FullName(t *testing.T) {
	tests := []struct {
		name string
		p    EmployeeProfile
		want string
	}{
		{"first and last", EmployeeProfile{FirstName: "Ana", LastName: "Diaz"}, "Ana Diaz"},
		{"first only", EmployeeProfile{FirstName: "Ana"}, "Ana"},
		{"extra spaces collapse", EmployeeProfile{FirstName: " Ana ", LastName: " Diaz "}, "Ana Diaz"},
		{"falls back to email", EmployeeProfile{Email: " ana@example.com "}, "ana@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, tt.p.fullName()) })
	}
}

// The vendor list and pickers read a non-Person vendor's name from
// vendor_legal_name, so an Employee vendor must carry its full name there.
func TestEmployeeProfile_Fields(t *testing.T) {
	f := EmployeeProfile{
		FirstName: "Ana", LastName: "Diaz", Email: "ana@example.com", Phone: "555-0100",
		JobTitle: "Installer", Department: "Field", Address: "1 Main St",
	}.fields()
	assert.Equal(t, "Ana Diaz", f.LegalName)
	assert.Equal(t, "Ana", f.GivenName)
	assert.Equal(t, "Diaz", f.FamilyName)
	assert.Equal(t, "ana@example.com", f.Email)
	assert.Equal(t, "Installer", f.JobTitle)
	assert.Equal(t, "Field", f.Department)
	assert.Equal(t, "1 Main St", f.PhysicalAddress)
	if assert.NotNil(t, f.ContactPoint) {
		assert.Equal(t, "555-0100", f.ContactPoint.Telephone)
	}

	// A profile with no first name still gets a non-empty given name.
	noFirst := EmployeeProfile{Email: "x@example.com"}.fields()
	assert.Equal(t, "x@example.com", noFirst.GivenName)
}

func TestValidateVendorType_RejectsEmployee(t *testing.T) {
	err := validateVendorType(EmployeeVendorType, vendorFields{})
	assert.Error(t, err)
	assert.True(t, IsClientError(err))
}
