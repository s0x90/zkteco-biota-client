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
	EnrollSN    string       `json:"enroll_sn"`
	SSN         string       `json:"ssn"`
	Religion    string       `json:"religion"`
	AttEmployee *AttEmployee `json:"attemployee"`
	// DevPrivilege is the device role: 0 employee, 14 super administrator.
	DevPrivilege int `json:"dev_privilege"`
	// Area lists the areas whose devices receive this employee.
	Area      []Ref[Area] `json:"area"`
	AppStatus int         `json:"app_status"`
	AppRole   int         `json:"app_role"`
	// FlowRole is the approval workflow role list; its shape varies by
	// server version so it is kept raw.
	FlowRole   json.RawMessage `json:"flow_role,omitempty"`
	UpdateTime DateTime        `json:"update_time"`
	// Biometric enrollment summaries such as "Ver 10:1" or "-".
	Fingerprint string `json:"fingerprint"`
	Face        string `json:"face"`
	Palm        string `json:"palm"`
	VLFace      string `json:"vl_face"`
	VLPalm      string `json:"vl_palm"`
	VLFacePhoto string `json:"vl_face_photo"`
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
// Extra.
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
	*e = Employee(p)
	e.Extra = extra
	return nil
}

// EmployeeFilter selects employees in [EmployeeService.List].
type EmployeeFilter struct {
	ListOptions
	EmpCode   string
	FirstName string
	LastName  string
	// Department filters by department identifier.
	Department int
	AppStatus  *int
	// Params holds additional raw query parameters for filters this struct
	// does not model, e.g. "departments" or "areas" on some servers.
	Params map[string]string
}

func (f *EmployeeFilter) values(pageSizeParam string) url.Values {
	q := newQuery()
	if f == nil {
		return q.Values
	}
	f.ListOptions.apply(q.Values, pageSizeParam)
	q.str("emp_code", f.EmpCode)
	q.str("first_name", f.FirstName)
	q.str("last_name", f.LastName)
	q.int("department", f.Department)
	q.intPtr("app_status", f.AppStatus)
	for k, v := range f.Params {
		q.Set(k, v)
	}
	return q.Values
}

// EmployeeParams is the payload for creating or updating an employee. Only
// non-nil fields are sent, so an update changes just the fields provided.
type EmployeeParams struct {
	EmpCode        *string `json:"emp_code,omitempty"`
	FirstName      *string `json:"first_name,omitempty"`
	LastName       *string `json:"last_name,omitempty"`
	Nickname       *string `json:"nickname,omitempty"`
	DevicePassword *string `json:"device_password,omitempty"`
	CardNo         *string `json:"card_no,omitempty"`
	// Department is the department identifier. Required on create.
	Department *int `json:"department,omitempty"`
	Position   *int `json:"position,omitempty"`
	// Area lists area identifiers. Required on create.
	Area         []int   `json:"area,omitempty"`
	HireDate     *Date   `json:"hire_date,omitempty"`
	Gender       *string `json:"gender,omitempty"`
	Birthday     *Date   `json:"birthday,omitempty"`
	VerifyMode   *int    `json:"verify_mode,omitempty"`
	EmpType      *int    `json:"emp_type,omitempty"`
	ContactTel   *string `json:"contact_tel,omitempty"`
	OfficeTel    *string `json:"office_tel,omitempty"`
	Mobile       *string `json:"mobile,omitempty"`
	National     *string `json:"national,omitempty"`
	City         *string `json:"city,omitempty"`
	Address      *string `json:"address,omitempty"`
	Postcode     *string `json:"postcode,omitempty"`
	Email        *string `json:"email,omitempty"`
	SSN          *string `json:"ssn,omitempty"`
	Religion     *string `json:"religion,omitempty"`
	DevPrivilege *int    `json:"dev_privilege,omitempty"`
	AppStatus    *int    `json:"app_status,omitempty"`
	AppRole      *int    `json:"app_role,omitempty"`
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
	c *Client
}

// List returns one page of employees matching filter (nil for all).
func (s *EmployeeService) List(ctx context.Context, filter *EmployeeFilter) (*Page[Employee], error) {
	return listPage[Employee](ctx, s.c, employeesPath, filter.values(s.c.pageSizeParam))
}

// All iterates over every employee matching filter, fetching pages on demand.
func (s *EmployeeService) All(ctx context.Context, filter *EmployeeFilter) iter.Seq2[Employee, error] {
	return iterate[Employee](ctx, s.c, employeesPath, filter.values(s.c.pageSizeParam))
}

// Get returns the employee with the given identifier.
func (s *EmployeeService) Get(ctx context.Context, id int) (*Employee, error) {
	var e Employee
	if err := s.c.Get(ctx, detailPath(employeesPath, id), nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// GetByCode returns the employee with the given employee code, or an error
// matching [ErrNotFound].
func (s *EmployeeService) GetByCode(ctx context.Context, empCode string) (*Employee, error) {
	page, err := s.List(ctx, &EmployeeFilter{EmpCode: empCode, ListOptions: ListOptions{PageSize: 50}})
	if err != nil {
		return nil, err
	}
	for i := range page.Results {
		if page.Results[i].EmpCode == empCode {
			return &page.Results[i], nil
		}
	}
	return nil, &Error{StatusCode: http.StatusNotFound, Method: http.MethodGet, URL: employeesPath, Message: "employee " + empCode + " not found"}
}

// Create adds an employee. EmpCode, FirstName, Department and Area are
// required by the server.
func (s *EmployeeService) Create(ctx context.Context, params *EmployeeParams) (*Employee, error) {
	var e Employee
	if err := s.c.Post(ctx, employeesPath, params, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Update changes the provided fields of an employee (HTTP PATCH).
func (s *EmployeeService) Update(ctx context.Context, id int, params *EmployeeParams) (*Employee, error) {
	var e Employee
	if err := s.c.Do(ctx, http.MethodPatch, detailPath(employeesPath, id), nil, params, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Delete removes an employee.
func (s *EmployeeService) Delete(ctx context.Context, id int) error {
	return s.c.Do(ctx, http.MethodDelete, detailPath(employeesPath, id), nil, nil, nil)
}
