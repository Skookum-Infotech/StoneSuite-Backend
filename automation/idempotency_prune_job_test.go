package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultJobsIncludesIdempotencyPrune(t *testing.T) {
	names := []string{}
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, idempotencyPruneJobName)
}
