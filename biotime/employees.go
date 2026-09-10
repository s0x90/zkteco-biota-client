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
	ID             int        `json:"id"`
	EmpCode        string     `json:"emp_code"`
	FirstName      string     `json:"first_name"`
	LastName       string     `json:"last_name"`
	Nickname       string     `json:"nickname"`
	FormatName     string     `json:"format_name"`
	FullName       string     `json:"full_name"`
	Photo          string     `json:"photo"`
	DevicePassword FlexString `json:"device_password"`
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
	// EnableHoliday instead.
	AttEmployee *AttEmployee `json:"attemployee"`
	// EnableAtt, EnableOvertime and EnableHoliday are the attendance
	// settings as returned by 8.x; nil when the server nests them in
	// AttEmployee.
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
	// settings; 8.x accepts them as top-level fields.
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
