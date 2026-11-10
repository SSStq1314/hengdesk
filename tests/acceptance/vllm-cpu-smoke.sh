#!/usr/bin/env bash
# D-47：用官方 vLLM CPU 镜像 + 约 0.5B 小模型做 OpenAI 兼容协议冒烟。
# 需 Docker 与出网（拉镜像和模型权重），不进入断网 CI 必跑门禁。
# 用法：./tests/acceptance/vllm-cpu-smoke.sh
# 可选环境变量：VLLM_IMAGE、VLLM_MODEL、VLLM_PORT、VLLM_TIMEOUT。
set -euo pipefail

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) TAG_ARCH=x86_64 ;;
  aarch64|arm64) TAG_ARCH=arm64 ;;
  *) echo "不支持的架构：$ARCH（需要 x86_64 或 arm64）" >&2; exit 2 ;;
esac

IMAGE="${VLLM_IMAGE:-vllm/vllm-openai-cpu:latest-${TAG_ARCH}}"
MODEL="${VLLM_MODEL:-Qwen/Qwen2.5-0.5B-Instruct}"
PORT="${VLLM_PORT:-18000}"
TIMEOUT="${VLLM_TIMEOUT:-600}"
NAME="${VLLM_CONTAINER:-hengdesk-vllm-cpu-smoke}"

if ! command -v docker >/dev/null 2>&1; then
  echo "未找到 docker，无法拉起官方 CPU 镜像。记录本次为未实跑。" >&2
  exit 2
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "需要 curl" >&2
  exit 2
fi

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> 镜像 $IMAGE ，模型 $MODEL ，端口 $PORT"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker pull "$IMAGE"
docker run -d --name "$NAME" \
  --shm-size=4g \
  -p "${PORT}:8000" \
  -e VLLM_CPU_KVCACHE_SPACE="${VLLM_CPU_KVCACHE_SPACE:-2}" \
  "$IMAGE" \
  --model "$MODEL" \
  --max-model-len 1024 \
  --dtype bfloat16

echo "==> 等待 /v1/models （最长 ${TIMEOUT}s）"
ok=0
for _ in $(seq 1 "$TIMEOUT"); do
  if curl -fsS "http://127.0.0.1:${PORT}/v1/models" >/tmp/vllm-models.json 2>/dev/null; then
    ok=1
    break
  fi
  sleep 1
done
if [ "$ok" != 1 ]; then
  echo "vLLM 启动超时" >&2
  docker logs --tail 80 "$NAME" >&2 || true
  exit 1
fi

echo "==> 非流式 chat/completions"
code=$(curl -sS -o /tmp/vllm-nonstream.json -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"只用一个词回答：ping\"}],\"max_tokens\":16}" \
  "http://127.0.0.1:${PORT}/v1/chat/completions")
if [ "$code" != 200 ]; then
  echo "非流式失败：HTTP $code" >&2
  cat /tmp/vllm-nonstream.json >&2 || true
  exit 1
fi
python3 - "$MODEL" << 'PY'
import json,sys
model=sys.argv[1]
d=json.load(open("/tmp/vllm-nonstream.json"))
choice=(d.get("choices") or [{}])[0]
msg=(choice.get("message") or {})
content=msg.get("content") or ""
if d.get("object")!="chat.completion":
    raise SystemExit(f"object 异常：{d.get('object')}")
if not content.strip():
    raise SystemExit("非流式 content 为空")
usage=d.get("usage") or {}
print(f"非流式通过：content_len={len(content)} usage={usage}")
PY

echo "==> 流式 chat/completions"
code=$(curl -sS -o /tmp/vllm-stream.sse -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"只用一个词回答：pong\"}],\"max_tokens\":16,\"stream\":true}" \
  "http://127.0.0.1:${PORT}/v1/chat/completions")
if [ "$code" != 200 ]; then
  echo "流式失败：HTTP $code" >&2
  cat /tmp/vllm-stream.sse >&2 || true
  exit 1
fi
if ! grep -q 'data:' /tmp/vllm-stream.sse; then
  echo "流式响应不含 data: 行" >&2
  exit 1
fi
if ! grep -q '\[DONE\]' /tmp/vllm-stream.sse; then
  echo "流式响应未以 [DONE] 结束" >&2
  exit 1
fi
echo "流式通过（含 data: 与 [DONE]）"
echo "vLLM CPU 协议冒烟通过。"
