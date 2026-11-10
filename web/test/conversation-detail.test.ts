import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  unwrapConversationDetail,
  type ConversationGetResponse,
} from '../src/lib/conversation-detail.ts'

const conversation = {
  id: 42,
  title: '差旅咨询',
  model_id: 7,
  kb_ids: [3],
  tool_ids: [8, 9],
  agent_id: 15,
  agent_debug: false,
  agent_name: '财务助手',
  opening: '我是财务助手，可以帮你查制度、建工单。',
  agent_readonly: true,
  created_at: '2026-11-09T00:00:00Z',
  updated_at: '2026-11-09T00:00:00Z',
}

test('unwrapConversationDetail 展平嵌套的 conversation 字段', () => {
  const r: ConversationGetResponse = {
    conversation,
    messages: [
      {
        id: 1,
        role: 'user',
        content: '你好',
        status: 'done',
        prompt_tokens: 0,
        completion_tokens: 0,
        created_at: '2026-11-09T00:00:00Z',
      },
    ],
    files: [],
    aigc: { badge: 'AI 生成', suffix: '（本内容由 AI 生成）' },
  }
  const got = unwrapConversationDetail(r)
  assert.equal(got.id, 42)
  assert.equal(got.agent_id, 15)
  assert.equal(got.agent_name, '财务助手')
  assert.equal(got.opening, conversation.opening)
  assert.equal(got.agent_readonly, true)
  assert.equal(got.model_id, 7)
  assert.deepEqual(got.kb_ids, [3])
  assert.deepEqual(got.tool_ids, [8, 9])
  assert.equal(got.messages.length, 1)
  assert.equal(got.messages[0].content, '你好')
  assert.deepEqual(got.files, [])
  assert.deepEqual(got.aigc, r.aigc)
})

test('unwrapConversationDetail 缺省 messages/files 时给空数组', () => {
  const got = unwrapConversationDetail({
    conversation,
    messages: undefined as unknown as ConversationGetResponse['messages'],
    files: undefined as unknown as ConversationGetResponse['files'],
  })
  assert.deepEqual(got.messages, [])
  assert.deepEqual(got.files, [])
  assert.equal(got.agent_id, 15)
})

test('unwrapConversationDetail 不会把外层空字段当成会话属性', () => {
  const flatMisread = {
    id: undefined,
    agent_id: undefined,
    opening: undefined,
    messages: [],
    files: [],
  }
  // 对照：若按扁平结构读取，智能体标识会全部丢失。
  assert.equal(flatMisread.agent_id, undefined)
  const got = unwrapConversationDetail({
    conversation,
    messages: [],
    files: [],
  })
  assert.notEqual(got.agent_id, undefined)
  assert.equal(got.agent_id, conversation.agent_id)
})
