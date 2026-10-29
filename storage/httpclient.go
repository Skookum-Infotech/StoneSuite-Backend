package storage

import (
	"net/http"
	"time"
)

// storageHTTPTimeout bounds every outbound R2 / Cloudflare call so a stalled
// upstream cannot pin a request goroutine indefinitely.
const storageHTTPTimeout = 2 * time.Minute

// storageHTTPClient is the shared client for R2 calls; unlike
// http.DefaultClient it has a total-request timeout.
var storageHTTPClient = &http.Client{Timeout: storageHTTPTimeout}
