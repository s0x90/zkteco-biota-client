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
// # Time zones
//
// The server stores and returns wall-clock times with no zone. The client
// interprets them in the zone given with [WithLocation], which defaults to
// [time.Local] and is therefore wrong whenever the program does not run in
// the server's zone, the norm in containers. A zone that observes daylight
// saving cannot describe two hours a year; see [WithLocation] for what
// happens to them.
//
// Endpoints that this package does not model can be reached with [Client.Do].
//
// The example below is the shape of a program that uses this package. It is
// compiled on every test run rather than written out here as prose, so that
// it cannot drift away from the API.
package biotime
