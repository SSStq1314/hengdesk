package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// 密文格式：version(1 字节, =1) || nonce(12) || GCM 密文+tag。AAD 绑定用途与供应商名，防止密文在记录之间挪用。
const keyVersion = 1

// ErrNoMasterKey 未配置主密钥：此时不允许保存或使用上游密钥（fail-closed）。
var ErrNoMasterKey = errors.New("gateway: 未配置主密钥（HENGDESK_MASTER_KEY_FILE 或 HENGDESK_MASTER_KEY）")

// Keyring 用主密钥做 AES-256-GCM 加解密。
type Keyring struct{ aead cipher.AEAD }

// NewKeyring 由 32 字节主密钥构造。
func NewKeyring(master []byte) (*Keyring, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("gateway: 主密钥必须是 32 字节，实际 %d", len(master))
	}
	b, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Keyring{aead: a}, nil
}

// ParseMasterKey 接受 64 位 hex 或 base64（标准 / URL，可无填充）编码的 32 字节。
func ParseMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("gateway: 主密钥格式无效（需 32 字节，hex 或 base64 编码）")
}

// LoadKeyring 优先读密钥文件，其次读环境变量值；都为空时返回 (nil, nil)，表示未配置。
func LoadKeyring(file, envValue string) (*Keyring, error) {
	var raw string
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("gateway: 读取主密钥文件失败: %w", err)
		}
		raw = string(b)
	case envValue != "":
		raw = envValue
	default:
		return nil, nil
	}
	k, err := ParseMasterKey(raw)
	if err != nil {
		return nil, err
	}
	return NewKeyring(k)
}

func aad(provider string) []byte { return []byte("hengdesk/model_provider/api_key/" + provider) }

// Seal 加密上游密钥。
func (k *Keyring) Seal(provider, plaintext string) ([]byte, error) {
	if k == nil {
		return nil, ErrNoMasterKey
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{keyVersion}, nonce...)
	return k.aead.Seal(out, nonce, []byte(plaintext), aad(provider)), nil
}

// Open 解密上游密钥。
func (k *Keyring) Open(provider string, blob []byte) (string, error) {
	if k == nil {
		return "", ErrNoMasterKey
	}
	ns := k.aead.NonceSize()
	if len(blob) < 1+ns+k.aead.Overhead() || blob[0] != keyVersion {
		return "", errors.New("gateway: 密文格式无效")
	}
	pt, err := k.aead.Open(nil, blob[1:1+ns], blob[1+ns:], aad(provider))
	if err != nil {
		return "", errors.New("gateway: 密钥解密失败（主密钥不匹配或密文被篡改）")
	}
	return string(pt), nil
}
