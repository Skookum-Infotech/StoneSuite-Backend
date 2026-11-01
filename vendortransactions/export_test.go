package vendortransactions

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// formulaTriggers are the characters a spreadsheet treats as a formula start.
var formulaTriggers = []struct{ name, char string }{
	{"equals", "="}, {"plus", "+"}, {"minus", "-"}, {"at", "@"}, {"tab", "	"}, {"carriage return", ""},
}

func TestCSVSafe(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"equals", "=HYPERLINK(\"x\")", "'=HYPERLINK(\"x\")"},
		{"plus", "+1", "'+1"},
		{"minus", "-1", "'-1"},
		{"at", "@SUM(A1)", "'@SUM(A1)"},
		{"tab", "	x", "'	x"},
		{"carriage return", "x", "'x"},
		// Documents current behaviour: only the first byte is inspected, so a
		// leading space shields the formula in most spreadsheets and is not escaped.
		{"leading space then equals", " =1+1", " =1+1"},
		{"plain", "VBIL-1001", "VBIL-1001"},
		{"empty", "", ""},
		{"inner equals", "a=b", "a=b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, csvSafe(tc.in))
		})
	}
}

func TestWriteCSV_EscapesEveryTriggerInNumberAndStatus(t *testing.T) {
	for _, tr := range formulaTriggers {
		t.Run(tr.name, func(t *testing.T) {
			var buf bytes.Buffer
			num, status := tr.char+"N-1", tr.char+"Open"
			require.NoError(t, WriteCSV(&buf, []Row{{TypeLabel: "Vendor Bill", Number: num, Status: status}}))
			recs, err := csv.NewReader(&buf).ReadAll()
			require.NoError(t, err)
			require.Len(t, recs, 2)
			assert.Equal(t, "'"+num, recs[1][1], "number gets a leading apostrophe")
			assert.Equal(t, "'"+status, recs[1][2], "status gets a leading apostrophe")
		})
	}
}

// Amount and Balance are numeric cells written with %.2f and are intentionally
// not escaped: a negative amount legitimately starts with '-'. Spreadsheets parse
// it as a number, not a formula, and an apostrophe would turn it into text.
func TestWriteCSV_NumericCellsNotEscaped(t *testing.T) {
	var buf bytes.Buffer
	amt, bal := -12.5, -0.0
	require.NoError(t, WriteCSV(&buf, []Row{{Number: "N-1", Amount: &amt, Balance: &bal}}))
	recs, err := csv.NewReader(&buf).ReadAll()
	require.NoError(t, err)
	assert.Equal(t, "-12.50", recs[1][5])
	f, perr := strconv.ParseFloat(recs[1][5], 64)
	require.NoError(t, perr, "a negative amount stays a plain parseable number")
	assert.InDelta(t, -12.5, f, 0.0001)
	_, perr = strconv.ParseFloat(recs[1][6], 64)
	assert.NoError(t, perr)
}

func TestWriteCSV_EscapesNumberAndHeader(t *testing.T) {
	var buf bytes.Buffer
	amt := 12.5
	require.NoError(t, WriteCSV(&buf, []Row{{TypeLabel: "Vendor Bill", Number: "=HYPERLINK(\"http://x\",\"a\")", Status: "Approved", Amount: &amt, IsOpen: true}}))
	recs, err := csv.NewReader(&buf).ReadAll()
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, csvHeader, recs[0])
	assert.Equal(t, "'=HYPERLINK(\"http://x\",\"a\")", recs[1][1])
	assert.Equal(t, "12.50", recs[1][5])
	assert.Equal(t, "", recs[1][6])
	assert.Equal(t, "true", recs[1][7])
}
