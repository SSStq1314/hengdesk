#!/usr/bin/env python3
"""按各厂商公开文档构造 vLLM / Anthropic 契约测试回放带（D-35）。

这些回放带 **不是** 真实录制，也不是通义实测。字段形状参考公开文档，正文是构造值。
dashscope_cassette.json 已按 D-49 改为通义官方文档示例，本脚本不得覆盖它。
运行：python3 make-doc-cassettes.py  （输出到 server/internal/gateway/testdata/）
"""
import json, os

NOTE = "文档构造，待真实录制替换"
OUT = os.path.join(os.path.dirname(__file__), "../../../server/internal/gateway/testdata")


def oai_chunk(model, delta=None, finish=None, usage=None, cid="chatcmpl-doc-1"):
    c = {"id": cid, "object": "chat.completion.chunk", "created": 1793600000, "model": model,
         "choices": [] if delta is None else [{"index": 0, "delta": delta, "finish_reason": finish}]}
    if usage is not None:
        c["usage"] = usage
    return c


def oai_vendor(model, reasoning_key, thinking_req, usage, err_key, err_model, err_ctx, src, extra_final_choice=False):
    msgs = [{"role": "user", "content": "用一句话介绍你自己。"}]
    total = usage["total_tokens"]
    cases = {}
    cases["chat-thinking"] = {
        "source_case": src + "#chat-thinking", "status": 200,
        "request_body": {"model": model, "messages": msgs, **thinking_req(True)},
        "body": {"id": "chatcmpl-doc-1", "object": "chat.completion", "created": 1793600000, "model": model,
                 "choices": [{"index": 0, "finish_reason": "stop",
                              "message": {"role": "assistant", "content": "我是一个 AI 助手。", reasoning_key: "用户要一句话自我介绍。"}}],
                 "usage": usage}}
    cases["chat-no-thinking"] = {
        "source_case": src + "#chat-no-thinking", "status": 200,
        "request_body": {"model": model, "messages": msgs, **thinking_req(False)},
        "body": {"id": "chatcmpl-doc-2", "object": "chat.completion", "created": 1793600000, "model": model,
                 "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "我是一个 AI 助手。"}}],
                 "usage": usage}}
    for name, on in (("stream-thinking", True), ("stream-no-thinking", False)):
        chunks = [oai_chunk(model, {"role": "assistant", "content": ""})]
        if on:
            chunks += [oai_chunk(model, {reasoning_key: "用户要"}), oai_chunk(model, {reasoning_key: "一句话自我介绍。"})]
        chunks += [oai_chunk(model, {"content": "我是一个"}), oai_chunk(model, {"content": " AI 助手。"}),
                   oai_chunk(model, {}, "stop")]
        chunks.append(oai_chunk(model, usage=usage))
        cases[name] = {"source_case": src + "#" + name, "status": 200,
                       "request_body": {"model": model, "stream": True, "messages": msgs, **thinking_req(on)},
                       "chunks": chunks, "done_marker": True, "finish_reason": "stop",
                       "usage": {"total_tokens": total}}
    cases["err-invalid-key"] = {"source_case": src + "#err-invalid-key", "status": 401, "body": err_key}
    cases["err-invalid-model"] = {"source_case": src + "#err-invalid-model", "status": err_model[0], "body": err_model[1]}
    cases["err-context-too-long"] = {"source_case": src + "#err-context-too-long", "status": 400, "body": err_ctx}
    return cases


def dashscope():
    usage = {"prompt_tokens": 14, "completion_tokens": 30, "total_tokens": 44,
             "prompt_tokens_details": {"cached_tokens": 0}, "completion_tokens_details": {"reasoning_tokens": 12}}
    return oai_vendor(
        "qwen-plus", "reasoning_content", lambda on: {"enable_thinking": on}, usage,
        {"error": {"code": "invalid_api_key", "param": None, "type": "invalid_request_error",
                   "message": "Incorrect API key provided. "}, "request_id": "doc-req-1"},
        (404, {"error": {"code": "model_not_found", "param": None, "type": "invalid_request_error",
                         "message": "The model `qwen-nope` does not exist or you do not have access to it."}, "request_id": "doc-req-2"}),
        {"error": {"code": "invalid_parameter_error", "param": None, "type": "invalid_request_error",
                   "message": "Range of input length should be [1, 129024]"}, "request_id": "doc-req-3"},
        "dashscope-compatible-mode-docs")


def vllm():
    usage = {"prompt_tokens": 14, "completion_tokens": 30, "total_tokens": 44}
    return oai_vendor(
        "Qwen/Qwen3-8B", "reasoning", lambda on: {"chat_template_kwargs": {"enable_thinking": on}}, usage,
        {"error": {"message": "Unauthorized", "type": "Unauthorized", "code": 401}},
        (404, {"object": "error", "message": "The model `nope` does not exist.", "type": "NotFoundError", "param": None, "code": 404}),
        {"object": "error", "message": "This model's maximum context length is 32768 tokens. However, you requested 40000 tokens.",
         "type": "BadRequestError", "param": None, "code": 400},
        "vllm-openai-server-docs")


def anthropic():
    model = "claude-sonnet-4-5"
    msgs = [{"role": "user", "content": "用一句话介绍你自己。"}]
    usage = {"input_tokens": 20, "output_tokens": 40, "cache_read_input_tokens": 5, "cache_creation_input_tokens": 0}
    def body(thinking):
        content = []
        if thinking:
            content.append({"type": "thinking", "thinking": "用户要一句话自我介绍。", "signature": "doc-signature"})
        content.append({"type": "text", "text": "我是 Claude，一个 AI 助手。"})
        return {"id": "msg_doc_1", "type": "message", "role": "assistant", "model": model, "content": content,
                "stop_reason": "end_turn", "stop_sequence": None, "usage": usage}
    def events(thinking):
        ev = [("message_start", {"type": "message_start", "message": {"id": "msg_doc_2", "type": "message", "role": "assistant",
              "model": model, "content": [], "stop_reason": None,
              "usage": {"input_tokens": 20, "output_tokens": 1, "cache_read_input_tokens": 5, "cache_creation_input_tokens": 0}}}),
              ("ping", {"type": "ping"})]
        i = 0
        if thinking:
            ev += [("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "thinking", "thinking": ""}}),
                   ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "用户要"}}),
                   ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "一句话自我介绍。"}}),
                   ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "signature_delta", "signature": "doc-signature"}}),
                   ("content_block_stop", {"type": "content_block_stop", "index": 0})]
            i = 1
        ev += [("content_block_start", {"type": "content_block_start", "index": i, "content_block": {"type": "text", "text": ""}}),
               ("content_block_delta", {"type": "content_block_delta", "index": i, "delta": {"type": "text_delta", "text": "我是 Claude，"}}),
               ("content_block_delta", {"type": "content_block_delta", "index": i, "delta": {"type": "text_delta", "text": "一个 AI 助手。"}}),
               ("content_block_stop", {"type": "content_block_stop", "index": i}),
               ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 40}}),
               ("message_stop", {"type": "message_stop"})]
        return [{"event": e, "data": d} for e, d in ev]
    def thinking_req(on):
        return {"thinking": {"type": "enabled", "budget_tokens": 2048}} if on else {}
    def err(t, m):
        return {"type": "error", "error": {"type": t, "message": m}}
    src = "anthropic-messages-api-docs"
    cases = {}
    for name, on in (("chat-thinking", True), ("chat-no-thinking", False)):
        cases[name] = {"source_case": src + "#" + name, "status": 200,
                       "request_body": {"model": model, "max_tokens": 4096, "messages": msgs, **thinking_req(on)}, "body": body(on)}
    for name, on in (("stream-thinking", True), ("stream-no-thinking", False)):
        cases[name] = {"source_case": src + "#" + name, "status": 200,
                       "request_body": {"model": model, "max_tokens": 4096, "stream": True, "messages": msgs, **thinking_req(on)},
                       "events": events(on), "finish_reason": "stop", "usage": {"total_tokens": 65}}
    cases["stream-overloaded"] = {"source_case": src + "#stream-overloaded", "status": 200,
                                  "request_body": {"model": model, "stream": True},
                                  "events": events(False)[:3] + [{"event": "error", "data": err("overloaded_error", "Overloaded")}]}
    cases["err-invalid-key"] = {"source_case": src + "#err-invalid-key", "status": 401, "body": err("authentication_error", "invalid x-api-key")}
    cases["err-invalid-model"] = {"source_case": src + "#err-invalid-model", "status": 404, "body": err("not_found_error", "model: claude-nope")}
    cases["err-context-too-long"] = {"source_case": src + "#err-context-too-long", "status": 400,
                                     "body": err("invalid_request_error", "prompt is too long: 210000 tokens > 200000 maximum")}
    cases["err-overloaded"] = {"source_case": src + "#err-overloaded", "status": 529, "body": err("overloaded_error", "Overloaded")}
    return cases


for vendor, fn in (("vllm", vllm), ("anthropic", anthropic)):
    data = {
        "_note": NOTE,
        "_source": "按公开文档构造，非真实录制",
        "_claim": "constructed_not_a_live_call_not_tongyi",
        "cases": fn(),
    }
    with open(os.path.join(OUT, vendor + "_cassette.json"), "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print("wrote", vendor)
dash = os.path.join(OUT, "dashscope_cassette.json")
print("skipped dashscope (D-49 official examples only):", dash)
