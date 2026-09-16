package chartofaccounts

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxChildSuffix caps children per parent at 99. The suffix is zero-padded to
// two digits so codes sort lexically in the same order they sort numerically
// ("1103.09" < "1103.10"), which is what lets the report and the keyset cursor
// both order by code alone.
const MaxChildSuffix = 99

// childSeparator joins a parent code to its child suffix: 1103 -> 1103.01.
const childSeparator = "."

// NextChildCode returns the next child code under parentCode, given every code
// currently taken anywhere in the tenant.
//
// It appends after the highest suffix already in use (1103.03 exists -> next
// is 1103.04), so newly created children keep landing at the bottom of the
// list instead of backfilling wherever an earlier one was deleted. Only once
// suffix 99 is reached does it fall back to the lowest free gap -- a tenant
// that repeatedly adds and removes children does not permanently lose a slot
// to a deleted one, it just stops being the FIRST thing tried.
//
// parentCode must itself be top-level: the tree is capped at two levels (AD-4).
func NextChildCode(parentCode string, taken []string) (string, error) {
	if strings.TrimSpace(parentCode) == "" {
		return "", ClientError{Msg: "A parent account code is required."}
	}
	if strings.Contains(parentCode, childSeparator) {
		return "", ClientError{Msg: fmt.Sprintf(
			"Account %s is already a child. The chart of accounts is limited to two levels.",
			parentCode)}
	}

	prefix := parentCode + childSeparator
	used := make(map[int]bool, len(taken))
	highest := 0
	for _, code := range taken {
		suffix, ok := strings.CutPrefix(code, prefix)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		used[n] = true
		if n > highest {
			highest = n
		}
	}

	if highest < MaxChildSuffix {
		return fmt.Sprintf("%s%s%02d", parentCode, childSeparator, highest+1), nil
	}

	for i := 1; i <= MaxChildSuffix; i++ {
		if !used[i] {
			return fmt.Sprintf("%s%s%02d", parentCode, childSeparator, i), nil
		}
	}
	return "", ConflictError{Msg: fmt.Sprintf(
		"Account %s already has the maximum of %d sub-accounts.", parentCode, MaxChildSuffix)}
}

// NextTopLevelCode returns the next integer code in [rangeLow, rangeHigh],
// given every code currently taken. Child codes (those containing a
// separator) are ignored, since they never occupy an integer slot.
//
// Same two-phase policy as NextChildCode: append one past the highest code
// already in use within the range, so new accounts sort to the bottom of
// their sub-category/category instead of backfilling wherever an earlier
// account was deleted. Only once the range is full at the top does it fall
// back to the lowest free gap.
func NextTopLevelCode(rangeLow, rangeHigh int, taken []string) (string, error) {
	// An inverted or non-positive range is a programming error, not an
	// exhausted range. Without this it falls through the loop and reports
	// "No account codes remain in the range 1200-1100", which sends whoever
	// reads it hunting for accounts that do not exist.
	if rangeLow <= 0 || rangeHigh < rangeLow {
		return "", fmt.Errorf("invalid account code range %d-%d", rangeLow, rangeHigh)
	}

	used := make(map[int]bool, len(taken))
	highest := rangeLow - 1 // sentinel below the range, so +1 lands on rangeLow when nothing is used yet
	for _, code := range taken {
		if strings.Contains(code, childSeparator) {
			continue
		}
		n, err := strconv.Atoi(code)
		if err != nil {
			continue
		}
		used[n] = true
		if n >= rangeLow && n <= rangeHigh && n > highest {
			highest = n
		}
	}

	if highest < rangeHigh {
		return strconv.Itoa(highest + 1), nil
	}

	for i := rangeLow; i <= rangeHigh; i++ {
		if !used[i] {
			return strconv.Itoa(i), nil
		}
	}
	return "", ConflictError{Msg: fmt.Sprintf(
		"No account codes remain in the range %d-%d.", rangeLow, rangeHigh)}
}

// Block sizes for the taxonomy. A category owns a thousand-block (1000 Assets
// covers 1000-1999); a sub-category owns a hundred-block inside its category
// (1100 Current Assets covers 1100-1199). Both match the seeded rows, so a
// tenant-created category or sub-category is indistinguishable in shape from a
// seeded one.
const (
	CategoryBlockSize    = 1000
	SubCategoryBlockSize = 100
)

// FirstCategoryCode is where category allocation starts. MaxCategoryCode caps
// it: codes stay at most five digits, which keeps every account code short
// enough to stay readable in the report's fixed-width code column.
const (
	FirstCategoryCode = 1000
	MaxCategoryCode   = 99000
)

// CategoryDirectRange returns the code window reserved for accounts placed
// directly under a category rather than under one of its sub-categories.
//
// It is the FIRST hundred-block of the category's range and never more, even
// when the category has no sub-categories yet. Sizing it by the gap below the
// lowest existing sub-category would be wider today and would silently overlap
// tomorrow: a direct account allocated at 1150 while 1100 did not exist would
// land inside sub-category 1100's range the moment someone created it, and
// codes are immutable after allocation. A fixed first block cannot collide,
// because NextSubCategoryCode never hands that block out.
func CategoryDirectRange(rangeLow, rangeHigh int) (int, int) {
	high := rangeLow + SubCategoryBlockSize - 1
	if high > rangeHigh {
		high = rangeHigh
	}
	return rangeLow, high
}

// NextCategoryCode returns the next thousand-block code for a new category,
// given every category code currently in use. The nine seeded categories
// occupy 1000-9000, so the first tenant-created category is 10000, the second
// 11000, and so on -- same append-then-gap-fill policy as NextTopLevelCode:
// only once MaxCategoryCode is reached does it fall back to the lowest free
// block.
func NextCategoryCode(taken []int) (int, error) {
	used := make(map[int]bool, len(taken))
	highest := FirstCategoryCode - CategoryBlockSize // sentinel, so +block lands on FirstCategoryCode
	for _, c := range taken {
		used[c] = true
		// Only a block-aligned code can have come from this allocator; an
		// unaligned one (stray data, or a code from a different scheme
		// entirely) must not be able to push the append point past where a
		// real block boundary sits.
		if c >= FirstCategoryCode && c <= MaxCategoryCode &&
			(c-FirstCategoryCode)%CategoryBlockSize == 0 && c > highest {
			highest = c
		}
	}

	if next := highest + CategoryBlockSize; next <= MaxCategoryCode {
		return next, nil
	}

	for code := FirstCategoryCode; code <= MaxCategoryCode; code += CategoryBlockSize {
		if !used[code] {
			return code, nil
		}
	}
	return 0, ConflictError{Msg: fmt.Sprintf(
		"No category codes remain below %d.", MaxCategoryCode)}
}

// NextSubCategoryCode returns the next hundred-block code for a new
// sub-category of the category spanning [rangeLow, rangeHigh], given every
// sub-category code currently in use anywhere. Same append-then-gap-fill
// policy as NextTopLevelCode.
//
// Allocation starts one block ABOVE rangeLow, leaving the category's first
// block to CategoryDirectRange. That matches the seeded rows (1000 Assets ->
// 1100, 1200, 1300) and is what keeps the two allocators from ever colliding.
func NextSubCategoryCode(rangeLow, rangeHigh int, taken []int) (int, error) {
	if rangeLow <= 0 || rangeHigh < rangeLow {
		return 0, fmt.Errorf("invalid category code range %d-%d", rangeLow, rangeHigh)
	}
	used := make(map[int]bool, len(taken))
	highest := rangeLow // sentinel: never a real sub-category code (that block is CategoryDirectRange's)
	for _, c := range taken {
		used[c] = true
		// Same alignment guard as NextCategoryCode: an unaligned code must not
		// push the append point past a real block boundary.
		if c >= rangeLow && c <= rangeHigh && (c-rangeLow)%SubCategoryBlockSize == 0 && c > highest {
			highest = c
		}
	}

	if next := highest + SubCategoryBlockSize; next+SubCategoryBlockSize-1 <= rangeHigh {
		return next, nil
	}

	for code := rangeLow + SubCategoryBlockSize; code+SubCategoryBlockSize-1 <= rangeHigh; code += SubCategoryBlockSize {
		if !used[code] {
			return code, nil
		}
	}
	return 0, ConflictError{Msg: fmt.Sprintf(
		"No sub-category codes remain in the range %d-%d.", rangeLow, rangeHigh)}
}
