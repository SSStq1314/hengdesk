// 金额单位换算：后端用「微元」（1 元 = 1e6），价格为每百万 token。
export const yuan = (micros: number) =>
  (micros / 1e6).toFixed(micros % 10000 === 0 ? 2 : 4)
export const toMicros = (v: string) => Math.round(Number(v || '0') * 1e6)
export const num = (n: number | undefined) => (n ?? 0).toLocaleString('zh-CN')
