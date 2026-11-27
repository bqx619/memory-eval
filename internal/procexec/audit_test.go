package procexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Audit all owned Go sources (including both OS build tags and test helpers).
// Keep os/exec command construction/Wait, but require the common launch boundary.
func TestExternalLaunchSitesUseHygiene(t *testing.T) {
	root := filepath.Join("..", "..")
	files := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if filepath.Dir(path) == filepath.Join(root, "internal", "procexec") {
				return nil
			}
			f, err := parser.ParseFile(files, path, nil, 0)
			if err != nil {
				return err
			}
			imports := map[string]string{}
			hasExec := false
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				name := filepath.Base(p)
				if imp.Name != nil {
					name = imp.Name.Name
				}
				imports[name] = p
				if p == "os/exec" {
					hasExec = true
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if receiver, ok := sel.X.(*ast.Ident); ok {
					pkg := imports[receiver.Name]
					if pkg == "os" && sel.Sel.Name == "StartProcess" || (pkg == "syscall" || pkg == "golang.org/x/sys/unix") && (sel.Sel.Name == "Exec" || sel.Sel.Name == "ForkExec" || sel.Sel.Name == "StartProcess") {
						t.Errorf("unhygienic process launch: %s", files.Position(call.Pos()))
					}
					// Package calls (procexec.Run, runner.Run, etc.) and testing's
					// t.Run/m.Run are not exec.Cmd methods.
					if pkg != "" || strings.HasSuffix(path, "_test.go") && (receiver.Name == "t" || receiver.Name == "m") {
						return true
					}
				}
				if hasExec {
					switch sel.Sel.Name {
					case "Start", "Run", "Output", "CombinedOutput":
						t.Errorf("launch must use procexec: %s", files.Position(call.Pos()))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
