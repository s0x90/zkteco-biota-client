package biotime

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/url"
	"reflect"
)

const employeesPath = "/personnel/api/employees/"

// Employee is a personnel record.
type Employee struct {
	ID         int    `json:"id"`
	EmpCode    string `json:"emp_code"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	Nickname   string `json:"nickname"`
	FormatName string `json:"format_name"`
	FullName   string `json:"full_name"`
	Photo      string `json:"photo"`
	// DevicePassword is the PIN the employee types on a terminal, returned
	// in clear text. It prints redacted; see [Secret].
	DevicePassword Secret     `json:"device_password"`
	CardNo         FlexString `json:"card_no"`
	// Department is expanded on reads and a bare identifier on writes.
	Department Ref[Department] `json:"department"`
	Position   Ref[Position]   `json:"position"`
	HireDate   Date            `json:"hire_date"`
	// Gender is "M" or "F".
	Gender     string `json:"gender"`
	Birthday   Date   `json:"birthday"`
	VerifyMode int    `json:"verify_mode"`
	EmpType    *int   `json:"emp_type"`
	ContactTel string `json:"contact_tel"`
	OfficeTel  string `json:"office_tel"`
	Mobile     string `json:"mobile"`
	National   string `json:"national"`
	City       string `json:"city"`
	Address    string `json:"address"`
	Postcode   string `json:"postcode"`
	Email      string `json:"email"`
	// EnrollSN is the serial number of the device the employee was enrolled on.
	EnrollSN string `json:"enroll_sn"`
	SSN      string `json:"ssn"`
	Religion string `json:"religion"`
	// AttEmployee holds the attendance settings as nested by 9.0; 8.x
	// returns them as the top-level EnableAtt, EnableOvertime and
	// EnableHoliday instead. Read them through [Employee.AttendanceEnabled],
	// [Employee.OvertimeEnabled] and [Employee.HolidayEnabled], which
	// consult both shapes.
	AttEmployee *AttEmployee `json:"attemployee"`
	// EnableAtt, EnableOvertime and EnableHoliday are the attendance
	// settings as returned by 8.x; nil when the server nests them in
	// AttEmployee. Prefer the accessor methods.
	EnableAtt      *bool `json:"enable_att"`
	EnableOvertime *bool `json:"enable_overtime"`
	EnableHoliday  *bool `json:"enable_holiday"`
	// DevPrivilege is the device role: 0 employee, 14 super administrator.
	DevPrivilege int `json:"dev_privilege"`
	// Area lists the areas whose devices receive this employee.
	Area      []Ref[Area] `json:"area"`
	AppStatus int         `json:"app_status"`
	AppRole   int         `json:"app_role"`
	// FlowRole is the approval workflow role list; its shape varies by
	// server version so it is kept raw.
	FlowRole   json.RawMessage `json:"flow_role,omitzero"`
	UpdateTime DateTime        `json:"update_time"`
	// Biometric enrollment summaries such as "Ver 10:1" or "-"; 8.x reports
	// a bare template count for some of them.
	Fingerprint FlexString `json:"fingerprint"`
	Face        FlexString `json:"face"`
	Palm        FlexString `json:"palm"`
	VLFace      FlexString `json:"vl_face"`
	VLPalm      FlexString `json:"vl_palm"`
	VLFacePhoto FlexString `json:"vl_face_photo"`
	// Extra holds custom attributes defined by the administrator that are
	// not part of the standard schema, keyed by their JSON name.
	Extra map[string]json.RawMessage `json:"-"`
}

// AttEmployee holds the attendance settings of an employee.
type AttEmployee struct {
	ID               int  `json:"id"`
	EnableAttendance bool `json:"enable_attendance"`
	EnableOvertime   bool `json:"enable_overtime"`
	EnableHoliday    bool `json:"enable_holiday"`
	EnableSchedule   bool `json:"enable_schedule"`
}

var employeeType = reflect.TypeFor[Employee]()

// flag returns the attendance setting from whichever shape the server used:
// the 8.x top-level field when present, else the 9.0 nested object. ok is
// false when neither carried it.
func (e *Employee) flag(top *bool, nested func(*AttEmployee) bool) (value, ok bool) {
	switch {
	case top != nil:
		return *top, true
	case e.AttEmployee != nil:
		return nested(e.AttEmployee), true
	default:
		return false, false
	}
}

// AttendanceEnabled reports whether attendance is calculated for the
// employee, on either server generation. ok is false when the record does
// not carry the setting.
func (e *Employee) AttendanceEnabled() (enabled, ok bool) {
	return e.flag(e.EnableAtt, func(a *AttEmployee) bool { return a.EnableAttendance })
}

// OvertimeEnabled reports whether overtime is calculated for the employee.
// See [Employee.AttendanceEnabled].
func (e *Employee) OvertimeEnabled() (enabled, ok bool) {
	return e.flag(e.EnableOvertime, func(a *AttEmployee) bool { return a.EnableOvertime })
}

// HolidayEnabled reports whether holidays apply to the employee. See
// [Employee.AttendanceEnabled].
func (e *Employee) HolidayEnabled() (enabled, ok bool) {
	return e.flag(e.EnableHoliday, func(a *AttEmployee) bool { return a.EnableHoliday })
}

// UnmarshalJSON implements [json.Unmarshaler], capturing unknown members in
// Extra. The self-service password hash that 8.x includes in every employee
// record is dropped rather than reported: it is of no use to an API client
// and would otherwise end up wherever Extra is logged or stored. Set it with
// [EmployeeParams.SelfPassword].
func (e *Employee) UnmarshalJSON(b []byte) error {
	type plain Employee
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := extraFields(b, employeeType)
	if err != nil {
		return err
	}
	delete(extra, "self_password")
	if len(extra) == 0 {
		extra = nil
	}
	*e = Employee(p)
	e.Extra = extra
	return nil
}

// EmployeeFilter selects employees in [EmployeeService.List]. The string
// fields match exactly; use [ListOptions.Search] for a substring match, or
// the server's "<field>_icontains" parameters through Params.
type EmployeeFilter struct {
	ListOptions
	EmpCode   string
	FirstName string
	LastName  string
	// Department filters by department identifier.
	Department int
	AppStatus  *int
	// Params holds additional raw query parameters for filters this struct
	// does not model, e.g. "departments", "areas" or "emp_code_icontains".
	Params map[string]string
}

func (f *EmployeeFilter) values(pageSizeParam string) url.Values {
	if f == nil {
		return url.Values{}
	}
	return buildQuery(f.ListOptions, f.Params, pageSizeParam, func(q query) {
		q.str("emp_code", f.EmpCode)
		q.str("first_name", f.FirstName)
		q.str("last_name", f.LastName)
		q.int("department", f.Department)
		q.intPtr("app_status", f.AppStatus)
	})
}

// EmployeeParams is the payload for creating or updating an employee. Only
// fields that are set are sent, so an update changes just the fields
// provided: nil pointers, zero dates and a nil Area are omitted. An empty,
// non-nil Area ([]int{}) is sent and clears the assignment. On create the
// server requires EmpCode, Department and Area; 9.0 also requires FirstName.
type EmployeeParams struct {
	EmpCode        *string `json:"emp_code,omitzero"`
	FirstName      *string `json:"first_name,omitzero"`
	LastName       *string `json:"last_name,omitzero"`
	Nickname       *string `json:"nickname,omitzero"`
	DevicePassword *string `json:"device_password,omitzero"`
	CardNo         *string `json:"card_no,omitzero"`
	// Department is the department identifier. Required on create.
	Department *int `json:"department,omitzero"`
	Position   *int `json:"position,omitzero"`
	// Area lists area identifiers. Required on create.
	Area       []int   `json:"area,omitzero"`
	HireDate   Date    `json:"hire_date,omitzero"`
	Gender     *string `json:"gender,omitzero"`
	Birthday   Date    `json:"birthday,omitzero"`
	VerifyMode *int    `json:"verify_mode,omitzero"`
	EmpType    *int    `json:"emp_type,omitzero"`
	ContactTel *string `json:"contact_tel,omitzero"`
	OfficeTel  *string `json:"office_tel,omitzero"`
	Mobile     *string `json:"mobile,omitzero"`
	National   *string `json:"national,omitzero"`
	City       *string `json:"city,omitzero"`
	Address    *string `json:"address,omitzero"`
	Postcode   *string `json:"postcode,omitzero"`
	Email      *string `json:"email,omitzero"`
	EnrollSN   *string `json:"enroll_sn,omitzero"`
	SSN        *string `json:"ssn,omitzero"`
	Religion   *string `json:"religion,omitzero"`
	// EnableAtt, EnableOvertime and EnableHoliday are the attendance
	// settings as 8.x accepts them. A server that does not know these
	// members ignores them without complaint, so [EmployeeService.Create]
	// and [EmployeeService.Update] read the record back and report a
	// mismatch with an [*UnsupportedFieldError].
	EnableAtt      *bool `json:"enable_att,omitzero"`
	EnableOvertime *bool `json:"enable_overtime,omitzero"`
	EnableHoliday  *bool `json:"enable_holiday,omitzero"`
	DevPrivilege   *int  `json:"dev_privilege,omitzero"`
	// SelfPassword sets the employee's self-service login password. The
	// server never returns it in clear text; see [Employee.UnmarshalJSON].
	SelfPassword *string `json:"self_password,omitzero"`
	AppStatus    *int    `json:"app_status,omitzero"`
	AppRole      *int    `json:"app_role,omitzero"`
	// Extra holds custom attributes, keyed by their JSON name, that are
	// merged into the payload.
	Extra map[string]any `json:"-"`
}

// MarshalJSON implements [json.Marshaler], merging Extra into the payload.
func (p EmployeeParams) MarshalJSON() ([]byte, error) {
	type plain EmployeeParams
	return mergeExtra(plain(p), p.Extra)
}

// EmployeeService accesses /personnel/api/employees/.
type EmployeeService struct {
	resource[Employee, EmployeeParams, *EmployeeFilter]
}

// Create adds an employee. See [EmployeeParams] for the required fields.
// When params set an attendance flag that the server did not apply, the
// employee has nevertheless been created and the error is an
// [*UnsupportedFieldError] carrying the record; the client does not delete
// it.
func (s *EmployeeService) Create(ctx context.Context, params *EmployeeParams) (*Employee, error) {
	e, err := s.resource.Create(ctx, params)
	if err != nil {
		return nil, err
	}
	if err := s.verifyFlags(ctx, params, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Update changes the provided fields of an employee (HTTP PATCH). When
// params set an attendance flag that the server did not apply, the other
// fields have nevertheless been written and the error is an
// [*UnsupportedFieldError] carrying the record.
func (s *EmployeeService) Update(ctx context.Context, id int, params *EmployeeParams) (*Employee, error) {
	e, err := s.resource.Update(ctx, id, params)
	if err != nil {
		return nil, err
	}
	if err := s.verifyFlags(ctx, params, e); err != nil {
		return nil, err
	}
	return e, nil
}

// flagCheck pairs a requested attendance flag with the accessor that reads
// it back from a record.
type flagCheck struct {
	name string
	want *bool
	got  func(*Employee) (bool, bool)
}

func flagChecks(p *EmployeeParams) []flagCheck {
	return []flagCheck{
		{"enable_att", p.EnableAtt, (*Employee).AttendanceEnabled},
		{"enable_overtime", p.EnableOvertime, (*Employee).OvertimeEnabled},
		{"enable_holiday", p.EnableHoliday, (*Employee).HolidayEnabled},
	}
}

// verifyFlags checks that every attendance flag set in params is reflected
// by the record. A write response that omits a requested setting, as the
// 9.0 create response does, proves nothing either way, so the record is
// fetched once and judged on that. Outcomes: every flag matches; a flag
// differs (ignored by the server); the record does not carry it even on
// the detail view (not reported by the server); or the read-back itself
// failed, which is reported with the cause and no verdict.
func (s *EmployeeService) verifyFlags(ctx context.Context, params *EmployeeParams, e *Employee) error {
	checks := flagChecks(params)
	needsFetch := false
	for _, c := range checks {
		if c.want == nil {
			continue
		}
		if _, ok := c.got(e); !ok {
			needsFetch = true
			break
		}
	}
	if needsFetch {
		if e.ID == 0 {
			return &UnsupportedFieldError{Reason: VerdictNoID, Employee: e}
		}
		full, err := s.Get(ctx, e.ID)
		if err != nil {
			return &UnsupportedFieldError{Reason: VerdictUnverified, Employee: e, Cause: err}
		}
		e = full
	}
	for _, c := range checks {
		if c.want == nil {
			continue
		}
		got, ok := c.got(e)
		switch {
		case !ok:
			return &UnsupportedFieldError{Field: c.name, Reason: VerdictNotReported, Employee: e}
		case got != *c.want:
			return &UnsupportedFieldError{Field: c.name, Reason: VerdictIgnored, Employee: e}
		}
	}
	return nil
}

// List returns one page of employees matching filter (nil for all).
func (s *EmployeeService) List(ctx context.Context, filter *EmployeeFilter) (*Page[Employee], error) {
	return s.resource.List(ctx, filter)
}

// All iterates over every employee matching filter, fetching pages on demand
// by following the server's "next" links. See [ListOptions] for what makes
// a walk stable.
func (s *EmployeeService) All(ctx context.Context, filter *EmployeeFilter) iter.Seq2[Employee, error] {
	return s.resource.All(ctx, filter)
}

// Get returns the employee with the given identifier.
func (s *EmployeeService) Get(ctx context.Context, id int) (*Employee, error) {
	return s.resource.Get(ctx, id)
}

// GetByCode returns the employee with the given employee code, or an error
// matching [ErrNotFound]. Some servers match emp_code as a prefix, so every
// page of candidates is scanned for the exact code.
func (s *EmployeeService) GetByCode(ctx context.Context, empCode string) (*Employee, error) {
	filter := &EmployeeFilter{EmpCode: empCode, ListOptions: ListOptions{PageSize: 100}}
	for e, err := range s.All(ctx, filter) {
		if err != nil {
			return nil, err
		}
		if e.EmpCode == empCode {
			return &e, nil
		}
	}
	return nil, &Error{StatusCode: http.StatusNotFound, Method: http.MethodGet, URL: employeesPath, Message: "employee " + empCode + " not found"}
}
