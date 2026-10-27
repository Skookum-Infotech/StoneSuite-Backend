package anomaly

// Detection thresholds. They are code constants, not per-workspace settings:
// every workspace shares them until there is a real need to tune one. Each is
// deliberately conservative, since a rule that cries wolf gets ignored.
const (
	// lookbackDays bounds rules about something that happened ("created", "voided"):
	// only the last month is examined, so enabling the scan never floods a
	// workspace with years of history.
	lookbackDays = 30
	// longLookbackDays bounds rules about a state that persists (an overdue
	// invoice): anything older is treated as already known.
	longLookbackDays = 180

	// minHistory is how many earlier records a "compared with its own history"
	// rule needs before it will say anything; fewer is not a baseline.
	minHistory = 5

	// Sales and money in.
	discountFloorPercent      = 20   // never flag a line discount below this
	discountBaselineMultiple  = 3    // flag a discount this many times the workspace average
	discountBaselineMinLines  = 5    // other discounted lines needed before "the usual discount" means anything
	discountAlwaysFlagPercent = 80   // a line this deep is flagged whatever the workspace usually gives
	orderTotalMultiple        = 5    // flag an order this many times the customer's median
	orderTotalFloor           = 1000 // ... and at least this large
	duplicateInvoiceDays      = 3    // same customer and total within this many days
	overdueGraceDays          = 30   // an invoice this far past due is flagged
	staleUnappliedDays        = 30   // a payment unapplied this long is flagged
	quickRefundDays           = 1    // a refund within this many days of its payment
	quickRefundFloor          = 500  // ... of at least this much
	creditMemoBurstCount      = 3    // credit memos for one customer within lookbackDays
	voidedLookbackDays        = 7    // how far back voids are examined
	backdateDays              = 30   // a document dated this far before it was created
	futureDateDays            = 30   // ... or this far after
	conversionDriftPercent    = 5    // quote-to-order total drift
	zeroTaxMinHistory         = 3    // earlier taxed invoices before a zero-tax one is odd
	notInvoicedDays           = 14   // a filled order with no invoice this long

	// Purchasing and money out.
	poBillTolerancePercent = 1    // bills may exceed the PO total by this much ...
	poBillToleranceAmount  = 1    // ... plus this absolute amount
	moneyEpsilon           = 0.01 // one cent: rounding noise, not an overpayment
	newVendorDays          = 30   // a vendor this young
	newVendorPaymentFloor  = 5000 // paid at least this much
	poPriceMultiple        = 1.5  // PO price this many times the item's purchase price
	splitPOCount           = 3    // POs to one vendor by one person in one day
	poLateGraceDays        = 3    // days past the expected date
	poPartialStuckDays     = 30   // a partly received PO with no receipt this long
	expenseHighMultiple    = 3    // an expense line this many times its category median
	expenseHighFloor       = 200  // ... and at least this large
	expenseMinHistory      = 10   // earlier lines in the category before comparing

	// Inventory.
	writeOffFloor        = 1000 // a posted write-off worth at least this
	adjustmentBurstCount = 5    // adjustments posted by one person ...
	adjustmentBurstDays  = 7    // ... within this many days
	countVariancePercent = 5    // a counted line off by this much
	deadStockDays        = 180  // untouched this long
	binTransferStuckDays = 7    // a transfer open this long
	shrinkageMultiple    = 2    // write-offs this many times the previous period
	shrinkageFloor       = 10   // ... and at least this many units
	shrinkagePeriodDays  = 30   // the period compared

	// Fabrication.
	jobStuckDays          = 14 // a job in one status this long
	jobHoldStuckDays      = 7  // a job on hold this long
	scheduleOverdueDays   = 3  // an unfinished schedule activity this late
	templateRevisionCount = 4  // revisions on one job
	jobsPerOrderCount     = 2  // jobs on one sales order

	// Ledger and finance.
	cashTransferMultiple = 5    // a transfer this many times the median
	cashTransferFloor    = 1000 // ... and at least this large
	cashTransferHistory  = 3    // earlier transfers needed to compare
	coaChangeDays        = 7    // chart-of-accounts changes examined
)

// excluded status codes per family; passed to queries as text[].
var (
	salesInactiveStatuses = []string{"CANC", "VOID", "RJCT", "EXPR"}
	invoiceUnpaidSkip     = []string{"PAID", "VOID", "DRFT"}
)
