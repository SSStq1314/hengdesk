import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { BookOpen, Plus } from 'lucide-react'
import { kbApi } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { KBDialog } from './settings-dialog'

const t = zh.kb

// 知识库列表（M1 第四块 PR3，FR-16）：只列出本人有「查看」权限的知识库。
export function KBListPage() {
  const list = useQuery({ queryKey: ['kbs'], queryFn: kbApi.list })
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
            <h1 className='text-2xl font-bold tracking-tight'>{t.title}</h1>
            <p className='text-sm text-muted-foreground'>{t.desc}</p>
          </div>
          {list.data?.can_create && (
            <Button className='ms-auto' onClick={() => setCreating(true)}>
              <Plus className='size-4' />
              {t.create}
            </Button>
          )}
        </div>
        {items.length === 0 && !list.isLoading && (
          <p className='text-sm text-muted-foreground'>{t.empty}</p>
        )}
        <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
          {items.map((k) => (
            <Link
              key={k.id}
              to='/kbs/$id'
              params={{ id: String(k.id) }}
              className='rounded-lg border p-4 hover:bg-accent'
            >
              <div className='flex items-center gap-2 font-medium'>
                <BookOpen className='size-4' />
                <span className='truncate'>{k.name}</span>
              </div>
              <p className='mt-1 line-clamp-2 text-sm text-muted-foreground'>
                {k.description}
              </p>
              <div className='mt-2 flex flex-wrap gap-1 text-xs'>
                <Badge variant='secondary'>{t.files(k.file_count)}</Badge>
                <Badge variant='outline'>{k.embedding_model}</Badge>
                {k.perms
                  .filter((p) => p !== 'use')
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
      {creating && <KBDialog onClose={() => setCreating(false)} />}
    </>
  )
}
