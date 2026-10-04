package fabrication

import (
	"fmt"
	"strings"
)

func purchaseRequirementDescription(line TemplateLine, revision int) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Approved fabrication template revision %d.\nRequired finished pieces (length × width × thickness, mm); not slab order sizes.\n", revision)
	finish := strings.TrimSpace(line.Finish)
	if finish == "" {
		finish = "Confirm with supplier"
	}
	fmt.Fprintf(&out, "Finish: %s", finish)
	for _, piece := range line.Pieces {
		fmt.Fprintf(&out, "\n%s: %g × %g × %g mm", piece.Name, piece.LengthMM, piece.WidthMM, piece.ThicknessMM)
	}
	out.WriteString("\nConfirm slab dimensions, layout suitability and cutting allowances before ordering.")
	return out.String()
}
