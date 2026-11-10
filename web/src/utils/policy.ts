import type { GroupOverride } from '@/api/types'

/** 可按群覆盖的布尔能力字段（tri-state：null = 跟随全局 / 默认）。 */
export type OverrideBoolKey =
  | 'enabled'
  | 'reply_to_bots'
  | 'quote_reply'
  | 'polish'
  | 'tools'
  | 'tasks'
  | 'memory'
  | 'web'
  | 'exec'
  | 'affect'

/** 可按群覆盖的枚举档位字段（null = 跟随全局）。 */
export type OverrideLevelKey = 'proactive' | 'humanize' | 'affect_inject_mode'

/**
 * 把「按群覆盖」表格里的草稿行整成提交给后端的形状。
 *
 * **必须透传**（`...row`），不能按字段白名单重建：
 * 之前这里是 8 个字段的白名单，结果 D-035 加的七个能力覆盖
 * （tools / proactive / tasks / memory / humanize / web / exec）一保存就被抹掉 ——
 * 用户只改了一行 QPM，其它群的开关全丢，而且界面不会报错。
 * 两阶段生成的 `polish` 同样在这条线上：新增覆盖列只加控件，不改白名单。
 *
 * 只做三件整理，其余字段原样带上（后端以后新增字段也不会再被前端吞掉）：
 * 1. `group_openid` 去首尾空格（输入框里很容易粘进空格）；
 * 2. 可空枚举 / 可空布尔统一成 `null`（= 跟随全局），避免 undefined 与 null 混用；
 * 3. 保留 `enabled` 的三态（null / true / false），缺省按 null 处理。
 */
export function toGroupOverridePayload(rows: readonly GroupOverride[]): GroupOverride[] {
  return rows.map((row) => ({
    ...row,
    group_openid: row.group_openid.trim(),
    enabled: row.enabled ?? null,
    trigger_mode: row.trigger_mode ?? null,
    context_mode: row.context_mode ?? null,
    context_scope: row.context_scope ?? null,
    reply_to_bots: row.reply_to_bots ?? null,
    quote_reply: row.quote_reply ?? null,
    polish: row.polish ?? null,
    tools: row.tools ?? null,
    proactive: row.proactive ?? null,
    tasks: row.tasks ?? null,
    memory: row.memory ?? null,
    humanize: row.humanize ?? null,
    web: row.web ?? null,
    exec: row.exec ?? null,
    affect: row.affect ?? null,
    affect_inject_mode: row.affect_inject_mode ?? null,
    rate_limit_group_qpm: row.rate_limit_group_qpm ?? null,
  }))
}
