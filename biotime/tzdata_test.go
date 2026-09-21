package biotime

// The IANA zone database is embedded in the test binary, not in the
// library. Two checks here depend on it and would otherwise turn
// themselves off without saying so on a host that has none, such as a slim
// or distroless CI container: TestDaylightSavingEdges would skip, which
// reads as green, and `make test-tz` would resolve TZ to UTC and quietly
// become a second UTC run that proves nothing.
//
// This is a test file, so nothing the library ships gains a dependency.
import _ "time/tzdata"
