package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultJobsIncludesDomainEventDrain(t *testing.T) {
	names := []string{}
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, "domain_event_drain")
}
