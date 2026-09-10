package biotime

import (
	"context"
	"encoding/json"
	"iter"
	"net/url"
	"reflect"
	"strconv"
	"time"
)

const transactionsPath = "/iclock/api/transactions/"

// PunchState is the kind of punch recorded by a transaction.
type PunchState int

// Punch states as reported by ZKBio Time.
const (
	PunchCheckIn     PunchState = 0
	PunchCheckOut    PunchState = 1
	PunchBreakOut    PunchState = 2
	PunchBreakIn     PunchState = 3
	PunchOvertimeIn  PunchState = 4
	PunchOvertimeOut PunchState = 5
	// PunchUnknown is reported by devices that do not track a punch state.
	PunchUnknown PunchState = 255
)

// String implements fmt.Stringer.
func (p PunchState) String() string {
	switch p {
	case PunchCheckIn:
		return "Check In"
	case PunchCheckOut:
		return "Check Out"
	case PunchBreakOut:
		return "Break Out"
	case PunchBreakIn:
		return "Break In"
	case PunchOvertimeIn:
		return "Overtime In"
	case PunchOvertimeOut:
		return "Overtime Out"
	case PunchUnknown:
		return "Unknown"
	default:
		return "PunchState(" + strconv.Itoa(int(p)) + ")"
	}
}

// UnmarshalJSON implements [json.Unmarshaler]. The server encodes the state
// as a string ("0") on some versions and as a number on others.
func (p *PunchState) UnmarshalJSON(b []byte) error {
	var v FlexInt
	if err := v.UnmarshalJSON(b); err != nil {
		return err
	}
	*p = PunchState(v)
	return nil
}

// VerifyType is the verification method used for a punch. Values follow the
// device firmware; the 8.x API also reports a human readable
// [Transaction.VerifyTypeDisplay].
type VerifyType int

// UnmarshalJSON implements [json.Unmarshaler].
func (v *VerifyType) UnmarshalJSON(b []byte) error {
	var n FlexInt
	if err := n.UnmarshalJSON(b); err != nil {
		return err
	}
	*v = VerifyType(n)
	return nil
}

// Transaction is an attendance punch uploaded by a device or entered
// manually. Fields not present on a given server generation are left zero.
type Transaction struct {
	ID int `json:"id"`
	// Emp references the employee record. 8.x returns the identifier, 9.0
	// may return null.
	Emp     Ref[Employee] `json:"emp"`
	EmpCode string        `json:"emp_code"`
	// FirstName, LastName, Department and Position are populated by 8.x.
	FirstName  string     `json:"first_name"`
	LastName   string     `json:"last_name"`
	Department string     `json:"department"`
	Position   string     `json:"position"`
	PunchTime  DateTime   `json:"punch_time"`
	PunchState PunchState `json:"punch_state"`
	// PunchStateDisplay is populated by 8.x.
	PunchStateDisplay string     `json:"punch_state_display"`
	VerifyType        VerifyType `json:"verify_type"`
	// VerifyTypeDisplay is populated by 8.x.
	VerifyTypeDisplay string        `json:"verify_type_display"`
	WorkCode          FlexString    `json:"work_code"`
	TerminalSN        string        `json:"terminal_sn"`
	TerminalAlias     string        `json:"terminal_alias"`
	AreaAlias         string        `json:"area_alias"`
	Longitude         *FlexFloat    `json:"longitude"`
	Latitude          *FlexFloat    `json:"latitude"`
	GPSLocation       string        `json:"gps_location"`
	Mobile            FlexString    `json:"mobile"`
	Source            *FlexInt      `json:"source"`
	Purpose           *FlexInt      `json:"purpose"`
	CRC               FlexString    `json:"crc"`
	IsAttendance      *FlexInt      `json:"is_attendance"`
	Reserved          FlexString    `json:"reserved"`
	UploadTime        DateTime      `json:"upload_time"`
	SyncStatus        *FlexInt      `json:"sync_status"`
	SyncTime          DateTime      `json:"sync_time"`
	Terminal          Ref[Terminal] `json:"terminal"`
	// IsMask is 0 or 1, or 255 when mask detection is disabled; nil when the
	// device does not report it.
	IsMask *FlexInt `json:"is_mask"`
	// Temperature is the measured body temperature, or 255 when temperature
	// checking is disabled; nil when the device does not report it.
	Temperature *FlexFloat `json:"temperature"`
	// Extra holds members not part of the standard schema.
	Extra map[string]json.RawMessage `json:"-"`
}

var transactionType = reflect.TypeFor[Transaction]()

// UnmarshalJSON implements [json.Unmarshaler], capturing unknown members in
// Extra.
func (t *Transaction) UnmarshalJSON(b []byte) error {
	type plain Transaction
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := extraFields(b, transactionType)
	if err != nil {
		return err
	}
	*t = Transaction(p)
	t.Extra = extra
	return nil
}

// TransactionFilter selects punches in [TransactionService.List].
//
// Punches arrive continuously and many share a punch_time, so a walk ordered
// by "punch_time" alone is not stable across pages. For a lossless export use
// Ordering "punch_time,id", set EndTime in the past, and continue from there
// with a later window on the next run. The filter is sent as written.
type TransactionFilter struct {
	ListOptions
	EmpCode    string
	TerminalSN string
	// StartTime and EndTime bound punch_time (inclusive). They are formatted
	// in the zone returned by [Location].
	StartTime time.Time
	EndTime   time.Time
	// Params holds additional raw query parameters.
	Params map[string]string
}

func (f *TransactionFilter) values(pageSizeParam string) url.Values {
	if f == nil {
		return url.Values{}
	}
	return buildQuery(f.ListOptions, f.Params, pageSizeParam, func(q query) {
		q.str("emp_code", f.EmpCode)
		q.str("terminal_sn", f.TerminalSN)
		q.time("start_time", f.StartTime)
		q.time("end_time", f.EndTime)
	})
}

// TransactionService accesses /iclock/api/transactions/. Punches are
// read-only through this endpoint.
type TransactionService struct {
	collection[Transaction, *TransactionFilter]
}

// List returns one page of punches matching filter (nil for all).
func (s *TransactionService) List(ctx context.Context, filter *TransactionFilter) (*Page[Transaction], error) {
	return s.collection.List(ctx, filter)
}

// All iterates over every punch matching filter, fetching pages on demand
// by following the server's "next" links. See [ListOptions] for what makes
// a walk stable.
func (s *TransactionService) All(ctx context.Context, filter *TransactionFilter) iter.Seq2[Transaction, error] {
	return s.collection.All(ctx, filter)
}

// Get returns the punch with the given identifier.
func (s *TransactionService) Get(ctx context.Context, id int) (*Transaction, error) {
	return s.collection.Get(ctx, id)
}
