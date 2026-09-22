package biotime_test

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readmePath and goModPath are relative to the package directory, which is
// the working directory of a test binary.
const (
	readmePath = "../README.md"
	goModPath  = "../go.mod"
)

// fragmentPrelude wraps a README snippet that is not a whole file. The
// snippets use a client, a context and an employee they never declare, and
// some of them `return err`, so the wrapper provides all three and returns
// an error. The snippet goes in its own block so its `:=` declarations shadow
// the prelude's instead of clashing with them.
const fragmentPrelude = `package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/s0x90/zkteco-biota-client/biotime"
)

func _(client *biotime.Client, ctx context.Context, emp *biotime.Employee) error {
	{
%s
	}
	return nil
}
`

// tolerated matches the type errors an illustrative fragment is allowed to
// produce: it may bind a result it never reads, and the prelude imports
// every package any snippet might need. Everything else is a real error.
var tolerated = regexp.MustCompile(`^(declared and not used: |".+" imported and not used$)`)

// TestREADMESnippets type-checks every fenced Go block in README.md against
// the package as it is, at the language version go.mod declares. The
// snippets are the first code a new user copies, and nothing else compiles
// them: a field rename or a syntax rewrite that misses the README is
// otherwise caught only by that user's build.
func TestREADMESnippets(t *testing.T) {
	blocks := readmeGoBlocks(t)
	if len(blocks) == 0 {
		t.Fatalf("no ```go blocks found in %s", readmePath)
	}
	checker := newSnippetChecker(goModVersion(t))

	for _, b := range blocks {
		t.Run(fmt.Sprintf("line_%d", b.line), func(t *testing.T) {
			src := b.src
			if !strings.HasPrefix(src, "package ") {
				src = fmt.Sprintf(fragmentPrelude, src)
			}
			for _, err := range checker.check(src) {
				t.Error(err)
			}
		})
	}
}

type readmeBlock struct {
	line int // line of the opening fence, for the failure message
	src  string
}

// readmeGoBlocks returns the contents of every ```go fenced block in the
// README with the line the fence opens on.
func readmeGoBlocks(t *testing.T) []readmeBlock {
	t.Helper()
	f, err := os.Open(filepath.FromSlash(readmePath))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var (
		blocks []readmeBlock
		cur    *readmeBlock
		body   strings.Builder
		line   int
	)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line++
		text := sc.Text()
		switch {
		case cur == nil && text == "```go":
			cur = &readmeBlock{line: line}
			body.Reset()
		case cur != nil && text == "```":
			cur.src = body.String()
			blocks = append(blocks, *cur)
			cur = nil
		case cur != nil:
			body.WriteString(text)
			body.WriteByte('\n')
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if cur != nil {
		t.Fatalf("%s:%d: ```go block is never closed", readmePath, cur.line)
	}
	return blocks
}

// goModVersion returns the go directive of the library's go.mod in the
// "go1.27" form go/types expects, so a snippet cannot use syntax the module
// does not allow.
func goModVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(goModPath))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return "go" + v
		}
	}
	t.Fatalf("%s: no go directive", goModPath)
	return ""
}

// snippetChecker type-checks snippets one at a time. It keeps one importer
// across snippets because the source importer type-checks the biotime
// package from the working tree, which costs more than a second, and caches
// the result; a fresh importer per snippet would redo that for every block.
// The snippets are checked against the code in this very commit, not
// against a published version.
type snippetChecker struct {
	fset     *token.FileSet
	importer types.Importer
	version  string
}

func newSnippetChecker(goVersion string) *snippetChecker {
	fset := token.NewFileSet()
	return &snippetChecker{
		fset:     fset,
		importer: importer.ForCompiler(fset, "source", nil),
		version:  goVersion,
	}
}

// check parses and type-checks src as a single-file package and returns the
// errors a user's build would report, minus the tolerated ones.
func (c *snippetChecker) check(src string) []error {
	file, err := parser.ParseFile(c.fset, "snippet.go", src, parser.SkipObjectResolution)
	if err != nil {
		return []error{err}
	}

	var errs []error
	conf := types.Config{
		GoVersion: c.version,
		Importer:  c.importer,
		Error: func(err error) {
			var typeErr types.Error
			if errors.As(err, &typeErr) && tolerated.MatchString(typeErr.Msg) {
				return
			}
			errs = append(errs, err)
		},
	}
	_, _ = conf.Check("snippet", c.fset, []*ast.File{file}, nil)
	return errs
}
