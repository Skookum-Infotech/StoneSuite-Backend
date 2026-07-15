package vendors

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid vendor status transition")

// allowedTransitions maps a status code to the set of codes reachable from
// it. Unlike Sales Order, a vendor directory entry has no terminal state —
// an Inactive vendor can always be reactivated.
var allowedTransitions = map[string]map[string]bool{
	"ACT_": {"ONHD": true, "INA_": true},
	"ONHD": {"ACT_": true, "INA_": true},
	"INA_": {"ACT_": true},
}

// CanTransition reports whether moving fromCode->toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}
