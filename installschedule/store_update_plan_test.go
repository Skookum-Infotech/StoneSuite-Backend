package installschedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dateonly"
)

func planFixture(t *testing.T) (*Appointment, *time.Location, time.Time) {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	start := time.Date(2032, 6, 10, 8, 0, 0, 0, loc)
	d := dateonly.New(start)
	return &Appointment{
		VisitType: VisitInstall, StartsAt: start.UTC(), EndsAt: start.Add(4 * time.Hour).UTC(),
		StartLocal: "2032-06-10T08:00", EndLocal: "2032-06-10T12:00", Timezone: "America/Chicago",
		LocalStartDate: d, LocalEndDate: d, Notes: "n",
		Crew: []CrewMember{{EmployeeID: 1, Role: CrewLead}, {EmployeeID: 2, Role: CrewMemberRole}},
	}, loc, start.Add(-72 * time.Hour)
}

func TestPlanUpdate(t *testing.T) {
	s := func(v string) *string { return &v }
	crew := func(c ...CrewInput) *[]CrewInput { return &c }
	tests := []struct {
		name       string
		in         UpdateInput
		wantErr    bool
		time, crew bool
		added, rm  []int
	}{
		{name: "empty", in: UpdateInput{}, wantErr: true},
		{name: "same values", in: UpdateInput{StartLocal: s("2032-06-10T08:00"), Notes: s("n")}, wantErr: true},
		{name: "reschedule", in: UpdateInput{StartLocal: s("2032-06-10T09:00"), EndLocal: s("2032-06-10T13:00")}, time: true},
		{name: "swap member", in: UpdateInput{Crew: crew(CrewInput{1, CrewLead}, CrewInput{3, CrewMemberRole})}, crew: true, added: []int{3}, rm: []int{2}},
		{name: "role swap only", in: UpdateInput{Crew: crew(CrewInput{2, CrewLead}, CrewInput{1, CrewMemberRole})}, crew: true},
		{name: "two leads", in: UpdateInput{Crew: crew(CrewInput{1, CrewLead}, CrewInput{2, CrewLead})}, wantErr: true},
		{name: "backdated", in: UpdateInput{StartLocal: s("2032-06-01T09:00"), EndLocal: s("2032-06-01T10:00")}, wantErr: true},
		{name: "bad visit type", in: UpdateInput{VisitType: func() *VisitType { v := VisitType("x"); return &v }()}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cur, loc, now := planFixture(t)
			p, err := planUpdate(cur, tc.in, loc, now)
			if tc.wantErr {
				var ve *ValidationError
				require.ErrorAs(t, err, &ve)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.time, p.timeChanged)
			assert.Equal(t, tc.crew, p.crewChanged)
			assert.ElementsMatch(t, tc.added, p.added)
			assert.ElementsMatch(t, tc.rm, p.removed)
		})
	}
}
