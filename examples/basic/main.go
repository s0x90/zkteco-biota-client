// Command basic demonstrates the biotime client against a live server.
//
//	BIOTIME_URL=http://biotime.example.com:8080 BIOTIME_USER=admin BIOTIME_PASS=secret \
//	  go run ./examples/basic -version 8 -since 24h -tz Europe/Moscow
//
// Pass -tz the zone the server keeps its wall-clock times in. Without it the
// host's own zone is assumed, which shifts both the window requested and
// every timestamp printed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/s0x90/zkteco-biota-client/biotime"
)

// options are the command's flags. They are grouped rather than passed
// positionally: two of them are bools, and adjacent bool parameters are how
// a call site ends up meaning the opposite of what it reads.
type options struct {
	version int
	jwt     bool
	since   time.Duration
	debug   bool
	tz      string
}

func main() {
	var o options
	flag.IntVar(&o.version, "version", 9, "server generation: 8 for legacy BioTime, 9 for ZKBio Time 9.0")
	flag.BoolVar(&o.jwt, "jwt", false, "authenticate with /jwt-api-token-auth/ (Authorization: JWT) instead of /api-token-auth/")
	flag.DurationVar(&o.since, "since", 24*time.Hour, "list punches recorded within this duration")
	flag.BoolVar(&o.debug, "debug", false, "log every request")
	flag.StringVar(&o.tz, "tz", "", "IANA zone the server keeps its wall-clock times in (default: this host's zone)")
	flag.Parse()

	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(o options) error {
	baseURL := os.Getenv("BIOTIME_URL")
	if baseURL == "" {
		return errors.New("BIOTIME_URL is not set")
	}

	// The server stores wall-clock times with no zone, so the client has to
	// be told which one. Getting it wrong shifts the window requested below
	// and every timestamp printed, without any error.
	if o.tz != "" {
		loc, err := time.LoadLocation(o.tz)
		if err != nil {
			return fmt.Errorf("bad -tz: %w", err)
		}
		biotime.SetLocation(loc)
	}
	fmt.Printf("reading the server's timestamps as %s\n", biotime.Location())

	opts := []biotime.Option{
		biotime.WithVersion(biotime.Version(o.version)),
		biotime.WithCredentials(os.Getenv("BIOTIME_USER"), os.Getenv("BIOTIME_PASS")),
		biotime.WithTimeout(15 * time.Second),
	}
	if o.jwt {
		opts = append(opts, biotime.WithAuthScheme(biotime.AuthJWT))
	}
	if o.debug {
		opts = append(opts, biotime.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	}

	client, err := biotime.New(baseURL, opts...)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Println("== Devices")
	for term, err := range client.Terminals.All(ctx, nil) {
		if err != nil {
			return err
		}
		fmt.Printf("  %-20s %-25s state=%s last seen %s\n", term.SN, term.Alias, term.State, term.LastActivity)
	}

	fmt.Println("== Departments")
	departments, err := biotime.Collect(client.Departments.All(ctx, nil))
	if err != nil {
		return err
	}
	for _, d := range departments {
		fmt.Printf("  %-6s %s\n", d.DeptCode, d.DeptName)
	}

	fmt.Println("== Employees (first page)")
	page, err := client.Employees.List(ctx, &biotime.EmployeeFilter{
		ListOptions: biotime.ListOptions{PageSize: 20, Ordering: "emp_code"},
	})
	if err != nil {
		return err
	}
	fmt.Printf("  total: %d\n", page.Count)
	for _, e := range page.Results {
		dept := ""
		if e.Department.Object != nil {
			dept = e.Department.Object.DeptName
		}
		fmt.Printf("  %-8s %-30s %s\n", e.EmpCode, e.FirstName+" "+e.LastName, dept)
	}

	fmt.Printf("== Punches since %s\n", o.since)
	filter := &biotime.TransactionFilter{
		StartTime:   time.Now().Add(-o.since),
		ListOptions: biotime.ListOptions{PageSize: 100, Ordering: "punch_time,id"},
	}
	var n int
	for tx, err := range client.Transactions.All(ctx, filter) {
		if err != nil {
			if apiErr, ok := errors.AsType[*biotime.Error](err); ok {
				return fmt.Errorf("server rejected the request: %w", apiErr)
			}
			return err
		}
		n++
		fmt.Printf("  %s %-8s %-12s via %s\n", tx.PunchTime, tx.EmpCode, tx.PunchState, tx.TerminalSN)
	}
	fmt.Printf("  %d punches\n", n)
	return nil
}
