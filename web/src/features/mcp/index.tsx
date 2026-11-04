import { zh } from '@/locales/zh-CN'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { ServersTab } from './servers-tab'
import { ToolsTab } from './tools-tab'

// 连接器与工具（M1 第五块 PR5）：MCP 服务器登记 / 发现，工具治理（风险、策略、审批人、使用权限）。
export function McpPage() {
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <h1 className='mb-2 text-2xl font-bold tracking-tight'>
          {zh.mcp.title}
        </h1>
        <p className='mb-4 max-w-3xl text-sm text-muted-foreground'>
          {zh.mcp.desc}
        </p>
        <Tabs defaultValue='servers'>
          <TabsList>
            <TabsTrigger value='servers'>{zh.mcp.tabServers}</TabsTrigger>
            <TabsTrigger value='tools'>{zh.mcp.tabTools}</TabsTrigger>
          </TabsList>
          <TabsContent value='servers' className='pt-4'>
            <ServersTab />
          </TabsContent>
          <TabsContent value='tools' className='pt-4'>
            <ToolsTab />
          </TabsContent>
        </Tabs>
      </Main>
    </>
  )
}
