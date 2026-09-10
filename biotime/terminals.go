package biotime

import (
	"context"
	"encoding/json"
	"iter"
	"net/url"
	"reflect"
)

const terminalsPath = "/iclock/api/terminals/"

// Terminal is an attendance device registered with the server.
type Terminal struct {
	ID           int    `json:"id"`
	SN           string `json:"sn"`
	IPAddress    string `json:"ip_address"`
	Alias        string `json:"alias"`
	TerminalName string `json:"terminal_name"`
	FWVer        string `json:"fw_ver"`
	PushVer      string `json:"push_ver"`
	// State is the connection state as reported by the server (e.g. "1"
	// online, "4" offline). It is encoded as a string on some servers.
	State      FlexString `json:"state"`
	TerminalTZ FlexInt    `json:"terminal_tz"`
	Area       Ref[Area]  `json:"area"`
	AreaName   string     `json:"area_name"`
	// LastActivity is the last time the device contacted the server.
	LastActivity     DateTime `json:"last_activity"`
	UserCount        FlexInt  `json:"user_count"`
	FPCount          FlexInt  `json:"fp_count"`
	FaceCount        FlexInt  `json:"face_count"`
	PalmCount        FlexInt  `json:"palm_count"`
	TransactionCount FlexInt  `json:"transaction_count"`
	PushTime         DateTime `json:"push_time"`
	// TransferTime lists the daily upload times as "HH:MM;HH:MM".
	TransferTime     string  `json:"transfer_time"`
	TransferInterval FlexInt `json:"transfer_interval"`
	IsAttendance     FlexInt `json:"is_attendance"`
	// Extra holds members not part of the standard schema.
	Extra map[string]json.RawMessage `json:"-"`
}

var terminalType = reflect.TypeFor[Terminal]()

// UnmarshalJSON implements [json.Unmarshaler], capturing unknown members in
// Extra.
func (t *Terminal) UnmarshalJSON(b []byte) error {
	type plain Terminal
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := extraFields(b, terminalType)
	if err != nil {
		return err
	}
	*t = Terminal(p)
	t.Extra = extra
	return nil
}

// TerminalFilter selects devices in [TerminalService.List].
type TerminalFilter struct {
	ListOptions
	SN    string
	Alias string
	// Area filters by area identifier.
	Area int
	// Params holds additional raw query parameters.
	Params map[string]string
}

func (f *TerminalFilter) values(pageSizeParam string) url.Values {
	q := newQuery()
	if f == nil {
		return q.Values
	}
	f.ListOptions.apply(q.Values, pageSizeParam)
	q.str("sn", f.SN)
	q.str("alias", f.Alias)
	q.int("area", f.Area)
	for k, v := range f.Params {
		q.Set(k, v)
	}
	return q.Values
}

// TerminalService accesses /iclock/api/terminals/.
type TerminalService struct {
	c *Client
}

// List returns one page of devices matching filter (nil for all).
func (s *TerminalService) List(ctx context.Context, filter *TerminalFilter) (*Page[Terminal], error) {
	return listPage[Terminal](ctx, s.c, terminalsPath, filter.values(s.c.pageSizeParam))
}

// All iterates over every device matching filter.
func (s *TerminalService) All(ctx context.Context, filter *TerminalFilter) iter.Seq2[Terminal, error] {
	return iterate[Terminal](ctx, s.c, terminalsPath, filter.values(s.c.pageSizeParam))
}

// Get returns the device with the given identifier.
func (s *TerminalService) Get(ctx context.Context, id int) (*Terminal, error) {
	var t Terminal
	if err := s.c.Get(ctx, detailPath(terminalsPath, id), nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
