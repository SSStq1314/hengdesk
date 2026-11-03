import { zh } from '@/locales/zh-CN'
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
        <h1 className='mb-4 text-2xl font-bold tracking-tight'>
          {zh.dashboard.title}
        </h1>
        <Card>
          <CardHeader>
            <CardTitle>
              {zh.dashboard.welcome(user?.display_name || user?.email || '')}
            </CardTitle>
            <CardDescription>{zh.dashboard.subtitle}</CardDescription>
          </CardHeader>
          <CardContent className='text-sm text-muted-foreground'>
            {zh.dashboard.upcoming}
          </CardContent>
        </Card>
      </Main>
    </>
  )
}
