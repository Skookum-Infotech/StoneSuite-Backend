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
	// roleSkip is a recognised column whose values belong to no field (a
	// second amount-like column, e.g. "Extension" beside "Ext. Cost"); it
	// exists so its numbers don't drift into the description.
	roleSkip colRole = "skip"
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
	"item cost": rolePrice, "ext cost": roleAmount, "extended cost": roleAmount,
	// Two-row headers (GST invoices): "Item Details / HSN Code", "Unit Price / Net Price".
	"item details": roleDesc, "item name": roleDesc, "hsn code": roleSkip, "hsn/sac": roleSkip, "net price": roleSkip,
}

// headerWords are single-word column titles.
var headerWords = map[string]colRole{
	"item": roleSKU, "sku": roleSKU, "code": roleSKU, "part": roleSKU,
	"description": roleDesc, "desc": roleDesc, "product": roleDesc,
	"qty": roleQty, "quantity": roleQty, "ordered": roleQty,
	"uom": roleUoM, "unit": roleUoM, "u/m": roleUoM, "um": roleUoM,
	"price": rolePrice, "rate": rolePrice, "cost": rolePrice,
	"amount": roleAmount, "total": roleAmount, "ext": roleAmount, "extended": roleAmount,
	"extension": roleAmount,
	// Columns whose numbers belong to no field; they never make a header row.
	"discount": roleSkip, "tax": roleSkip, "vat": roleSkip,
}

// serialHeaders are column titles for a row-number column ("Sr. No", "S.No",
// "SN.", "Sl"); its values belong to no field. A bare "#" counts only as the first
// header word, since "Part #" and "Item #" are SKU titles.
var serialHeaders = map[string]bool{
	"sn": true, "sn.": true, "s.n": true, "s.n.": true,
	"sr": true, "sr.": true, "sl": true, "sl.": true, "sno": true, "s.no": true, "s.no.": true,
	"srno": true, "sr.no": true, "sr.no.": true, "slno": true, "sl.no": true, "sl.no.": true,
}

// isSerialHeader reports whether word i of a header row titles a row-number column.
func isSerialHeader(text string, i int) bool {
	t := strings.ToLower(strings.TrimRight(text, ":"))
	if t == "#" {
		return i == 0
	}
	return serialHeaders[t]
}

// maxHeaderBandRows bounds how many rows under the first header row may
// continue a multi-row header ("Qty." / "(Rs.)" under "Item" / "Unit Price").
const maxHeaderBandRows = 2

// extendHeader joins the rows under header row i that only continue it into
// one band and re-detects the columns on the merged words. It returns the
// columns and the index of the last header row; without a continuation (or when
// the merged band is no longer a header) it returns base and i.
func extendHeader(rows []Row, i int, base []column) ([]column, int) {
	words := append([]Word(nil), rows[i].Words...)
	end := i
	for j := i + 1; j < len(rows) && j <= i+maxHeaderBandRows; j++ {
		if !headerContinuation(rows[j]) {
			break
		}
		words = append(words, rows[j].Words...)
		end = j
	}
	if end == i {
		return base, i
	}
	sort.SliceStable(words, func(a, b int) bool { return words[a].X < words[b].X })
	cols, ok := detectTableHeader(Row{Y: rows[i].Y, Words: words})
	if !ok {
		return base, i
	}
	return cols, end
}

// headerContinuation reports whether a row holds only header vocabulary: no
// digits, and at least one column word. A data row always carries a number.
func headerContinuation(r Row) bool {
	vocab := false
	for _, w := range r.Words {
		if hasDigit(w.Text) {
			return false
		}
		tok := normHeaderWord(w.Text)
		if _, ok := headerWords[tok]; ok || tok == "details" || tok == "hsn" {
			vocab = true
		}
	}
	return vocab
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
		if isSerialHeader(w.Text, i) {
			// Recognised so its values stay out of the description; it never
			// counts toward the roles that make a header row.
			cols = append(cols, column{role: roleSkip, x0: w.X, x1: w.X + w.W})
			continue
		}
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
		col := column{role: role, x0: w.X, x1: last.X + last.W}
		switch {
		case role == roleSkip:
			// Recognised only so its numbers stay out of the neighbouring columns.
		case !seen[role]:
			seen[role] = true
		case role == roleAmount:
			// The rightmost amount-like column is the line total; an earlier
			// one ("Extension" before "Ext. Cost") is kept but ignored.
			for ci := range cols {
				if cols[ci].role == roleAmount {
					cols[ci].role = roleSkip
				}
			}
		default:
			col.role = roleSkip
		}
		cols = append(cols, col)
		i += span - 1
	}
	if !isLineTableHeader(seen) {
		return nil, false
	}
	sort.SliceStable(cols, func(a, b int) bool { return cols[a].x0 < cols[b].x0 })
	return cols, true
}

// isLineTableHeader reports whether the roles found make a line-item table: at
// least minHeaderRoles of them, one being a quantity or an amount. A label
// strip such as "Date | Job Number | Cost Code | Cost Code Description" hits
// three column words but carries neither.
func isLineTableHeader(seen map[colRole]bool) bool {
	return len(seen) >= minHeaderRoles && (seen[roleQty] || seen[roleAmount])
}

func isNumericRole(r colRole) bool {
	return r == roleQty || r == rolePrice || r == roleAmount || r == roleSkip
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
