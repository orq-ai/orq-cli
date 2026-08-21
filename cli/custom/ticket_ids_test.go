package custom

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Linear issue keys: two or more capitals, a dash, digits.
var ticketID = regexp.MustCompile(`\b[A-Z]{2,}-\d+\b`)

// TestNoTicketIDsInStrings keeps our issue tracker out of the terminal. A key
// like RES-1407 is a link only we can follow: to a user it is noise attached to
// the one line that was supposed to explain what to do next, and it dates the
// binary the moment the issue closes.
//
// String literals only. Rationale in a comment is for maintainers and stays.
//
// The pattern is deliberately wider than our team keys: matching only the keys
// we have today would let a new team's first ticket through, and a guard that
// fails open on the case it has not seen is not one. The cost is that standards
// share the shape, so those are named below. A new standard trips this once and
// costs a line; a new team key is caught for free. That is the trade we want.
//
// The sweep covers non-user-facing literals too — a URL, a test fixture. The
// alternative is deciding per call site which strings reach a screen, and that
// judgement is exactly what fails quietly.
func TestNoTicketIDsInStrings(t *testing.T) {
	// Standards, not issues.
	allowed := map[string]bool{"UTF-8": true, "GPT-5": true}

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// Generated from the OpenAPI spec: the descriptions are the API's, and an
		// edit here is overwritten by the next regeneration.
		if strings.Contains(path, string(filepath.Separator)+"generated"+string(filepath.Separator)) {
			return nil
		}
		// ParseComments omitted on purpose: comments are allowed to name issues.
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				value = lit.Value
			}
			for _, hit := range ticketID.FindAllString(value, -1) {
				if allowed[hit] {
					continue
				}
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d: %q names issue %s. Say what the user should do instead, or drop the reference",
					rel, fset.Position(lit.Pos()).Line, value, hit)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
