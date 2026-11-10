import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { Bot, Plus } from 'lucide-react'
import { agentApi } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { AgentCreateDialog } from './create-dialog'

const t = zh.agents

// 智能体管理列表：本人有 edit 的智能体（含未发布）。
export function AgentAdminPage() {
  const list = useQuery({
    queryKey: ['agents', 'manage'],
    queryFn: () => agentApi.list('manage'),
  })
  const [creating, setCreating] = useState(false)
  const items = list.data?.items ?? []
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <div className='mb-4 flex items-center gap-2'>
          <div>
            <h1 className='text-2xl font-bold tracking-tight'>{t.adminTitle}</h1>
            <p className='text-sm text-muted-foreground'>{t.adminDesc}</p>
          </div>
          {list.data?.can_create && (
            <Button className='ms-auto' onClick={() => setCreating(true)}>
              <Plus className='size-4' />
              {t.create}
            </Button>
          )}
        </div>
        {items.length === 0 && !list.isLoading && (
          <p className='text-sm text-muted-foreground'>{t.adminEmpty}</p>
        )}
        <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
          {items.map((a) => (
            <Link
              key={a.id}
              to='/admin/agents/$id'
              params={{ id: String(a.id) }}
              className='rounded-lg border p-4 hover:bg-accent'
            >
              <div className='flex items-center gap-2 font-medium'>
                <Bot className='size-4' />
                <span className='truncate'>{a.name}</span>
              </div>
              <p className='mt-1 line-clamp-2 text-sm text-muted-foreground'>
                {a.description}
              </p>
              <div className='mt-2 flex flex-wrap gap-1 text-xs'>
                {a.published ? (
                  <Badge variant='secondary'>
                    {t.published} · {t.versionN(a.published_version)}
                  </Badge>
                ) : (
                  <Badge variant='outline'>{t.unpublished}</Badge>
                )}
                {a.draft_dirty && (
                  <Badge variant='destructive'>{t.draftDirty}</Badge>
                )}
                {a.perms
                  .filter((p) => p === 'edit' || p === 'manage')
                  .map((p) => (
                    <Badge key={p} variant='outline'>
                      {t.perm[p]}
                    </Badge>
                  ))}
              </div>
            </Link>
          ))}
        </div>
      </Main>
      {creating && <AgentCreateDialog onClose={() => setCreating(false)} />}
    </>
  )
}
