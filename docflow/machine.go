package docflow

import "errors"

// ErrInvalidTransition is returned when a status move is not on the machine.
var ErrInvalidTransition = errors.New("invalid status transition")

// Machine is a document's status graph: the set of codes reachable from each
// code. A code mapping to an empty set is terminal.
//
// Pure and table-driven so each document's legal moves are one readable literal
// that a test can enumerate exhaustively — the alternative, scattered
// if-statements at each call site, is how a module ends up with an edge that
// exists in one path and not another.
type Machine map[string]map[string]bool

// Can reports whether from -> to is allowed.
func (m Machine) Can(from, to string) bool { return m[from][to] }

// Validate returns ErrInvalidTransition when from -> to is not allowed.
func (m Machine) Validate(from, to string) error {
	if !m.Can(from, to) {
		return ErrInvalidTransition
	}
	return nil
}

// Next returns the codes reachable from a status, for a UI that wants to render
// only the buttons the document can actually take.
func (m Machine) Next(from string) []string {
	out := []string{}
	for code := range m[from] {
		out = append(out, code)
	}
	return out
}

// IsTerminal reports whether a status has no outgoing moves.
func (m Machine) IsTerminal(code string) bool { return len(m[code]) == 0 }

// Known reports whether a code appears in the machine at all, either as a
// source or as a destination. Used to reject a status the document has never
// heard of before it reaches the database.
func (m Machine) Known(code string) bool {
	if _, ok := m[code]; ok {
		return true
	}
	for _, to := range m {
		if to[code] {
			return true
		}
	}
	return false
}
