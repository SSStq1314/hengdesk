import { zh } from '@/locales/zh-CN'
import {
  Bot,
  Building2,
  LayoutDashboard,
  ScrollText,
  Globe,
  ShieldCheck,
  Users,
  Wallet,
} from 'lucide-react'
import { type SidebarData } from '../types'

// 侧边栏导航。M0 仅保留「工作台」，后续模块（对话、知识库、智能体、审计中心…）按里程碑加入。
export const sidebarData: SidebarData = {
  navGroups: [
    {
      title: zh.nav.general,
      items: [{ title: zh.nav.dashboard, url: '/', icon: LayoutDashboard }],
    },
    {
      title: zh.nav.org,
      items: [
        {
          title: zh.nav.members,
          url: '/members',
          icon: Users,
          perm: 'org.member.read',
        },
        {
          title: zh.nav.departments,
          url: '/departments',
          icon: Building2,
          perm: 'org.dept.read',
        },
        {
          title: zh.nav.roles,
          url: '/roles',
          icon: ShieldCheck,
          perm: 'org.role.read',
        },
      ],
    },
    {
      title: zh.nav.ai,
      items: [
        {
          title: zh.nav.models,
          url: '/models',
          icon: Bot,
          perm: 'model.manage',
        },
        {
          title: zh.nav.budgets,
          url: '/budgets',
          icon: Wallet,
          perm: 'budget.read',
        },
      ],
    },
    {
      title: zh.nav.security,
      items: [
        {
          title: zh.nav.audit,
          url: '/audit',
          icon: ScrollText,
          perm: 'audit.read',
        },
        {
          title: zh.nav.egress,
          url: '/egress',
          icon: Globe,
          perm: 'egress.log.read',
        },
      ],
    },
  ],
}
