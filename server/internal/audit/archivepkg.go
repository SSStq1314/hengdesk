package audit

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
)

// 审计归档包（FR-40 / D-34 第 4 点）。tar.gz 内含：
//
//	records.jsonl  每行一条审计记录（全部字段，含 prev_hash / hash）
//	manifest.json  归档元数据：范围、条数、哈希链首尾值、records.jsonl 的 sha256
//	SHA256SUMS     records.jsonl 与 manifest.json 的 sha256，可直接 `sha256sum -c SHA256SUMS`
//	manifest.sig   （可选）manifest.json 的 Ed25519 签名
//	VERIFY.md      离线校验说明
const (
	// CheckpointAction 归档断点：写在审计链上，记录被归档区间的链尾哈希，剩余链以它为锚点继续校验。
	CheckpointAction = "audit.archive.checkpoint"
	ArchiveFormat    = "hengdesk-audit-archive/v1"
)

// ArchiveRecord 归档中的一条审计记录。
type ArchiveRecord struct {
	ID           int            `json:"id"`
	Ts           time.Time      `json:"ts"`
	OrgID        int            `json:"org_id,omitempty"`
	ActorID      int            `json:"actor_id,omitempty"`
	ActorEmail   string         `json:"actor_email"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Phase        string         `json:"phase"`
	Outcome      string         `json:"outcome"`
	IP           string         `json:"ip"`
	UserAgent    string         `json:"user_agent"`
	RequestID    string         `json:"request_id"`
	TraceID      string         `json:"trace_id"`
	Detail       map[string]any `json:"detail,omitempty"`
	PrevHash     string         `json:"prev_hash"`
	Hash         string         `json:"hash"`
}

// RecordFromEnt 转换。
func RecordFromEnt(r *ent.AuditLog) ArchiveRecord {
	return ArchiveRecord{ID: r.ID, Ts: r.Ts.UTC(), OrgID: r.OrgID, ActorID: r.ActorID, ActorEmail: r.ActorEmail,
		Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID, Phase: string(r.Phase),
		Outcome: r.Outcome, IP: r.IP, UserAgent: r.UserAgent, RequestID: r.RequestID, TraceID: r.TraceID,
		Detail: r.Detail, PrevHash: r.PrevHash, Hash: r.Hash}
}

// ComputeHash 按与在线链相同的规则重算哈希。
func (a ArchiveRecord) ComputeHash() string {
	return canonical(a.PrevHash, a.Ts, a.OrgID, a.ActorID, a.ActorEmail, a.Action, a.ResourceType, a.ResourceID,
		a.Phase, a.Outcome, a.IP, a.UserAgent, a.RequestID, a.TraceID, a.Detail)
}

// Manifest 归档清单。
type Manifest struct {
	Format        string    `json:"format"`
	ArchiveID     string    `json:"archive_id"`
	CreatedAt     time.Time `json:"created_at"`
	Cutoff        time.Time `json:"cutoff"`
	RetentionDays int       `json:"retention_days"`
	FirstID       int       `json:"first_id"`
	LastID        int       `json:"last_id"`
	Count         int       `json:"count"`
	Chained       int       `json:"chained"` // 有哈希的记录数（其余为 M1 之前的旧记录）
	FirstPrevHash string    `json:"first_prev_hash"`
	FirstHash     string    `json:"first_hash"`
	LastHash      string    `json:"last_hash"`
	RecordsSHA256 string    `json:"records_sha256"`
	SignAlg       string    `json:"sign_alg,omitempty"`
	SignPublicKey string    `json:"sign_public_key,omitempty"` // hex；仅供参考，校验时应使用单独保管的公钥
}

// ChainSegment 校验一段按 ID 升序的记录：链连续、哈希正确。prev 是区间之前的链尾（无则为空）。
// 返回首尾哈希和有哈希的条数。
func ChainSegment(recs []ArchiveRecord, prev string) (first, last string, chained int, err error) {
	for i, r := range recs {
		if i > 0 && r.ID <= recs[i-1].ID {
			return "", "", 0, fmt.Errorf("记录 %d 顺序错误", r.ID)
		}
		if r.Hash == "" {
			continue // 旧记录不在链上
		}
		if chained == 0 {
			first = r.Hash
			if prev != "" && r.PrevHash != prev {
				return "", "", 0, fmt.Errorf("记录 %d 的 prev_hash 与锚点不一致", r.ID)
			}
		} else if r.PrevHash != last {
			return "", "", 0, fmt.Errorf("记录 %d 的 prev_hash 断链", r.ID)
		}
		if r.ComputeHash() != r.Hash {
			return "", "", 0, fmt.Errorf("记录 %d 的哈希不匹配（内容被修改）", r.ID)
		}
		last = r.Hash
		chained++
	}
	return first, last, chained, nil
}

const verifyDoc = `# 衡台审计归档包：离线校验

本包由 ` + "`hengdesk audit archive`" + ` 生成，包含 records.jsonl、manifest.json、SHA256SUMS（以及可选的 manifest.sig）。

## 1. 只用系统自带工具（完整性）

    mkdir pkg && tar -xzf <归档>.tar.gz -C pkg && cd pkg
    sha256sum -c SHA256SUMS

两行都显示 OK，说明 records.jsonl 与 manifest.json 未被改动（与 manifest 中 records_sha256 一致）。

## 2. 用 hengdesk 二进制（完整性 + 哈希链 + 签名）

    hengdesk audit verify-archive <归档>.tar.gz [--pubkey <hex 公钥>]

逐条重算哈希链，核对 manifest 中的条数、首尾哈希；给出 --pubkey 时还会校验 manifest.sig。
该命令不连接数据库或网络，可在隔离环境运行。

## 3. 与在线审计链衔接

在线审计链中有一条 action=audit.archive.checkpoint 的断点记录，其 detail.last_hash 等于本包
manifest.last_hash，detail.sha256 等于本 tar.gz 文件的 sha256。
`

// BuildPackage 生成归档包（tar.gz 字节）。recs 必须已通过 ChainSegment 校验。
func BuildPackage(m Manifest, recs []ArchiveRecord, signKey ed25519.PrivateKey) ([]byte, Manifest, error) {
	var jl bytes.Buffer
	enc := json.NewEncoder(&jl)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return nil, m, err
		}
	}
	rs := sha256.Sum256(jl.Bytes())
	m.Format, m.RecordsSHA256 = ArchiveFormat, hex.EncodeToString(rs[:])
	var sig []byte
	if signKey != nil {
		m.SignAlg = "ed25519"
		m.SignPublicKey = hex.EncodeToString(signKey.Public().(ed25519.PublicKey))
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, m, err
	}
	mb = append(mb, '\n')
	if signKey != nil {
		sig = []byte(hex.EncodeToString(ed25519.Sign(signKey, mb)) + "\n")
	}
	ms := sha256.Sum256(mb)
	sums := fmt.Sprintf("%s  records.jsonl\n%s  manifest.json\n", m.RecordsSHA256, hex.EncodeToString(ms[:]))
	files := []struct {
		name string
		data []byte
	}{{"records.jsonl", jl.Bytes()}, {"manifest.json", mb}, {"SHA256SUMS", []byte(sums)}, {"VERIFY.md", []byte(verifyDoc)}}
	if sig != nil {
		files = append(files, struct {
			name string
			data []byte
		}{"manifest.sig", sig})
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o444, Size: int64(len(f.data)), ModTime: m.CreatedAt}); err != nil {
			return nil, m, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, m, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, m, err
	}
	if err := gz.Close(); err != nil {
		return nil, m, err
	}
	return out.Bytes(), m, nil
}

// PackageReport 离线校验结果。
type PackageReport struct {
	Manifest  Manifest `json:"manifest"`
	Signature string   `json:"signature"` // verified / absent / not_checked
}

// VerifyPackage 离线校验归档包：SHA256SUMS、manifest 一致性、逐条哈希链、（可选）签名。不访问数据库与网络。
func VerifyPackage(r io.Reader, pub ed25519.PublicKey) (PackageReport, error) {
	var rep PackageReport
	gz, err := gzip.NewReader(r)
	if err != nil {
		return rep, fmt.Errorf("不是 gzip 文件: %w", err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rep, fmt.Errorf("读取 tar: %w", err)
		}
		b, err := io.ReadAll(io.LimitReader(tr, 4<<30))
		if err != nil {
			return rep, err
		}
		files[h.Name] = b
	}
	for _, n := range []string{"records.jsonl", "manifest.json", "SHA256SUMS"} {
		if _, ok := files[n]; !ok {
			return rep, fmt.Errorf("缺少 %s", n)
		}
	}
	// SHA256SUMS
	listed := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(files["SHA256SUMS"]))
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) != 2 {
			return rep, errors.New("SHA256SUMS 格式错误")
		}
		name := strings.TrimPrefix(parts[1], "*")
		b, ok := files[name]
		if !ok {
			return rep, fmt.Errorf("SHA256SUMS 引用了不存在的文件 %s", name)
		}
		s := sha256.Sum256(b)
		if hex.EncodeToString(s[:]) != parts[0] {
			return rep, fmt.Errorf("%s 的 sha256 不匹配", name)
		}
		listed[name] = true
	}
	if !listed["records.jsonl"] || !listed["manifest.json"] {
		return rep, errors.New("SHA256SUMS 未覆盖 records.jsonl 与 manifest.json")
	}
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		return rep, fmt.Errorf("manifest.json: %w", err)
	}
	rep.Manifest = m
	if m.Format != ArchiveFormat {
		return rep, fmt.Errorf("不支持的归档格式 %q", m.Format)
	}
	rs := sha256.Sum256(files["records.jsonl"])
	if hex.EncodeToString(rs[:]) != m.RecordsSHA256 {
		return rep, errors.New("records.jsonl 与 manifest.records_sha256 不一致")
	}
	var recs []ArchiveRecord
	dec := json.NewDecoder(bytes.NewReader(files["records.jsonl"]))
	for {
		var a ArchiveRecord
		if err := dec.Decode(&a); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return rep, fmt.Errorf("records.jsonl 第 %d 行: %w", len(recs)+1, err)
		}
		recs = append(recs, a)
	}
	if len(recs) != m.Count || len(recs) == 0 || recs[0].ID != m.FirstID || recs[len(recs)-1].ID != m.LastID {
		return rep, errors.New("记录条数或 ID 范围与 manifest 不一致")
	}
	first, last, chained, err := ChainSegment(recs, m.FirstPrevHash)
	if err != nil {
		return rep, err
	}
	if first != m.FirstHash || last != m.LastHash || chained != m.Chained {
		return rep, errors.New("哈希链首尾值与 manifest 不一致")
	}
	rep.Signature = "absent"
	if sig, ok := files["manifest.sig"]; ok {
		rep.Signature = "not_checked"
		if pub != nil {
			raw, err := hex.DecodeString(strings.TrimSpace(string(sig)))
			if err != nil || !ed25519.Verify(pub, files["manifest.json"], raw) {
				return rep, errors.New("manifest.sig 签名校验失败")
			}
			rep.Signature = "verified"
		}
	} else if pub != nil {
		return rep, errors.New("指定了公钥，但归档包没有 manifest.sig")
	}
	return rep, nil
}
