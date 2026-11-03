package egress_test

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

// TestNoEgressBypass 防止业务代码绕过统一出站客户端：
// 除 internal/egress 外，非测试代码不得使用 http.DefaultClient / DefaultTransport、
// http.Get/Post/Head/PostForm、自建 http.Client{} / http.Transport{} 或 net.Dialer。
// 需要出站时请使用 egress.Client.HTTP()。
func TestNoEgressBypass(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := map[string]map[string]bool{
		"net/http": {"DefaultClient": true, "DefaultTransport": true, "Get": true, "Post": true, "Head": true, "PostForm": true},
		"net":      {"Dialer": true, "Dial": true, "DialTimeout": true},
	}
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case "internal/egress", "internal/ent", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		alias := map[string]string{}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if banned[p] == nil {
				continue
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if im.Name != nil {
				name = im.Name.Name
			}
			alias[name] = p
		}
		ctor := func(e ast.Expr) {
			// 构造 http.Client{} / http.Transport{} / new(http.Client)；作为类型使用（字段、参数）是允许的。
			if sel, ok := e.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && alias[id.Name] == "net/http" && (sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
					hits = append(hits, fset.Position(sel.Pos()).String()+": construct http."+sel.Sel.Name)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				ctor(x.Type)
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
					ctor(x.Args[0])
				}
			}
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if p, ok := alias[id.Name]; ok && banned[p][sel.Sel.Name] {
				hits = append(hits, fset.Position(sel.Pos()).String()+": "+id.Name+"."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("以下代码绕过了统一出站客户端（请改用 egress.Client.HTTP()）：\n%s", strings.Join(hits, "\n"))
	}
}
