import { zh } from '@/locales/zh-CN'
import {
  BookOpen,
  ClipboardCheck,
  Plug,
  Bot,
  Sparkles,
  Building2,
  LayoutDashboard,
  MessagesSquare,
  ScrollText,
  Globe,
  Archive,
  ShieldCheck,
  Users,
  Wallet,
  BarChart3,
  BadgeCheck,
} from 'lucide-react'
import { type SidebarData } from '../types'

// 侧边栏导航。M0 仅保留「工作台」，后续模块（对话、知识库、智能体、审计中心…）按里程碑加入。
export const sidebarData: SidebarData = {
  navGroups: [
    {
      title: zh.nav.general,
      items: [
        { title: zh.nav.dashboard, url: '/', icon: LayoutDashboard },
        { title: zh.nav.meUsage, url: '/me/usage', icon: BarChart3 },
        { title: zh.nav.chat, url: '/chat', icon: MessagesSquare },
        { title: zh.nav.kb, url: '/kbs', icon: BookOpen },
        { title: zh.nav.agents, url: '/agents', icon: Bot },
        { title: zh.nav.approvals, url: '/approvals', icon: ClipboardCheck },
      ],
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
          title: zh.nav.mcp,
          url: '/mcp',
          icon: Plug,
          perm: 'tool.manage',
        },
        {
          title: zh.nav.adminAgents,
          url: '/admin/agents',
          icon: Sparkles,
          // 有 agent.create 或任何智能体 edit 的人都可进管理列表（服务端再过滤）
          perm: 'agent.create',
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
        {
          title: zh.nav.retention,
          url: '/retention',
          icon: Archive,
          // D-41：超级管理员管理保留期并可设置保全；安全审计员查看、设置、解除保全
          perm: ['system.settings', 'audit.read'],
        },
        {
          title: zh.nav.aigc,
          url: '/aigc',
          icon: BadgeCheck,
          perm: 'system.settings',
        },
      ],
    },
  ],
}
