import { Link } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { Wrench } from 'lucide-react'
import type { ChatToolCall } from '@/lib/api'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'

const t = zh.chat

// ToolCalls 回复中的工具调用卡片：状态、脱敏后的参数摘要与结果摘要；等待审批时给出审批单链接。
export function ToolCalls({ items }: { items: ChatToolCall[] }) {
  return (
    <div className='mb-2 space-y-1.5'>
      {items.map((c, i) => (
        <details
          key={`${c.id}-${i}`}
          className='rounded-md border bg-background/60 px-2 py-1 text-xs'
        >
          <summary className='flex cursor-pointer items-center gap-2'>
            <Wrench className='size-3' />
            <span className='font-medium'>{c.tool}</span>
            <Badge
              variant={
                c.status === 'failed' || c.status === 'rejected'
                  ? 'destructive'
                  : c.status === 'succeeded'
                    ? 'secondary'
                    : 'outline'
              }
              className={cn(
                'h-5',
                (c.status === 'running' || c.status === 'pending') &&
                  'animate-pulse'
              )}
            >
              {t.toolStatus[c.status] ?? c.status}
            </Badge>
            {c.approval_id ? (
              <Link
                to='/approvals'
                search={{ id: c.approval_id }}
                className='ms-auto text-primary hover:underline'
                onClick={(e) => e.stopPropagation()}
              >
                {t.viewApproval}
              </Link>
            ) : null}
          </summary>
          {c.args_summary && (
            <p className='mt-1 break-all'>
              <span className='text-muted-foreground'>{t.toolArgs}：</span>
              {c.args_summary}
            </p>
          )}
          {c.result_summary && (
            <p className='mt-1 break-all whitespace-pre-wrap'>
              <span className='text-muted-foreground'>{t.toolResult}：</span>
              {c.result_summary}
            </p>
          )}
        </details>
      ))}
    </div>
  )
}
