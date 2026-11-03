// 对话工作台（FR-14）：会话列表、流式显示、Markdown 渲染、停止生成、模型选择。
// 布局参考 shadcn-admin 的 chats 页面（MIT）。
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { Plus, Send, Square, Trash2 } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { toast } from 'sonner'
import { chatApi, sendMessage, type ChatMessage } from '@/lib/api'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { NativeSelect } from '@/features/models/fields'

const t = zh.chat

export function ChatPage() {
  const qc = useQueryClient()
  const [active, setActive] = useState<number | null>(null)
  const [model, setModel] = useState('')
  const [input, setInput] = useState('')
  const [live, setLive] = useState<ChatMessage[]>([])
  const [streaming, setStreaming] = useState(false)
  const [removing, setRemoving] = useState<number | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const endRef = useRef<HTMLDivElement>(null)

  const models = useQuery({ queryKey: ['my-models'], queryFn: chatApi.models })
  const convs = useQuery({ queryKey: ['conversations'], queryFn: chatApi.list })
  const conv = useQuery({
    queryKey: ['conversation', active],
    queryFn: () => chatApi.get(active!),
    enabled: active != null,
  })

  const modelItems = models.data?.items ?? []
  const effectiveModel =
    model ||
    modelItems.find((m) => m.id === conv.data?.model_id)?.name ||
    modelItems.find((m) => m.is_default)?.name ||
    modelItems[0]?.name ||
    ''

  const messages = [...(conv.data?.messages ?? []), ...live]
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [messages.length, live])

  const create = useMutation({
    mutationFn: () => chatApi.create({ model: effectiveModel || undefined }),
    onSuccess: (c) => {
      qc.invalidateQueries({ queryKey: ['conversations'] })
      setActive(c.id)
      setLive([])
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: (id: number) => chatApi.remove(id),
    onSuccess: (_, id) => {
      qc.invalidateQueries({ queryKey: ['conversations'] })
      if (active === id) setActive(null)
      setRemoving(null)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  async function send() {
    const content = input.trim()
    if (!content || streaming) return
    let id = active
    if (id == null) {
      const c = await chatApi.create({ model: effectiveModel || undefined })
      id = c.id
      setActive(id)
    }
    const now = new Date().toISOString()
    const base = { prompt_tokens: 0, completion_tokens: 0, created_at: now }
    setLive([
      { ...base, id: -1, role: 'user', content, status: 'done' },
      { ...base, id: -2, role: 'assistant', content: '', status: 'streaming' },
    ])
    setInput('')
    setStreaming(true)
    const ac = new AbortController()
    abortRef.current = ac
    const patch = (f: (m: ChatMessage) => ChatMessage) =>
      setLive((l) => l.map((m) => (m.id === -2 ? f(m) : m)))
    try {
      await sendMessage(
        id,
        { content, model: effectiveModel || undefined },
        {
          onToken: (tok) => patch((m) => ({ ...m, content: m.content + tok })),
          onDone: (m) => patch(() => ({ ...m, id: -2 })),
          onError: (_code, msg) =>
            patch((m) => ({ ...m, status: 'error', error: msg })),
        },
        ac.signal
      )
    } catch (e) {
      if (ac.signal.aborted) patch((m) => ({ ...m, status: 'stopped' }))
      else patch((m) => ({ ...m, status: 'error', error: String(e) }))
    } finally {
      setStreaming(false)
      abortRef.current = null
      await qc.invalidateQueries({ queryKey: ['conversation', id] })
      await qc.invalidateQueries({ queryKey: ['conversations'] })
      setLive([])
    }
  }

  return (
    <>
      <Header fixed>
        <div className='ms-auto flex items-center space-x-4'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main fixed>
        <div className='flex h-full gap-4'>
          <aside className='flex w-64 shrink-0 flex-col gap-2 border-e pe-3'>
            <Button onClick={() => create.mutate()} disabled={create.isPending}>
              <Plus className='size-4' />
              {t.newChat}
            </Button>
            <div className='flex-1 space-y-1 overflow-y-auto'>
              {(convs.data?.items ?? []).length === 0 && (
                <p className='p-2 text-sm text-muted-foreground'>{t.empty}</p>
              )}
              {(convs.data?.items ?? []).map((c) => (
                <div
                  key={c.id}
                  className={cn(
                    'group flex items-center rounded-md px-2 py-1.5 text-sm hover:bg-accent',
                    active === c.id && 'bg-accent'
                  )}
                >
                  <button
                    className='flex-1 truncate text-start'
                    onClick={() => {
                      if (streaming) return
                      setActive(c.id)
                      setModel('')
                      setLive([])
                    }}
                  >
                    {c.title || t.untitled}
                  </button>
                  <button
                    aria-label={t.remove}
                    className='text-muted-foreground opacity-0 group-hover:opacity-100'
                    onClick={() => setRemoving(c.id)}
                  >
                    <Trash2 className='size-4' />
                  </button>
                </div>
              ))}
            </div>
          </aside>
          <section className='flex min-w-0 flex-1 flex-col'>
            <div className='flex items-center gap-2 border-b pb-2'>
              <span className='text-sm text-muted-foreground'>{t.model}</span>
              {modelItems.length === 0 ? (
                <span className='text-sm text-destructive'>{t.noModel}</span>
              ) : (
                <NativeSelect
                  ariaLabel={t.model}
                  value={effectiveModel}
                  onChange={(v) => !streaming && setModel(v)}
                  options={modelItems.map((m) => ({
                    value: m.name,
                    label: m.name,
                  }))}
                />
              )}
            </div>
            <div className='flex-1 space-y-4 overflow-y-auto py-4'>
              {active == null && live.length === 0 && (
                <p className='text-center text-sm text-muted-foreground'>
                  {t.selectHint}
                </p>
              )}
              {messages.map((m) => (
                <MessageBubble key={m.id} m={m} />
              ))}
              <div ref={endRef} />
            </div>
            <div className='flex items-end gap-2 border-t pt-3'>
              <Textarea
                value={input}
                placeholder={t.placeholder}
                rows={3}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (
                    e.key === 'Enter' &&
                    !e.shiftKey &&
                    !e.nativeEvent.isComposing
                  ) {
                    e.preventDefault()
                    void send()
                  }
                }}
              />
              {streaming ? (
                <Button
                  variant='destructive'
                  onClick={() => abortRef.current?.abort()}
                >
                  <Square className='size-4' />
                  {t.stop}
                </Button>
              ) : (
                <Button
                  onClick={() => void send()}
                  disabled={!input.trim() || modelItems.length === 0}
                >
                  <Send className='size-4' />
                  {t.send}
                </Button>
              )}
            </div>
          </section>
        </div>
      </Main>
      <ConfirmDialog
        open={removing != null}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={t.removeTitle}
        desc={t.removeDesc}
        destructive
        confirmText={t.remove}
        handleConfirm={() => removing != null && remove.mutate(removing)}
      />
    </>
  )
}

function MessageBubble({ m }: { m: ChatMessage }) {
  const mine = m.role === 'user'
  return (
    <div className={cn('flex', mine ? 'justify-end' : 'justify-start')}>
      <div
        className={cn(
          'max-w-[80%] rounded-lg px-3 py-2 text-sm',
          mine ? 'bg-primary text-primary-foreground' : 'bg-muted'
        )}
      >
        {mine ? (
          <p className='whitespace-pre-wrap'>{m.content}</p>
        ) : (
          <div className='chat-md break-words'>
            {m.content ? (
              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                {m.content}
              </ReactMarkdown>
            ) : (
              m.status === 'streaming' && (
                <span className='text-muted-foreground'>{t.thinking}</span>
              )
            )}
          </div>
        )}
        {!mine && (
          <div className='mt-1 flex gap-2 text-xs text-muted-foreground'>
            {m.model_name && <span>{m.model_name}</span>}
            {m.fallback_from && <span>{t.fallback(m.fallback_from)}</span>}
            {m.status === 'stopped' && <span>{t.stopped}</span>}
            {m.status === 'error' && (
              <span className='text-destructive'>
                {t.failed(m.error || m.error_code || '')}
              </span>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
