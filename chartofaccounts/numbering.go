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

// NextChildCode returns the next free child code under parentCode, given every
// code currently taken anywhere in the tenant. Gaps left by deleted children
// are reused, so a tenant that repeatedly adds and removes bank accounts does
// not march toward the 99 ceiling.
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
	for _, code := range taken {
		suffix, ok := strings.CutPrefix(code, prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil {
			used[n] = true
		}
	}

	for i := 1; i <= MaxChildSuffix; i++ {
		if !used[i] {
			return fmt.Sprintf("%s%s%02d", parentCode, childSeparator, i), nil
		}
	}
	return "", ConflictError{Msg: fmt.Sprintf(
		"Account %s already has the maximum of %d sub-accounts.", parentCode, MaxChildSuffix)}
}

// NextTopLevelCode returns the lowest free integer code in [rangeLow, rangeHigh],
// given every code currently taken. Child codes (those containing a separator)
// are ignored, since they never occupy an integer slot.
func NextTopLevelCode(rangeLow, rangeHigh int, taken []string) (string, error) {
	used := make(map[int]bool, len(taken))
	for _, code := range taken {
		if strings.Contains(code, childSeparator) {
			continue
		}
		if n, err := strconv.Atoi(code); err == nil {
			used[n] = true
		}
	}

	for i := rangeLow; i <= rangeHigh; i++ {
		if !used[i] {
			return strconv.Itoa(i), nil
		}
	}
	return "", ConflictError{Msg: fmt.Sprintf(
		"No account codes remain in the range %d-%d.", rangeLow, rangeHigh)}
}
