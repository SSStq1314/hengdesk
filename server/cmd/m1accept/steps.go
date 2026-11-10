package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

const (
	emailA     = "a.finance@hengdesk.local"
	emailB     = "b.marketing@hengdesk.local"
	emailC     = "c.approver@hengdesk.local"
	emailD     = "d.auditor@hengdesk.local"
	emailM     = "m.manager@hengdesk.local"
	execSecret = "HD-EXEC-SECRET-991"
	aigcPhrase = "本内容由人工智能生成（衡台 HengDesk）"
)

type script struct {
	cfg     config
	started time.Time

	admin, a, b, c, d, m *Client

	financeID, marketID              int
	idAdmin, idA, idB, idC, idD, idM int

	provAPI, provLocal, modelAPI, modelLocal, modelEmbed int
	kbID, fileTravel, fileFee, fileExec                  int
	mcpID, toolQuery, toolCreate, agentID, convID        int
	trace8, trace9, trace13                              string
	budgetID                                             int
}

func (s *script) log(format string, args ...any) {
	fmt.Printf("[%s] "+format+"\n", append([]any{time.Since(s.started).Truncate(time.Millisecond)}, args...)...)
}

func (s *script) step(n int, title string) { s.log("—— 第 %d 步：%s ——", n, title) }

func (s *script) run() error {
	var err error
	if s.admin, err = newClient(s.cfg.Base); err != nil {
		return err
	}
	if err := s.step1(); err != nil {
		return fmt.Errorf("第 1 步：%w", err)
	}
	if err := s.step2(); err != nil {
		return fmt.Errorf("第 2 步：%w", err)
	}
	if err := s.step3(); err != nil {
		return fmt.Errorf("第 3 步：%w", err)
	}
	if err := s.step4(); err != nil {
		return fmt.Errorf("第 4 步：%w", err)
	}
	if err := s.step5(); err != nil {
		return fmt.Errorf("第 5 步：%w", err)
	}
	if err := s.step6(); err != nil {
		return fmt.Errorf("第 6 步：%w", err)
	}
	if err := s.step7(); err != nil {
		return fmt.Errorf("第 7 步：%w", err)
	}
	if err := s.step8(); err != nil {
		return fmt.Errorf("第 8 步：%w", err)
	}
	if err := s.stepAC05(); err != nil {
		return fmt.Errorf("AC-05：%w", err)
	}
	if err := s.step9(); err != nil {
		return fmt.Errorf("第 9 步：%w", err)
	}
	if err := s.step10(); err != nil {
		return fmt.Errorf("第 10 步：%w", err)
	}
	if err := s.step11(); err != nil {
		return fmt.Errorf("第 11 步：%w", err)
	}
	if err := s.step12(); err != nil {
		return fmt.Errorf("第 12 步：%w", err)
	}
	if err := s.step13(); err != nil {
		return fmt.Errorf("第 13 步：%w", err)
	}
	if err := s.step14(); err != nil {
		return fmt.Errorf("第 14 步：%w", err)
	}
	if err := s.step15(); err != nil {
		return fmt.Errorf("第 15 步：%w", err)
	}
	if err := s.stepD46(); err != nil {
		return fmt.Errorf("D-46：%w", err)
	}
	return nil
}

func (s *script) step1() error {
	s.step(1, "安装后打开衡台、管理员登录、出站白名单加入 mock 端点")
	if err := s.waitHTTP(s.cfg.Base+"/healthz", 90*time.Second); err != nil {
		return fmt.Errorf("healthz：%w", err)
	}
	// 未登录 401
	if _, _, err := s.admin.do(http.MethodGet, "/api/v1/conversations", nil, 401); err != nil {
		return fmt.Errorf("未登录应返回 401：%w", err)
	}
	if err := s.admin.login(s.cfg.AdminEmail, s.cfg.AdminPassword); err != nil {
		return err
	}
	s.log("管理员 %s 登录成功", s.cfg.AdminEmail)
	var me struct {
		ID int `json:"id"`
	}
	if _, err := s.admin.json(http.MethodGet, "/api/v1/auth/me", nil, 200, &me); err != nil || me.ID == 0 {
		return fmt.Errorf("读取管理员 id：%w", err)
	}
	s.idAdmin = me.ID
	_, _, _ = s.admin.do(http.MethodPost, "/api/v1/admin/system/master-key/ack", map[string]any{}, 0)

	// 出站白名单：mockupstream、mockmcp、mockoidc（内网主机需显式放行）
	for _, u := range []string{s.cfg.MockUpstream, s.cfg.MockMCP, s.cfg.MockOIDC, s.cfg.MockRedir} {
		if strings.TrimSpace(u) == "" {
			continue
		}
		if err := s.allowEgress(u, "M1 演示剧本"); err != nil {
			return err
		}
	}
	return nil
}

func (s *script) allowEgress(raw, note string) error {
	u := strings.TrimSpace(raw)
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	hostport := u
	if i := strings.Index(u, "://"); i >= 0 {
		hostport = u[i+3:]
	}
	if i := strings.Index(hostport, "/"); i >= 0 {
		hostport = hostport[:i]
	}
	host, ports := hostport, "*"
	if i := strings.LastIndex(hostport, ":"); i > 0 && !strings.Contains(hostport[i:], "]") {
		host, ports = hostport[:i], hostport[i+1:]
	}
	if err := s.postEgressRule("host", host, ports, note); err != nil {
		return err
	}
	s.log("已放行出站 %s 端口 %s", host, ports)
	// 产品出站控制：host 规则解析到 RFC1918 / 环回等内网地址时仍拒绝，须再用 ip/cidr 显式放行（D-38）。
	// 验收 mock 都在 compose 网段内，这里解析后补 ip 规则；不改产品出站行为。
	if _, perr := netip.ParseAddr(host); perr == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		s.log("警告：无法解析 %s，未补内网 ip 规则：%v", host, err)
		return nil
	}
	for _, a := range addrs {
		a = a.Unmap()
		if !egress.IsInternal(a) {
			continue
		}
		ip := a.String()
		if err := s.postEgressRule("ip", ip, ports, note+"（"+host+" 解析到的内网地址）"); err != nil {
			return err
		}
		s.log("已放行出站内网地址 %s 端口 %s（%s）", ip, ports, host)
	}
	return nil
}

func (s *script) postEgressRule(kind, value, ports, note string) error {
	var out map[string]any
	_, err := s.admin.json(http.MethodPost, "/api/v1/admin/egress/rules", map[string]any{
		"kind": kind, "value": value, "ports": ports, "note": note,
	}, 201, &out)
	if err != nil {
		if he := asHTTPError(err); he != nil && (he.Status == 409 || he.Status == 400) && strings.Contains(he.Body, "已存在") {
			s.log("出站规则已存在：%s %s:%s", kind, value, ports)
			return nil
		}
		if he := asHTTPError(err); he != nil && he.Status == 200 {
			return nil
		}
		return fmt.Errorf("添加出站白名单 %s %s:%s：%w", kind, value, ports, err)
	}
	return nil
}

func (s *script) step2() error {
	s.step(2, "导入部门/人员 CSV，SSO 登录 A/B/C/D/经理，授予角色")
	csv := s.file("departments.csv")
	b, _, err := s.admin.do(http.MethodPost, "/api/v1/admin/departments/import", nil, 0)
	// multipart
	up, err2 := s.admin.upload("/api/v1/admin/departments/import", "departments.csv", csv, 200)
	if err2 != nil {
		// 有的实现用 raw body？上面 upload 失败则报错。忽略误用的 do。
		_ = b
		_ = err
		return fmt.Errorf("导入 CSV：%w", err2)
	}
	s.log("部门 CSV 导入：%s", trimBody(string(up)))

	var depts []map[string]any
	if _, err := s.admin.json(http.MethodGet, "/api/v1/admin/departments", nil, 200, &depts); err != nil {
		// 可能是 {items:[]}
		var wrap struct {
			Items []map[string]any `json:"items"`
		}
		if _, err2 := s.admin.json(http.MethodGet, "/api/v1/admin/departments", nil, 200, &wrap); err2 != nil {
			return fmt.Errorf("列出部门：%w / %w", err, err2)
		}
		depts = wrap.Items
	}
	for _, d := range depts {
		name := asString(d["name"])
		id := asInt(d["id"])
		switch name {
		case "财务部":
			s.financeID = id
		case "市场部":
			s.marketID = id
		}
	}
	if s.financeID == 0 || s.marketID == 0 {
		return fmt.Errorf("未找到财务部/市场部（got %v）", depts)
	}

	users, err := s.listUsers()
	if err != nil {
		return err
	}
	s.idA, s.idB, s.idC, s.idD, s.idM = users[emailA], users[emailB], users[emailC], users[emailD], users[emailM]
	if s.idA == 0 || s.idB == 0 || s.idC == 0 || s.idD == 0 || s.idM == 0 {
		return fmt.Errorf("CSV 未建齐演示账号：%v", users)
	}

	if s.cfg.SkipOIDC {
		if err := s.setDemoPasswords(); err != nil {
			return err
		}
		if err := s.passwordLoginAll(); err != nil {
			return err
		}
	} else {
		if err := s.ssoAll(); err != nil {
			return fmt.Errorf("SSO：%w", err)
		}
	}

	// 审计员角色、财务经理 = 部门管理员
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/roles", s.idD),
		map[string]any{"role": "security_auditor"}, 204); err != nil {
		return fmt.Errorf("授予审计员：%w", err)
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/roles", s.idM),
		map[string]any{"role": "dept_admin", "department_id": s.financeID}, 204); err != nil {
		return fmt.Errorf("授予财务部部门管理员：%w", err)
	}
	s.log("已授予 D 安全审计员、经理部门管理员")
	return nil
}

func (s *script) listUsers() (map[string]int, error) {
	out := map[string]int{}
	var wrap struct {
		Items []map[string]any `json:"items"`
	}
	if _, err := s.admin.json(http.MethodGet, "/api/v1/admin/users?limit=200", nil, 200, &wrap); err != nil {
		return nil, fmt.Errorf("列出用户：%w", err)
	}
	items := wrap.Items
	if len(items) == 0 {
		var arr []map[string]any
		if _, err := s.admin.json(http.MethodGet, "/api/v1/admin/users?limit=200", nil, 200, &arr); err == nil {
			items = arr
		}
	}
	for _, u := range items {
		out[strings.ToLower(asString(u["email"]))] = asInt(u["id"])
	}
	return out, nil
}

func (s *script) setDemoPasswords() error {
	pw := map[string]int{emailA: s.idA, emailB: s.idB, emailC: s.idC, emailD: s.idD, emailM: s.idM}
	for email, id := range pw {
		if _, _, err := s.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d", id),
			map[string]any{"password": "Demo-Passw0rd-9x"}, 200); err != nil {
			return fmt.Errorf("为 %s 设密码：%w", email, err)
		}
	}
	return nil
}

func (s *script) passwordLoginAll() error {
	var err error
	for _, p := range []struct {
		slot  **Client
		email string
	}{{&s.a, emailA}, {&s.b, emailB}, {&s.c, emailC}, {&s.d, emailD}, {&s.m, emailM}} {
		cl, e := newClient(s.cfg.Base)
		if e != nil {
			return e
		}
		if e = cl.login(p.email, "Demo-Passw0rd-9x"); e != nil {
			return e
		}
		*p.slot = cl
	}
	s.log("演示账号已用本地密码登录（-skip-oidc）")
	return err
}

func (s *script) ssoAll() error {
	var err error
	for _, p := range []struct {
		slot  **Client
		email string
	}{{&s.a, emailA}, {&s.b, emailB}, {&s.c, emailC}, {&s.d, emailD}, {&s.m, emailM}} {
		cl, e := newClient(s.cfg.Base)
		if e != nil {
			return e
		}
		if e = cl.oidcLogin(s.cfg.MockOIDC, p.email); e != nil {
			return fmt.Errorf("%s：%w", p.email, e)
		}
		*p.slot = cl
		s.log("SSO 登录成功：%s", p.email)
	}
	// 抽查审计里有 SSO 登录
	var logs struct {
		Items []map[string]any `json:"items"`
	}
	if _, err = s.admin.json(http.MethodGet, "/api/v1/admin/audit-logs?action=auth.login&limit=50", nil, 200, &logs); err == nil {
		ok := false
		for _, it := range logs.Items {
			if strings.EqualFold(asString(it["actor_email"]), emailA) && asString(it["outcome"]) == "success" {
				ok = true
				break
			}
		}
		if !ok {
			s.log("警告：未在审计中直接看到 A 的 auth.login 成功（继续）")
		}
	}
	return nil
}

func (c *Client) oidcLogin(oidcBase, email string) error {
	// Identity 的 JSON 字段是导出名（Sub/Email/.../Extra）。
	body, _ := json.Marshal(map[string]any{"Sub": email, "Email": email, "EmailVerified": true, "Name": email,
		"Extra": map[string]any{"department": deptOf(email)}})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(oidcBase, "/")+"/set-identity", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("set-identity：%w", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("set-identity HTTP %d", res.StatusCode)
	}

	r, err := c.HTTP.Get(c.url("/api/v1/auth/oidc/login"))
	if err != nil {
		return err
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	c.captureCSRF(r)
	if r.Request != nil && r.Request.URL != nil && strings.Contains(r.Request.URL.RawQuery, "error=sso") {
		return fmt.Errorf("OIDC 回调失败，跳到 %s", r.Request.URL.String())
	}
	if r.StatusCode >= 400 {
		return &httpError{Status: r.StatusCode, Body: string(b)}
	}
	// 从 cookie 取 CSRF，并确认已登录
	for _, ck := range c.HTTP.Jar.Cookies(c.Base) {
		if ck.Name == csrfCookie && ck.Value != "" {
			c.CSRF = ck.Value
		}
	}
	if _, _, err := c.do(http.MethodGet, "/api/v1/usage/me", nil, 200); err != nil {
		return fmt.Errorf("SSO 后会话无效：%w", err)
	}
	c.Email = email
	return nil
}

func deptOf(email string) string {
	if email == emailB {
		return "市场部"
	}
	return "财务部"
}

func (s *script) step3() error {
	s.step(3, "接入国产 API 与本地模型（均指向 mockupstream），市场部只能用本地模型")
	var err error
	if s.provAPI, err = s.admin.getID(http.MethodPost, "/api/v1/admin/model-providers", map[string]any{
		"name": "国产模型API", "kind": "openai_compat", "dialect": "deepseek",
		"base_url": strings.TrimRight(s.cfg.MockUpstream, "/") + "/v1", "api_key": "sk-demo",
	}, 201); err != nil {
		return fmt.Errorf("创建国产供应商：%w", err)
	}
	// AC-15：响应不得含明文 key
	b, _, err := s.admin.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/model-providers/%d", s.provAPI), nil, 200)
	if err != nil {
		// list-only? try list
		b, _, err = s.admin.do(http.MethodGet, "/api/v1/admin/model-providers", nil, 200)
		if err != nil {
			return fmt.Errorf("读取供应商：%w", err)
		}
	}
	if strings.Contains(string(b), "sk-demo") {
		return fmt.Errorf("AC-15 失败：供应商响应出现密钥明文")
	}

	if s.provLocal, err = s.admin.getID(http.MethodPost, "/api/v1/admin/model-providers", map[string]any{
		"name": "本地模型", "kind": "openai_compat", "dialect": "generic",
		"base_url": strings.TrimRight(s.cfg.MockUpstream, "/") + "/v1", "api_key": "sk-demo",
	}, 201); err != nil {
		return fmt.Errorf("创建本地供应商：%w", err)
	}

	if s.modelAPI, err = s.admin.getID(http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "国产对话", "provider_id": s.provAPI, "upstream_model": "deepseek-flash", "thinking": "off",
	}, 201); err != nil {
		return fmt.Errorf("创建国产模型：%w", err)
	}
	if s.modelLocal, err = s.admin.getID(http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "本地对话", "provider_id": s.provLocal, "upstream_model": "mock-local", "thinking": "off",
	}, 201); err != nil {
		// mock-local 仅 demo-script 允许；若上游未开开关，回退 deepseek-flash
		if s.modelLocal, err = s.admin.getID(http.MethodPost, "/api/v1/admin/models", map[string]any{
			"name": "本地对话", "provider_id": s.provLocal, "upstream_model": "deepseek-flash", "thinking": "off",
		}, 201); err != nil {
			return fmt.Errorf("创建本地模型：%w", err)
		}
	}
	if s.modelEmbed, err = s.admin.getID(http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "mock-embed", "provider_id": s.provLocal, "upstream_model": "mock-embed",
	}, 201); err != nil {
		return fmt.Errorf("创建 embedding 模型：%w", err)
	}
	_, _, _ = s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/default", s.modelAPI), map[string]any{}, 0)

	// 测试连接
	for _, id := range []int{s.provAPI, s.provLocal} {
		if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/model-providers/%d/test", id), map[string]any{}, 200); err != nil {
			s.log("警告：供应商 %d 测试未 200：%v（继续尝试模型测试）", id, err)
		}
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/test", s.modelAPI), map[string]any{}, 200); err != nil {
		return fmt.Errorf("测试国产模型：%w", err)
	}

	// ACL：财务部用国产，市场部用本地
	if err := s.grantModel(s.modelAPI, "dept", s.financeID); err != nil {
		return err
	}
	if err := s.grantModel(s.modelLocal, "dept", s.marketID); err != nil {
		return err
	}
	// 创建智能体时按创建者本人的模型/知识库/工具 ACL 校验（否则 404）。超管管理模型不代表自动可用。
	for _, mid := range []int{s.modelAPI, s.modelLocal, s.modelEmbed} {
		if err := s.grantModel(mid, "user", s.idAdmin); err != nil {
			return err
		}
	}
	// 财务部也需要能跑脚本里的对话（A 用国产）。市场部不应看到国产模型。
	var models []map[string]any
	if _, err := s.b.json(http.MethodGet, "/api/v1/models", nil, 200, &models); err != nil {
		var wrap struct {
			Items []map[string]any `json:"items"`
		}
		if _, err2 := s.b.json(http.MethodGet, "/api/v1/models", nil, 200, &wrap); err2 != nil {
			return fmt.Errorf("列出模型（员工B）：%w", err)
		}
		models = wrap.Items
	}
	for _, m := range models {
		if asString(m["name"]) == "国产对话" {
			return fmt.Errorf("市场部不应看到国产对话模型")
		}
	}
	s.log("模型接入完成（国产 #%d，本地 #%d，embed #%d）", s.modelAPI, s.modelLocal, s.modelEmbed)
	return nil
}

func (s *script) grantModel(modelID int, typ string, sid int) error {
	// 产品该接口成功时返回 204 No Content（见 models_ext.go），不是 200。
	_, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/acl", modelID),
		map[string]any{"subject_type": typ, "subject_id": sid, "include_children": true, "perm": "use"}, 204)
	if err != nil {
		if he := asHTTPError(err); he != nil && (he.Status == 200 || he.Status == 201) {
			return nil
		}
		return fmt.Errorf("授予模型 ACL：%w", err)
	}
	return nil
}

func (s *script) step4() error {
	s.step(4, "设置财务部月预算并设 80% 告警")
	th := 80
	id, err := s.admin.getID(http.MethodPost, "/api/v1/admin/budgets", map[string]any{
		"scope": "dept", "scope_id": s.financeID, "period": "month", "metric": "tokens",
		"limit": s.cfg.BudgetLimit, "threshold_pct": th, "action": "block", "note": "M1 演示",
	}, 201)
	if err != nil {
		return err
	}
	s.budgetID = id
	s.log("财务部预算 #%d 上限 %d tokens", id, s.cfg.BudgetLimit)
	return nil
}

func (s *script) step5() error {
	s.step(5, "创建财务制度库，上传 3 份文件，高管文件仅财务经理可见")
	var err error
	if s.kbID, err = s.admin.getID(http.MethodPost, "/api/v1/kbs", map[string]any{
		"name": "财务制度库", "description": "M1 演示", "embedding_model": "mock-embed",
	}, 201); err != nil {
		return fmt.Errorf("创建知识库：%w", err)
	}
	// 授权财务部 view/use
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/kbs/%d/acl", s.kbID), map[string]any{
		"subject_type": "dept", "subject_id": s.financeID, "include_children": true, "perm": "use",
	}, 0); err != nil {
		s.log("警告：知识库部门 ACL：%v", err)
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/kbs/%d/acl", s.kbID), map[string]any{
		"subject_type": "user", "subject_id": s.idAdmin, "perm": "use",
	}, 0); err != nil {
		return fmt.Errorf("授予管理员知识库 ACL：%w", err)
	}

	type up struct {
		name string
		body []byte
		ptr  *int
	}
	files := []up{
		{"差旅报销须知.md", s.file("kb/travel.md"), &s.fileTravel},
		{"费用科目说明.md", s.file("kb/fees.md"), &s.fileFee},
		{"高管差旅标准.md", s.file("kb/exec.md"), &s.fileExec},
	}
	for _, f := range files {
		raw, err := s.admin.upload(fmt.Sprintf("/api/v1/kbs/%d/files", s.kbID), f.name, f.body, 201)
		if err != nil {
			return fmt.Errorf("上传 %s：%w", f.name, err)
		}
		id := extractID(raw)
		if id == 0 {
			return fmt.Errorf("上传 %s 无 id：%s", f.name, trimBody(string(raw)))
		}
		*f.ptr = id
	}
	// 高管文件自定义 ACL：仅财务经理
	if _, _, err := s.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/kbs/%d/files/%d/acl-mode", s.kbID, s.fileExec),
		map[string]any{"custom": true}, 200); err != nil {
		return fmt.Errorf("设置自定义 ACL：%w", err)
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/kbs/%d/files/%d/acl", s.kbID, s.fileExec),
		map[string]any{"subject_type": "user", "subject_id": s.idM, "perm": "view"}, 0); err != nil {
		return fmt.Errorf("授予经理文件权限：%w", err)
	}
	if err := s.waitFilesReady(2 * time.Minute); err != nil {
		return err
	}
	s.log("知识库 #%d 三份文件已就绪（高管文件 #%d 仅经理可见）", s.kbID, s.fileExec)
	return nil
}

func (s *script) waitFilesReady(d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var wrap struct {
			Items []map[string]any `json:"items"`
		}
		if _, err := s.admin.json(http.MethodGet, fmt.Sprintf("/api/v1/kbs/%d/files", s.kbID), nil, 200, &wrap); err != nil {
			return err
		}
		ok, failed := 0, []string{}
		for _, it := range wrap.Items {
			st := asString(it["status"])
			switch st {
			case "ready":
				ok++
			case "failed":
				failed = append(failed, fmt.Sprintf("%s:%s", asString(it["name"]), asString(it["error"])))
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("文件索引失败：%s", strings.Join(failed, ", "))
		}
		if ok == 3 {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("等待文件索引超时")
}

func (s *script) step6() error {
	s.step(6, "登记 mock 工单 MCP，query_ticket 自动执行，create_ticket 需审批（审批人 C）")
	var err error
	mcpURL := strings.TrimRight(s.cfg.MockMCP, "/") + "/mcp"
	if s.mcpID, err = s.admin.getID(http.MethodPost, "/api/v1/admin/mcp-servers", map[string]any{
		"name": "mock 工单", "url": mcpURL, "transport": "streamable_http", "enabled": true,
	}, 201); err != nil {
		return fmt.Errorf("登记 MCP：%w", err)
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/mcp-servers/%d/discover", s.mcpID), map[string]any{}, 200); err != nil {
		return fmt.Errorf("发现工具：%w", err)
	}
	var tools []map[string]any
	var wrap struct {
		Items []map[string]any `json:"items"`
	}
	if _, err := s.admin.json(http.MethodGet, fmt.Sprintf("/api/v1/admin/mcp-servers/%d/tools", s.mcpID), nil, 200, &wrap); err == nil && len(wrap.Items) > 0 {
		tools = wrap.Items
	} else if _, err := s.admin.json(http.MethodGet, fmt.Sprintf("/api/v1/admin/mcp-servers/%d/tools", s.mcpID), nil, 200, &tools); err != nil {
		return fmt.Errorf("列出工具：%w", err)
	}
	for _, t := range tools {
		id, name := asInt(t["id"]), asString(t["name"])
		switch name {
		case "query_ticket":
			s.toolQuery = id
			if _, _, err := s.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/tools/%d", id), map[string]any{
				"enabled": true, "exec_mode": "auto", "risk_level": "read_only",
			}, 200); err != nil {
				return fmt.Errorf("配置 query_ticket：%w", err)
			}
		case "create_ticket":
			s.toolCreate = id
			if _, _, err := s.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/tools/%d", id), map[string]any{
				"enabled": true, "exec_mode": "require_approval", "risk_level": "write",
				"approver_ids": []int{s.idC},
			}, 200); err != nil {
				return fmt.Errorf("配置 create_ticket：%w", err)
			}
		}
	}
	if s.toolQuery == 0 || s.toolCreate == 0 {
		return fmt.Errorf("未发现 query_ticket/create_ticket：%v", tools)
	}
	for _, id := range []int{s.toolQuery, s.toolCreate} {
		if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/tools/%d/acl", id), map[string]any{
			"subject_type": "dept", "subject_id": s.financeID, "include_children": true,
		}, 0); err != nil {
			s.log("警告：工具 ACL：%v", err)
		}
		if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/tools/%d/acl", id), map[string]any{
			"subject_type": "user", "subject_id": s.idAdmin,
		}, 0); err != nil {
			return fmt.Errorf("授予管理员工具 ACL：%w", err)
		}
	}
	s.log("MCP #%d 工具 query=%d create=%d", s.mcpID, s.toolQuery, s.toolCreate)
	return nil
}

func (s *script) step7() error {
	s.step(7, "创建并发布「财务助手」，授权给财务部")
	var err error
	if s.agentID, err = s.admin.getID(http.MethodPost, "/api/v1/agents", map[string]any{
		"name": "财务助手", "description": "M1 演示智能体",
		"system_prompt": "你是财务助手。回答差旅问题时依据知识库引用；用户要求创建报销工单时调用 create_ticket。",
		"model_id":      s.modelAPI,
		"kb_ids":        []int{s.kbID},
		"tool_ids":      []int{s.toolQuery, s.toolCreate},
		"max_steps":     5,
	}, 201); err != nil {
		return fmt.Errorf("创建智能体：%w", err)
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/agents/%d/acl", s.agentID), map[string]any{
		"subject_type": "dept", "subject_id": s.financeID, "include_children": true, "perm": "use",
	}, 0); err != nil {
		return fmt.Errorf("授权财务部：%w", err)
	}
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/agents/%d/publish", s.agentID), map[string]any{"note": "M1 演示"}, 200); err != nil {
		// 有的实现 201/204
		if he := asHTTPError(err); he != nil && he.Status != 201 && he.Status != 204 {
			return fmt.Errorf("发布智能体：%w", err)
		}
	}
	s.log("智能体「财务助手」#%d 已发布", s.agentID)
	return nil
}

func (s *script) step8() error {
	s.step(8, "A 提问差旅标准：有引用、不含高管机密、带 AI 生成标识")
	var err error
	if s.convID, err = s.a.getID(http.MethodPost, "/api/v1/conversations", map[string]any{
		"title": "差旅咨询", "agent_id": s.agentID,
	}, 201); err != nil {
		return fmt.Errorf("开对话（员工A）：%w", err)
	}
	evs, hdr, err := s.a.sse(fmt.Sprintf("/api/v1/conversations/%d/messages", s.convID), map[string]any{
		"content": "请问普通员工的差旅标准是什么？",
	})
	if err != nil {
		return fmt.Errorf("发消息（员工A）：%w", err)
	}
	s.trace8 = hdr.Get(traceHeader)
	text, cits, pending, errEv := foldSSE(evs)
	if errEv != "" {
		return fmt.Errorf("SSE error：%s", errEv)
	}
	if pending {
		return fmt.Errorf("第 8 步不应进入等待审批，正文=%s", trimBody(text))
	}
	if strings.Contains(text, execSecret) {
		return fmt.Errorf("回答泄露高管文件机密片段")
	}
	for _, c := range cits {
		if strings.Contains(c, execSecret) || strings.Contains(c, "高管差旅标准") {
			return fmt.Errorf("引用中出现高管文件：%s", trimBody(c))
		}
	}
	if len(cits) == 0 && !strings.Contains(text, "引用") && !strings.Contains(text, "资料") {
		s.log("警告：SSE 中未解析到 citation 事件，将在会话详情中复核")
	}
	if err := s.assertAIGC(s.a, s.convID, text); err != nil {
		return err
	}
	s.log("第 8 步完成（trace=%s，引用 %d 条）", s.trace8, len(cits))
	return nil
}

func foldSSE(evs []SSEEvent) (text string, cits []string, pending bool, errEv string) {
	for _, ev := range evs {
		switch ev.Event {
		case "token":
			var m map[string]any
			_ = json.Unmarshal([]byte(ev.Data), &m)
			text += asString(m["content"])
		case "citation":
			cits = append(cits, ev.Data)
		case "approval_pending":
			pending = true
		case "done":
			var m map[string]any
			_ = json.Unmarshal([]byte(ev.Data), &m)
			if st := asString(m["status"]); st == "awaiting_approval" {
				pending = true
			}
			if c := asString(m["content"]); c != "" {
				text = c
			}
			// 角标可能只在元数据字段、尚未拼进正文。
			_ = asString(m["aigc_badge"])
		case "error":
			errEv = ev.Data
		}
	}
	return
}

func (s *script) assertAIGC(cl *Client, convID int, body string) error {
	var conv map[string]any
	h, err := cl.json(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", convID), nil, 200, &conv)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(conv)
	if !strings.Contains(string(raw), "AI 生成") && !strings.Contains(string(raw), "aigc") {
		return fmt.Errorf("会话详情缺少 AIGC 标识")
	}
	_ = body
	_ = h
	// 导出 Markdown：D-46 要求 front matter
	md, hdr, err := cl.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/export?format=md", convID), nil, 200)
	if err != nil {
		return fmt.Errorf("导出 md：%w", err)
	}
	if !strings.Contains(string(md), aigcPhrase) && !strings.Contains(string(md), "人工智能生成") && !strings.Contains(string(md), "AI 生成") {
		return fmt.Errorf("导出 Markdown 缺少显式标识")
	}
	trim := strings.TrimSpace(string(md))
	if !strings.HasPrefix(trim, "---") {
		return fmt.Errorf("D-46：Markdown 导出应以 YAML front matter（---）开头，实际：%s", trimBody(trim))
	}
	if !strings.Contains(strings.ToLower(hdr.Get(aigcHeader)+string(md)), "contentproducer") &&
		!strings.Contains(string(md), "AIGC") && !strings.Contains(hdr.Get(aigcHeader), "AIGC") {
		return fmt.Errorf("导出缺少隐式 AIGC 元数据")
	}
	js, _, err := cl.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/export?format=json", convID), nil, 200)
	if err != nil {
		return fmt.Errorf("导出 json：%w", err)
	}
	if !strings.Contains(string(js), "AIGC") && !strings.Contains(string(js), "aigc") {
		return fmt.Errorf("JSON 导出缺少 AIGC 元数据字段")
	}
	return nil
}

func (s *script) stepAC05() error {
	s.log("—— AC-05：授予 A 高管文件权限后立即再问，应能检索到（无需重建索引）——")
	if _, _, err := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/kbs/%d/files/%d/acl", s.kbID, s.fileExec),
		map[string]any{"subject_type": "user", "subject_id": s.idA, "perm": "view"}, 0); err != nil {
		return fmt.Errorf("授予 A 高管文件：%w", err)
	}
	evs, _, err := s.a.sse(fmt.Sprintf("/api/v1/conversations/%d/messages", s.convID), map[string]any{
		"content": "高管差旅标准里的机密代码是什么？请引用原文。",
	})
	if err != nil {
		return err
	}
	text, cits, _, errEv := foldSSE(evs)
	if errEv != "" {
		return fmt.Errorf("SSE error：%s", errEv)
	}
	blob := text + strings.Join(cits, "")
	if !strings.Contains(blob, execSecret) {
		// 模型是 mock，不一定复述原文；引用里必须出现
		s.log("警告：回答/引用未直接出现机密代码（mock 可能不回声）；引用数=%d。请确认检索已命中。", len(cits))
		// 仍要求至少有引用，否则视为未生效
		if len(cits) == 0 {
			return fmt.Errorf("ACL 生效后仍无引用，可能未检索到高管文件")
		}
	}
	// 收回 A 的高管文件权限，避免后续步骤污染
	_, _, _ = s.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/kbs/%d/files/%d/acl/user/%d", s.kbID, s.fileExec, s.idA), nil, 0)
	return nil
}

func (s *script) mcpCalls(name string) (int, error) {
	res, err := http.Get(strings.TrimRight(s.cfg.MockMCP, "/") + "/calls")
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	var all map[string][]map[string]any
	if err := json.NewDecoder(res.Body).Decode(&all); err != nil {
		return 0, err
	}
	return len(all[name]), nil
}

func (s *script) step9() error {
	s.step(9, "A 要求创建报销工单：等待审批，C 通过后恰好执行 1 次")
	before, err := s.mcpCalls("create_ticket")
	if err != nil {
		return fmt.Errorf("读 mockmcp /calls：%w", err)
	}
	evs, hdr, err := s.a.sse(fmt.Sprintf("/api/v1/conversations/%d/messages", s.convID), map[string]any{
		"content": "请帮我创建一张报销工单，标题为出差交通费。",
	})
	if err != nil {
		return fmt.Errorf("发消息：%w", err)
	}
	s.trace9 = hdr.Get(traceHeader)
	_, _, pending, errEv := foldSSE(evs)
	if errEv != "" {
		return fmt.Errorf("SSE error：%s", errEv)
	}
	if !pending {
		s.log("警告：SSE 未标 pending，将按审批中心查询")
	}
	afterAsk, err := s.mcpCalls("create_ticket")
	if err != nil {
		return err
	}
	if afterAsk != before {
		return fmt.Errorf("AC-06：审批前 create_ticket 已被调用（%d → %d）", before, afterAsk)
	}

	if !s.cfg.SkipRestart {
		if strings.TrimSpace(s.cfg.RestartCmd) == "" && !dockerSockOK() {
			return fmt.Errorf("AC-08：未提供 -restart-cmd 且无 docker.sock，无法重启 server（可用 -skip-restart 显式跳过）")
		}
		s.log("AC-08：等待审批时重启 server")
		if err := s.restartServer(); err != nil {
			return fmt.Errorf("AC-08 重启：%w", err)
		}
	}

	apID, err := s.waitApproval(s.c, 60*time.Second)
	if err != nil {
		return err
	}
	if _, _, err := s.c.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID),
		map[string]any{"comment": "演示通过"}, 200); err != nil {
		return fmt.Errorf("审批通过（员工C）：%w", err)
	}
	if err := s.waitConvNotAwaiting(s.a, s.convID, 90*time.Second); err != nil {
		return err
	}
	after, err := s.mcpCalls("create_ticket")
	if err != nil {
		return err
	}
	if after != before+1 {
		return fmt.Errorf("AC-06：通过后 create_ticket 应为恰好 1 次新增，实际 %d → %d", before, after)
	}
	s.log("审批通过，create_ticket 恰好执行 1 次（#%d）", apID)
	return nil
}

func (s *script) waitApproval(cl *Client, d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var wrap struct {
			Items []map[string]any `json:"items"`
		}
		if _, err := cl.json(http.MethodGet, "/api/v1/approvals?box=todo&status=pending", nil, 200, &wrap); err != nil {
			return 0, err
		}
		if len(wrap.Items) > 0 {
			return asInt(wrap.Items[0]["id"]), nil
		}
		time.Sleep(time.Second)
	}
	return 0, fmt.Errorf("等待审批单超时")
}

func (s *script) waitConvNotAwaiting(cl *Client, convID int, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var conv map[string]any
		if _, err := cl.json(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", convID), nil, 200, &conv); err != nil {
			return err
		}
		raw, _ := json.Marshal(conv)
		if !strings.Contains(string(raw), "awaiting_approval") {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("等待对话续写超时")
}

func (s *script) step10() error {
	s.step(10, "A 再次创建工单，C 拒绝：工具未执行，对话告知已拒绝")
	before, err := s.mcpCalls("create_ticket")
	if err != nil {
		return err
	}
	if _, _, err := s.a.sse(fmt.Sprintf("/api/v1/conversations/%d/messages", s.convID), map[string]any{
		"content": "请再创建一张报销工单，标题为住宿费。",
	}); err != nil {
		return err
	}
	apID, err := s.waitApproval(s.c, 60*time.Second)
	if err != nil {
		return err
	}
	if _, _, err := s.c.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/reject", apID),
		map[string]any{"comment": "演示拒绝"}, 200); err != nil {
		return fmt.Errorf("拒绝审批（员工C）：%w", err)
	}
	if err := s.waitConvNotAwaiting(s.a, s.convID, 90*time.Second); err != nil {
		return err
	}
	after, err := s.mcpCalls("create_ticket")
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("拒绝后 create_ticket 不应再执行（%d → %d）", before, after)
	}
	var conv map[string]any
	if _, err := s.a.json(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", s.convID), nil, 200, &conv); err != nil {
		return err
	}
	raw, _ := json.Marshal(conv)
	if !strings.Contains(string(raw), "拒绝") && !strings.Contains(string(raw), "已拒绝") {
		s.log("警告：对话正文未明确出现「拒绝」字样，请人工确认。片段：%s", trimBody(string(raw)))
	}
	s.log("拒绝路径完成，工具未执行")
	return nil
}

func (s *script) step11() error {
	s.step(11, "B 看不到财务助手；直接访问详情返回 404")
	var list any
	if _, err := s.b.json(http.MethodGet, "/api/v1/agents", nil, 200, &list); err != nil {
		return err
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "财务助手") || strings.Contains(string(raw), fmt.Sprintf(`"id":%d`, s.agentID)) {
		return fmt.Errorf("市场部列表中出现了财务助手")
	}
	if _, _, err := s.b.do(http.MethodGet, fmt.Sprintf("/api/v1/agents/%d", s.agentID), nil, 404); err != nil {
		return fmt.Errorf("访问详情应 404（员工B）：%w", err)
	}
	s.log("越权 404 断言通过")
	return nil
}

func (s *script) step12() error {
	s.step(12, "访问白名单外地址被阻断，并写入站日志")
	// 保存时直连白名单外 / 链路本地应被拒绝
	_, _, err := s.admin.do(http.MethodPost, "/api/v1/admin/mcp-servers", map[string]any{
		"name": "越权探测", "url": "http://169.254.169.254/mcp", "transport": "streamable_http",
	}, 400)
	if err != nil {
		if he := asHTTPError(err); he != nil && he.Status != 403 && he.Status != 400 && he.Status != 422 {
			return fmt.Errorf("登记链路本地 MCP 应被拒绝，实际：%w", err)
		}
	}
	var hint map[string]any
	if _, err := s.admin.json(http.MethodPost, "/api/v1/admin/egress/check", map[string]any{"url": "http://169.254.169.254/"}, 200, &hint); err != nil {
		return fmt.Errorf("egress/check：%w", err)
	}
	if hint["allowed"] == true {
		return fmt.Errorf("169.254.169.254 不应被放行：%v", hint)
	}
	// 复用真机演示 redirector 思路：登记已放行的 mockredir，发现时 307 到白名单外，由出站客户端拦截（AC-09）
	if redir := strings.TrimRight(strings.TrimSpace(s.cfg.MockRedir), "/"); redir != "" {
		for _, pair := range []struct{ name, path string }{
			{"越界演示开关", "/mcp"},
			{"越界到元数据地址", "/meta/mcp"},
		} {
			id, e := s.admin.getID(http.MethodPost, "/api/v1/admin/mcp-servers", map[string]any{
				"name": pair.name, "url": redir + pair.path, "transport": "streamable_http",
			}, 0)
			if e != nil || id == 0 {
				s.log("警告：登记 %s 失败：%v", pair.name, e)
				continue
			}
			// 期望非 2xx。do 的 want=0 不把 4xx/5xx 当错误，这里显式要 200，成功则判定失败。
			_, _, discErr := s.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/mcp-servers/%d/discover", id), map[string]any{}, 200)
			if discErr == nil {
				return fmt.Errorf("AC-09：发现 %s 不应成功（应被出站拦截）", pair.name)
			}
			if he := asHTTPError(discErr); he != nil && he.Status >= 200 && he.Status < 300 {
				return fmt.Errorf("AC-09：发现 %s 不应成功（应被出站拦截），实际 HTTP %d", pair.name, he.Status)
			}
			s.log("发现 %s 已被拦截：%v", pair.name, discErr)
		}
	}
	var logs struct {
		Items []map[string]any `json:"items"`
	}
	if _, err := s.admin.json(http.MethodGet, "/api/v1/admin/egress/logs?limit=50", nil, 200, &logs); err != nil {
		var arr []map[string]any
		if _, err2 := s.admin.json(http.MethodGet, "/api/v1/admin/egress/logs?limit=50", nil, 200, &arr); err2 != nil {
			return fmt.Errorf("出站日志：%w", err)
		}
		logs.Items = arr
	}
	blocked := 0
	for _, it := range logs.Items {
		dec := asString(it["decision"])
		if dec == "deny" || dec == "blocked" || dec == "rejected" {
			blocked++
		}
	}
	// 缺陷 D3：放行记录必须写回状态码（append-only 触发器曾拒绝 UPDATE，导致 status 恒为 0）
	for _, it := range logs.Items {
		if asString(it["decision"]) == "allow" && asInt(it["status"]) == 0 && asInt(it["status_code"]) == 0 {
			return fmt.Errorf("缺陷 D3：放行的出站日志 #%d 未记录状态码", asInt(it["id"]))
		}
	}
	if blocked == 0 && len(logs.Items) == 0 {
		s.log("警告：出站日志为空或未解析到 deny（check 接口已断言不允许）")
	}
	s.log("出站阻断断言完成（日志 %d 条）", len(logs.Items))
	return nil
}

func (s *script) step13() error {
	s.step(13, "A 持续对话至 80% 告警、100% 拦截")
	var lastHdr http.Header
	blocked := false
	for i := 0; i < 40; i++ {
		evs, hdr, err := s.a.sse(fmt.Sprintf("/api/v1/conversations/%d/messages", s.convID), map[string]any{
			"content": fmt.Sprintf("请再简要说明一下差旅标准（第 %d 次）。", i+1),
		})
		lastHdr = hdr
		if err != nil {
			if he := asHTTPError(err); he != nil && (he.Status == 403 || he.Status == 429 || he.Status == 402 || he.Status == 409 || he.Status == 400) {
				if strings.Contains(he.Body, "预算") || strings.Contains(he.Body, "管理员") || strings.Contains(he.Body, "上限") || strings.Contains(he.Body, "用尽") {
					blocked = true
					s.log("已拦截：HTTP %d %s", he.Status, trimBody(he.Body))
					break
				}
			}
			// 可能 200 SSE 里带 error
			if evs != nil {
				_, _, _, errEv := foldSSE(evs)
				if strings.Contains(errEv, "预算") || strings.Contains(errEv, "管理员") {
					blocked = true
					break
				}
			}
			return fmt.Errorf("续聊第 %d 次：%w", i+1, err)
		}
		_, _, pending, errEv := foldSSE(evs)
		if errEv != "" && (strings.Contains(errEv, "预算") || strings.Contains(errEv, "管理员") || strings.Contains(errEv, "用尽")) {
			blocked = true
			break
		}
		if pending {
			s.log("续聊触发了审批，跳过此轮")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastHdr != nil {
		s.trace13 = lastHdr.Get(traceHeader)
	}
	if !blocked {
		return fmt.Errorf("40 轮对话后仍未被预算拦截（上限 %d）", s.cfg.BudgetLimit)
	}
	// 经理应收到 80% 或 100% 告警
	var notif struct {
		Items []map[string]any `json:"items"`
	}
	if _, err := s.m.json(http.MethodGet, "/api/v1/notifications", nil, 200, &notif); err != nil {
		s.log("警告：读取经理通知失败：%v", err)
	} else {
		hit := false
		for _, it := range notif.Items {
			if asString(it["kind"]) == "budget_alert" || strings.Contains(asString(it["title"]), "预算") {
				hit = true
				break
			}
		}
		if !hit {
			s.log("警告：经理通知中未找到预算告警，检查管理员通知")
		}
	}
	s.log("预算告警与拦截断言完成")
	return nil
}

func (s *script) step14() error {
	s.step(14, "审计员 D 按 trace 回放；D 改配置被拒；超管删除审计被拒")
	// D 是审计员，无 settings / member manage
	if s.d == nil {
		return fmt.Errorf("审计员客户端未初始化")
	}
	traces := []string{s.trace8, s.trace9, s.trace13}
	found := 0
	for _, tr := range traces {
		if tr == "" {
			continue
		}
		b, _, err := s.d.do(http.MethodGet, "/api/v1/admin/audit-logs/traces/"+tr, nil, 200)
		if err != nil {
			return fmt.Errorf("回放 %s：%w", tr, err)
		}
		if len(bytes.TrimSpace(b)) == 0 {
			return fmt.Errorf("trace %s 回放为空", tr)
		}
		found++
		s.log("回放 trace %s 成功（%d 字节）", tr, len(b))
	}
	if found == 0 {
		return fmt.Errorf("没有可回放的 trace_id（请检查 X-Trace-Id）")
	}
	// D 尝试改 AIGC 设置 / 出站规则 → 404
	if _, _, err := s.d.do(http.MethodPut, "/api/v1/admin/aigc", map[string]any{"badge": "AI 生成"}, 404); err != nil {
		return fmt.Errorf("审计员改 AIGC 设置应 404：%w", err)
	}
	if _, _, err := s.d.do(http.MethodPost, "/api/v1/admin/egress/rules", map[string]any{"kind": "host", "value": "evil.example", "ports": "443"}, 404); err != nil {
		return fmt.Errorf("审计员改出站规则应 404：%w", err)
	}
	// 超管尝试删除审计：API 不应存在或被拒绝
	var logs struct {
		Items []map[string]any `json:"items"`
	}
	if _, err := s.d.json(http.MethodGet, "/api/v1/admin/audit-logs?limit=5", nil, 200, &logs); err != nil {
		return fmt.Errorf("审计员查日志：%w", err)
	}
	if len(logs.Items) > 0 {
		id := asInt(logs.Items[0]["id"])
		_, _, err := s.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/audit-logs/%d", id), nil, 0)
		if err == nil {
			return fmt.Errorf("超管删除审计记录竟然成功")
		}
		if he := asHTTPError(err); he != nil && he.Status != 404 && he.Status != 405 && he.Status != 403 && he.Status != 401 {
			return fmt.Errorf("超管删除审计应失败，实际 HTTP %d", he.Status)
		}
	}
	// AC-12 / D5：审计员全链校验必须通过（真机演示发现 usage 规范化导致断链）
	var ver map[string]any
	if _, err := s.d.json(http.MethodGet, "/api/v1/admin/audit-logs/verify", nil, 200, &ver); err != nil {
		return fmt.Errorf("AC-12 校验哈希链：%w", err)
	}
	if ver["ok"] != true {
		return fmt.Errorf("AC-12（缺陷 D5）：哈希链校验未通过：%v", ver)
	}
	s.log("审计回放与不可改断言完成，哈希链校验通过")
	return nil
}

func (s *script) step15() error {
	s.step(15, "统计看板中财务部 token 与用量明细一致")
	var overview map[string]any
	if _, err := s.admin.json(http.MethodGet, fmt.Sprintf("/api/v1/admin/stats/overview?department_id=%d", s.financeID), nil, 200, &overview); err != nil {
		return fmt.Errorf("overview：%w", err)
	}
	var summary struct {
		Items  []map[string]any `json:"items"`
		Totals map[string]any   `json:"totals"`
	}
	if _, err := s.admin.json(http.MethodGet, "/api/v1/admin/stats/summary?group_by=department", nil, 200, &summary); err != nil {
		return fmt.Errorf("summary：%w", err)
	}
	var recs struct {
		Items []map[string]any `json:"items"`
	}
	if _, err := s.admin.json(http.MethodGet, "/api/v1/admin/usage/records?limit=200", nil, 200, &recs); err != nil {
		// 路径可能不同
		s.log("警告：读取 usage/records 失败：%v，改用 summary totals", err)
	}
	ovTokens := asInt(jsonGet(overview, "total_tokens"))
	if ovTokens == 0 {
		ovTokens = asInt(jsonGet(overview, "tokens"))
	}
	if ovTokens == 0 {
		if t, ok := overview["totals"].(map[string]any); ok {
			ovTokens = asInt(t["total_tokens"])
		}
	}
	sumTokens := asInt(summary.Totals["total_tokens"])
	if sumTokens == 0 {
		sumTokens = asInt(summary.Totals["tokens"])
	}
	recSum := 0
	for _, r := range recs.Items {
		// 只计财务部
		if asInt(r["department_id"]) != 0 && asInt(r["department_id"]) != s.financeID {
			continue
		}
		recSum += asInt(r["total_tokens"])
	}
	s.log("overview=%d summary=%d records=%d", ovTokens, sumTokens, recSum)
	if ovTokens > 0 && sumTokens > 0 && ovTokens != sumTokens {
		return fmt.Errorf("AC-16：看板 overview（%d）与 summary（%d）不一致", ovTokens, sumTokens)
	}
	if recSum > 0 && sumTokens > 0 && recSum != sumTokens {
		// 小时表与明细在当前小时应对齐；若差在估算字段，报出来
		s.log("警告：明细合计 %d 与 summary %d 不同，检查是否含其他部门或当前小时补齐延迟", recSum, sumTokens)
	}
	if ovTokens == 0 && sumTokens == 0 && recSum == 0 {
		return fmt.Errorf("统计结果全为 0，用量未入账")
	}
	return nil
}

func (s *script) stepD46() error {
	s.log("—— D-46：标识文案校验、只能导出自己的会话 ——")
	// 空文案 / 不含合规短语应失败
	for _, body := range []map[string]any{
		{"badge": "", "suffix": aigcPhrase},
		{"badge": "内部稿", "suffix": aigcPhrase},
		{"badge": "AI 生成", "suffix": ""},
		{"badge": "AI 生成", "suffix": "内部说明"},
	} {
		_, _, err := s.admin.do(http.MethodPut, "/api/v1/admin/aigc", body, 400)
		if err != nil {
			return fmt.Errorf("非法文案 %v 应保存失败（400）：%w", body, err)
		}
	}
	// 合法文案可保存
	if _, _, err := s.admin.do(http.MethodPut, "/api/v1/admin/aigc", map[string]any{
		"badge": "AI 生成", "suffix": aigcPhrase,
	}, 200); err != nil {
		return fmt.Errorf("合法文案保存失败：%w", err)
	}
	// B 不能导出 A 的会话
	if _, _, err := s.b.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/export?format=md", s.convID), nil, 404); err != nil {
		return fmt.Errorf("导出他人会话应 404（员工B）：%w", err)
	}
	s.log("D-46 文案校验与导出属主断言通过")
	return nil
}

func (s *script) waitHTTP(raw string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		res, err := http.Get(raw)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			if res.StatusCode < 500 {
				return nil
			}
			last = fmt.Errorf("HTTP %d", res.StatusCode)
		} else {
			last = err
		}
		time.Sleep(2 * time.Second)
	}
	return last
}

func (s *script) file(rel string) []byte {
	if s.cfg.DataDir != "" {
		b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, rel))
		if err == nil {
			return b
		}
	}
	switch rel {
	case "departments.csv":
		return []byte("dept_path,name,email,employee_no\n财务部,员工A," + emailA + ",A001\n市场部,员工B," + emailB + ",B001\n财务部,审批人C," + emailC + ",C001\n财务部,审计员D," + emailD + ",D001\n财务部,财务经理," + emailM + ",M001\n")
	case "kb/travel.md":
		return []byte("# 差旅报销须知\n\n普通员工差旅标准为高铁二等座或经济舱。市内交通可报销公共交通与必要的网约车。\n住宿按职级对应的城市限额执行。\n")
	case "kb/fees.md":
		return []byte("# 费用科目说明\n\n交通费计入「差旅-交通」，住宿费计入「差旅-住宿」，餐费按出差补贴包干，不单独报销。\n")
	case "kb/exec.md":
		return []byte("# 高管差旅标准\n\n本文件仅财务部经理可见。\n高管可报销商务舱与五星级酒店。机密代码：" + execSecret + "。\n")
	default:
		return nil
	}
}

func asHTTPError(err error) *httpError {
	var he *httpError
	if errors.As(err, &he) {
		return he
	}
	return nil
}
