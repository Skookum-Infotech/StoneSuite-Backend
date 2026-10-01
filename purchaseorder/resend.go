package purchaseorder

// resendableStatuses are the statuses in which the vendor already holds the
// order. Mailing it again then is a retry or a reminder; before that it would
// be a way round the approval flow that "Send to Vendor" goes through.
var resendableStatuses = map[string]bool{
	SentStatusCode: true,
	"PART":         true,
	"RCVD":         true,
}

// CanResendToVendor reports whether an order in statusCode may be emailed to
// its vendor again.
func CanResendToVendor(statusCode string) bool {
	return resendableStatuses[statusCode]
}
