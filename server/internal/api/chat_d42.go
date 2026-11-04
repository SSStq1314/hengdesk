package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
)

// D-42 对话侧：检索失败 / 无资料 / 仅依据资料回答。

// kbStrictRefusal 开启「仅依据资料回答」且没有可用资料时的固定回复（不调用模型）。
const kbStrictRefusal = "所选知识库中没有找到与问题相关的资料。该知识库设置为「仅依据资料回答」，因此本次不作答。" +
	"可以换个问法、确认资料已上传并就绪，或联系知识库管理员。"

const kbStrictSkipMsg = "所选知识库开启了「仅依据资料回答」，不能跳过知识库作答"

// kbSkippedPrompt 用户在检索失败后选择「本次不使用知识库继续」时的系统提示。
const kbSkippedPrompt = "本次回答没有检索知识库（知识库暂时不可用，用户选择不使用知识库继续）。请基于通用知识回答，并说明该回答不来自知识库。"

// strictKBs 会话绑定的知识库中开启了「仅依据资料回答」的库（同组织、未删除）。
// 不按当前用户权限过滤：失去权限的库同样不能被绕过成通用回答。
func (h *convHandlers) strictKBs(ctx context.Context, p *auth.Principal, ids []int) ([]int, error) {
	return h.db.KnowledgeBase.Query().Where(knowledgebase.IDIn(ids...), knowledgebase.OrgID(p.OrgID),
		knowledgebase.DeletedAtIsNil(), knowledgebase.StrictGrounding(true)).IDs(ctx)
}

// refuseStrict 不调用模型，直接以固定回复结束本轮：落库、发 done 事件、写 result 审计。
func (h *convHandlers) refuseStrict(c echo.Context, cv *ent.Conversation, asst *ent.Message, action, notice string,
	strict []int, kbInfo, extra map[string]any) error {
	bg := context.WithoutCancel(c.Request().Context())
	if err := h.db.Message.UpdateOneID(asst.ID).SetStatus(message.StatusDone).SetContent(kbStrictRefusal).
		SetModelName("").Exec(bg); err != nil {
		return err
	}
	_ = h.db.Conversation.UpdateOneID(cv.ID).SetUpdatedAt(asst.CreatedAt).Exec(bg)
	detail := map[string]any{"message_id": asst.ID, "kb_strict_refused": true, "kb_strict_ids": strict, "model_called": false}
	for k, v := range kbInfo {
		detail[k] = v
	}
	for k, v := range extra {
		detail[k] = v
	}
	if err := audit.Record(c, audit.Entry{Action: action, Phase: "result", Outcome: "ok", ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: detail}); err != nil {
		h.ch.log.Error("chat result audit write failed", "conversation", cv.ID)
	}
	w := c.Response()
	w.Header().Set(echo.HeaderContentType, "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	b, _ := json.Marshal(msgView{ID: asst.ID, Role: "assistant", Content: kbStrictRefusal, Status: "done",
		CreatedAt: asst.CreatedAt, Notice: notice})
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", EventToken, mustJSON(map[string]string{"content": kbStrictRefusal}))
	_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", b)
	w.Flush()
	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
