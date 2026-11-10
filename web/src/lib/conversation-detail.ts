// 会话详情接口约定：GET /conversations/:id 返回嵌套对象
// {conversation, messages, files, aigc}，调用方按扁平的 Conversation 加 messages/files 使用。
import type { ChatMessage, Conversation, ConvFile } from './api'

export type ConversationAIGC = { badge: string; suffix: string }

export type ConversationGetResponse = {
  conversation: Conversation
  messages: ChatMessage[]
  files: ConvFile[]
  aigc?: ConversationAIGC
}

export type ConversationDetail = Conversation & {
  messages: ChatMessage[]
  files: ConvFile[]
  aigc?: ConversationAIGC
}

// unwrapConversationDetail 把后端嵌套结构展平。原先按扁平字段读时，
// agent_id / opening / agent_readonly 等会全部丢失。
export function unwrapConversationDetail(
  r: ConversationGetResponse
): ConversationDetail {
  return {
    ...r.conversation,
    messages: r.messages ?? [],
    files: r.files ?? [],
    aigc: r.aigc,
  }
}
