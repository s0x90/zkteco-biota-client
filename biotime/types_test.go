package biotime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDateTimeJSON(t *testing.T) {
	SetLocation(time.UTC)
	t.Cleanup(func() { SetLocation(nil) })

	cases := map[string]time.Time{
		`"2024-06-26 17:16:09"`:        time.Date(2024, 6, 26, 17, 16, 9, 0, time.UTC),
		`"2024-06-26T17:16:09"`:        time.Date(2024, 6, 26, 17, 16, 9, 0, time.UTC),
		`"2024-06-26T17:16:09.123456"`: time.Date(2024, 6, 26, 17, 16, 9, 123456000, time.UTC),
		`"2024-06-26T17:16:09+03:00"`:  time.Date(2024, 6, 26, 14, 16, 9, 0, time.UTC),
		`"2024-06-26"`:                 time.Date(2024, 6, 26, 0, 0, 0, 0, time.UTC),
		`null`:                         {},
		`""`:                           {},
	}
	for in, want := range cases {
		var got DateTime
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("%s: got %v want %v", in, got.Time, want)
		}
	}

	var bad DateTime
	if err := json.Unmarshal([]byte(`"yesterday"`), &bad); err == nil {
		t.Error("expected error for unparsable time")
	}

	out, err := json.Marshal(struct {
		A DateTime `json:"a"`
		B DateTime `json:"b"`
	}{A: NewDateTime(time.Date(2019, 3, 4, 9, 50, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"2019-03-04 09:50:00","b":null}`; string(out) != want {
		t.Errorf("got %s want %s", out, want)
	}
}

func TestDateJSON(t *testing.T) {
	SetLocation(time.UTC)
	t.Cleanup(func() { SetLocation(nil) })

	var d Date
	if err := json.Unmarshal([]byte(`"2018-06-26"`), &d); err != nil {
		t.Fatal(err)
	}
	if !d.Equal(time.Date(2018, 6, 26, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v", d.Time)
	}
	if err := json.Unmarshal([]byte(`null`), &d); err != nil || !d.IsZero() {
		t.Errorf("null: %v %v", d, err)
	}
	out, _ := json.Marshal(NewDate(time.Date(2024, 6, 26, 15, 4, 5, 0, time.UTC)))
	if string(out) != `"2024-06-26"` {
		t.Errorf("got %s", out)
	}
}

func TestFlexTypes(t *testing.T) {
	var v struct {
		S1 FlexString `json:"s1"`
		S2 FlexString `json:"s2"`
		S3 FlexString `json:"s3"`
		I1 FlexInt    `json:"i1"`
		I2 FlexInt    `json:"i2"`
		I3 *FlexInt   `json:"i3"`
		I4 *FlexInt   `json:"i4"`
		F1 FlexFloat  `json:"f1"`
		F2 *FlexFloat `json:"f2"`
	}
	in := `{"s1":"5659812","s2":5659812,"s3":null,"i1":"4","i2":7,"i3":null,"i4":"255","f1":"36.4","f2":36.4}`
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatal(err)
	}
	if v.S1 != "5659812" || v.S2 != "5659812" || v.S3 != "" {
		t.Errorf("strings: %+v", v)
	}
	if v.I1 != 4 || v.I2 != 7 || v.I3 != nil || v.I4 == nil || *v.I4 != 255 {
		t.Errorf("ints: %+v", v)
	}
	if v.F1 != 36.4 || v.F2 == nil || *v.F2 != 36.4 {
		t.Errorf("floats: %+v", v)
	}

	var bad FlexInt
	if err := json.Unmarshal([]byte(`"abc"`), &bad); err == nil {
		t.Error("expected error for non-numeric string")
	}
}

func TestRefJSON(t *testing.T) {
	var got struct {
		A Ref[Department]   `json:"a"`
		B Ref[Department]   `json:"b"`
		C Ref[Department]   `json:"c"`
		D []Ref[Area]       `json:"d"`
		E Ref[Department]   `json:"e"`
		F []Ref[Department] `json:"f"`
	}
	in := `{"a":{"id":1,"dept_code":"1","dept_name":"Department"},"b":2,"c":null,"d":[1,{"id":2,"area_code":"2","area_name":"ZKTeco"}],"e":"3","f":[]}`
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatal(err)
	}
	if got.A.ID != 1 || got.A.Object == nil || got.A.Object.DeptName != "Department" {
		t.Errorf("A: %+v", got.A)
	}
	if got.B.ID != 2 || got.B.Object != nil {
		t.Errorf("B: %+v", got.B)
	}
	if !got.C.IsZero() {
		t.Errorf("C: %+v", got.C)
	}
	if len(got.D) != 2 || got.D[0].ID != 1 || got.D[1].ID != 2 || got.D[1].Object == nil || got.D[1].Object.AreaName != "ZKTeco" {
		t.Errorf("D: %+v", got.D)
	}
	if got.E.ID != 3 {
		t.Errorf("E: %+v", got.E)
	}
	if len(got.F) != 0 {
		t.Errorf("F: %+v", got.F)
	}

	out, err := json.Marshal(struct {
		A Ref[Department] `json:"a"`
		B Ref[Department] `json:"b"`
	}{A: RefID[Department](5)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":5,"b":null}`; string(out) != want {
		t.Errorf("got %s want %s", out, want)
	}
}

func TestPageJSON(t *testing.T) {
	legacy := `{"count":2,"next":"http://x/?page=2","previous":null,"results":[{"id":1},{"id":2}]}`
	var p Page[Department]
	if err := json.Unmarshal([]byte(legacy), &p); err != nil {
		t.Fatal(err)
	}
	if p.Count != 2 || !p.HasNext() || p.Previous != "" || len(p.Results) != 2 || p.Results[1].ID != 2 {
		t.Errorf("legacy: %+v", p)
	}

	modern := `{"count":1,"next":null,"previous":null,"msg":"","code":0,"data":[{"id":7}]}`
	if err := json.Unmarshal([]byte(modern), &p); err != nil {
		t.Fatal(err)
	}
	if p.Count != 1 || p.HasNext() || len(p.Results) != 1 || p.Results[0].ID != 7 || p.Code != 0 {
		t.Errorf("modern: %+v", p)
	}

	empty := `{"count":0,"next":null,"previous":null,"msg":"","code":0,"results":[],"data":[]}`
	if err := json.Unmarshal([]byte(empty), &p); err != nil {
		t.Fatal(err)
	}
	if p.Results == nil || len(p.Results) != 0 {
		t.Errorf("empty: %+v", p)
	}

	failed := `{"count":0,"msg":"boom","code":3,"data":[]}`
	if err := json.Unmarshal([]byte(failed), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != 3 || p.Msg != "boom" {
		t.Errorf("failed: %+v", p)
	}
}

func TestEmployeeExtraFields(t *testing.T) {
	SetLocation(time.UTC)
	t.Cleanup(func() { SetLocation(nil) })

	in := `{
		"id": 1, "emp_code": "1", "first_name": "Harry", "last_name": "Potter",
		"department": {"id": 1, "dept_code": "1", "dept_name": "Department"},
		"position": null, "hire_date": "2018-06-26", "card_no": null,
		"attemployee": {"id": 1, "enable_attendance": true},
		"area": [{"id": 2, "area_code": "2", "area_name": "ZKTeco"}],
		"update_time": "2024-06-26 17:16:09", "fingerprint": "Ver 10:1",
		"CNIC": null, "Passport": "AB123", "Primary Job": 4
	}`
	var e Employee
	if err := json.Unmarshal([]byte(in), &e); err != nil {
		t.Fatal(err)
	}
	if e.ID != 1 || e.FirstName != "Harry" || e.Department.ID != 1 || e.Department.Object.DeptName != "Department" {
		t.Errorf("core: %+v", e)
	}
	if !e.Position.IsZero() || e.HireDate.String() != "2018-06-26" || e.UpdateTime.String() != "2024-06-26 17:16:09" {
		t.Errorf("dates/refs: %+v", e)
	}
	if e.AttEmployee == nil || !e.AttEmployee.EnableAttendance {
		t.Errorf("attemployee: %+v", e.AttEmployee)
	}
	if on, ok := e.AttendanceEnabled(); !ok || !on {
		t.Errorf("AttendanceEnabled from nested object: %v %v", on, ok)
	}
	if _, ok := e.OvertimeEnabled(); !ok {
		t.Error("OvertimeEnabled should be known when attemployee is present")
	}
	if len(e.Area) != 1 || e.Area[0].Object.AreaName != "ZKTeco" {
		t.Errorf("area: %+v", e.Area)
	}
	if len(e.Extra) != 3 || string(e.Extra["Passport"]) != `"AB123"` || string(e.Extra["Primary Job"]) != `4` {
		t.Errorf("extra: %v", e.Extra)
	}
	if _, leaked := e.Extra["emp_code"]; leaked {
		t.Error("known field reported as extra")
	}

	var plain Employee
	if err := json.Unmarshal([]byte(`{"id":2,"emp_code":"2"}`), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.Extra != nil {
		t.Errorf("expected nil Extra, got %v", plain.Extra)
	}
}

// TestEmployeeLegacyShape decodes an employee as returned by BioTime 8.x,
// where the attendance flags are top level and the record carries the
// self-service password hash.
func TestEmployeeLegacyShape(t *testing.T) {
	SetLocation(time.UTC)
	t.Cleanup(func() { SetLocation(nil) })

	in := `{
		"id": 4, "emp_code": "1", "first_name": "admin", "last_name": null, "nickname": null,
		"device_password": "441820", "card_no": null,
		"department": {"id": 1, "dept_code": "1", "dept_name": "Workshop"}, "position": null,
		"hire_date": "2025-11-13", "gender": null, "birthday": null, "verify_mode": 0, "emp_type": null,
		"enroll_sn": "NYU7251601121", "enable_att": true, "enable_overtime": false, "enable_holiday": true,
		"dev_privilege": 14, "self_password": "pbkdf2_sha256$36000$salt$hash", "flow_role": [],
		"area": [{"id": 2, "area_code": "2", "area_name": "A"}, {"id": 3, "area_code": "3", "area_name": "B"}],
		"app_status": 0, "app_role": 1, "update_time": "2026-05-22 11:33:11",
		"fingerprint": "-", "face": "-", "palm": "-", "vl_face": 1
	}`
	var e Employee
	if err := json.Unmarshal([]byte(in), &e); err != nil {
		t.Fatal(err)
	}
	if e.ID != 4 || e.LastName != "" || e.EmpType != nil || e.DevPrivilege != 14 || len(e.Area) != 2 {
		t.Errorf("core: %+v", e)
	}
	if e.EnableAtt == nil || !*e.EnableAtt || e.EnableOvertime == nil || *e.EnableOvertime || e.EnableHoliday == nil || !*e.EnableHoliday {
		t.Errorf("attendance flags: %v %v %v", e.EnableAtt, e.EnableOvertime, e.EnableHoliday)
	}
	if e.AttEmployee != nil {
		t.Errorf("attemployee should be absent on 8.x, got %+v", e.AttEmployee)
	}
	if e.VLFace != "1" || e.Face != "-" {
		t.Errorf("biometric summaries: vl_face %q face %q", e.VLFace, e.Face)
	}
	if on, ok := e.AttendanceEnabled(); !ok || !on {
		t.Errorf("AttendanceEnabled: %v %v", on, ok)
	}
	if on, ok := e.OvertimeEnabled(); !ok || on {
		t.Errorf("OvertimeEnabled: %v %v", on, ok)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if e.DevicePassword.Value() != "441820" {
		t.Errorf("device password value %q", e.DevicePassword.Value())
	}
	for _, s := range []string{fmt.Sprintf("%v", e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e), fmt.Sprint(e.DevicePassword)} {
		if strings.Contains(s, "441820") {
			t.Errorf("device PIN leaked through fmt: %s", s)
		}
	}
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("emp", "pin", e.DevicePassword)
	if strings.Contains(logged.String(), "441820") || !strings.Contains(logged.String(), "redacted") {
		t.Errorf("device PIN leaked through slog: %s", logged.String())
	}
	if !strings.Contains(string(b), `"device_password":"441820"`) {
		t.Error("JSON encoding must keep the value")
	}

	var none Employee
	if _, ok := none.AttendanceEnabled(); ok {
		t.Error("flag reported as known on an empty record")
	}
	if e.Extra != nil {
		t.Errorf("password hash or other members leaked into Extra: %v", e.Extra)
	}

	var reencoded map[string]json.RawMessage
	_ = json.Unmarshal(b, &reencoded)
	if _, ok := reencoded["self_password"]; ok {
		t.Error("self_password re-encoded")
	}
}

func TestEmployeeParamsJSON(t *testing.T) {
	// NewDate takes the calendar date in Location(); pin it so the expected
	// value does not depend on where the test runs.
	SetLocation(time.UTC)
	t.Cleanup(func() { SetLocation(nil) })

	p := EmployeeParams{
		EmpCode:    new("employee333"),
		FirstName:  new("emp3"),
		Department: new(1),
		Area:       []int{1},
		HireDate:   NewDate(time.Date(2024, 6, 26, 0, 0, 0, 0, time.UTC)),
		Extra:      map[string]any{"Passport": "AB123", "emp_code": "ignored"},
	}
	out, err := json.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["emp_code"] != "employee333" || m["first_name"] != "emp3" || m["department"] != float64(1) {
		t.Errorf("got %s", out)
	}
	if m["hire_date"] != "2024-06-26" || m["Passport"] != "AB123" {
		t.Errorf("got %s", out)
	}
	for _, k := range []string{"last_name", "birthday", "area_omitted"} {
		if _, ok := m[k]; ok {
			t.Errorf("unset field %q was encoded: %s", k, out)
		}
	}

	// A non-nil empty Area is a request to clear the assignment and must be
	// sent; a nil one is omitted.
	out, _ = json.Marshal(&EmployeeParams{Area: []int{}})
	if string(out) != `{"area":[]}` {
		t.Errorf("empty area: %s", out)
	}
	out, _ = json.Marshal(&EmployeeParams{})
	if string(out) != `{}` {
		t.Errorf("zero params: %s", out)
	}
	if _, ok := m["Extra"]; ok {
		t.Errorf("Extra map itself was encoded: %s", out)
	}
}

func TestPunchStateString(t *testing.T) {
	var tx Transaction
	if err := json.Unmarshal([]byte(`{"punch_state":"1","verify_type":15}`), &tx); err != nil {
		t.Fatal(err)
	}
	if tx.PunchState != PunchCheckOut || tx.PunchState.String() != "Check Out" || tx.VerifyType != 15 {
		t.Errorf("%+v", tx)
	}
	if got := PunchState(42).String(); got != "PunchState(42)" {
		t.Error(got)
	}
}

func TestTimeEncodingUsesServerZone(t *testing.T) {
	// The process runs in UTC, the server is at UTC+3: encoded values must
	// carry the server's wall clock, and dates must be the server's date.
	SetLocation(time.FixedZone("srv", 3*3600))
	t.Cleanup(func() { SetLocation(nil) })

	instant := time.Date(2024, 6, 26, 22, 30, 0, 0, time.UTC) // 01:30 next day on the server
	if got := NewDateTime(instant).String(); got != "2024-06-27 01:30:00" {
		t.Errorf("DateTime: %s", got)
	}
	out, _ := json.Marshal(NewDateTime(instant))
	if string(out) != `"2024-06-27 01:30:00"` {
		t.Errorf("DateTime JSON: %s", out)
	}
	if got := NewDate(instant).String(); got != "2024-06-27" {
		t.Errorf("Date: %s", got)
	}
	// Round trip: what the server sent comes back unchanged.
	var d DateTime
	if err := json.Unmarshal([]byte(`"2024-06-27 01:30:00"`), &d); err != nil || d.String() != "2024-06-27 01:30:00" {
		t.Errorf("round trip: %v %v", d, err)
	}
}
