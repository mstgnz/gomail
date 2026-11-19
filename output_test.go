package gomail

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestLibraryCodeHasNoOutput walks every non-test source file of the package: the library
// reports through returned errors and never prints or logs, so a caller's logs carry no
// recipient address unless the caller puts it there.
func TestLibraryCodeHasNoOutput(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	walked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		walked++
		// The local name of fmt and os in this file, so an aliased import is still caught.
		local := map[string]string{}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			switch path {
			case "log", "log/slog":
				t.Errorf("%s imports %s", name, path)
			case "fmt", "os":
				local[path] = path
				if imp.Name != nil {
					local[path] = imp.Name.Name
				}
			}
		}
		isPkg := func(e ast.Expr, path string) bool {
			id, ok := e.(*ast.Ident)
			return ok && local[path] != "" && id.Name == local[path]
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "print" || id.Name == "println") {
					t.Errorf("%s calls %s", fset.Position(x.Pos()), id.Name)
				}
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && isPkg(sel.X, "fmt") && strings.HasPrefix(sel.Sel.Name, "Print") {
					t.Errorf("%s calls fmt.%s", fset.Position(x.Pos()), sel.Sel.Name)
				}
			case *ast.SelectorExpr:
				if isPkg(x.X, "os") && (x.Sel.Name == "Stdout" || x.Sel.Name == "Stderr") {
					t.Errorf("%s uses os.%s", fset.Position(x.Pos()), x.Sel.Name)
				}
			}
			return true
		})
	}
	// define.go, doc.go, func.go, mail.go, pool.go: a smaller count means the walk lost files.
	if walked < 5 {
		t.Fatalf("walked %d source files, want at least 5", walked)
	}
}
