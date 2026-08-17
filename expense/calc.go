package expense

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// ComputeHeaderTotal sums line amounts into the claim's stored total (AD-3:
// no discount/tax term -- a reimbursement claim isn't a priced commitment).
func ComputeHeaderTotal(lineAmounts []float64) float64 {
	var total float64
	for _, a := range lineAmounts {
		total += a
	}
	return round2(total)
}
