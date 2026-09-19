package biotime

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzObjectMembers holds the hand-rolled scanner to what encoding/json does
// with the same bytes. The scanner replaced a second full decode of every
// record in the list path, so a disagreement here is silent corruption of
// Extra or of a related object's identifier rather than a crash, which is
// the kind of thing a table of handwritten inputs does not find.
//
// The seeds run on every `go test`. To explore from them:
//
//	go test ./biotime -run FuzzObjectMembers -fuzz FuzzObjectMembers
func FuzzObjectMembers(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"a":1}`,
		` { "a" : [1,2] , "b" : {"c":"}"} } `,
		`{"a\"b":"x,}"}`,
		`{"a":null,"b":true,"c":-1.5e10}`,
		`{"nested":{"deep":[{"x":"]"},[["}"]]]}}`,
		`{"esc":"\\","accented":"caf\u00e9"}`,
		`{"":""}`,
		`{"a":1,"a":2}`,
		// A key the server sent in a non-UTF-8 encoding.
		"{\"\xbb\xbc\":\"\"}",
		// Not objects: the scanner may reject these however it likes.
		`[1]`, `null`, `"x"`, `{`, `{"a"}`, `{"a":}`, `{"a":1`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		var want map[string]json.RawMessage
		if err := json.Unmarshal([]byte(in), &want); err != nil || want == nil {
			// extraFields is only ever handed a body that already decoded
			// into a struct, so anything else is out of contract.
			return
		}

		got := make(map[string]json.RawMessage, len(want))
		err := objectMembers([]byte(in), func(key, value []byte) error {
			got[coerceUTF8(string(key))] = json.RawMessage(value)
			return nil
		})
		if err != nil {
			t.Fatalf("scanner rejected %q, which encoding/json accepted: %v", in, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%q: scanner found %v, encoding/json found %v", in, sortedKeys(got), sortedKeys(want))
		}
		for key, wantVal := range want {
			gotVal, ok := got[key]
			if !ok {
				t.Fatalf("%q: scanner missed key %q", in, key)
			}
			if !sameJSON(t, gotVal, wantVal) {
				t.Fatalf("%q: key %q: scanner read %q, encoding/json read %q", in, key, gotVal, wantVal)
			}
		}
	})
}

// sameJSON reports whether two raw values mean the same thing. The bytes
// themselves may differ by insignificant whitespace.
func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("scanner produced %q, which does not decode: %v", a, err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return true // encoding/json handed back something it cannot re-read
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// coerceUTF8 replaces each invalid UTF-8 byte with U+FFFD, the way
// encoding/json does when it decodes a string. The scanner deliberately
// keeps the original bytes; see [objectMembers].
func coerceUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
