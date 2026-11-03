import { zh } from '@/locales/zh-CN'
import { useAuthStore } from '@/stores/auth-store'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { LogsTab } from './logs-tab'
import { RulesTab } from './rules-tab'

// 出站控制（M1 第二块 PR4，AC-09）：白名单管理（超级管理员）+ 出站日志（审计员只读）。
export function EgressPage() {
  const perms = useAuthStore((s) => s.auth.user?.permissions ?? [])
  const canManage = perms.includes('egress.manage')
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <h1 className='mb-4 text-2xl font-bold tracking-tight'>
          {zh.egress.title}
        </h1>
        <Tabs defaultValue={canManage ? 'rules' : 'logs'}>
          <TabsList>
            {canManage && (
              <TabsTrigger value='rules'>{zh.egress.tabRules}</TabsTrigger>
            )}
            <TabsTrigger value='logs'>{zh.egress.tabLogs}</TabsTrigger>
          </TabsList>
          {canManage && (
            <TabsContent value='rules' className='pt-4'>
              <RulesTab />
            </TabsContent>
          )}
          <TabsContent value='logs' className='pt-4'>
            <LogsTab readOnly={!canManage} />
          </TabsContent>
        </Tabs>
      </Main>
    </>
  )
}
