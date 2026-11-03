# Spike 1：Go 文档解析库对比（PDF / docx / xlsx / pptx / md / txt）

日期：2026-11-02（America/Los_Angeles）　状态：**已实测（样本规模小，见局限）**

## 1. 结论（先说结论）

| 格式 | 推荐方案 | 许可 | 理由 |
|---|---|---|---|
| PDF（主力） | **go-pdfium（WebAssembly 模式，无 cgo）** | MIT（PDFium 本体 BSD-3/Apache-2.0） | 中英文、表格行、多栏顺序在样本上全部正确；对坏文件只返回错误，不 panic；代价是冷启动约 2.8 s、每页约 1.5–3 ms（v1.21.4 的数据；锁定的 v1.19.8 每页约 2.7–7 ms，见第 7 节） |
| PDF（备选/降级） | **ledongthuc/pdf，`GetPlainText` 模式** | BSD-3-Clause | 纯 Go、快（约 1.1–1.8 ms/页），中英文字符全部正确；但**表格行被打散（0/4 行保留）**；`GetTextByRow` 模式会把英文单词拆成单个字母（词召回只有 0.24），**不能用** |
| PDF | ~~pdfcpu~~ | Apache-2.0 | **不是文本抽取库**：`ExtractContent` 只导出原始内容流（`<0102…>Tj` 这类 CID 字节），见 `pdfcpu-probe.txt`。适合做校验、拆分、合并、加密处理，不适合抽文本 |
| PDF | ~~go-fitz~~ | **AGPL-3.0**（已核对仓库 COPYING） | 违反许可约束，排除 |
| PDF | ~~unidoc/unipdf~~ | **商业 EULA，需要 license key**（已核对 LICENSE.md） | 排除 |
| docx | **自研：标准库 `archive/zip` + `encoding/xml`** | 无第三方依赖 | 标题、段落、表格（结构化 Rows）都正确保留，约 0.25 ms/文件 |
| pptx | **自研：标准库** | 无第三方依赖 | 按 slideN 数字顺序输出，每张幻灯片的文本正确 |
| xlsx | **xuri/excelize v2** | BSD-3-Clause | 每个工作表输出为结构化表格 |
| md/txt | 标准库 | — | 原样输出，按空行分段 |

**可插拔接口**见 `parser/parser.go`：`Parser{Name, Supports(ext), Parse(ctx, io.ReaderAt, size) (*Document, error)}`；`Document.Blocks` 区分 heading / paragraph / table（`Rows [][]string`）/ page_text，并带页码。`Registry.Parse` 按注册顺序尝试，**首个成功者胜出**，因此 PDF 可以注册为 `pdfium → ledongthuc-plain` 两级降级。

## 2. 样本与方法

- 样本由 `samples/gen_samples.py` 生成 Flat ODF，再用 LibreOffice 转换为 docx / pdf / xlsx / pptx（该脚本只是**测试工具**，不属于产品代码）。中文字体 Noto Serif CJK SC 已嵌入（`pdffonts` 确认是 Type1 子集 + ToUnicode）。
- 每种语言（zh、en）各一份：标题、3 段正文、4×4 表格、**两栏**区块（左栏 / 右栏各一段）；pptx 两页；xlsx 一个表；大样本 `*_big.pdf` 把正文重复 120 次（zh 37 页、en 35 页），用于测速度。
- 指标（`cmd/bench`）：
  - **字符召回 / 精度**：去掉空白后，对真值与输出做 LCS（最长公共子序列）。
  - **词召回（仅英文）**：真值单词以完整 token 形式出现的比例，用来发现"单词被拆成字母"的问题。
  - **多栏顺序**：左栏、右栏各自的完整段落是否作为连续子串出现（即没有按行把两栏交错拼接）。
  - **表格行保留**：表格每一行的单元格是否按顺序出现在**同一输出行**里。
  - **耗时**：重复 5 次取均值；单进程、单实例；机器 8 vCPU。
- 原始结果：`results.json`、`bench.txt`；坏文件测试：`results-broken.json`、`bench-broken.txt`。

## 3. 实测结果（全部为【实测】）（第 3 节为 go1.26 + go-pdfium v1.21.4 的原始数据，备份在 `_go126-reference/`；当前的 results.json 和 bench.txt 已换成 Go 1.25 锁定版本的结果）

| 文件 | 解析器 | 字符召回 | 词召回 | 多栏顺序 | 表格行 | 结构化表格 | 耗时 ms |
|---|---|---|---|---|---|---|---|
| zh_report.pdf | ledongthuc-plain | 1.000 | — | ✔ | **0/4** | ✘ | 0.45 |
| zh_report.pdf | ledongthuc-rows | 1.000 | — | ✔ | 4/4 | ✘ | 0.45 |
| zh_report.pdf | pdfium-wasm | 1.000 | — | ✔ | 4/4 | ✘ | 5.28 |
| en_report.pdf | ledongthuc-plain | 1.000 | 1.000 | ✔ | **0/4** | ✘ | 0.57 |
| en_report.pdf | ledongthuc-rows | 1.000 | **0.238** | ✔ | 4/4 | ✘ | 0.66 |
| en_report.pdf | pdfium-wasm | 1.000 | 1.000 | ✔ | 4/4 | ✘ | 1.91 |
| zh/en_slides.pdf | 三种都 | 1.000 | 1.000 / 1.000 / 0.516（rows） | — | — | — | 0.4–3.1 |
| zh/en_report.docx | docx-stdlib | 1.000 | 1.000 | ✔ | 4/4 | ✔ | 0.24–0.49 |
| zh/en_slides.pptx | pptx-stdlib | 1.000 | 1.000 | — | — | — | 0.20 |
| zh/en_table.xlsx | excelize | 1.000 | n/a | — | 4/4 | ✔ | 0.66–0.71 |
| zh/en md、txt | text | 1.000 | 1.000 | ✔ | 4/4 | ✘ | ≤0.01 |

吞吐（大样本，【实测】）：

| 文件 | ledongthuc-plain | ledongthuc-rows | pdfium-wasm |
|---|---|---|---|
| zh_big.pdf（37 页） | 41.0 ms（1.11 ms/页） | 38.9 ms | 54.2 ms（1.47 ms/页） |
| en_big.pdf（35 页） | 63.6 ms（1.82 ms/页） | 54.6 ms | 104.3 ms（2.98 ms/页） |
| zh_big.docx | docx-stdlib 20.1 ms（约 40.8k 字） | | |

其他实测：
- pdfium-wasm **冷启动**（wazero 编译 wasm）：整个进程约 2.8 s → 应当在进程启动时初始化实例池并复用。bench 可执行文件 21.7 MB（含 PDFium wasm）。
- 截断的 PDF / docx：三种 PDF 解析器都返回 error，没有 panic（ledongthuc 已用 `recover` 兜底，因为社区报告过它在异常 PDF 上会 panic——该报告**本次未复现**）。

## 4. 局限（未测，必须在 M1 补）

1. 样本都由 LibreOffice 生成，内容流天然按阅读顺序排列，所以"多栏顺序"对三种解析器都偏乐观。**Word 导出、LaTeX 双栏、扫描件、带旋转或页眉页脚的真实 PDF 都未测**。建议 M1 收集 20–50 份真实业务文档（公文、合同、论文）回归。
2. **扫描件 PDF 没有文本层**，需要 OCR，不在本 spike 范围内（D-24 要求不引入 Python 服务；可选方案：外部 OCR API，或 PaddleOCR 以独立推理服务形式部署，这需要再做决策）。
3. PDF 表格只做到"行文本保留"，**没有恢复成结构化表格**；如果需要表格结构，可以基于 pdfium 的字符坐标（`GetPageTextStructured`）自己做列聚类——工作量估计为中等，未验证。
4. docx 未覆盖：合并单元格、文本框、脚注、修订记录、嵌入对象、`.doc` 老格式（老格式需转换服务，可考虑 LibreOffice headless 作为独立容器，许可为 MPL-2.0）。
5. 样本只有 2 份 × 6 种格式，字符召回 1.0 不能代表真实世界的准确率。

## 5. 依赖与许可（`go.mod`）

| 依赖 | 版本 | 许可 |
|---|---|---|
| github.com/klippa-app/go-pdfium | **v1.19.8**（已锁定；第 3、5 节的原始数据来自 v1.21.4） | MIT（核对 GitHub API）；内含 PDFium wasm（BSD-3 / Apache-2.0） |
| github.com/tetratelabs/wazero（go-pdfium 间接依赖） | — | Apache-2.0 |
| github.com/ledongthuc/pdf | v0.0.0-20261020125853 | BSD-3-Clause（核对 GitHub API） |
| github.com/pdfcpu/pdfcpu（仅用于探测） | v0.16.4 | Apache-2.0（核对 GitHub API） |
| github.com/xuri/excelize/v2 | **v2.11.1-0.20260915055537-22f76f9acb94**（已锁定；原来是 v2.11.1） | BSD-3-Clause（核对 LICENSE） |
| golang.org/x/*、google/uuid、jolestar/go-commons-pool | — | BSD-3 / BSD-3 / Apache-2.0 |

✅ 版本冲突已经处理：模块已改为 go 1.25.0 并锁定可用版本，见第 7 节。pdfcpu 已排除，它的 probe 移到 `_excluded/`，不再参与构建。

## 6. 复现

```bash
cd pdf-docx
python3 samples/gen_samples.py   # 在 samples/ 下执行；随后 soffice --headless --convert-to docx|pdf|xlsx|pptx
go run ./cmd/bench -dir eval      # 输出 bench.txt / results.json
go run ./cmd/pdfcpuprobe eval/zh_report.pdf
```

## 7. Go 1.25 兼容版本锁定（2026-11-02 实测，工具链 go1.25.14）

### 7.1 怎么选的版本
逐个读取 proxy.golang.org 上每个版本 `.mod` 文件里的 go 指令：

| 库 | 最后一个支持 go 1.25 的版本 | 第一个要求 go 1.26 的版本 |
|---|---|---|
| go-pdfium | **v1.19.8**（go 1.25.0） | v1.20.0 |
| excelize 正式 tag | v2.11.0（go 1.25.0） | v2.11.1 |
| excelize 提交 | **22f76f9acb94**（2026-09-15，go 1.25.0） | f0b1c24ee69c（2026-09-16，"Breaking changes: Go 1.26.0 and later required"） |
| golang.org/x/net | v0.58.0 | v0.60.0 |
| golang.org/x/crypto | v0.55.0 | v0.56.0 |
| golang.org/x/text | v0.41.0 | — |

**推荐锁定版本：**
- `github.com/klippa-app/go-pdfium v1.19.8`
- `github.com/xuri/excelize/v2 v2.11.1-0.20260915055537-22f76f9acb94`：这是一个伪版本，指向 excelize 在 go 1.26 升级前的最后一个提交。和 v2.11.0 相比，它多修复了 3 个**代码可达**的漏洞。
- `golang.org/x/net v0.58.0`、`golang.org/x/crypto v0.55.0`、`golang.org/x/text v0.41.0`
- 其余间接依赖由 tidy 选定（wazero v1.12.0、mscfb v1.0.8 等）。

`GOTOOLCHAIN=go1.25.14 go build/vet ./...` 全部通过，API 没有变化，代码零修改。

### 7.2 质量：与新版完全一致（实测）
用 go1.25.14 + 锁定版本重跑 eval/ 和 eval-broken/，与 go1.26 + 新版的结果（保存在 `_go126-reference/`）逐字段对比：
- 所有非耗时字段**完全一致**：CharRecall、CharPrecision、ColumnsInOrder、TableRowsKept、StructuredTable、OK 以及样本文本，差异为 0 项。
- 损坏文件：每个文件的报错行为都和原来一样，没有 panic。

### 7.3 性能：pdfium v1.19.8 慢约 2 倍（实测，每个配置跑 3 次）

| 文件 | pdfium v1.21.4（ms/页） | **pdfium v1.19.8（ms/页）** | ledongthuc（ms/页，不受影响） |
|---|---|---|---|
| zh_big.pdf（37 页） | 1.44–1.49 | **2.65–3.66** | 0.89–1.23 |
| en_big.pdf（35 页） | 2.77–3.42 | **5.7–7.8** | 1.44–1.85 |
| zh_report.pdf（1 页） | 5.28 | 5.3–6.0 | 0.4 |

- **变慢的原因是库版本，不是 Go 工具链**：用 go1.27 编译 v1.19.8，同样是 3.5 / 6.9 ms/页。两个版本内嵌的 pdfium.wasm 是不同构建（5.2 MB 对 5.8 MB），具体差在哪里**未分析**。
- 冷启动：启动进程并初始化 pdfium、不解析任何文件，约 2.0 s（单次测量）。二进制 18.9 MB。
- 结论：每页约 3–7 ms，仍远低于建议的 ≤20 ms/页基线，**可以接受**。以后升级到 Go 1.26 时，再升 go-pdfium ≥ v1.20 就能拿回这部分性能。

### 7.4 govulncheck（v1.9.0，go1.25.14，原始输出见 `govulncheck.txt`）

**代码可达（Symbol 级）的漏洞：**
- excelize v2.11.0：有 4 个可达漏洞：GO-2026-6689、6688、6684、6685。
- 换成推荐的伪版本后只剩 **1 个：GO-2026-6685**。这个漏洞是 xlsx 里写一个超大的行号，就能让 GetRows 或 Rows 迭代器"循环几天"，属于 DoS。
  - 修复提交是 01a9ff32fb3c，它要求 go 1.26。**这是 Go 1.25 下无法通过升级消除的缺口。**
  - 缓解办法（未实现）：
    - xlsx 解析放到有超时和内存上限的独立 worker 里（进程级 kill）；
    - 不用 GetRows，改用 Rows 迭代器，并设置行数和单元格数上限；
    - 解析前先在 zip 层读 `<dimension>` 和行号，超过阈值就拒绝。

**导入了但代码没有调用（Package 级）的漏洞：**
- excelize GO-2026-6695（透视表缓存 panic）、GO-2026-6687（GetSlicers 空指针），修复都需要 go 1.26；
- x/net/html GO-2026-6866、6860；
- os GO-2026-6604，只影响 Windows。

**仅在依赖模块中（Module 级）的漏洞：**
- **Go 1.25 标准库共 11 项，修复版本都只有 go1.26.9 / 1.26.10**，涉及 net/http（HTTP/2 服务端崩溃、内存耗尽、请求走私类问题）、crypto/tls、crypto/x509、mime/multipart、html/template；
- x/crypto/ssh 和 openpgp 共 3 项。

⚠ **最关键的发现**：Go 1.27 已经发布（本机有 go1.27.3），按照 Go 只维护最近两个大版本的政策，**Go 1.25 已经不再接收安全补丁**。go1.25.14 是 1.25 的最后一个补丁版本，上面这些标准库漏洞**在 1.25 上永远不会修**。本模块是解析库，不直接起 HTTP 服务，所以这些漏洞不可达；但平台的 API 网关、MCP、模型网关都依赖 net/http，**锁定 Go 1.25 意味着带着已知的 HTTP/2 DoS 类漏洞上线**。这一点建议重新评估 D-24。

govulncheck 只检查 Go 代码，**不覆盖内嵌 PDFium（C++ 编译成 wasm）的 CVE**；wasm 沙箱能降低风险，但这部分未评估。

### 7.5 复现
```bash
GOTOOLCHAIN=go1.25.14 go build -o bench ./cmd/bench
./bench -dir eval -out results.json > bench.txt
./bench -dir eval-broken -out results-broken.json > bench-broken.txt
GOTOOLCHAIN=go1.25.14 govulncheck -show verbose ./... > govulncheck.txt
```
