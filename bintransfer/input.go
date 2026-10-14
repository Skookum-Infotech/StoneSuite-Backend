package bintransfer

import (
	"fmt"
	"strings"
)

// MaxLines caps how many slabs one transfer may move. A transfer is applied in
// one transaction that row-locks every slab, so an unbounded document would hold
// those locks for as long as the loop runs; a yard reorganisation that big is
// several transfers anyway.
const MaxLines = 200

// validateInput checks the shape of a create/update body before any query runs,
// trimming the identifiers it keeps. Every failure is a ClientError (a 400).
func validateInput(in *Input) error {
	if in.WarehouseID <= 0 {
		return ClientError{Msg: "A bin transfer needs a location."}
	}
	if len(in.Lines) == 0 {
		return ClientError{Msg: "A bin transfer needs at least one slab."}
	}
	if len(in.Lines) > MaxLines {
		return ClientError{Msg: fmt.Sprintf("A bin transfer can move at most %d slabs; split it into several.", MaxLines)}
	}
	seen := make(map[string]int, len(in.Lines))
	for i := range in.Lines {
		l := &in.Lines[i]
		l.UnitID = strings.TrimSpace(l.UnitID)
		l.ToBinID = strings.TrimSpace(l.ToBinID)
		n := i + 1
		if l.UnitID == "" {
			return ClientError{Msg: fmt.Sprintf("Line %d needs a slab.", n)}
		}
		if l.ToBinID == "" {
			return ClientError{Msg: fmt.Sprintf("Line %d needs a destination bin.", n)}
		}
		if first, dup := seen[l.UnitID]; dup {
			return ClientError{Msg: fmt.Sprintf("Line %d repeats the slab on line %d; each slab can appear once.", n, first)}
		}
		seen[l.UnitID] = n
	}
	return nil
}

// outcome is what completing one line does.
type outcome int

const (
	// outcomeMove sends the slab to the line's destination bin.
	outcomeMove outcome = iota
	// outcomeAlreadyThere leaves a slab that is already in the destination alone,
	// so completing is idempotent and writes no history for a move that is not one.
	outcomeAlreadyThere
)

// lineOutcome decides what completing a line does with a slab now in curBinID.
//
// The destination is what was approved, so a slab that has since wandered to a
// third bin is still sent to it; only one already there is left in place.
func lineOutcome(curBinID *int, toBinID int) outcome {
	if curBinID != nil && *curBinID == toBinID {
		return outcomeAlreadyThere
	}
	return outcomeMove
}
