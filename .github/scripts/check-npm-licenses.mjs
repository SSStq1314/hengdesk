// 读取 `pnpm licenses list --json` 输出，发现 copyleft 许可即失败。
import { readFileSync } from 'node:fs'
const data = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const deny = /(^|[^L])GPL|AGPL|LGPL|SSPL|EUPL|CC-BY-NC/i
const bad = []
for (const [license, pkgs] of Object.entries(data)) {
  if (deny.test(license) && !/OR (MIT|Apache|BSD|ISC)/i.test(license)) {
    for (const p of pkgs) bad.push(`${p.name}@${(p.versions || [p.version]).join(',')}: ${license}`)
  }
}
if (bad.length) { console.error('禁止的许可证：\n' + bad.join('\n')); process.exit(1) }
console.log(`许可检查通过，共 ${Object.keys(data).length} 种许可`)
