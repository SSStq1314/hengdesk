import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { Bot, MessagesSquare } from 'lucide-react'
import { agentApi } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

const t = zh.agents

// 智能体广场：本人有 use 的已发布智能体，点击开始对话。
export function AgentPlazaPage() {
  const list = useQuery({
    queryKey: ['agents', 'plaza'],
    queryFn: () => agentApi.list('use'),
  })
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
        <div className='mb-4'>
          <h1 className='text-2xl font-bold tracking-tight'>{t.plazaTitle}</h1>
          <p className='text-sm text-muted-foreground'>{t.plazaDesc}</p>
        </div>
        {items.length === 0 && !list.isLoading && (
          <p className='text-sm text-muted-foreground'>{t.plazaEmpty}</p>
        )}
        <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
          {items.map((a) => (
            <div key={a.id} className='flex flex-col rounded-lg border p-4'>
              <div className='flex items-center gap-2 font-medium'>
                <Bot className='size-4' />
                <span className='truncate'>{a.name}</span>
                <Badge variant='secondary' className='ms-auto'>
                  {t.versionN(a.published_version)}
                </Badge>
              </div>
              <p className='mt-1 line-clamp-3 flex-1 text-sm text-muted-foreground'>
                {a.description || a.opening}
              </p>
              <Button asChild className='mt-3' size='sm'>
                <Link to='/chat' search={{ agent: a.id }}>
                  <MessagesSquare className='size-4' />
                  {t.startChat}
                </Link>
              </Button>
            </div>
          ))}
        </div>
      </Main>
    </>
  )
}
