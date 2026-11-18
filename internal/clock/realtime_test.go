package clock_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func isTimeSource(t *ast.FuncType) bool {
	if t.Params != nil && len(t.Params.List) != 0 {
		return false
	}
	if t.Results == nil || len(t.Results.List) != 1 {
		return false
	}
	sel, ok := t.Results.List[0].Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time" && sel.Sel.Name == "Time"
}

func TestNoTimeSourceOutsideClock(t *testing.T) {
	clockDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if d.IsDir() && abs == clockDir {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			checked++
			ast.Inspect(file, func(n ast.Node) bool {
				if ft, ok := n.(*ast.FuncType); ok && isTimeSource(ft) {
					t.Errorf("%s: a func() time.Time lets a test choose the time; take a time.Time or a *clock.Clock", fset.Position(ft.Pos()))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 100 {
		t.Fatalf("checked only %d files; the walk is not reaching internal and cmd", checked)
	}
}
