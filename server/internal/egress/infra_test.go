package egress_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

// D-38 第 6 条：对象存储客户端只能连部署配置里声明的地址。
func TestInfraHTTPOnlyDeclared(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer ok.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer other.Close()
	addrs, err := egress.ObjectStoreAddrs(ok.URL, "", "b")
	if err != nil {
		t.Fatal(err)
	}
	hc := egress.InfraHTTP(addrs)
	resp, err := hc.Get(ok.URL)
	if err != nil {
		t.Fatalf("声明的对象存储地址应可连接：%v", err)
	}
	_ = resp.Body.Close()
	resp2, err := hc.Get(other.URL)
	if err == nil {
		_ = resp2.Body.Close()
	}
	var ue *egress.ErrInfraUndeclared
	if !errors.As(err, &ue) {
		t.Fatalf("未声明的地址应被拒绝，got %v", err)
	}
	if a, _ := egress.ObjectStoreAddrs("", "cn-north-1", "Bk"); len(a) != 2 || a[0] != "s3.cn-north-1.amazonaws.com:443" || a[1] != "bk.s3.cn-north-1.amazonaws.com:443" {
		t.Fatalf("AWS 默认端点：%v", a)
	}
	if a, _ := egress.ObjectStoreAddrs("http://SeaweedFS:8333", "", ""); len(a) != 1 || a[0] != "seaweedfs:8333" {
		t.Fatalf("自定义端点：%v", a)
	}
	u, _ := url.Parse(ok.URL)
	if _, err := egress.ObjectStoreAddrs("ftp://"+u.Host, "", ""); err == nil {
		t.Fatal("非 http(s) 端点应报错")
	}
}

// TestInfraClientsOnlyFromConfig 数据库 / Valkey / 对象存储客户端只能在约定位置构造（地址来自部署配置），
// 业务代码不得自建这些客户端连接别的地址来绕过出站白名单（D-38 第 6 条）。
func TestInfraClientsOnlyFromConfig(t *testing.T) {
	root := filepath.Join("..", "..")
	ctors := map[string]map[string]bool{
		"database/sql":                            {"Open": true},
		"github.com/redis/go-redis/v9":            {"NewClient": true, "NewUniversalClient": true, "NewClusterClient": true, "NewFailoverClient": true},
		"github.com/hibiken/asynq":                {"NewServer": true, "NewScheduler": true, "NewClient": true, "NewInspector": true},
		"github.com/aws/aws-sdk-go-v2/service/s3": {"New": true, "NewFromConfig": true},
	}
	allowedFiles := map[string]bool{
		"internal/store/store.go":               true, // DATABASE_URL
		"internal/objstore/objstore.go":         true, // S3_ENDPOINT，经 egress.InfraHTTP
		"cmd/migdiff/main.go":                   true, // 开发工具，连本地临时库
		"internal/store/storetest/storetest.go": true, // 测试辅助，内存 SQLite
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
			case "internal/ent", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowedFiles[rel] {
			return nil
		}
		// pkg/cli 是启动代码：只允许用 cfg.* 的配置项构造（下面逐个检查参数）。
		startup := strings.HasPrefix(rel, "pkg/cli/")
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		alias := map[string]string{}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if ctors[p] == nil {
				continue
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if p == "github.com/redis/go-redis/v9" {
				name = "redis"
			}
			if im.Name != nil {
				name = im.Name.Name
			}
			alias[name] = p
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
			id, ok := sel.X.(*ast.Ident)
			if !ok || !ctors[alias[id.Name]][sel.Sel.Name] {
				return true
			}
			if startup && usesConfig(call) {
				return true
			}
			hits = append(hits, fset.Position(call.Pos()).String()+": "+id.Name+"."+sel.Sel.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("以下代码在约定位置之外构造了基础设施客户端（地址必须来自部署配置）：\n%s", strings.Join(hits, "\n"))
	}
}

// usesConfig 调用参数里引用了 cfg.RedisAddr / cfg.DatabaseURL 等部署配置。
func usesConfig(call *ast.CallExpr) bool {
	found := false
	for _, a := range call.Args {
		ast.Inspect(a, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "cfg" && (s.Sel.Name == "RedisAddr" || s.Sel.Name == "DatabaseURL") {
					found = true
				}
			}
			return !found
		})
	}
	return found
}
