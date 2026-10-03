package docextractjob

import "strings"

// Alias key prefixes: an item alias is keyed by the document's SKU or by its
// description, kept apart so one can never collide with the other.
const (
	aliasPrefixSKU  = "sku:"
	aliasPrefixDesc = "desc:"
)

// skuStripper removes the characters a SKU comparison ignores.
var skuStripper = strings.NewReplacer(" ", "", "\t", "", "-", "", "_", "", ".", "")

// NormalizeSKU lower-cases a SKU and strips spaces, '-', '_' and '.', matching
// the SQL expression the item lookup compares against. Variant suffixes such
// as "-3CM" are kept, so they never match a different SKU.
func NormalizeSKU(s string) string { return skuStripper.Replace(strings.ToLower(strings.TrimSpace(s))) }
