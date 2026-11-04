import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { Wrench } from 'lucide-react'
import { chatApi } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'

const t = zh.chat
const MAX = 20

// ToolPicker 对话的工具多选（最多 20 个）。列表只含本人当前可用的工具；
// 每次调用前服务端还会重新判定策略与 ACL，这里只是选择入口。
export function ToolPicker(props: {
  value: number[]
  onChange: (ids: number[]) => void
  disabled?: boolean
}) {
  const list = useQuery({ queryKey: ['my-tools'], queryFn: chatApi.tools })
  const items = list.data?.items ?? []
  const sel = new Set(props.value)
  const toggle = (id: number) => {
    if (sel.has(id)) props.onChange(props.value.filter((x) => x !== id))
    else if (props.value.length < MAX) props.onChange([...props.value, id])
  }
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          size='sm'
          variant={props.value.length ? 'secondary' : 'outline'}
          disabled={props.disabled}
          aria-label={t.toolPick}
        >
          <Wrench className='size-4' />
          {props.value.length ? t.toolCount(props.value.length) : t.toolPick}
        </Button>
      </PopoverTrigger>
      <PopoverContent className='w-72 p-2' align='start'>
        {items.length === 0 ? (
          <p className='p-2 text-sm text-muted-foreground'>{t.toolEmptyList}</p>
        ) : (
          <ul className='max-h-64 overflow-y-auto'>
            {items.map((k) => (
              <li key={k.id}>
                <label className='flex cursor-pointer items-start gap-2 rounded px-2 py-1.5 text-sm hover:bg-accent'>
                  <Checkbox
                    checked={sel.has(k.id)}
                    disabled={!sel.has(k.id) && props.value.length >= MAX}
                    onCheckedChange={() => toggle(k.id)}
                  />
                  <span className='min-w-0 flex-1'>
                    <span className='block truncate'>{k.name}</span>
                    <span className='block truncate text-xs text-muted-foreground'>
                      {k.server_name} · {zh.toolRisk[k.risk_level]} ·{' '}
                      {zh.toolMode[k.exec_mode]}
                    </span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
        )}
        <div className='flex items-center border-t px-2 pt-2 text-xs text-muted-foreground'>
          <span>{t.toolMax}</span>
          {props.value.length > 0 && (
            <button
              className='ms-auto hover:text-foreground'
              onClick={() => props.onChange([])}
            >
              {t.toolNone}
            </button>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
