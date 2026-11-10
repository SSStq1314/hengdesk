package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const dockerAPI = "/v1.41"

func dockerSockPath() string {
	h := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if h == "" {
		h = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(h, "unix://") {
		return ""
	}
	return strings.TrimPrefix(h, "unix://")
}

func dockerSockOK() bool {
	p := dockerSockPath()
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

func (s *script) restartServer() error {
	if cmd := strings.TrimSpace(s.cfg.RestartCmd); cmd != "" {
		c := exec.Command("sh", "-c", cmd)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return err
		}
	} else if err := s.restartViaDockerSock(); err != nil {
		return err
	}
	if err := s.waitHTTP(s.cfg.Base+"/healthz", 90*time.Second); err != nil {
		return err
	}
	// 会话在 Valkey，应仍有效；若失效则重新 SSO
	if _, _, err := s.a.do(http.MethodGet, "/api/v1/usage/me", nil, 200); err != nil {
		s.log("重启后 A 会话失效，重新登录")
		if s.cfg.SkipOIDC {
			return s.a.login(emailA, "Demo-Passw0rd-9x")
		}
		return s.a.oidcLogin(s.cfg.MockOIDC, emailA)
	}
	return nil
}

func (s *script) restartViaDockerSock() error {
	sock := dockerSockPath()
	if sock == "" {
		return fmt.Errorf("未设置 unix Docker 套接字（DOCKER_HOST）")
	}
	project := strings.TrimSpace(s.cfg.ComposeProject)
	if project == "" {
		project = env("M1ACCEPT_COMPOSE_PROJECT", "hengdesk")
	}
	cli := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	filters, _ := json.Marshal(map[string][]string{
		"label": {
			"com.docker.compose.project=" + project,
			"com.docker.compose.service=server",
		},
	})
	listURL := "http://localhost" + dockerAPI + "/containers/json?filters=" + url.QueryEscape(string(filters))
	res, err := cli.Get(listURL)
	if err != nil {
		return fmt.Errorf("列举 server 容器：%w", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("列举 server 容器：HTTP %d %s", res.StatusCode, truncate(string(body), 200))
	}
	var list []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("解析容器列表：%w", err)
	}
	if len(list) == 0 {
		return fmt.Errorf("compose 项目 %q 中未找到 service=server 的容器", project)
	}
	id := list[0].ID
	s.log("AC-08：通过 docker.sock 重启 %s（%s）", strings.Join(list[0].Names, ","), id[:12])
	req, err := http.NewRequest(http.MethodPost, "http://localhost"+dockerAPI+"/containers/"+id+"/restart?t=15", nil)
	if err != nil {
		return err
	}
	res, err = cli.Do(req)
	if err != nil {
		return fmt.Errorf("重启容器：%w", err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("重启容器：HTTP %d %s", res.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
