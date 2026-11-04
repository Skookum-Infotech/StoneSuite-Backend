// Package installschedule holds install, template and service appointments for fabrication jobs.
package installschedule

import (
	"errors"
	"fmt"
	"time"

	"stonesuite-backend/dateonly"
)

// VisitType is the kind of site visit.
type VisitType string

// Visit types.
const (
	VisitTemplate VisitType = "template"
	VisitInstall  VisitType = "install"
	VisitService  VisitType = "service"
)

// Status is the lifecycle state of an appointment.
type Status string

// Appointment statuses.
const (
	StatusScheduled Status = "scheduled"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
)

// CrewRole is a crew member's role on an appointment.
type CrewRole string

// Crew roles. The member role is CrewMemberRole because CrewMember names the response struct.
const (
	CrewLead       CrewRole = "lead"
	CrewMemberRole CrewRole = "member"
)

// Limits and sizes.
const (
	MaxDuration     = 14 * 24 * time.Hour
	SlotMinutes     = 15
	MaxCrew         = 10
	MaxNotes        = 2000
	MaxReason       = 500
	MaxFeedDays     = 93
	FeedRowCap      = 2000
	ConflictListCap = 20
)

// CrewInput is one crew assignment in a request.
type CrewInput struct {
	EmployeeID int
	Role       CrewRole
}

// CreateInput is the create request after JSON decoding.
type CreateInput struct {
	FabricationJobUUID   string
	VisitType            VisitType
	StartLocal, EndLocal string
	AllDay               bool
	Crew                 []CrewInput
	Notes                string
	RequestID            string
	AcknowledgeConflicts bool
	ActorUserID          string `json:"-"`
}

// UpdateInput is the patch request; nil pointers mean unchanged.
type UpdateInput struct {
	StartLocal, EndLocal *string
	AllDay               *bool
	Crew                 *[]CrewInput
	Notes                *string
	VisitType            *VisitType
	ExpectedVersion      int
	AcknowledgeConflicts bool
	Reason               string
	ActorUserID          string `json:"-"`
}

// Interval is a resolved appointment span in UTC plus its local dates.
type Interval struct {
	Start, End                   time.Time
	AllDay                       bool
	LocalStartDate, LocalEndDate dateonly.Date
	TZ                           string
}

// CrewMember is one assigned employee in a response.
type CrewMember struct {
	EmployeeID int      `json:"employeeId"`
	Name       string   `json:"name"`
	Role       CrewRole `json:"role"`
}

// Appointment is the response shape for one visit.
type Appointment struct {
	ID               string        `json:"id"`
	FabricationJobID string        `json:"fabricationJobId"`
	JobNumber        string        `json:"jobNumber"`
	CustomerName     string        `json:"customerName"`
	SiteCity         string        `json:"siteCity"`
	VisitType        VisitType     `json:"visitType"`
	Status           Status        `json:"status"`
	StartsAt         time.Time     `json:"startsAt"`
	EndsAt           time.Time     `json:"endsAt"`
	StartLocal       string        `json:"startLocal"`
	EndLocal         string        `json:"endLocal"`
	Timezone         string        `json:"timezone"`
	AllDay           bool          `json:"allDay"`
	LocalStartDate   dateonly.Date `json:"localStartDate"`
	LocalEndDate     dateonly.Date `json:"localEndDate"`
	Crew             []CrewMember  `json:"crew"`
	Notes            string        `json:"notes"`
	CancelReason     *string       `json:"cancelReason"`
	Version          int           `json:"version"`
}

// Conflict describes one overlap found while scheduling.
type Conflict struct {
	Type            string `json:"type"`
	Severity        string `json:"severity"`
	EmployeeID      int    `json:"employeeId,omitempty"`
	EmployeeName    string `json:"employeeName,omitempty"`
	AppointmentUUID string `json:"appointmentId,omitempty"`
	JobNumber       string `json:"jobNumber,omitempty"`
	StartLocal      string `json:"startLocal"`
	EndLocal        string `json:"endLocal"`
}

// Sentinel errors.
var (
	ErrNotFound             = errors.New("installschedule: appointment not found")
	ErrStaleVersion         = errors.New("installschedule: stale version")
	ErrInvalidStatus        = errors.New("installschedule: invalid status for this action")
	ErrNotStarted           = errors.New("installschedule: appointment has not started")
	ErrJobClosed            = errors.New("installschedule: fabrication job is closed")
	ErrNonexistentLocalTime = errors.New("installschedule: local time does not exist (daylight saving gap)")
)

// ValidationError is a 400-class input problem tied to one field.
type ValidationError struct {
	Field, Message string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("installschedule: invalid %s: %s", e.Field, e.Message)
}

// ConflictError reports overlaps that the caller must acknowledge.
type ConflictError struct {
	Conflicts []Conflict
	More      int
}

// Error implements error.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("installschedule: %d scheduling conflicts", len(e.Conflicts)+e.More)
}
