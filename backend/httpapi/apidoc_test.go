package httpapi

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// -update-apidoc rewrites the generated table in docs/api.md from the router
// itself. The reference is 120-odd rows; nobody hand-edits it.
var updateAPIDoc = flag.Bool("update-apidoc", false, "rewrite the generated route table in docs/api.md")

const (
	apiDocPath      = "../../docs/api.md"
	apiDocBegin     = "<!-- BEGIN ROUTES (generated: go test ./httpapi -run TestAPIReference -update-apidoc) -->"
	apiDocEnd       = "<!-- END ROUTES -->"
	apiDocRoleTable = "| Method | Path | Role | Source |\n| --- | --- | --- | --- |\n"
)

// apiRoute is one registered route as the router declares it.
type apiRoute struct{ Method, Path, Role, File string }

func (r apiRoute) row() string {
	return fmt.Sprintf("| `%s` | `%s` | %s | `%s` |", r.Method, r.Path, r.Role, r.File)
}

// TestAPIReferenceMatchesRouter is the drift gate: the
// route table in docs/api.md is generated from the registrations themselves,
// so a new endpoint that skips the reference fails the build. Roles come from
// each middleware's `auth.Require(verifier, auth.RoleX)` initializer, never
// from the variable's name -- `staff` requires moderator and `adminOnly`
// requires plain admin, so names would lie.
func TestAPIReferenceMatchesRouter(t *testing.T) {
	routes := scanRoutes(t)
	if len(routes) < 100 {
		t.Fatalf("scanned only %d routes; the extractor has stopped seeing registrations", len(routes))
	}
	want := apiDocRoleTable
	for _, r := range routes {
		want += r.row() + "\n"
	}

	doc, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatal(err)
	}
	before, rest, ok := strings.Cut(string(doc), apiDocBegin)
	if !ok {
		t.Fatalf("%s has no %q marker", apiDocPath, apiDocBegin)
	}
	got, after, ok := strings.Cut(rest, apiDocEnd)
	if !ok {
		t.Fatalf("%s has no %q marker", apiDocPath, apiDocEnd)
	}

	if *updateAPIDoc {
		out := before + apiDocBegin + "\n\n" + want + "\n" + apiDocEnd + after
		if err := os.WriteFile(apiDocPath, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s with %d routes", apiDocPath, len(routes))
		return
	}

	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Errorf("docs/api.md is out of date with the router.\n"+
			"Regenerate: go test ./httpapi -run TestAPIReference -update-apidoc\n\n%s",
			apiDocDiff(strings.TrimSpace(got), strings.TrimSpace(want)))
	}
}

// apiDocDiff reports the rows each side is missing, which is what a stale
// reference actually looks like: an endpoint added, renamed, or re-roled.
func apiDocDiff(got, want string) string {
	inGot := map[string]bool{}
	for _, l := range strings.Split(got, "\n") {
		inGot[strings.TrimSpace(l)] = true
	}
	var b strings.Builder
	inWant := map[string]bool{}
	for _, l := range strings.Split(want, "\n") {
		l = strings.TrimSpace(l)
		inWant[l] = true
		if !inGot[l] {
			fmt.Fprintf(&b, "  missing from docs/api.md: %s\n", l)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if l = strings.TrimSpace(l); l != "" && !inWant[l] {
			fmt.Fprintf(&b, "  documented but not registered: %s\n", l)
		}
	}
	return b.String()
}

// scanRoutes reads every mux.Handle / mux.HandleFunc registration in this
// package, sorted by path then method.
func scanRoutes(t *testing.T) []apiRoute {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files[filepath.Base(name)] = f
		}
	}
	roles := resolveWrapperRoles(files)

	var routes []apiRoute
	for base, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			scope := roles[fn.Name.Name]
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				pattern, wrapper, ok := handleCall(n)
				if !ok {
					return true
				}
				role := "public"
				if r, found := scope[wrapper]; found {
					role = r
				}
				method, path := "ANY", pattern
				if m, p, cut := strings.Cut(pattern, " "); cut {
					method, path = m, p
				}
				routes = append(routes, apiRoute{method, path, role, base})
				return true
			})
			return true
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes
}

// handleCall matches `mux.Handle("PAT", wrapper(...))` and
// `mux.HandleFunc("PAT", ...)`, returning the pattern and the middleware
// identifier ("" when the handler is not wrapped).
func handleCall(n ast.Node) (pattern, wrapper string, ok bool) {
	call, isCall := n.(*ast.CallExpr)
	if !isCall || len(call.Args) < 2 {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
		return "", "", false
	}
	if id, isIdent := sel.X.(*ast.Ident); !isIdent || id.Name != "mux" {
		return "", "", false
	}
	lit, isLit := call.Args[0].(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", "", false
	}
	pattern, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", "", false
	}
	if inner, isCall := call.Args[1].(*ast.CallExpr); isCall {
		if id, isIdent := inner.Fun.(*ast.Ident); isIdent {
			wrapper = id.Name
		}
	}
	return pattern, wrapper, true
}

// resolveWrapperRoles maps each function's middleware variables to the role
// they enforce: first from `x := auth.Require(v, auth.RoleX)` initializers,
// then propagated to register* helpers that receive a middleware as a
// parameter (registerDrafts, registerItemsBulk). Iterating to a fixpoint
// carries a role through a chain of such calls.
func resolveWrapperRoles(files map[string]*ast.File) map[string]map[string]string {
	scopes := map[string]map[string]string{}
	params := map[string][]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			scopes[fn.Name.Name] = localRequireRoles(fn)
			var names []string
			for _, p := range fn.Type.Params.List {
				for _, n := range p.Names {
					names = append(names, n.Name)
				}
			}
			params[fn.Name.Name] = names
		}
	}
	for round := 0; round < 4; round++ {
		changed := false
		for _, f := range files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				caller := scopes[fn.Name.Name]
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					callee, ok := call.Fun.(*ast.Ident)
					if !ok {
						return true
					}
					names, known := params[callee.Name]
					if !known {
						return true
					}
					for i, arg := range call.Args {
						id, ok := arg.(*ast.Ident)
						if !ok || i >= len(names) {
							continue
						}
						role, ok := caller[id.Name]
						if !ok {
							continue
						}
						if scopes[callee.Name][names[i]] != role {
							scopes[callee.Name][names[i]] = role
							changed = true
						}
					}
					return true
				})
			}
		}
		if !changed {
			break
		}
	}
	return scopes
}

// localRequireRoles collects `name := auth.Require(verifier, auth.RoleX)`
// bindings in one function body.
func localRequireRoles(fn *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Require" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "auth" {
			return true
		}
		role, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(role.Sel.Name, "Role") {
			return true
		}
		out[lhs.Name] = strings.ToLower(strings.TrimPrefix(role.Sel.Name, "Role"))
		return true
	})
	return out
}
