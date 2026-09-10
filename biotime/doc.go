// Package biotime is a dependency-free Go client for the ZKTeco ZKBio Time
// (formerly BioTime) attendance server REST API.
//
// The client supports both the legacy 8.x servers (the API documented per
// app at http://<server>/api/personnel_docs/ and /api/iclock_docs/ after
// login) and the current 9.0 servers (documented in the "ZKBio Time 9.0 API
// User Manual"). The two generations differ in a few
// details that the client hides from callers:
//
//   - 8.x paginates with the "page_size" query parameter, 9.0 with "limit".
//   - The tested 8.x build and 9.0 wrap list responses as
//     {count,next,previous,code,msg,data}; [Page] also decodes the
//     {count,next,previous,results} form documented for other 8.x builds.
//   - 8.x issues JWT tokens from /jwt-api-token-auth/ ("Authorization: JWT ...")
//     and static tokens from /api-token-auth/ ("Authorization: Token ...");
//     9.0 documents only the latter. Both schemes are available, see [AuthScheme].
//   - Related objects are sometimes returned expanded ({"id":1,"dept_code":..})
//     and sometimes as bare identifiers (1). [Ref] decodes both.
//
// Select the server generation with [WithVersion]; everything else is shared.
//
// Basic usage:
//
//	client, err := biotime.New("http://192.168.0.27:8080",
//		biotime.WithVersion(biotime.Version8),
//		biotime.WithCredentials("admin", "secret"),
//	)
//	if err != nil {
//		return err
//	}
//
//	for tx, err := range client.Transactions.All(ctx, &biotime.TransactionFilter{
//		StartTime: time.Now().Add(-24 * time.Hour),
//	}) {
//		if err != nil {
//			return err
//		}
//		fmt.Println(tx.EmpCode, tx.PunchTime, tx.PunchState)
//	}
//
// Endpoints that this package does not model can be reached with [Client.Do].
package biotime
