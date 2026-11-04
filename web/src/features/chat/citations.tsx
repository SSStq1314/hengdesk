import { zh } from '@/locales/zh-CN'
import { FileText } from 'lucide-react'
import type { ChatCitation } from '@/lib/api'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

const t = zh.chat

// Citations 助手消息下方的引用来源；序号与回答中的 [n] 对应。
export function Citations({ items }: { items: ChatCitation[] }) {
  return (
    <Collapsible className='mt-2 border-t pt-2'>
      <CollapsibleTrigger className='text-xs text-muted-foreground hover:text-foreground'>
        {t.citations}（{items.length}）
      </CollapsibleTrigger>
      <CollapsibleContent>
        <ol className='mt-1 space-y-1.5'>
          {items.map((c) => (
            <li key={c.index} className='text-xs'>
              <div className='flex items-center gap-1 font-medium'>
                <span>[{c.index}]</span>
                <FileText className='size-3' />
                <span className='truncate'>
                  {t.citationFrom(c.kb_name, c.file_name)}
                </span>
                {c.heading && (
                  <span className='truncate text-muted-foreground'>
                    · {c.heading}
                  </span>
                )}
              </div>
              <p className='line-clamp-3 text-muted-foreground'>{c.snippet}</p>
            </li>
          ))}
        </ol>
      </CollapsibleContent>
    </Collapsible>
  )
}
