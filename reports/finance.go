package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Finance reports read the general ledger: journal_entry / journal_entry_line
// against the chart of accounts. Every journal entry is posted (there is no
// draft state) and a reversal is just another entry with opposite lines, so
// summing lines already nets reversals out. The ledger has no per-row owner, so
// the caller's report_finance:read grant is the only gate.

const (
	// Chart-of-accounts category codes the Gross profit subtotal is built from.
	revenueCategoryCode = 4000
	cogsCategoryCode    = 5000

	normalBalanceCredit = "credit"

	// KindIncome / KindExpense say how a section's amount is signed.
	KindIncome  = "income"
	KindExpense = "expense"

	// maxLedgerLines caps the lines listed for one account.
	maxLedgerLines = 200
)

// PLAccount is one P&L account's net amount for the period.
type PLAccount struct {
	ID     string  `json:"id"`
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
}

// PLSubcategory groups accounts under a chart-of-accounts sub-category.
type PLSubcategory struct {
	Name     string      `json:"name"`
	Total    float64     `json:"total"`
	Accounts []PLAccount `json:"accounts"`
}

// PLSection is one chart-of-accounts category (Revenue, Operating Expenses...).
// Income sections are credit minus debit; expense sections debit minus credit,
// so every section total is a positive number when it behaves normally.
type PLSection struct {
	Code          int             `json:"code"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Total         float64         `json:"total"`
	Subcategories []PLSubcategory `json:"subcategories"`
}

// ProfitLoss is the Profit & Loss statement for a period.
type ProfitLoss struct {
	Sections    []PLSection `json:"sections"`
	Income      float64     `json:"income"`
	Expenses    float64     `json:"expenses"`
	GrossProfit float64     `json:"grossProfit"`
	NetProfit   float64     `json:"netProfit"`
	NetMargin   float64     `json:"netMarginPercent"`
}

// plRow is one account's raw ledger totals, in statement order.
type plRow struct {
	categoryCode  int
	categoryName  string
	normalBalance string
	subcategory   string
	accountID     string
	accountCode   string
	accountName   string
	debit, credit float64
}

func kindFor(normalBalance string) string {
	if normalBalance == normalBalanceCredit {
		return KindIncome
	}
	return KindExpense
}

// plAmount signs an account's activity so a normally-behaving account is
// positive: income is credit less debit, expense is debit less credit.
func plAmount(kind string, debit, credit float64) float64 {
	if kind == KindIncome {
		return credit - debit
	}
	return debit - credit
}

// buildProfitLoss folds ordered account rows into sections, sub-categories and
// totals. Rows must arrive grouped by category then sub-category (the query
// orders them so); an account whose activity nets to zero never reaches here.
func buildProfitLoss(rows []plRow) ProfitLoss {
	pl := ProfitLoss{Sections: []PLSection{}}
	secIdx := map[int]int{}
	subIdx := map[string]int{}
	for _, r := range rows {
		kind := kindFor(r.normalBalance)
		si, ok := secIdx[r.categoryCode]
		if !ok {
			pl.Sections = append(pl.Sections, PLSection{Code: r.categoryCode, Name: r.categoryName, Kind: kind, Subcategories: []PLSubcategory{}})
			si = len(pl.Sections) - 1
			secIdx[r.categoryCode] = si
		}
		key := fmt.Sprintf("%d|%s", r.categoryCode, r.subcategory)
		sub, ok := subIdx[key]
		sec := &pl.Sections[si]
		if !ok {
			sec.Subcategories = append(sec.Subcategories, PLSubcategory{Name: r.subcategory, Accounts: []PLAccount{}})
			sub = len(sec.Subcategories) - 1
			subIdx[key] = sub
		}
		amount := plAmount(kind, r.debit, r.credit)
		s := &sec.Subcategories[sub]
		s.Accounts = append(s.Accounts, PLAccount{ID: r.accountID, Code: r.accountCode, Name: r.accountName, Amount: amount})
		s.Total += amount
		sec.Total += amount
	}
	var revenue, cogs float64
	for _, s := range pl.Sections {
		if s.Kind == KindIncome {
			pl.Income += s.Total
		} else {
			pl.Expenses += s.Total
		}
		switch s.Code {
		case revenueCategoryCode:
			revenue = s.Total
		case cogsCategoryCode:
			cogs = s.Total
		}
	}
	pl.GrossProfit = revenue - cogs
	pl.NetProfit = pl.Income - pl.Expenses
	if pl.Income > 0 {
		pl.NetMargin = pl.NetProfit / pl.Income * 100
	}
	return pl
}

// ProfitAndLoss builds the Profit & Loss for journal entries dated in the
// window: every P&L account with net activity, grouped by category and
// sub-category in chart-of-accounts order.
func ProfitAndLoss(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) (ProfitLoss, error) {
	args := []any{}
	dates := dateClause("je.entry_date", from, to, &args)
	q := fmt.Sprintf(`
		SELECT cat.category_code, cat.category_name, cat.category_normal_balance, sub.subcategory_name,
		       a.coa_account_uuid::text, a.coa_account_code, a.coa_account_name,
		       SUM(l.debit), SUM(l.credit)
		FROM journal_entry_line l
		JOIN journal_entry je ON je.journal_entry_id = l.journal_entry_id
		JOIN coa_account a ON a.coa_account_id = l.coa_account_id
		JOIN lkp_coa_subcategory sub ON sub.subcategory_id = a.subcategory_id
		JOIN lkp_coa_category cat ON cat.category_id = sub.category_id
		WHERE a.coa_account_bs_pnl = 'PNL'%s
		GROUP BY cat.category_code, cat.category_name, cat.category_normal_balance, cat.category_sort_order,
		         sub.subcategory_name, sub.subcategory_sort_order,
		         a.coa_account_uuid, a.coa_account_code, a.coa_account_name
		HAVING SUM(l.debit) <> SUM(l.credit)
		ORDER BY cat.category_sort_order, sub.subcategory_sort_order, a.coa_account_code
		LIMIT %d`, dates, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return ProfitLoss{}, fmt.Errorf("query profit and loss: %w", err)
	}
	defer r.Close()
	var rows []plRow
	for r.Next() {
		var row plRow
		if err := r.Scan(&row.categoryCode, &row.categoryName, &row.normalBalance, &row.subcategory,
			&row.accountID, &row.accountCode, &row.accountName, &row.debit, &row.credit); err != nil {
			return ProfitLoss{}, fmt.Errorf("scan profit and loss: %w", err)
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return ProfitLoss{}, fmt.Errorf("query profit and loss: %w", err)
	}
	return buildProfitLoss(rows), nil
}

// LedgerLine is one journal line on an account, for the drill-down panel.
type LedgerLine struct {
	EntryID    string    `json:"entryId"`
	Number     string    `json:"number"`
	Date       time.Time `json:"date"`
	Memo       string    `json:"memo"`
	SourceType string    `json:"sourceType"`
	Debit      float64   `json:"debit"`
	Credit     float64   `json:"credit"`
}

// AccountLines lists the journal lines posted to one account in the window,
// newest first.
func AccountLines(ctx context.Context, pool *pgxpool.Pool, accountUUID string, from, to time.Time) ([]LedgerLine, error) {
	out := []LedgerLine{}
	args := []any{accountUUID}
	dates := dateClause("je.entry_date", from, to, &args)
	q := fmt.Sprintf(`
		SELECT je.journal_entry_uuid::text, COALESCE(je.journal_entry_number, ''), je.entry_date, je.memo, je.source_type,
		       l.debit, l.credit
		FROM journal_entry_line l
		JOIN journal_entry je ON je.journal_entry_id = l.journal_entry_id
		JOIN coa_account a ON a.coa_account_id = l.coa_account_id
		WHERE a.coa_account_uuid = $1%s
		ORDER BY je.entry_date DESC, je.journal_entry_id DESC, l.line_number
		LIMIT %d`, dates, maxLedgerLines)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query account lines: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var l LedgerLine
		if err := r.Scan(&l.EntryID, &l.Number, &l.Date, &l.Memo, &l.SourceType, &l.Debit, &l.Credit); err != nil {
			return nil, fmt.Errorf("scan account line: %w", err)
		}
		out = append(out, l)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("query account lines: %w", err)
	}
	return out, nil
}
