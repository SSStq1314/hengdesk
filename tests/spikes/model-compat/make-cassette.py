#!/usr/bin/env python3
"""从 DeepSeek 真实调用记录（deepseek-runs/deepseek-results.json，密钥已脱敏）生成契约测试回放带。
只保留请求体、状态码、响应体 / 流式分片；不保留任何请求头和响应头。
用法：python3 tests/spikes/model-compat/make-cassette.py
重新录制：按 tests/spikes/model-compat 的说明在本机用自己的 DEEPSEEK_API_KEY 跑探针脚本，再执行本脚本；
密钥只在本机环境变量里，不进仓库、CI、日志和回放带。"""
import json, os, re
here = os.path.dirname(os.path.abspath(__file__))
src = json.load(open(os.path.join(here, "deepseek-runs", "deepseek-results.json")))
rec = {r["case"]: r for r in src["records"]}
CASES = {
    "chat-thinking": "T-enabled/chat#1",
    "chat-no-thinking": "T-disabled/chat#1",
    "stream-thinking": "T-enabled/stream-include-usage#1",
    "stream-no-thinking": "T-disabled/stream-include-usage#1",
    "err-invalid-key": "err/invalid-key",
    "err-invalid-model": "err/invalid-model",
    "err-context-too-long": "err/context-too-long",
}
out = {}
for name, case in CASES.items():
    r = rec[case]
    c = {"source_case": case, "status": r["status"], "request_body": r["request_body"]}
    if r["stream"]:
        chunks = [r["first_chunk"]]
        if r.get("reasoning_excerpt"):  # 中间分片未逐条保存：用记录的思考摘录合成一个 reasoning 分片
            mid = json.loads(json.dumps(r["first_chunk"]))
            mid["choices"][0]["delta"] = {"content": None, "reasoning_content": r["reasoning_excerpt"]}
            chunks.append(mid)
        chunks += r["last_chunks"]
        c["chunks"], c["done_marker"] = chunks, r["done_marker"]
        c["usage"], c["finish_reason"] = r["usage"], r["finish_reason"]
    else:
        c["body"] = r["body"]
    out[name] = c
s = json.dumps(out, ensure_ascii=False, indent=1, sort_keys=True)
assert not re.search(r"sk-[A-Za-z0-9]{8,}", s), "回放带里出现疑似密钥"
dst = os.path.join(here, "..", "..", "..", "server", "internal", "gateway", "testdata", "deepseek_cassette.json")
open(dst, "w").write(s + "\n")
print("wrote", os.path.normpath(dst))
