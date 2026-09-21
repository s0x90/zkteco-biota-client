package biotime

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
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
	// Integers written the long way are fine; anything that would be
	// truncated is an error, never a silently wrong number.
	for in, want := range map[string]int{`"5.0"`: 5, `1e3`: 1000, `"-7"`: -7} {
		var v FlexInt
		if err := json.Unmarshal([]byte(in), &v); err != nil || int(v) != want {
			t.Errorf("%s: got %d, %v", in, v, err)
		}
	}
	tooBig := []string{`"1.9"`, `5.5`, `1e30`, `-1e30`, `"9223372036854775808"`}
	if math.MaxInt < math.MaxInt64 {
		// 32-bit int: values that fit int64 but not int must not wrap.
		tooBig = append(tooBig, `4294967296`, `"2147483648"`)
	}
	for _, in := range tooBig {
		var v FlexInt
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Errorf("%s: decoded to %d, expected an error", in, v)
		}
	}
}

func TestObjectMembers(t *testing.T) {
	in := []byte(` { "a" : 1 , "b\"q" : "x,}" , "c":{"d":[1,{"e":"}"}]} , "f" : null, "g":true }`)
	var keys []string
	var values []string
	err := objectMembers(in, func(key, value []byte) error {
		keys = append(keys, string(key))
		values = append(values, string(value))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(keys, "|"), `a|b"q|c|f|g`; got != want {
		t.Errorf("keys %q", got)
	}
	if got, want := strings.Join(values, "|"), `1|"x,}"|{"d":[1,{"e":"}"}]}|null|true`; got != want {
		t.Errorf("values %q", got)
	}
	if err := objectMembers([]byte(`{}`), func(_, _ []byte) error { t.Error("called"); return nil }); err != nil {
		t.Error(err)
	}
	// Invalid input is reported, never a panic, and a callback error stops
	// the scan.
	for _, bad := range []string{``, `[]`, `{`, `{"a"}`, `{"a":}`, `{"a":1`, `{"a":1 "b":2}`, `{"a":"x}`, `{"a":{"b":1}`} {
		if err := objectMembers([]byte(bad), func(_, _ []byte) error { return nil }); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	sentinel := errors.New("stop")
	if err := objectMembers([]byte(`{"a":1,"b":2}`), func(_, _ []byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("got %v", err)
	}
}

func BenchmarkTransactionPageDecode(b *testing.B) {
	row := `{"id":1,"emp":17,"emp_code":"100001","first_name":"Harry","last_name":"Potter","department":"Workshop","position":"","punch_time":"2019-03-04 09:50:00","punch_state":"0","punch_state_display":"Check In","verify_type":1,"verify_type_display":"Fingerprint","work_code":"","terminal_sn":"SN0000000001","terminal_alias":"Door","area_alias":"HQ","longitude":null,"latitude":null,"gps_location":"","mobile":"","source":1,"purpose":9,"crc":"","is_attendance":1,"reserved":"","upload_time":"2019-03-04 09:50:05","sync_status":0,"sync_time":null,"terminal":{"id":3,"sn":"SN0000000001","alias":"Door"},"is_mask":null,"temperature":null,"custom":true}`
	rows := make([]string, 1000)
	for i := range rows {
		rows[i] = row
	}
	data := []byte(`{"count":1000,"next":null,"previous":null,"code":0,"msg":"","data":[` + strings.Join(rows, ",") + `]}`)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		var p Page[Transaction]
		if err := json.Unmarshal(data, &p); err != nil {
			b.Fatal(err)
		}
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

// TestPageShapes covers what a body that is not one of the two list
// envelopes decodes to. Reporting "I did not recognize this" as an empty
// page would turn a misrouted request into an export that writes no rows.
func TestPageShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		in      string
		wantErr string
		count   int
		results int
	}{
		"8.x envelope": {in: `{"count":2,"next":null,"results":[{"id":1},{"id":2}]}`, count: 2, results: 2},
		"9.0 envelope": {in: `{"count":2,"next":null,"code":0,"data":[{"id":1},{"id":2}]}`, count: 2, results: 2},
		"empty 8.x":    {in: `{"count":0,"next":null,"results":[]}`},
		"empty 9.0":    {in: `{"count":0,"next":null,"data":[]}`},
		"data null":    {in: `{"count":0,"next":null,"data":null}`},
		// A failure envelope reaches the caller as an *Error built from the
		// code, so it must decode even without a data member.
		"failure envelope":  {in: `{"code":3,"msg":"boom"}`},
		"failure with data": {in: `{"count":0,"msg":"boom","code":3,"data":[]}`},
		// Contradictions: a count with no list, or no envelope at all.
		"data is an object": {in: `{"count":5,"next":null,"data":{"id":1}}`, wantErr: "an object"},
		"data is a string":  {in: `{"count":5,"data":"denied"}`, wantErr: "a string"},
		"data is a number":  {in: `{"count":5,"data":7}`, wantErr: "a number"},
		"bare object":       {in: `{"id":1,"area_code":"2","area_name":"HQ"}`, wantErr: "not a list"},
		"empty object":      {in: `{}`, wantErr: "not a list"},
	} {
		t.Run(name, func(t *testing.T) {
			var p Page[Area]
			err := json.Unmarshal([]byte(tc.in), &p)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("got %v, want an error mentioning %q", err, tc.wantErr)
			case tc.wantErr != "":
				// The value itself must not be quoted back: a body holds
				// personal data.
				if strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "HQ") {
					t.Errorf("error quotes the body: %v", err)
				}
				return
			}
			if p.Count != tc.count || len(p.Results) != tc.results {
				t.Errorf("count %d results %d", p.Count, len(p.Results))
			}
			if p.Results == nil {
				t.Error("Results is nil, want an empty slice")
			}
		})
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
		"device_password": "135790", "card_no": null,
		"department": {"id": 1, "dept_code": "1", "dept_name": "Workshop"}, "position": null,
		"hire_date": "2025-11-13", "gender": null, "birthday": null, "verify_mode": 0, "emp_type": null,
		"enroll_sn": "SN0000000001", "enable_att": true, "enable_overtime": false, "enable_holiday": true,
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
	if e.DevicePassword.Value() != "135790" {
		t.Errorf("device password value %q", e.DevicePassword.Value())
	}
	for _, s := range []string{fmt.Sprintf("%v", e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e), fmt.Sprint(e.DevicePassword)} {
		if strings.Contains(s, "135790") {
			t.Errorf("device PIN leaked through fmt: %s", s)
		}
	}
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("emp", "pin", e.DevicePassword)
	if strings.Contains(logged.String(), "135790") || !strings.Contains(logged.String(), "redacted") {
		t.Errorf("device PIN leaked through slog: %s", logged.String())
	}
	if strings.Contains(string(b), "135790") || !strings.Contains(string(b), `"device_password":"[redacted]"`) {
		t.Errorf("device PIN leaked through JSON: %s", b)
	}
	var jsonLog strings.Builder
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("emp", "employee", e)
	if strings.Contains(jsonLog.String(), "135790") {
		t.Errorf("device PIN leaked through the slog JSON handler: %s", jsonLog.String())
	}
	// A dump of the record cannot be fed back as a credential.
	var roundTrip Employee
	if err := json.Unmarshal(b, &roundTrip); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("redaction placeholder accepted as a value: %v", err)
	}
	var s Secret
	if err := json.Unmarshal([]byte(`"135790"`), &s); err != nil || s.Value() != "135790" {
		t.Errorf("real value rejected: %v", err)
	}
	if err := json.Unmarshal([]byte(`135790`), &s); err != nil || s.Value() != "135790" {
		t.Errorf("numeric value rejected: %v", err)
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
		Extra:      map[string]any{"Passport": "AB123"},
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

	// A key set both on the struct and in Extra is a caller bug that must
	// not be resolved by silently dropping one of the two.
	_, err = json.Marshal(&EmployeeParams{EmpCode: new("1"), Extra: map[string]any{"emp_code": "2"}})
	if err == nil || !strings.Contains(err.Error(), `"emp_code"`) {
		t.Errorf("got %v", err)
	}
	// Set only in Extra, a declared key is sent as written.
	out, err = json.Marshal(&EmployeeParams{Extra: map[string]any{"emp_code": "2"}})
	if err != nil || string(out) != `{"emp_code":"2"}` {
		t.Errorf("got %s, %v", out, err)
	}
}

// TestDaylightSavingEdges pins what a zone with daylight saving does to the
// two hours a year a naive timestamp cannot describe. Neither is an error:
// see parseTime and the README. The test exists so that a change to either
// is deliberate.
func TestDaylightSavingEdges(t *testing.T) {
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// tzdata_test.go embeds the database, so this cannot be a skip:
		// a skip reads as green and this test is the only thing pinning
		// the two hours a year a naive timestamp cannot describe.
		t.Fatalf("zone database unavailable with time/tzdata embedded: %v", err)
	}
	SetLocation(nyc)
	t.Cleanup(func() { SetLocation(nil) })

	// The hour the spring transition skips does not exist: the value is
	// normalized to a neighboring instant and does not round-trip.
	var gap DateTime
	if err := gap.UnmarshalJSON([]byte(`"2025-03-09 02:30:00"`)); err != nil {
		t.Fatal(err)
	}
	if got := gap.String(); got != "2025-03-09 01:30:00" {
		t.Errorf("skipped hour re-encodes as %q", got)
	}

	// The hour the autumn transition repeats exists twice: the earlier of
	// the two instants is chosen, an hour before the other one.
	var dup DateTime
	if err := dup.UnmarshalJSON([]byte(`"2025-11-02 01:30:00"`)); err != nil {
		t.Fatal(err)
	}
	if got := dup.UTC().Format(time.RFC3339); got != "2025-11-02T05:30:00Z" {
		t.Errorf("repeated hour resolves to %s, want the earlier instant", got)
	}
	if got := dup.String(); got != "2025-11-02 01:30:00" {
		t.Errorf("repeated hour re-encodes as %q", got)
	}

	// Every other timestamp round-trips, which is the point of pinning the
	// two that do not.
	var ok DateTime
	if err := ok.UnmarshalJSON([]byte(`"2025-06-15 14:05:00"`)); err != nil {
		t.Fatal(err)
	}
	if got := ok.String(); got != "2025-06-15 14:05:00" {
		t.Errorf("ordinary timestamp re-encodes as %q", got)
	}
}

// TestWestOfUTCRun makes the west-of-UTC run prove it happened. Without a
// zone database TZ resolves to UTC, and `make test-tz` would report ok
// having run the suite a second time in the zone it was trying to leave.
//
// The marker comes from the Makefile rather than from TZ, which is not
// parsed here: TZ may legitimately hold a POSIX form such as UTC0 or a
// path such as :/etc/localtime, and resolving those as zone names would
// fail `make test` for a contributor whose shell sets one, over something
// unrelated to their change.
func TestWestOfUTCRun(t *testing.T) {
	if os.Getenv("BIOTIME_TEST_WEST_OF_UTC") == "" {
		return // a plain run makes no such claim; nothing to assert
	}
	if _, offset := time.Now().Zone(); offset >= 0 {
		t.Fatalf("this run is meant to be west of UTC, but the process is at UTC%+d: TZ did not take effect", offset/3600)
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
