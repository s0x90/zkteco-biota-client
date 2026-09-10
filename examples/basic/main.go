// Command basic demonstrates the biotime client against a live server.
//
//	BIOTIME_URL=http://192.168.0.27:8080 BIOTIME_USER=admin BIOTIME_PASS=secret \
//	  go run ./examples/basic -version 8 -since 24h
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

func main() {
	version := flag.Int("version", 9, "server generation: 8 for legacy BioTime, 9 for ZKBio Time 9.0")
	jwt := flag.Bool("jwt", false, "authenticate with /jwt-api-token-auth/ (Authorization: JWT) instead of /api-token-auth/")
	since := flag.Duration("since", 24*time.Hour, "list punches recorded within this duration")
	debug := flag.Bool("debug", false, "log every request")
	flag.Parse()

	if err := run(*version, *jwt, *since, *debug); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(version int, jwt bool, since time.Duration, debug bool) error {
	baseURL := os.Getenv("BIOTIME_URL")
	if baseURL == "" {
		return errors.New("BIOTIME_URL is not set")
	}

	opts := []biotime.Option{
		biotime.WithVersion(biotime.Version(version)),
		biotime.WithCredentials(os.Getenv("BIOTIME_USER"), os.Getenv("BIOTIME_PASS")),
		biotime.WithTimeout(15 * time.Second),
	}
	if jwt {
		opts = append(opts, biotime.WithAuthScheme(biotime.AuthJWT))
	}
	if debug {
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

	fmt.Printf("== Punches since %s\n", since)
	filter := &biotime.TransactionFilter{
		StartTime:   time.Now().Add(-since),
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
