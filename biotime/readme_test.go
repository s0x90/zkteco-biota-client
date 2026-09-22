package biotime_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"go/version"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
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
// the prelude's instead of clashing with them. The snippet itself starts
// with a //line directive, so errors point at the README.
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
%s	}
	return nil
}
`

// tolerated matches the type errors an illustrative fragment is allowed to
// produce: it may bind a result it never reads, show a bare literal, and the
// prelude imports every package any snippet might need. Everything else is
// a real error.
var tolerated = regexp.MustCompile(`^(declared and not used: |".+" imported and not used$|.* is not used$)`)

// goFence matches the opening fence of a Go block at any indentation, so a
// block inside a list item counts too.
var goFence = regexp.MustCompile("(?m)^[ \t]*```go[ \t]*$")

// TestREADMESnippets type-checks every fenced Go block in README.md against
// the package as it is, at the language version go.mod declares. The
// snippets are the first code a new user copies, and nothing else compiles
// them: a field rename or a syntax rewrite that misses the README is
// otherwise caught only by that user's build.
func TestREADMESnippets(t *testing.T) {
	blocks := readmeGoBlocks(t)

	fset := token.NewFileSet()
	files := make([]*ast.File, len(blocks))
	parseErrs := make([]error, len(blocks))
	for i, b := range blocks {
		// The block's first line is the one after its opening fence.
		src := fmt.Sprintf("//line README.md:%d\n%s", b.line+1, b.src)
		if !isWholeFile(b.src) {
			src = fmt.Sprintf(fragmentPrelude, src)
		}
		files[i], parseErrs[i] = parser.ParseFile(fset, "snippet.go", src, parser.SkipObjectResolution)
	}

	checker := newSnippetChecker(t, fset, goModVersion(t), files)
	for i, b := range blocks {
		t.Run(fmt.Sprintf("line_%d", b.line), func(t *testing.T) {
			if err := parseErrs[i]; err != nil {
				t.Error(err)
				return
			}
			for _, err := range checker.check(files[i]) {
				t.Error(err)
			}
		})
	}
}

// isWholeFile reports whether src is a complete Go file rather than a
// fragment, judged by the parser so leading comments do not matter.
func isWholeFile(src string) bool {
	_, err := parser.ParseFile(token.NewFileSet(), "", src, parser.PackageClauseOnly)
	return err == nil
}

type readmeBlock struct {
	line int // line of the opening fence, for the failure message
	src  string
}

// readmeGoBlocks returns the contents of every ```go fenced block in the
// README with the line the fence opens on. A block indented inside a list
// item has the indentation stripped. It fails when the number of blocks
// found differs from the number of opening fences in the file, so a layout
// the scanner does not understand cannot skip a block quietly.
func readmeGoBlocks(t *testing.T) []readmeBlock {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(readmePath))
	if err != nil {
		t.Fatal(err)
	}

	var (
		blocks []readmeBlock
		cur    *readmeBlock
		indent string
		body   strings.Builder
		line   int
	)
	for text := range strings.Lines(string(data)) {
		line++
		text = strings.TrimRight(text, "\r\n")
		trimmed := strings.TrimSpace(text)
		switch {
		case cur == nil && trimmed == "```go":
			cur = &readmeBlock{line: line}
			indent = text[:strings.Index(text, "`")]
			body.Reset()
		case cur != nil && trimmed == "```":
			cur.src = body.String()
			blocks = append(blocks, *cur)
			cur = nil
		case cur != nil:
			body.WriteString(strings.TrimPrefix(text, indent))
			body.WriteByte('\n')
		}
	}
	if cur != nil {
		t.Fatalf("%s:%d: ```go block is never closed", readmePath, cur.line)
	}
	if want := len(goFence.FindAllIndex(data, -1)); len(blocks) != want {
		t.Fatalf("%s: found %d ```go blocks but the file has %d opening fences", readmePath, len(blocks), want)
	}
	if len(blocks) == 0 {
		t.Fatalf("%s: no ```go blocks", readmePath)
	}
	return blocks
}

// goModVersion returns the go directive of the library's go.mod in the
// "go1.27" form go/types expects, so a snippet cannot use syntax the module
// does not allow. It is validated here because go/types treats a version it
// cannot parse as no constraint at all, silently.
func goModVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(goModPath))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "go ")
		if !ok {
			continue
		}
		v, _, _ = strings.Cut(v, "//")
		v = "go" + strings.TrimSpace(v)
		if !version.IsValid(v) {
			t.Fatalf("%s: go directive %q is not a version go/types accepts", goModPath, v)
		}
		return v
	}
	t.Fatalf("%s: no go directive", goModPath)
	return ""
}

// snippetChecker type-checks one snippet at a time against export data the
// go command produced for the working tree, so the snippets are checked
// against the code in this very commit, not against a published version.
// Export data is what a real build uses, and reading it is fast; the
// "source" importer would type-check the standard library from source
// instead, which under the race detector costs over ten seconds.
type snippetChecker struct {
	fset     *token.FileSet
	importer types.Importer
	version  string
}

// newSnippetChecker resolves export data for every package the files
// import, transitively, in a single go list invocation. A nil file (one
// that failed to parse) contributes no imports.
func newSnippetChecker(t *testing.T, fset *token.FileSet, goVersion string, files []*ast.File) *snippetChecker {
	t.Helper()
	exports := exportData(t, importPaths(files))
	lookup := func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(file)
	}
	return &snippetChecker{
		fset:     fset,
		importer: importer.ForCompiler(fset, "gc", lookup),
		version:  goVersion,
	}
}

// check type-checks one parsed snippet as a single-file package and returns
// the errors a user's build would report, minus the tolerated ones.
func (c *snippetChecker) check(file *ast.File) []error {
	var errs []error
	conf := types.Config{
		GoVersion: c.version,
		Importer:  c.importer,
		Error: func(err error) {
			if typeErr, ok := errors.AsType[types.Error](err); ok && tolerated.MatchString(typeErr.Msg) {
				return
			}
			errs = append(errs, err)
		},
	}
	_, _ = conf.Check("snippet", c.fset, []*ast.File{file}, nil)
	return errs
}

// importPaths returns the distinct import paths of the files, sorted.
func importPaths(files []*ast.File) []string {
	seen := make(map[string]bool)
	var paths []string
	for _, f := range files {
		if f == nil {
			continue
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

// exportData maps each of the packages and their transitive dependencies to
// the export data file the go command built for it. Export data is specific
// to the toolchain that wrote it, so the go command on PATH must be the one
// that built this test binary.
func exportData(t *testing.T, pkgs []string) map[string]string {
	t.Helper()
	if got := strings.TrimSpace(goCommand(t, "env", "GOVERSION")); got != runtime.Version() {
		t.Fatalf("go on PATH is %s but this test was built with %s; its export data would not match", got, runtime.Version())
	}

	args := append([]string{"list", "-export", "-deps", "-f", "{{.ImportPath}}\t{{.Export}}"}, pkgs...)
	exports := make(map[string]string)
	for line := range strings.Lines(goCommand(t, args...)) {
		path, file, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if path == "unsafe" {
			continue // built into the importer; it has no export data
		}
		if !ok || file == "" {
			t.Fatalf("go list -export: no export data in %q", line)
		}
		exports[path] = file
	}
	return exports
}

// goCommand runs the go command on PATH and returns its standard output,
// failing the test with the command's standard error when it exits non-zero.
func goCommand(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, exitErr.Stderr)
		}
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
