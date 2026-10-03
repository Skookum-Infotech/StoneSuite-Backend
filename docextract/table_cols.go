package docextract

import (
	"sort"
	"strings"
)

type colRole string

const (
	roleSKU    colRole = "sku"
	roleDesc   colRole = "desc"
	roleQty    colRole = "qty"
	roleUoM    colRole = "uom"
	rolePrice  colRole = "price"
	roleAmount colRole = "amount"
)

const (
	minHeaderRoles   = 3    // distinct column roles that make a header row
	leftAssignTol    = 12.0 // points a word may sit left of its column start
	rightAlignTol    = 8.0  // points of right-edge slack for numeric columns
	numericLeftReach = 40.0 // a right-aligned value may start this far left of its header
	headerTrimChars  = ":.#"
	headerPhraseSize = 2
	phraseGap        = 14.0 // max points between the words of a two-word column title
)

type column struct {
	role   colRole
	x0, x1 float64
}

// headerPhrases are two-word column titles checked before single words.
var headerPhrases = map[string]colRole{
	"unit price": rolePrice, "unit cost": rolePrice, "item description": roleDesc,
	"line total": roleAmount, "ext price": roleAmount, "ext amount": roleAmount,
	"item number": roleSKU, "item no": roleSKU, "part number": roleSKU, "part no": roleSKU,
	"product code": roleSKU, "item code": roleSKU,
}

// headerWords are single-word column titles.
var headerWords = map[string]colRole{
	"item": roleSKU, "sku": roleSKU, "code": roleSKU, "part": roleSKU,
	"description": roleDesc, "desc": roleDesc, "product": roleDesc,
	"qty": roleQty, "quantity": roleQty, "ordered": roleQty,
	"uom": roleUoM, "unit": roleUoM, "u/m": roleUoM, "um": roleUoM,
	"price": rolePrice, "rate": rolePrice, "cost": rolePrice,
	"amount": roleAmount, "total": roleAmount, "ext": roleAmount, "extended": roleAmount,
}

// normHeaderWord lower-cases a header word and trims punctuation.
func normHeaderWord(s string) string {
	return strings.ToLower(strings.Trim(s, headerTrimChars+" "))
}

// detectTableHeader recognises a line-table header row by its vocabulary.
func detectTableHeader(r Row) ([]column, bool) {
	var cols []column
	seen := map[colRole]bool{}
	for i := 0; i < len(r.Words); i++ {
		w := r.Words[i]
		tok := normHeaderWord(w.Text)
		if tok == "" {
			continue
		}
		role, span := colRole(""), 1
		if rr, ok := headerPhrases[tok]; ok {
			role = rr
		} else if i+1 < len(r.Words) && r.Words[i+1].X-(w.X+w.W) <= phraseGap {
			pair := tok + " " + normHeaderWord(r.Words[i+1].Text)
			if rr, ok := headerPhrases[pair]; ok {
				role, span = rr, headerPhraseSize
			}
		}
		if role == "" {
			rr, ok := headerWords[tok]
			if !ok {
				continue
			}
			role = rr
		}
		last := r.Words[i+span-1]
		if !seen[role] {
			seen[role] = true
			cols = append(cols, column{role: role, x0: w.X, x1: last.X + last.W})
		}
		i += span - 1
	}
	if len(cols) < minHeaderRoles {
		return nil, false
	}
	sort.SliceStable(cols, func(a, b int) bool { return cols[a].x0 < cols[b].x0 })
	return cols, true
}

func isNumericRole(r colRole) bool {
	return r == roleQty || r == rolePrice || r == roleAmount
}

// assignWords places each word of a data row into its nearest column. Numeric
// columns win on right-edge alignment; otherwise the last column whose start is
// at or left of the word (with slack) takes it.
func assignWords(r Row, cols []column) map[colRole][]Word {
	out := map[colRole][]Word{}
	for _, w := range r.Words {
		idx := -1
		best := rightAlignTol + 1
		for i, c := range cols {
			if !isNumericRole(c.role) {
				continue
			}
			d := (w.X + w.W) - c.x1
			if d < 0 {
				d = -d
			}
			if d <= rightAlignTol && d < best && w.X >= c.x0-numericLeftReach {
				best, idx = d, i
			}
		}
		if idx < 0 {
			idx = 0
			for i, c := range cols {
				if c.x0-leftAssignTol <= w.X {
					idx = i
				}
			}
		}
		role := cols[idx].role
		out[role] = append(out[role], w)
	}
	return out
}

// cellText joins the words of one cell.
func cellText(ws []Word) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.Text
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// columnByRole returns the column for a role.
func columnByRole(cols []column, role colRole) (column, bool) {
	for _, c := range cols {
		if c.role == role {
			return c, true
		}
	}
	return column{}, false
}
