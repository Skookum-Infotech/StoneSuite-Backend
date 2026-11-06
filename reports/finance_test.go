package reports

import "testing"

func TestPlAmountSigns(t *testing.T) {
	cases := []struct {
		kind          string
		debit, credit float64
		want          float64
	}{
		{KindIncome, 0, 100, 100}, // sale
		{KindIncome, 30, 0, -30},  // sales return reduces revenue
		{KindExpense, 80, 0, 80},  // cost
		{KindExpense, 0, 20, -20}, // refunded cost
		{KindExpense, 50, 50, 0},
	}
	for _, c := range cases {
		if got := plAmount(c.kind, c.debit, c.credit); !approx(got, c.want) {
			t.Errorf("plAmount(%s,%v,%v) = %v want %v", c.kind, c.debit, c.credit, got, c.want)
		}
	}
}

func TestKindFor(t *testing.T) {
	if kindFor("credit") != KindIncome || kindFor("debit") != KindExpense || kindFor("") != KindExpense {
		t.Error("credit-normal categories are income; everything else is expense")
	}
}

func row(cat int, name, nb, sub, code string, debit, credit float64) plRow {
	return plRow{categoryCode: cat, categoryName: name, normalBalance: nb, subcategory: sub,
		accountID: "id-" + code, accountCode: code, accountName: "Acct " + code, debit: debit, credit: credit}
}

func TestBuildProfitLoss(t *testing.T) {
	pl := buildProfitLoss([]plRow{
		row(4000, "Revenue", "credit", "Sales", "4101", 0, 1000),
		row(4000, "Revenue", "credit", "Sales", "4102", 0, 500),
		row(4000, "Revenue", "credit", "Returns", "4201", 100, 0),
		row(5000, "Cost of Goods Sold", "debit", "COGS", "5101", 600, 0),
		row(6000, "Operating Expenses", "debit", "Payroll", "6101", 300, 0),
		row(8000, "Other Income", "credit", "Interest", "8101", 0, 50),
	})
	if len(pl.Sections) != 4 {
		t.Fatalf("sections = %d", len(pl.Sections))
	}
	rev := pl.Sections[0]
	if rev.Name != "Revenue" || !approx(rev.Total, 1400) || len(rev.Subcategories) != 2 {
		t.Errorf("revenue = %+v", rev)
	}
	if !approx(rev.Subcategories[0].Total, 1500) || len(rev.Subcategories[0].Accounts) != 2 || !approx(rev.Subcategories[1].Total, -100) {
		t.Errorf("revenue subcategories = %+v", rev.Subcategories)
	}
	if !approx(pl.Income, 1450) || !approx(pl.Expenses, 900) {
		t.Errorf("income %v expenses %v", pl.Income, pl.Expenses)
	}
	if !approx(pl.GrossProfit, 800) {
		t.Errorf("gross profit = %v (revenue 1400 - cogs 600)", pl.GrossProfit)
	}
	if !approx(pl.NetProfit, 550) || !approx(pl.NetMargin, 550.0/1450.0*100) {
		t.Errorf("net %v margin %v", pl.NetProfit, pl.NetMargin)
	}
}

func TestBuildProfitLossEmptyAndLoss(t *testing.T) {
	empty := buildProfitLoss(nil)
	if len(empty.Sections) != 0 || empty.NetProfit != 0 || empty.NetMargin != 0 || empty.Sections == nil {
		t.Errorf("empty = %+v", empty)
	}
	loss := buildProfitLoss([]plRow{row(6000, "Operating Expenses", "debit", "Rent", "6201", 400, 0)})
	if !approx(loss.NetProfit, -400) || loss.NetMargin != 0 {
		t.Errorf("a loss with no income has a negative profit and no margin: %+v", loss)
	}
}
