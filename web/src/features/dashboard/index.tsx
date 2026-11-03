import { useAuthStore } from '@/stores/auth-store'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { ConfigDrawer } from '@/components/config-drawer'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

export function Dashboard() {
  const user = useAuthStore((s) => s.auth.user)
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ConfigDrawer />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <h1 className='mb-4 text-2xl font-bold tracking-tight'>工作台</h1>
        <Card>
          <CardHeader>
            <CardTitle>
              欢迎使用衡台，{user?.display_name || user?.email}
            </CardTitle>
            <CardDescription>
              衡台 · 企业 AI 办公平台（M0 空白后台）
            </CardDescription>
          </CardHeader>
          <CardContent className='text-sm text-muted-foreground'>
            对话、知识库、智能体、审批与审计等模块将在后续里程碑陆续开放。
          </CardContent>
        </Card>
      </Main>
    </>
  )
}
