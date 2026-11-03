import { zh } from '@/locales/zh-CN'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { ModelsTab } from './models-tab'
import { ProvidersTab } from './providers-tab'
import { UsageTab } from './usage-tab'

// 模型网关管理（M1 第二块 PR2）：供应商、模型（价格 / 默认 / 使用权限 / 测试连接）、用量统计。
export function ModelsPage() {
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
          {zh.models.title}
        </h1>
        <Tabs defaultValue='models'>
          <TabsList>
            <TabsTrigger value='models'>{zh.models.tabModels}</TabsTrigger>
            <TabsTrigger value='providers'>
              {zh.models.tabProviders}
            </TabsTrigger>
            <TabsTrigger value='usage'>{zh.models.tabUsage}</TabsTrigger>
          </TabsList>
          <TabsContent value='models' className='pt-4'>
            <ModelsTab />
          </TabsContent>
          <TabsContent value='providers' className='pt-4'>
            <ProvidersTab />
          </TabsContent>
          <TabsContent value='usage' className='pt-4'>
            <UsageTab />
          </TabsContent>
        </Tabs>
      </Main>
    </>
  )
}
