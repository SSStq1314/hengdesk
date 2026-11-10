import assert from 'node:assert/strict'
import { test } from 'node:test'
import { describeNotification, unreadNoticesTitle } from './notice-text.ts'

test('describeNotification 预算告警走用量模板', () => {
  const text = describeNotification(
    { kind: 'budget_alert', title: '预算告警' },
    () => '财务部本月预算已用 80%（已用 4800 token / 上限 6000 token）。'
  )
  assert.match(text, /财务部本月预算已用 80%/)
})

test('describeNotification 审批通知使用服务端标题，不套预算模板', () => {
  const title = '工具「create_ticket」待你审批'
  const text = describeNotification(
    { kind: 'approval', title },
    () => '本日预算已用 0%（已用 0 token / 上限 0 token）。'
  )
  assert.equal(text, title)
  assert.doesNotMatch(text, /预算已用/)
})

test('describeNotification 未知类型也回退到标题', () => {
  assert.equal(
    describeNotification({ kind: 'other', title: '系统通知' }, () => '预算'),
    '系统通知'
  )
  assert.equal(
    describeNotification({ kind: 'approval', title: '' }, () => '预算'),
    ''
  )
})

test('unreadNoticesTitle 按未读类型区分标题', () => {
  assert.equal(
    unreadNoticesTitle(2, ['budget_alert', 'budget_alert']),
    '有 2 条未读的预算提醒'
  )
  assert.equal(unreadNoticesTitle(1, ['approval']), '有 1 条未读的审批提醒')
  assert.equal(
    unreadNoticesTitle(3, ['budget_alert', 'approval']),
    '有 3 条未读提醒'
  )
})
