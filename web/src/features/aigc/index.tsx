import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import { modelApi, type AigcSettings } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { Field } from '@/features/models/fields'

const t = zh.aigc

export function AigcSettingsPage() {
  const qc = useQueryClient()
  const perms = useAuthStore((s) => s.auth.user?.permissions) ?? []
  const can = perms.includes('system.settings')
  const q = useQuery({
    queryKey: ['aigc-settings'],
    queryFn: modelApi.aigcGet,
    enabled: can,
  })
  const [draft, setDraft] = useState<AigcSettings | null>(null)
  const st = draft ?? q.data?.settings
  const save = useMutation({
    mutationFn: () => modelApi.aigcPut(st!),
    onSuccess: () => {
      toast.success(t.saved)
      setDraft(null)
      qc.invalidateQueries({ queryKey: ['aigc-settings'] })
    },
    onError: (e: Error) => toast.error(e.message || t.copyInvalid),
  })
  const onSave = () => {
    if (!st) return
    if (!st.badge.trim() || !st.suffix.trim() || !t.hasRequiredMark(st.badge) || !t.hasRequiredMark(st.suffix)) {
      toast.error(t.copyInvalid)
      return
    }
    save.mutate()
  }
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main className='max-w-xl space-y-4'>
        <div>
          <h1 className='text-2xl font-bold'>{t.title}</h1>
          <p className='text-sm text-muted-foreground'>{t.desc}</p>
        </div>
        {!can ? (
          <p className='text-sm text-muted-foreground'>{t.needPerm}</p>
        ) : !st ? (
          <p className='text-sm text-muted-foreground'>{t.loading}</p>
        ) : (
          <>
            <Field label={t.badge}>
              <Input
                value={st.badge}
                onChange={(e) => setDraft({ ...st, badge: e.target.value })}
              />
              <p className='text-xs text-muted-foreground'>{t.badgeHint}</p>
            </Field>
            <Field label={t.suffix}>
              <Input
                value={st.suffix}
                onChange={(e) => setDraft({ ...st, suffix: e.target.value })}
              />
              <p className='text-xs text-muted-foreground'>{t.suffixHint}</p>
            </Field>
            <Field label={t.producer}>
              <Input
                value={st.producer}
                onChange={(e) => setDraft({ ...st, producer: e.target.value })}
              />
            </Field>
            <Field label={t.label}>
              <Input
                value={st.label}
                onChange={(e) => setDraft({ ...st, label: e.target.value })}
              />
            </Field>
            <Button disabled={save.isPending || !draft} onClick={onSave}>
              {t.save}
            </Button>
          </>
        )}
      </Main>
    </>
  )
}
