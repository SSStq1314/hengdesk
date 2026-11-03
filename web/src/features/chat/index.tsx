// 对话工作台（FR-14）：会话列表、流式显示、Markdown 渲染、停止生成、模型选择；会话内上传文件（FR-15）。
// 布局参考 shadcn-admin 的 chats 页面（MIT）。
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import {
  Download,
  FileText,
  Paperclip,
  Plus,
  RotateCcw,
  Send,
  Square,
  Trash2,
  X,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { toast } from 'sonner'
import {
  chatApi,
  regenerateMessage,
  sendMessage,
  type ChatMessage,
  type ConvFile,
  type StreamHandlers,
} from '@/lib/api'
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
const ACCEPT = '.pdf,.docx,.xlsx,.pptx,.txt,.md,.markdown'

function fmtSize(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export function ChatPage() {
  const qc = useQueryClient()
  const [active, setActive] = useState<number | null>(null)
  const [model, setModel] = useState('')
  const [input, setInput] = useState('')
  const [live, setLive] = useState<ChatMessage[]>([])
  const [hidden, setHidden] = useState<number | null>(null)
  const [streaming, setStreaming] = useState(false)
  const [removing, setRemoving] = useState<number | null>(null)
  const [uploading, setUploading] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
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

  const messages = [
    ...(conv.data?.messages ?? []).filter((m) => m.id !== hidden),
    ...live,
  ]
  const lastId = messages.length ? messages[messages.length - 1].id : null
  const files = active != null ? (conv.data?.files ?? []) : []
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

  const removeFile = useMutation({
    mutationFn: (f: ConvFile) => chatApi.removeFile(active!, f.id),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['conversation', active] }),
    onError: (e: Error) => toast.error(e.message),
  })

  async function ensureConv() {
    if (active != null) return active
    const c = await chatApi.create({ model: effectiveModel || undefined })
    setActive(c.id)
    qc.invalidateQueries({ queryKey: ['conversations'] })
    return c.id
  }

  async function uploadFiles(list: FileList | File[]) {
    if (streaming || uploading) return
    const files = Array.from(list)
    if (files.length === 0) return
    let id: number
    try {
      id = await ensureConv()
    } catch (e) {
      toast.error(String((e as Error).message ?? e))
      return
    }
    for (const f of files) {
      setUploading(f.name)
      try {
        await chatApi.upload(id, f)
        toast.success(t.uploaded(f.name))
      } catch (e) {
        toast.error(t.uploadFailed(f.name, (e as Error).message))
      }
    }
    setUploading(null)
    await qc.invalidateQueries({ queryKey: ['conversation', id] })
  }

  // run 以 SSE 流式生成：发送新消息或重新生成 / 重试最后一条回复（D-40 第 6 条）。
  // hide 为被替换的旧回复 id：生成期间先在界面隐藏，结束后以服务端为准（旧回复软保留，不再显示）。
  async function run(
    id: number,
    call: (h: StreamHandlers, s: AbortSignal) => Promise<void>,
    init: ChatMessage[],
    hide: number | null
  ) {
    setLive(init)
    setHidden(hide)
    setStreaming(true)
    const ac = new AbortController()
    abortRef.current = ac
    const patch = (f: (m: ChatMessage) => ChatMessage) =>
      setLive((l) => l.map((m) => (m.id === -2 ? f(m) : m)))
    try {
      await call(
        {
          onToken: (tok) => patch((m) => ({ ...m, content: m.content + tok })),
          onDone: (m) => {
            if (m.notice) toast.info(m.notice)
            patch(() => ({ ...m, id: -2 }))
          },
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
      setHidden(null)
    }
  }

  async function send() {
    const content = input.trim()
    if (!content || streaming) return
    const id = await ensureConv()
    const now = new Date().toISOString()
    const base = { prompt_tokens: 0, completion_tokens: 0, created_at: now }
    setInput('')
    // 只有用户在下拉框里明确换了模型才传 model；否则由服务端用会话记住的模型，
    // 记住的模型不可用时回退默认模型并提示（D-40 第 8 条）。
    await run(
      id,
      (h, s) => sendMessage(id, { content, model: model || undefined }, h, s),
      [
        { ...base, id: -1, role: 'user', content, status: 'done' },
        {
          ...base,
          id: -2,
          role: 'assistant',
          content: '',
          status: 'streaming',
        },
      ],
      null
    )
  }

  async function regenerate(m: ChatMessage) {
    if (streaming || active == null) return
    const id = active
    const base = {
      prompt_tokens: 0,
      completion_tokens: 0,
      created_at: new Date().toISOString(),
    }
    await run(
      id,
      (h, s) => regenerateMessage(id, m.id, h, s),
      [
        {
          ...base,
          id: -2,
          role: 'assistant',
          content: '',
          status: 'streaming',
        },
      ],
      m.id
    )
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
          <section
            className={cn(
              'relative flex min-w-0 flex-1 flex-col',
              dragging && 'rounded-md ring-2 ring-primary'
            )}
            onDragOver={(e) => {
              if (!e.dataTransfer.types.includes('Files')) return
              e.preventDefault()
              setDragging(true)
            }}
            onDragLeave={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node))
                setDragging(false)
            }}
            onDrop={(e) => {
              e.preventDefault()
              setDragging(false)
              void uploadFiles(e.dataTransfer.files)
            }}
          >
            {dragging && (
              <div className='pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-md bg-background/80 text-sm font-medium'>
                {t.dropHere}
              </div>
            )}
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
                <MessageBubble
                  key={m.id}
                  m={m}
                  onRegenerate={
                    m.role === 'assistant' &&
                    m.id > 0 &&
                    m.id === lastId &&
                    !streaming &&
                    m.status !== 'streaming'
                      ? () => regenerate(m)
                      : undefined
                  }
                />
              ))}
              <div ref={endRef} />
            </div>
            {(files.length > 0 || uploading) && (
              <div className='flex flex-wrap gap-2 border-t pt-2'>
                {files.map((f) => (
                  <FileChip
                    key={f.id}
                    f={f}
                    href={chatApi.fileURL(active!, f.id)}
                    onRemove={() => removeFile.mutate(f)}
                  />
                ))}
                {uploading && (
                  <span className='rounded-md border px-2 py-1 text-xs text-muted-foreground'>
                    {t.uploading(uploading)}
                  </span>
                )}
              </div>
            )}
            <div className='flex items-end gap-2 border-t pt-3'>
              <input
                ref={fileRef}
                type='file'
                multiple
                accept={ACCEPT}
                className='hidden'
                onChange={(e) => {
                  if (e.target.files) void uploadFiles(e.target.files)
                  e.target.value = ''
                }}
              />
              <Button
                variant='outline'
                size='icon'
                aria-label={t.attach}
                title={t.attachHint}
                disabled={
                  streaming || uploading != null || modelItems.length === 0
                }
                onClick={() => fileRef.current?.click()}
              >
                <Paperclip className='size-4' />
              </Button>
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

function MessageBubble({
  m,
  onRegenerate,
}: {
  m: ChatMessage
  onRegenerate?: () => void
}) {
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
            {m.context_truncated && (
              <span className='text-amber-600'>{t.contextTruncated}</span>
            )}
            {m.history_truncated && (
              <span className='text-amber-600'>{t.historyTruncated}</span>
            )}
            {m.usage_estimated && <span>{t.usageEstimated}</span>}
            {m.status === 'error' && (
              <span className='text-destructive'>
                {t.failed(m.error || m.error_code || '')}
              </span>
            )}
            {onRegenerate && (
              <button
                className='inline-flex items-center gap-1 hover:text-foreground'
                onClick={onRegenerate}
              >
                <RotateCcw className='size-3' />
                {m.status === 'error' ? t.retry : t.regenerate}
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

function FileChip({
  f,
  href,
  onRemove,
}: {
  f: ConvFile
  href: string
  onRemove: () => void
}) {
  return (
    <span className='flex max-w-xs items-center gap-1.5 rounded-md border bg-muted/50 px-2 py-1 text-xs'>
      <FileText className='size-3.5 shrink-0' />
      <span className='truncate' title={f.name}>
        {f.name}
      </span>
      <span className='shrink-0 text-muted-foreground'>{fmtSize(f.size)}</span>
      {f.truncated && (
        <span className='shrink-0 text-amber-600' title={t.fileTruncated}>
          {t.truncatedBadge}
        </span>
      )}
      <a href={href} aria-label={t.download} title={t.download} download>
        <Download className='size-3.5' />
      </a>
      <button aria-label={t.removeFile} title={t.removeFile} onClick={onRemove}>
        <X className='size-3.5' />
      </button>
    </span>
  )
}
