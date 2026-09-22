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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	fset := token.NewFileSet()
	files := make([]*ast.File, len(blocks))
	for i, b := range blocks {
		// The block's first line is the one after its opening fence.
		src := fmt.Sprintf("//line README.md:%d\n%s", b.line+1, b.src)
		if !strings.HasPrefix(b.src, "package ") {
			src = fmt.Sprintf(fragmentPrelude, src)
		}
		f, err := parser.ParseFile(fset, "snippet.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files[i] = f
	}

	checker := newSnippetChecker(t, fset, goModVersion(t), files)
	for i, b := range blocks {
		t.Run(fmt.Sprintf("line_%d", b.line), func(t *testing.T) {
			for _, err := range checker.check(files[i]) {
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
// import, transitively, in a single go list invocation.
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

// importPaths returns the distinct import paths of the files, sorted.
func importPaths(files []*ast.File) []string {
	seen := make(map[string]bool)
	var paths []string
	for _, f := range files {
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
// the export data file the go command built for it.
func exportData(t *testing.T, pkgs []string) map[string]string {
	t.Helper()
	args := append([]string{"list", "-export", "-deps", "-f", "{{.ImportPath}}\t{{.Export}}"}, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	exports := make(map[string]string)
	for line := range strings.Lines(string(out)) {
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
