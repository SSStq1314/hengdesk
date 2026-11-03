// Package oidctest 提供一个最小的 OIDC 模拟 IdP，仅用于测试（自行编写，不含第三方 IdP 代码）。
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

// Identity 下一次登录时 IdP 返回的身份。
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
	ClientID, ClientSecret string

	mu     sync.Mutex
	next   Identity
	codes  map[string]grant
	tokens map[string]Identity
	key    *rsa.PrivateKey
	signer jose.Signer
	// BadNonce 为 true 时签发错误的 nonce（测试重放防护）。
	BadNonce bool
}

// New 启动模拟 IdP。
func New(clientID, secret string) *IdP {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"))
	if err != nil {
		panic(err)
	}
	m := &IdP{ClientID: clientID, ClientSecret: secret, codes: map[string]grant{}, tokens: map[string]Identity{}, key: k, signer: sg}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", m.discovery)
	mux.HandleFunc("/jwks", m.jwks)
	mux.HandleFunc("/authorize", m.authorize)
	mux.HandleFunc("/token", m.token)
	mux.HandleFunc("/userinfo", m.userinfo)
	m.Server = httptest.NewServer(mux)
	return m
}

// SetIdentity 设置下一次登录的身份。
func (m *IdP) SetIdentity(id Identity) { m.mu.Lock(); m.next = id; m.mu.Unlock() }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"issuer": m.URL, "authorization_endpoint": m.URL + "/authorize", "token_endpoint": m.URL + "/token",
		"jwks_uri": m.URL + "/jwks", "userinfo_endpoint": m.URL + "/userinfo", "response_types_supported": []string{"code"},
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
	code := rnd()
	m.mu.Lock()
	m.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), id: m.next}
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
	claims := map[string]any{"iss": m.URL, "aud": m.ClientID, "sub": g.id.Sub, "nonce": nonce,
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
	out := map[string]any{"sub": id.Sub, "email": id.Email, "email_verified": id.EmailVerified}
	for k, v := range id.Extra {
		out[k] = v
	}
	writeJSON(w, 200, out)
}
