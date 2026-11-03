import { LayoutDashboard } from 'lucide-react'
import { type SidebarData } from '../types'

// 侧边栏导航。M0 仅保留「工作台」，后续模块（对话、知识库、智能体、审计中心…）按里程碑加入。
export const sidebarData: SidebarData = {
  navGroups: [
    {
      title: '通用',
      items: [{ title: '工作台', url: '/', icon: LayoutDashboard }],
    },
  ],
}
