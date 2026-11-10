// Package oidctest 提供一个最小的 OIDC 模拟 IdP，仅用于测试与 M1 演示（自行编写，不含第三方 IdP 代码）。
//
// 支持 discovery、JWKS、/authorize（自动同意，返回 code）、/token（校验 client 凭据与 PKCE S256）。
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Identity 登录时 IdP 返回的身份。
type Identity struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
	// Extra 额外 claim（如部门）；ExtraInUserInfo 为 true 时只放在 UserInfo，不放进 ID Token。
	Extra           map[string]any
	ExtraInUserInfo bool
}

type grant struct {
	challenge, nonce, redirect string
	id                         Identity
}

// IdP 模拟身份提供方。
type IdP struct {
	*httptest.Server
	// BaseURL 对外公布的 issuer（发现文档与 ID Token 的 iss）。空则用 httptest 的 URL。
	// 容器网络里应设为 http://mockoidc:18081 这类对端都能访问的地址。
	BaseURL                string
	ClientID, ClientSecret string

	mu     sync.Mutex
	next   Identity
	users  map[string]Identity // 按邮箱注册，authorize 可用 login_hint 选用
	codes  map[string]grant
	tokens map[string]Identity
	key    *rsa.PrivateKey
	signer jose.Signer
	// BadNonce 为 true 时签发错误的 nonce（测试重放防护）。
	BadNonce bool
}

func newIDP(clientID, secret string) *IdP {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"))
	if err != nil {
		panic(err)
	}
	return &IdP{ClientID: clientID, ClientSecret: secret, users: map[string]Identity{}, codes: map[string]grant{}, tokens: map[string]Identity{}, key: k, signer: sg}
}

func (m *IdP) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", m.discovery)
	mux.HandleFunc("/jwks", m.jwks)
	mux.HandleFunc("/authorize", m.authorize)
	mux.HandleFunc("/token", m.token)
	mux.HandleFunc("/userinfo", m.userinfo)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/set-identity", m.setIdentityHTTP)
	return mux
}

// New 启动 httptest 模拟 IdP（单元测试用）。
func New(clientID, secret string) *IdP {
	m := newIDP(clientID, secret)
	m.Server = httptest.NewServer(m.mux())
	return m
}

// NewStandalone 构造可在真实端口上 Serve 的 IdP（演示 / 验收用）。publicURL 为 issuer，例如 http://mockoidc:18081。
func NewStandalone(clientID, secret, publicURL string) *IdP {
	m := newIDP(clientID, secret)
	m.BaseURL = strings.TrimRight(publicURL, "/")
	return m
}

// Handler 返回 HTTP 处理函数，供 ListenAndServe 使用。
func (m *IdP) Handler() http.Handler { return m.mux() }

// Issuer 发现文档与 ID Token 中的 iss。
func (m *IdP) Issuer() string {
	if m.BaseURL != "" {
		return strings.TrimRight(m.BaseURL, "/")
	}
	if m.Server != nil {
		return m.URL
	}
	return ""
}

// SetIdentity 设置下一次登录的默认身份（无 login_hint 且未命中已注册用户时使用）。
func (m *IdP) SetIdentity(id Identity) { m.mu.Lock(); m.next = id; m.mu.Unlock() }

// AddUser 注册一个可被 login_hint 选中的用户。
func (m *IdP) AddUser(id Identity) {
	email := strings.ToLower(strings.TrimSpace(id.Email))
	if email == "" {
		return
	}
	if id.Sub == "" {
		id.Sub = email
	}
	m.mu.Lock()
	m.users[email] = id
	m.mu.Unlock()
}

func (m *IdP) pick(r *http.Request) Identity {
	q := r.URL.Query()
	hint := strings.ToLower(strings.TrimSpace(q.Get("login_hint")))
	if hint == "" {
		hint = strings.ToLower(strings.TrimSpace(q.Get("hd_user")))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if hint != "" {
		if id, ok := m.users[hint]; ok {
			return id
		}
	}
	if m.next.Email != "" || m.next.Sub != "" {
		return m.next
	}
	return Identity{}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *IdP) setIdentityHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var id Identity
	if err := json.NewDecoder(r.Body).Decode(&id); err != nil || (id.Email == "" && id.Sub == "") {
		http.Error(w, "bad identity", http.StatusBadRequest)
		return
	}
	m.SetIdentity(id)
	if id.Email != "" {
		m.AddUser(id)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "email": id.Email})
}

func (m *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	iss := m.Issuer()
	writeJSON(w, 200, map[string]any{
		"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
		"jwks_uri": iss + "/jwks", "userinfo_endpoint": iss + "/userinfo", "response_types_supported": []string{"code"},
		"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func (m *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &m.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

func rnd() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (m *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != m.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	id := m.pick(r)
	code := rnd()
	m.mu.Lock()
	m.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), id: id}
	m.mu.Unlock()
	u, _ := url.Parse(q.Get("redirect_uri"))
	v := u.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (m *IdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, sec, ok := r.BasicAuth()
	if !ok {
		id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != m.ClientID || sec != m.ClientSecret {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	m.mu.Lock()
	g, found := m.codes[r.PostForm.Get("code")]
	delete(m.codes, r.PostForm.Get("code"))
	bad := m.BadNonce
	m.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || g.redirect != r.PostForm.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	nonce := g.nonce
	if bad {
		nonce = "wrong"
	}
	now := time.Now()
	iss := m.Issuer()
	claims := map[string]any{"iss": iss, "aud": m.ClientID, "sub": g.id.Sub, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"email": g.id.Email, "email_verified": g.id.EmailVerified, "name": g.id.Name}
	if !g.id.ExtraInUserInfo {
		for k, v := range g.id.Extra {
			claims[k] = v
		}
	}
	at := rnd()
	m.mu.Lock()
	m.tokens[at] = g.id
	m.mu.Unlock()
	raw, err := jwt.Signed(m.signer).Claims(claims).Serialize()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": 300, "id_token": raw})
}

func (m *IdP) userinfo(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	m.mu.Lock()
	id, ok := m.tokens[tok]
	m.mu.Unlock()
	if !ok {
		writeJSON(w, 401, map[string]string{"error": "invalid_token"})
		return
	}
	out := map[string]any{"sub": id.Sub, "email": id.Email, "email_verified": id.EmailVerified, "name": id.Name}
	for k, v := range id.Extra {
		out[k] = v
	}
	writeJSON(w, 200, out)
}
