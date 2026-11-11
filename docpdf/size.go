package docpdf

import "fmt"

// SizeText formats the slab size and count a line was priced from for the
// printed item cell: "120 × 60 in · 2 slabs". Length and width print together,
// followed by the unit they were measured in, or not at all; a slab count alone
// prints as "2 slabs". A line without any of them returns "".
func SizeText(length, width *float64, slabs *int, unit string) string {
	text := ""
	if length != nil && width != nil {
		if unit == "" {
			unit = "in"
		}
		text = fmt.Sprintf("%s × %s %s", trimNum(*length), trimNum(*width), unit)
	}
	if slabs != nil {
		count := fmt.Sprintf("%d slabs", *slabs)
		if *slabs == 1 {
			count = "1 slab"
		}
		if text != "" {
			text += " · "
		}
		text += count
	}
	return text
}

// SizeTextThick is SizeText with the slab thickness added between the size and
// the count: "120 × 60 in · 3 in thick · 2 slabs". A line that states no
// thickness prints exactly as SizeText does.
func SizeTextThick(length, width, thickness *float64, slabs *int, unit string) string {
	if thickness == nil {
		return SizeText(length, width, slabs, unit)
	}
	if unit == "" {
		unit = "in"
	}
	thick := fmt.Sprintf("%s %s thick", trimNum(*thickness), unit)
	sizeOnly := SizeText(length, width, nil, unit)
	text := thick
	if sizeOnly != "" {
		text = sizeOnly + " · " + thick
	}
	if slabs != nil {
		text += " · " + SizeText(nil, nil, slabs, unit)
	}
	return text
}
