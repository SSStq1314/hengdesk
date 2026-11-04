import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { BookOpen } from 'lucide-react'
import { kbApi } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'

const t = zh.chat
const MAX = 5

// KBPicker 对话的知识库多选（最多 5 个）。列表只含本人可查看的知识库；
// 发送时服务端还会按 ACL 重新过滤，这里只是选择入口。
export function KBPicker(props: {
  value: number[]
  onChange: (ids: number[]) => void
  disabled?: boolean
}) {
  const list = useQuery({ queryKey: ['kbs'], queryFn: kbApi.list })
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
          aria-label={t.kbPick}
        >
          <BookOpen className='size-4' />
          {props.value.length ? t.kbCount(props.value.length) : t.kbPick}
        </Button>
      </PopoverTrigger>
      <PopoverContent className='w-72 p-2' align='start'>
        {items.length === 0 ? (
          <p className='p-2 text-sm text-muted-foreground'>{t.kbEmptyList}</p>
        ) : (
          <ul className='max-h-64 overflow-y-auto'>
            {items.map((k) => (
              <li key={k.id}>
                <label className='flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-accent'>
                  <Checkbox
                    checked={sel.has(k.id)}
                    disabled={!sel.has(k.id) && props.value.length >= MAX}
                    onCheckedChange={() => toggle(k.id)}
                  />
                  <span className='truncate'>{k.name}</span>
                </label>
              </li>
            ))}
          </ul>
        )}
        <div className='flex items-center border-t px-2 pt-2 text-xs text-muted-foreground'>
          <span>{t.kbMax}</span>
          {props.value.length > 0 && (
            <button
              className='ms-auto hover:text-foreground'
              onClick={() => props.onChange([])}
            >
              {t.kbNone}
            </button>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
