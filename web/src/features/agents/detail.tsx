import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { ArrowLeft, MessagesSquare, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  agentApi,
  chatApi,
  type AgentConfig,
  type AgentInput,
  type AgentRun,
} from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { AclEditor } from '@/features/kb/acl-editor'
import { Field, NativeSelect } from '@/features/models/fields'
import { KBPicker } from '@/features/chat/kb-picker'
import { ToolPicker } from '@/features/chat/tool-picker'

const t = zh.agents

function emptyDraft(): AgentConfig {
  return {
    name: '',
    description: '',
    system_prompt: '',
    opening: '',
    model_id: null,
    kb_ids: [],
    tool_ids: [],
    max_steps: 5,
  }
}

// 智能体详情：配置 / 权限 / 版本 / 运行记录；调试用草稿开会话。
export function AgentDetailPage() {
  const { id: idStr } = useParams({ from: '/_authenticated/admin/agents/$id' })
  const id = Number(idStr)
  const qc = useQueryClient()
  const nav = useNavigate()
  const agent = useQuery({
    queryKey: ['agent', id],
    queryFn: () => agentApi.get(id),
    enabled: Number.isFinite(id) && id > 0,
  })
  const models = useQuery({ queryKey: ['my-models'], queryFn: chatApi.models })
  const versions = useQuery({
    queryKey: ['agent-versions', id],
    queryFn: () => agentApi.versions(id),
    enabled: !!agent.data?.perms.includes('edit'),
  })
  const runs = useQuery({
    queryKey: ['agent-runs', id],
    queryFn: () => agentApi.runs(id),
    enabled: !!agent.data?.perms.includes('manage'),
  })

  // draftEdit 非空表示用户已改过表单；否则直接展示服务端草稿，避免 effect 里 setState。
  const [draftEdit, setDraftEdit] = useState<AgentConfig | null>(null)
  const [publishNote, setPublishNote] = useState('')
  const [confirmPub, setConfirmPub] = useState(false)
  const [confirmDel, setConfirmDel] = useState(false)
  const [confirmDisable, setConfirmDisable] = useState(false)
  const [confirmRollback, setConfirmRollback] = useState<number | null>(null)
  const [runDetail, setRunDetail] = useState<AgentRun | null>(null)

  const canEdit = !!agent.data?.perms.includes('edit')
  const canManage = !!agent.data?.perms.includes('manage')
  const serverDraft = agent.data?.draft
    ? { ...emptyDraft(), ...agent.data.draft }
    : emptyDraft()
  const draft = draftEdit ?? serverDraft
  const set = (p: Partial<AgentConfig>) =>
    setDraftEdit((d) => ({ ...(d ?? serverDraft), ...p }))

  const save = useMutation({
    mutationFn: () => {
      const body: AgentInput = {
        name: draft.name,
        description: draft.description,
        system_prompt: draft.system_prompt,
        opening: draft.opening,
        kb_ids: draft.kb_ids,
        tool_ids: draft.tool_ids,
        max_steps: draft.max_steps,
      }
      if (draft.model_id == null) body.clear_model = true
      else body.model_id = draft.model_id
      return agentApi.update(id, body)
    },
    onSuccess: () => {
      toast.success(t.saved)
      setDraftEdit(null)
      qc.invalidateQueries({ queryKey: ['agent', id] })
      qc.invalidateQueries({ queryKey: ['agents'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const publish = useMutation({
    mutationFn: () => agentApi.publish(id, publishNote.trim()),
    onSuccess: () => {
      toast.success(t.publishedOk)
      setConfirmPub(false)
      setPublishNote('')
      qc.invalidateQueries({ queryKey: ['agent', id] })
      qc.invalidateQueries({ queryKey: ['agent-versions', id] })
      qc.invalidateQueries({ queryKey: ['agents'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const restore = useMutation({
    mutationFn: (v: number) => agentApi.restore(id, v),
    onSuccess: () => {
      toast.success(t.restoreOk)
      setDraftEdit(null)
      qc.invalidateQueries({ queryKey: ['agent', id] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: () => agentApi.remove(id),
    onSuccess: () => {
      toast.success(t.deleted)
      qc.invalidateQueries({ queryKey: ['agents'] })
      nav({ to: '/admin/agents' })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const setDisabled = useMutation({
    mutationFn: (disabled: boolean) =>
      disabled ? agentApi.disable(id) : agentApi.enable(id),
    onSuccess: (_a, disabled) => {
      toast.success(disabled ? t.disabledOk : t.enabledOk)
      setConfirmDisable(false)
      qc.invalidateQueries({ queryKey: ['agent', id] })
      qc.invalidateQueries({ queryKey: ['agents'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const rollback = useMutation({
    mutationFn: (v: number) => agentApi.rollback(id, v),
    onSuccess: () => {
      toast.success(t.rollbackOk)
      setConfirmRollback(null)
      setDraftEdit(null)
      qc.invalidateQueries({ queryKey: ['agent', id] })
      qc.invalidateQueries({ queryKey: ['agent-versions', id] })
      qc.invalidateQueries({ queryKey: ['agents'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const a = agent.data
  const modelOpts = [
    { value: '', label: t.modelNone },
    ...(models.data?.items ?? []).map((m) => ({
      value: String(m.id),
      label: m.name,
    })),
  ]

  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <Button asChild variant='ghost' size='sm'>
            <Link to='/admin/agents'>
              <ArrowLeft className='size-4' />
              {zh.common.goBack}
            </Link>
          </Button>
          <h1 className='text-2xl font-bold tracking-tight'>
            {a?.name ?? '…'}
          </h1>
          {a?.published ? (
            <Badge variant='secondary'>
              {t.published} · {t.versionN(a.published_version)}
            </Badge>
          ) : (
            <Badge variant='outline'>{t.unpublished}</Badge>
          )}
          {a?.draft_dirty && (
            <Badge variant='destructive'>{t.draftDirty}</Badge>
          )}
          {a?.disabled && (
            <Badge variant='destructive'>{t.disabled}</Badge>
          )}
          <div className='ms-auto flex flex-wrap gap-2'>
            {canEdit && (
              <Button asChild size='sm' variant='secondary'>
                <Link to='/chat' search={{ agent: id, debug: true }}>
                  <MessagesSquare className='size-4' />
                  {t.startDebug}
                </Link>
              </Button>
            )}
            {canManage && (
              <Button
                size='sm'
                variant='outline'
                onClick={() =>
                  a?.disabled ? setDisabled.mutate(false) : setConfirmDisable(true)
                }
                disabled={setDisabled.isPending}
              >
                {a?.disabled ? t.enable : t.disable}
              </Button>
            )}
            {canManage && (
              <Button
                size='sm'
                variant='destructive'
                onClick={() => setConfirmDel(true)}
              >
                <Trash2 className='size-4' />
                {t.delete}
              </Button>
            )}
          </div>
        </div>

        {!a && agent.isLoading && (
          <p className='text-sm text-muted-foreground'>…</p>
        )}
        {a && (
          <Tabs defaultValue='config'>
            <TabsList>
              <TabsTrigger value='config'>{t.config}</TabsTrigger>
              {canManage && (
                <TabsTrigger value='acl'>{t.acl}</TabsTrigger>
              )}
              {canEdit && (
                <TabsTrigger value='versions'>{t.versions}</TabsTrigger>
              )}
              {canManage && (
                <TabsTrigger value='runs'>{t.runs}</TabsTrigger>
              )}
            </TabsList>

            <TabsContent value='config' className='mt-4'>
              <div className='grid max-w-3xl gap-3'>
                <Field label={t.name}>
                  <Input
                    value={draft.name}
                    disabled={!canEdit}
                    onChange={(e) => set({ name: e.target.value })}
                  />
                </Field>
                <Field label={t.description}>
                  <Textarea
                    rows={2}
                    value={draft.description}
                    disabled={!canEdit}
                    onChange={(e) => set({ description: e.target.value })}
                  />
                </Field>
                <Field label={t.systemPrompt}>
                  <Textarea
                    rows={6}
                    value={draft.system_prompt}
                    disabled={!canEdit}
                    onChange={(e) => set({ system_prompt: e.target.value })}
                  />
                </Field>
                <Field label={t.opening}>
                  <Textarea
                    rows={2}
                    value={draft.opening}
                    disabled={!canEdit}
                    onChange={(e) => set({ opening: e.target.value })}
                  />
                </Field>
                <Field label={t.model}>
                  <NativeSelect
                    value={draft.model_id ? String(draft.model_id) : ''}
                    onChange={(v) =>
                      set({ model_id: v ? Number(v) : null })
                    }
                    options={modelOpts}
                  />
                </Field>
                <Field label={t.kbs}>
                  <KBPicker
                    value={draft.kb_ids}
                    onChange={(kb_ids) => set({ kb_ids })}
                    disabled={!canEdit}
                  />
                </Field>
                <Field label={t.tools}>
                  <ToolPicker
                    value={draft.tool_ids}
                    onChange={(tool_ids) => set({ tool_ids })}
                    disabled={!canEdit}
                  />
                </Field>
                <Field label={t.maxSteps}>
                  <Input
                    type='number'
                    min={1}
                    max={10}
                    value={draft.max_steps}
                    disabled={!canEdit}
                    onChange={(e) =>
                      set({
                        max_steps: Math.min(
                          10,
                          Math.max(1, Number(e.target.value) || 1)
                        ),
                      })
                    }
                  />
                </Field>
                {canEdit && (
                  <div className='flex flex-wrap gap-2 pt-2'>
                    <Button
                      disabled={save.isPending}
                      onClick={() => save.mutate()}
                    >
                      {t.save}
                    </Button>
                    {canManage && (
                      <>
                        <Input
                          className='max-w-xs'
                          placeholder={t.publishNote}
                          value={publishNote}
                          onChange={(e) => setPublishNote(e.target.value)}
                        />
                        <Button
                          variant='secondary'
                          onClick={() => setConfirmPub(true)}
                        >
                          {t.publish}
                        </Button>
                      </>
                    )}
                  </div>
                )}
                <p className='text-xs text-muted-foreground'>{t.debugHint}</p>
              </div>
            </TabsContent>

            {canManage && (
              <TabsContent value='acl' className='mt-4'>
                <AclEditor
                  queryKey={['agent-acl', id]}
                  load={() => agentApi.acl(id)}
                  grant={(b) => agentApi.grant(id, b)}
                  revoke={(type, sid) => agentApi.revoke(id, type, sid)}
                  perms={['view', 'use', 'edit', 'manage']}
                />
              </TabsContent>
            )}

            {canEdit && (
              <TabsContent value='versions' className='mt-4'>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t.versions}</TableHead>
                      <TableHead>{t.publishNote}</TableHead>
                      <TableHead>{t.runStarted}</TableHead>
                      <TableHead>{zh.common.actions}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(versions.data?.items ?? []).map((v) => (
                      <TableRow key={v.version}>
                        <TableCell>
                          {t.versionN(v.version)}
                          {v.current && (
                            <Badge className='ms-2' variant='secondary'>
                              {t.current}
                            </Badge>
                          )}
                        </TableCell>
                        <TableCell className='max-w-xs truncate'>
                          {v.note || '—'}
                        </TableCell>
                        <TableCell>
                          {new Date(v.created_at).toLocaleString()}
                        </TableCell>
                        <TableCell className='space-x-2'>
                          <Button
                            size='sm'
                            variant='outline'
                            disabled={restore.isPending}
                            onClick={() => restore.mutate(v.version)}
                          >
                            {t.restore}
                          </Button>
                          {canManage && !v.current && (
                            <Button
                              size='sm'
                              variant='secondary'
                              disabled={rollback.isPending}
                              onClick={() => setConfirmRollback(v.version)}
                            >
                              {t.rollback}
                            </Button>
                          )}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </TabsContent>
            )}

            {canManage && (
              <TabsContent value='runs' className='mt-4'>
                {(runs.data?.items ?? []).length === 0 ? (
                  <p className='text-sm text-muted-foreground'>{t.runsEmpty}</p>
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>ID</TableHead>
                        <TableHead>{t.runStatus}</TableHead>
                        <TableHead>{t.runSteps}</TableHead>
                        <TableHead>{t.runTools}</TableHead>
                        <TableHead>{t.runModel}</TableHead>
                        <TableHead>{t.runStarted}</TableHead>
                        <TableHead>{zh.common.actions}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {(runs.data?.items ?? []).map((r) => (
                        <TableRow key={r.id}>
                          <TableCell>
                            {r.id}
                            {r.debug && (
                              <Badge className='ms-1' variant='outline'>
                                {t.debugBadge}
                              </Badge>
                            )}
                          </TableCell>
                          <TableCell>{r.status}</TableCell>
                          <TableCell>
                            {r.steps}/{r.max_steps}
                            {r.step_limited ? t.stepLimited : ''}
                          </TableCell>
                          <TableCell>{r.tool_calls}</TableCell>
                          <TableCell>{r.model_name || '—'}</TableCell>
                          <TableCell>
                            {new Date(r.started_at).toLocaleString()}
                          </TableCell>
                          <TableCell className='flex flex-wrap gap-1'>
                            <Button
                              size='sm'
                              variant='outline'
                              onClick={() => setRunDetail(r)}
                            >
                              {zh.approvals.detail}
                            </Button>
                            {r.trace_id && (
                              <Button
                                size='sm'
                                variant='ghost'
                                onClick={() => {
                                  void navigator.clipboard.writeText(r.trace_id)
                                  toast.success(t.runTrace + ': ' + r.trace_id)
                                  nav({ to: '/audit' })
                                }}
                              >
                                {t.openTrace}
                              </Button>
                            )}
                            {r.conversation_id > 0 && (
                              <Button asChild size='sm' variant='ghost'>
                                <Link
                                  to='/chat'
                                  search={{ conv: r.conversation_id }}
                                >
                                  {t.openChat}
                                </Link>
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
              </TabsContent>
            )}
          </Tabs>
        )}
      </Main>

      <ConfirmDialog
        open={confirmPub}
        onOpenChange={setConfirmPub}
        title={t.publish}
        desc={t.publishConfirm}
        confirmText={t.publish}
        handleConfirm={() => publish.mutate()}
        isLoading={publish.isPending}
      />
      <ConfirmDialog
        open={confirmDel}
        onOpenChange={setConfirmDel}
        title={t.delete}
        desc={t.deleteConfirm}
        confirmText={t.delete}
        destructive
        handleConfirm={() => remove.mutate()}
        isLoading={remove.isPending}
      />
      <ConfirmDialog
        open={confirmDisable}
        onOpenChange={setConfirmDisable}
        title={t.disable}
        desc={t.disableConfirm}
        confirmText={t.disable}
        destructive
        handleConfirm={() => setDisabled.mutate(true)}
        isLoading={setDisabled.isPending}
      />
      <ConfirmDialog
        open={confirmRollback != null}
        onOpenChange={(o) => !o && setConfirmRollback(null)}
        title={t.rollback}
        desc={t.rollbackConfirm}
        confirmText={t.rollback}
        handleConfirm={() =>
          confirmRollback != null && rollback.mutate(confirmRollback)
        }
        isLoading={rollback.isPending}
      />

      {runDetail && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setRunDetail(null)}
          title={`${t.runs} #${runDetail.id}`}
          desc={
            <div className='grid gap-1 text-sm'>
              <div>
                {t.runStatus}：{runDetail.status}
                {runDetail.error_code ? ` (${runDetail.error_code})` : ''}
              </div>
              <div>
                {t.runSteps}：{runDetail.steps}/{runDetail.max_steps}
              </div>
              <div>
                {t.runTokens}：{runDetail.prompt_tokens}+
                {runDetail.completion_tokens}
              </div>
              <div>
                {t.runTrace}：
                <code className='text-xs'>{runDetail.trace_id}</code>
              </div>
              {(runDetail.dropped?.length ?? 0) > 0 && (
                <div>
                  {t.runDropped}：
                  <ul className='list-inside list-disc'>
                    {runDetail.dropped!.map((d, i) => (
                      <li key={i}>
                        {d.type}#{d.id} {d.name} — {d.reason}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          }
          confirmText={zh.common.cancel}
          handleConfirm={() => setRunDetail(null)}
        />
      )}
    </>
  )
}
