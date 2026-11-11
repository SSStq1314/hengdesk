#!/usr/bin/env bash
# D-49：在官方 vLLM CPU 镜像上做 OpenAI 兼容协议冒烟（流式、usage）。
# 这是 vLLM，不是通义实测，也不使用 Ollama 代替。
# 需要 Docker 与出网（拉镜像和公开模型权重），不进入断网 CI。
# 用法：./tests/acceptance/vllm-cpu-smoke.sh
# 环境变量：VLLM_IMAGE VLLM_MODEL VLLM_PORT VLLM_TIMEOUT VLLM_EVIDENCE_DIR
# 退出码：0 协议冒烟通过；1 跑了但未通过；2 缺少 docker / 架构不支持。
set -euo pipefail

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) TAG_ARCH=x86_64 ;;
  aarch64|arm64) TAG_ARCH=arm64 ;;
  *) echo "不支持的架构：$ARCH（需要 x86_64 或 arm64）" >&2; exit 2 ;;
esac

IMAGE="${VLLM_IMAGE:-vllm/vllm-openai-cpu:latest-${TAG_ARCH}}"
# 先试约 0.5B。启动失败时再试更小的公开模型，两次都走同一个 vLLM 镜像。
PRIMARY_MODEL="${VLLM_MODEL:-Qwen/Qwen2.5-0.5B-Instruct}"
FALLBACK_MODEL="${VLLM_FALLBACK_MODEL:-HuggingFaceTB/SmolLM2-135M-Instruct}"
PORT="${VLLM_PORT:-18000}"
TIMEOUT="${VLLM_TIMEOUT:-900}"
NAME="${VLLM_CONTAINER:-hengdesk-vllm-cpu-smoke}"
EVID="${VLLM_EVIDENCE_DIR:-/tmp/vllm-cpu-smoke-evidence}"

mkdir -p "$EVID"
exec > >(tee -a "$EVID/script.log") 2>&1

echo "==> $(date -u +%Y-%m-%dT%H:%M:%SZ) vLLM CPU 协议冒烟"
echo "声明：本记录是 vLLM CPU 服务的协议冒烟，不是通义（DashScope）真实调用，也不用 Ollama 冒充 vLLM。"
{
  echo "date_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "arch=$ARCH"
  uname -a || true
  echo "nproc=$(nproc || true)"
  free -h || true
  df -h || true
  lscpu | sed -n '1,40p' || true
  docker version || true
} > "$EVID/host.txt"

if ! command -v docker >/dev/null 2>&1; then
  echo "未找到 docker，无法拉起官方 CPU 镜像。本次记为未实跑。" >&2
  echo "result=not_run reason=no_docker" | tee "$EVID/result.txt"
  exit 2
fi
if ! command -v curl >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
  echo "需要 curl 和 python3" >&2
  exit 2
fi

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

redact() {
  sed -E 's/(Authorization: Bearer )[^[:space:]]+/\1[REDACTED]/Ig; s/(api[_-]?key["=: ]+)[^"[:space:]]+/\1[REDACTED]/Ig'
}

echo "==> 拉取镜像 $IMAGE"
if ! docker pull "$IMAGE" 2>&1 | redact | tee "$EVID/docker-pull.log"; then
  echo "result=unverified reason=image_pull_failed" | tee "$EVID/result.txt"
  exit 1
fi
docker image inspect "$IMAGE" --format 'Id={{.Id}} Digests={{json .RepoDigests}} Size={{.Size}} Created={{.Created}}' \
  | tee "$EVID/image.txt"

start_one() {
  local model="$1"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  echo "==> 启动 model=$model dtype=float32 max-model-len=512 enforce-eager"
  # CPU 兼容：bfloat16 在无 GPU 的 vLLM CPU 镜像上经常直接拒绝，故用 float32。
  # 这仍是 vLLM OpenAI 服务，不是换成别的推理引擎。
  local mem_kb
  local -a mem_args=()
  mem_kb=$(awk '/MemTotal:/ {print $2}' /proc/meminfo)
  # 给宿主机留约 2 GiB。硬限制高于可用内存时 Docker 会拒绝创建容器。
  if [ "$((mem_kb / 1024))" -gt 4096 ]; then
    local limit_mb=$((mem_kb / 1024 - 2048))
    [ "$limit_mb" -gt 8192 ] && limit_mb=8192
    mem_args=(--memory="${limit_mb}m")
    echo "容器内存上限 ${limit_mb}m（MemTotal=${mem_kb}kB）"
  else
    echo "MemTotal=${mem_kb}kB，不设 --memory，若被 OOM killer 结束则记为资源不足"
  fi
  docker run -d --name "$NAME" \
    --shm-size=1g \
    "${mem_args[@]}" \
    -p "127.0.0.1:${PORT}:8000" \
    -e VLLM_CPU_KVCACHE_SPACE="${VLLM_CPU_KVCACHE_SPACE:-1}" \
    -e HF_HUB_DISABLE_TELEMETRY=1 \
    -e HF_HUB_ENABLE_HF_TRANSFER=0 \
    "$IMAGE" \
    --model "$model" \
    --max-model-len 512 \
    --max-num-seqs 1 \
    --dtype float32 \
    --enforce-eager \
    | tee "$EVID/container-id-${model//\//_}.txt"
  echo "$model" > "$EVID/attempted-model.txt"
  local ok=0
  for _ in $(seq 1 "$TIMEOUT"); do
    if curl -fsS "http://127.0.0.1:${PORT}/v1/models" > "$EVID/models.json" 2>/dev/null; then
      ok=1
      break
    fi
    sleep 1
  done
  if [ "$ok" != 1 ]; then
    echo "模型 $model 在 ${TIMEOUT}s 内未就绪" | tee -a "$EVID/start-failures.txt"
    docker logs --tail 200 "$NAME" 2>&1 | redact > "$EVID/logs-${model//\//_}.txt" || true
    return 1
  fi
  return 0
}

MODEL=""
if start_one "$PRIMARY_MODEL"; then
  MODEL="$PRIMARY_MODEL"
elif [ "$FALLBACK_MODEL" != "$PRIMARY_MODEL" ] && start_one "$FALLBACK_MODEL"; then
  MODEL="$FALLBACK_MODEL"
  echo "主模型未就绪，已改用同一 vLLM 镜像上的更小公开模型：$FALLBACK_MODEL" | tee "$EVID/fallback.txt"
else
  echo "result=unverified reason=server_not_ready" | tee "$EVID/result.txt"
  exit 1
fi
echo "model=$MODEL" | tee "$EVID/model.txt"
docker logs --tail 200 "$NAME" 2>&1 | redact > "$EVID/logs-ready.txt" || true

echo "==> 非流式 chat/completions"
code=$(curl -sS -o "$EVID/nonstream.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"只用一个词回答：ping\"}],\"max_tokens\":16}" \
  "http://127.0.0.1:${PORT}/v1/chat/completions")
echo "nonstream_http=$code" | tee "$EVID/nonstream.status"
if [ "$code" != 200 ]; then
  echo "非流式失败：HTTP $code" >&2
  echo "result=fail reason=nonstream_http" | tee "$EVID/result.txt"
  exit 1
fi
python3 - "$MODEL" "$EVID/nonstream.json" << 'PY'
import json, sys
model, path = sys.argv[1], sys.argv[2]
d = json.load(open(path))
choice = (d.get("choices") or [{}])[0]
msg = choice.get("message") or {}
content = msg.get("content") or ""
if d.get("object") != "chat.completion":
    raise SystemExit(f"object 异常：{d.get('object')}")
if not str(content).strip():
    raise SystemExit("非流式 content 为空")
usage = d.get("usage") or {}
total = usage.get("total_tokens") or 0
if not total and not (usage.get("prompt_tokens") or usage.get("completion_tokens")):
    raise SystemExit(f"非流式缺少 usage：{usage}")
print(f"非流式通过：content_len={len(str(content))} usage={usage}")
PY

echo "==> 流式 chat/completions（stream_options.include_usage）"
code=$(curl -sS -o "$EVID/stream.sse" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"只用一个词回答：pong\"}],\"max_tokens\":16,\"stream\":true,\"stream_options\":{\"include_usage\":true}}" \
  "http://127.0.0.1:${PORT}/v1/chat/completions")
echo "stream_http=$code" | tee "$EVID/stream.status"
if [ "$code" != 200 ]; then
  echo "流式失败：HTTP $code" >&2
  echo "result=fail reason=stream_http" | tee "$EVID/result.txt"
  exit 1
fi
if ! grep -q 'data:' "$EVID/stream.sse"; then
  echo "流式响应不含 data: 行" >&2
  echo "result=fail reason=stream_no_data" | tee "$EVID/result.txt"
  exit 1
fi
if ! grep -q '\[DONE\]' "$EVID/stream.sse"; then
  echo "流式响应未以 [DONE] 结束" >&2
  echo "result=fail reason=stream_no_done" | tee "$EVID/result.txt"
  exit 1
fi
python3 - "$EVID/stream.sse" << 'PY'
import json, sys
text = open(sys.argv[1], encoding="utf-8", errors="replace").read()
saw_usage = False
saw_content = False
for line in text.splitlines():
    if not line.startswith("data:"):
        continue
    data = line[len("data:"):].strip()
    if data == "[DONE]":
        continue
    try:
        obj = json.loads(data)
    except json.JSONDecodeError:
        continue
    usage = obj.get("usage") or {}
    if usage.get("total_tokens") or usage.get("prompt_tokens") or usage.get("completion_tokens"):
        saw_usage = True
    for ch in obj.get("choices") or []:
        delta = ch.get("delta") or {}
        if delta.get("content"):
            saw_content = True
if not saw_content:
    raise SystemExit("流式增量 content 为空")
if not saw_usage:
    raise SystemExit("已请求 include_usage，但流里没有 usage")
print("流式通过：含 data:、[DONE]、增量 content 与 usage")
PY

{
  echo "result=pass"
  echo "image=$IMAGE"
  echo "model=$MODEL"
  echo "endpoint=http://127.0.0.1:${PORT}/v1"
  echo "not_tongyi=true"
  echo "not_ollama=true"
  echo "finished_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} | tee "$EVID/result.txt"
echo "vLLM CPU 协议冒烟通过。这不是通义实测。"
