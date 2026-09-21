package biotime_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/s0x90/zkteco-biota-client/biotime"
)

// Example walks the punches of the last day on a legacy 8.x server. It is
// compiled on every test run and never executed: it needs a live server.
func Example() {
	// The zone the server keeps its wall-clock times in. Without this the
	// client reads them as local time, which is wrong in a container.
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		log.Fatal(err)
	}
	biotime.SetLocation(loc)

	client, err := biotime.New("http://biotime.example.com:8080",
		biotime.WithVersion(biotime.Version8),
		biotime.WithAuthScheme(biotime.AuthJWT),
		biotime.WithCredentials("admin", "secret"),
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	filter := &biotime.TransactionFilter{
		StartTime:   time.Now().Add(-24 * time.Hour),
		ListOptions: biotime.ListOptions{PageSize: 200, Ordering: "punch_time,id"},
	}
	for tx, err := range client.Transactions.All(ctx, filter) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(tx.PunchTime, tx.EmpCode, tx.PunchState, tx.TerminalSN)
	}
}
