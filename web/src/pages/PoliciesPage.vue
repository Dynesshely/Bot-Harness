<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import CopyButton from '@/components/CopyButton.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ToggleField from '@/components/ToggleField.vue'
import { policiesApi, toMessage } from '@/api'
import type { ContextMode, ContextScope, GroupOverride, Policies, TriggerMode } from '@/api/types'
import { toast } from '@/composables/useToast'
import {
  toGroupOverridePayload,
  type OverrideBoolKey,
  type OverrideLevelKey,
} from '@/utils/policy'
import { contextModeBadge, triggerModeBadge } from '@/utils/badges'
import type { BadgeInfo } from '@/utils/badges'

const TRIGGER_OPTIONS: { value: TriggerMode; title: string; desc: string }[] = [
  {
    value: 'at_only',
    title: '仅 @机器人 才回复',
    desc: '对应 E1：只有 @机器人 的消息触发回复，平台默认权限下可用。',
  },
  {
    value: 'all_messages',
    title: '群内任意消息都回复',
    desc: '对应 E2：群内任意消息触发回复，需平台授予群全量消息权限。',
  },
]

const CONTEXT_OPTIONS: { value: ContextMode; title: string; desc: string }[] = [
  {
    value: 'per_member',
    title: '每个成员独立上下文',
    desc: '对应 C1：以 group_openid + member_openid 隔离，成员之间互不可见。',
  },
  {
    value: 'shared',
    title: '整群共享上下文',
    desc: '对应 C2：全群共用一份记忆，需要群全量消息权限。',
  },
]

/** 上下文范围：模型能看到多少群内容。 */
const SCOPE_OPTIONS: { value: ContextScope; title: string; desc: string }[] = [
  {
    value: 'addressed_only',
    title: '只含 @机器人的对话',
    desc: '模型只看到和它有关的那些轮次，最省 token，也最不容易被群聊带偏。',
  },
  {
    value: 'include_unaddressed',
    title: '包含群内未 @ 的消息',
    desc: '机器人即使不接话，也能"听到"群里在聊什么；其它机器人的发言同样计入。它仍然只按触发模式决定要不要回复。',
  },
]

const original = ref<Policies | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref('')

const draft = reactive({
  trigger_mode: 'at_only' as TriggerMode,
  context_mode: 'per_member' as ContextMode,
  context_scope: 'addressed_only' as ContextScope,
  group_allowlist: '',
  member_allowlist: '',
  admin_openids: '',
  blocked_keywords: '',
  reply_to_bots: true,
  quote_reply: true,
  group_overrides: [] as GroupOverride[],
})

function parseList(text: string): string[] {
  return Array.from(
    new Set(
      text
        .split(/[\n,，;；]+/)
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  )
}

function joinList(list?: string[] | null): string {
  return (list ?? []).join('\n')
}

function sameList(a?: string[] | null, b?: string[] | null): boolean {
  return JSON.stringify(a ?? []) === JSON.stringify(b ?? [])
}

function syncFrom(data: Policies) {
  draft.trigger_mode = data.trigger_mode ?? 'at_only'
  draft.context_mode = data.context_mode ?? 'per_member'
  draft.context_scope = data.context_scope ?? 'addressed_only'
  draft.group_allowlist = joinList(data.group_allowlist)
  draft.member_allowlist = joinList(data.member_allowlist)
  draft.admin_openids = joinList(data.admin_openids)
  draft.blocked_keywords = joinList(data.blocked_keywords)
  draft.reply_to_bots = data.reply_to_bots ?? true
  draft.quote_reply = data.quote_reply ?? true
  draft.group_overrides = (data.group_overrides ?? []).map((item) => ({ ...item }))
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await policiesApi.get()
    original.value = data
    syncFrom(data)
  } catch (err) {
    error.value = toMessage(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => void load())

/** 变化的字段（只提交被修改的字段，减少误覆盖）。 */
const patch = computed<Partial<Policies>>(() => {
  const source = original.value
  if (!source) return {}
  const changed: Partial<Policies> = {}
  if (draft.trigger_mode !== source.trigger_mode) changed.trigger_mode = draft.trigger_mode
  if (draft.context_mode !== source.context_mode) changed.context_mode = draft.context_mode
  if (draft.context_scope !== source.context_scope) changed.context_scope = draft.context_scope

  const groupAllow = parseList(draft.group_allowlist)
  if (!sameList(groupAllow, source.group_allowlist)) changed.group_allowlist = groupAllow
  const memberAllow = parseList(draft.member_allowlist)
  if (!sameList(memberAllow, source.member_allowlist)) changed.member_allowlist = memberAllow
  const admins = parseList(draft.admin_openids)
  if (!sameList(admins, source.admin_openids)) changed.admin_openids = admins
  const keywords = parseList(draft.blocked_keywords)
  if (!sameList(keywords, source.blocked_keywords)) changed.blocked_keywords = keywords
  if (draft.reply_to_bots !== (source.reply_to_bots ?? true)) changed.reply_to_bots = draft.reply_to_bots
  if (draft.quote_reply !== (source.quote_reply ?? true)) changed.quote_reply = draft.quote_reply

  if (JSON.stringify(draft.group_overrides) !== JSON.stringify(source.group_overrides ?? [])) {
    // 透传式重建：只 trim 群号 + 可空枚举补 null，其余字段（含 D-035 的七项能力覆盖）
    // 原样带上。之前按白名单重建，改任意一行就会静默抹掉这些字段。
    changed.group_overrides = toGroupOverridePayload(draft.group_overrides)
  }
  return changed
})

const dirty = computed(() => Object.keys(patch.value).length > 0)

const emptyOverride = computed(
  () => draft.group_overrides.some((item) => !item.group_openid || !item.group_openid.trim()),
)

const warnings = computed(() => original.value?.warnings ?? [])

const contextScopeBadge = computed<BadgeInfo>(() =>
  original.value?.context_scope === 'include_unaddressed'
    ? { label: '上下文含未 @ 消息', tone: 'violet' }
    : { label: '上下文仅 @ 对话', tone: 'slate' },
)

/** 平台侧权限：已开放全量的群数量。 */
const fullAccessGroups = computed(() => original.value?.platform?.full_access_groups ?? [])
const limitedGroups = computed(() => original.value?.platform?.limited_groups ?? [])
const platformBadge = computed<BadgeInfo>(() => {
  const total = original.value?.platform?.known_groups ?? 0
  if (total === 0) return { label: '平台权限未知', tone: 'slate' }
  if (limitedGroups.value.length === 0) {
    return { label: `${fullAccessGroups.value.length} 个群已开放全量`, tone: 'emerald' }
  }
  if (fullAccessGroups.value.length === 0) {
    return { label: `${limitedGroups.value.length} 个群仅 @机器人`, tone: 'amber' }
  }
  return {
    label: `${fullAccessGroups.value.length} 全量 / ${limitedGroups.value.length} 仅@`,
    tone: 'sky',
  }
})

async function save() {
  if (!dirty.value || saving.value) return
  if (emptyOverride.value) {
    toast.error('按群覆盖中存在未填写 group_openid 的行')
    return
  }
  saving.value = true
  try {
    const updated = await policiesApi.update(patch.value)
    original.value = updated
    syncFrom(updated)
    toast.success('策略已保存')
    if (updated.warnings && updated.warnings.length > 0) {
      for (const warning of updated.warnings) toast.warn(warning)
    }
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    saving.value = false
  }
}

function reset() {
  if (original.value) syncFrom(original.value)
  toast.info('已撤销未保存的修改')
}

function addOverride() {
  draft.group_overrides.push({
    group_openid: '',
    enabled: true,
    trigger_mode: null,
    context_mode: null,
    polish: null,
    rate_limit_group_qpm: null,
  })
}

function removeOverride(index: number) {
  draft.group_overrides.splice(index, 1)
}

function setOverrideTrigger(row: GroupOverride, value: string) {
  row.trigger_mode = value === '' ? null : (value as TriggerMode)
}

function setOverrideContext(row: GroupOverride, value: string) {
  row.context_mode = value === '' ? null : (value as ContextMode)
}

function setOverrideScope(row: GroupOverride, value: string) {
  row.context_scope = value === '' ? null : (value as ContextScope)
}

/** 三态布尔覆盖：'' = 跟随全局（null），'true' / 'false' = 显式开 / 关。 */
function setOverrideBool(row: GroupOverride, key: OverrideBoolKey, value: string) {
  row[key] = value === '' ? null : value === 'true'
}

/** 群启用三态：'' = 跟随默认（启用），true = 启用，false = 该群暂停。 */
function setOverrideEnabled(row: GroupOverride, value: string) {
  setOverrideBool(row, 'enabled', value)
}

/** 枚举档位覆盖：'' = 跟随全局（null）。 */
function setOverrideLevel(row: GroupOverride, key: OverrideLevelKey, value: string) {
  row[key] = value === '' ? null : value
}

function setOverrideQpm(row: GroupOverride, value: string) {
  const parsed = Number(value)
  row.rate_limit_group_qpm = value === '' || Number.isNaN(parsed) ? null : parsed
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-sm font-semibold text-slate-200">策略配置</span>
        <StatusBadge :info="platformBadge" dot />
        <StatusBadge :info="triggerModeBadge(original?.trigger_mode)" />
        <StatusBadge :info="contextModeBadge(original?.context_mode)" />
        <StatusBadge :info="contextScopeBadge" />
        <span v-if="dirty" class="badge border-amber-500/40 bg-amber-500/10 text-amber-300">
          有未保存的修改
        </span>
      </div>
      <div class="flex items-center gap-2">
        <button type="button" class="btn" :disabled="loading" @click="load">
          <AppIcon name="refresh" :size="15" :class="loading ? 'animate-spin' : ''" />
          重新加载
        </button>
        <button type="button" class="btn" :disabled="!dirty || saving" @click="reset">撤销修改</button>
        <button type="button" class="btn btn-primary" :disabled="!dirty || saving" @click="save">
          <span
            v-if="saving"
            class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/40 border-t-white"
          />
          保存策略
        </button>
      </div>
    </div>

    <p
      v-if="error"
      class="flex items-center gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
    >
      <AppIcon name="warning" :size="15" class="text-rose-400" />
      {{ error }}
    </p>

    <!-- 平台侧权限事实：不再用 .env 闸门，只做提示 -->
    <div
      v-if="original && original.platform && original.platform.known_groups > 0"
      class="rounded-xl border border-slate-800 bg-slate-900/60 px-4 py-3"
    >
      <div class="flex flex-wrap items-center gap-2">
        <AppIcon name="key" :size="16" class="text-sky-400" />
        <span class="text-sm font-medium text-slate-200">平台侧群权限</span>
        <StatusBadge :info="platformBadge" />
        <span class="text-xs text-slate-500">
          触发模式与上下文模式存在数据库里，直接生效；这里只展示平台实际会推送什么。
        </span>
      </div>
      <div class="mt-2 grid grid-cols-1 gap-2 md:grid-cols-2">
        <div v-if="fullAccessGroups.length" class="text-xs">
          <p class="mb-1 text-emerald-300">已开启「接收所有消息」</p>
          <ul class="space-y-0.5 text-slate-400">
            <li v-for="g in fullAccessGroups" :key="g.group_openid" class="flex items-center gap-1">
              <span class="truncate">{{ g.group_name || g.group_openid }}</span>
              <CopyButton :value="g.group_openid" label="群 OpenID" />
              <span
                class="badge ml-auto"
                :class="
                  g.allow_proactive_msg
                    ? 'border-emerald-500/40 text-emerald-300'
                    : 'border-rose-500/40 text-rose-300'
                "
                >{{ g.allow_proactive_msg ? '可主动推送' : '禁止主动推送' }}</span
              >
            </li>
          </ul>
        </div>
        <div v-if="limitedGroups.length" class="text-xs">
          <p class="mb-1 text-amber-300">仍为「仅 @机器人」</p>
          <ul class="space-y-0.5 text-slate-400">
            <li v-for="g in limitedGroups" :key="g.group_openid" class="flex items-center gap-1">
              <span class="truncate">{{ g.group_name || g.group_openid }}</span>
              <CopyButton :value="g.group_openid" label="群 OpenID" />
              <span class="ml-auto text-slate-500">{{ g.recv_msg_setting }}</span>
            </li>
          </ul>
        </div>
      </div>
      <p class="mt-2 text-xs text-slate-500">
        需要群管理员在 QQ 群设置里打开「接收所有消息」，机器人才会收到未 @ 它的消息。
        <button type="button" class="link" @click="load">重新读取</button>
      </p>
    </div>

    <div
      v-for="warning in warnings"
      :key="warning"
      class="flex items-center gap-2 rounded-lg border border-amber-500/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-200"
    >
      <AppIcon name="warning" :size="15" class="text-amber-400" />
      {{ warning }}
    </div>

    <!-- 触发模式 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="bolt" :size="16" class="text-sky-400" />
          触发模式（E1 / E2）
        </h2>
      </div>
      <div class="card-body grid grid-cols-1 gap-3 md:grid-cols-2">
        <label
          v-for="option in TRIGGER_OPTIONS"
          :key="option.value"
          class="cursor-pointer rounded-xl border px-4 py-3 transition"
          :class="
            draft.trigger_mode === option.value
              ? 'border-sky-500/60 bg-sky-500/10'
              : 'border-slate-800 bg-slate-950/40 hover:border-slate-700'
          "
        >
          <div class="flex items-center gap-2">
            <input
              v-model="draft.trigger_mode"
              type="radio"
              class="accent-sky-500"
              :value="option.value"
            />
            <span class="text-sm font-medium text-slate-100">{{ option.title }}</span>
            <span class="mono ml-auto text-slate-500">{{ option.value }}</span>
          </div>
          <p class="mt-1.5 pl-6 text-xs leading-relaxed text-slate-400">{{ option.desc }}</p>
          <p
            v-if="option.value === 'all_messages' && limitedGroups.length > 0"
            class="mt-1.5 ml-6 flex items-start gap-1.5 text-xs text-amber-300"
          >
            <AppIcon name="warning" :size="13" class="mt-0.5" />
            有 {{ limitedGroups.length }} 个群在平台侧仍是「仅 @机器人」：选择后对这些群不会生效。
          </p>
        </label>
      </div>
    </section>

    <!-- 上下文模式 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="users" :size="16" class="text-sky-400" />
          上下文模式（C1 / C2）
        </h2>
      </div>
      <div class="card-body grid grid-cols-1 gap-3 md:grid-cols-2">
        <label
          v-for="option in CONTEXT_OPTIONS"
          :key="option.value"
          class="cursor-pointer rounded-xl border px-4 py-3 transition"
          :class="
            draft.context_mode === option.value
              ? 'border-sky-500/60 bg-sky-500/10'
              : 'border-slate-800 bg-slate-950/40 hover:border-slate-700'
          "
        >
          <div class="flex items-center gap-2">
            <input
              v-model="draft.context_mode"
              type="radio"
              class="accent-sky-500"
              :value="option.value"
            />
            <span class="text-sm font-medium text-slate-100">{{ option.title }}</span>
            <span class="mono ml-auto text-slate-500">{{ option.value }}</span>
          </div>
          <p class="mt-1.5 pl-6 text-xs leading-relaxed text-slate-400">{{ option.desc }}</p>
          <p
            v-if="option.value === 'shared' && limitedGroups.length > 0"
            class="mt-1.5 ml-6 flex items-start gap-1.5 text-xs text-amber-300"
          >
            <AppIcon name="warning" :size="13" class="mt-0.5" />
            「仅 @机器人」的群里，共享上下文只会包含被 @ 的消息。
          </p>
        </label>
      </div>
    </section>

    <!-- 白名单与屏蔽词 -->
        <!-- 上下文范围 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="inbox" :size="16" class="text-violet-400" />
          上下文范围（模型能看到多少）
        </h2>
        <span class="text-xs text-slate-500">与触发模式解耦</span>
      </div>
      <div class="card-body space-y-3">
        <p class="text-xs text-slate-400">
          「要不要回复」由触发模式决定，「模型能看到什么」由这里决定。两者相互独立 ——
          可以让机器人只在被 @ 时开口，但仍然听得懂群里在聊什么。
        </p>
        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label
            v-for="option in SCOPE_OPTIONS"
            :key="option.value"
            class="cursor-pointer rounded-xl border px-4 py-3 transition"
            :class="
              draft.context_scope === option.value
                ? 'border-violet-500/60 bg-violet-500/10'
                : 'border-slate-800 bg-slate-950/40 hover:border-slate-700'
            "
          >
            <div class="flex items-center gap-2">
              <input
                v-model="draft.context_scope"
                type="radio"
                class="accent-violet-500"
                :value="option.value"
              />
              <span class="text-sm font-medium text-slate-100">{{ option.title }}</span>
              <span class="mono ml-auto text-slate-500">{{ option.value }}</span>
            </div>
            <p class="mt-1.5 pl-6 text-xs leading-relaxed text-slate-400">{{ option.desc }}</p>
          </label>
        </div>
        <p class="field-hint">
          注意上下文模式的影响：<b>每人独立（C1）</b>下每个成员只"听得到"自己的发言；
          想让它听懂整个群，需要配合 <b>整群共享（C2）</b>。
          纳入旁观内容会明显增加 token 消耗，必要时调大「上下文最大轮次」。
        </p>
      </div>
    </section>

    <!-- 引用回复 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="chatAlt" :size="16" class="text-sky-400" />
          引用回复
        </h2>
        <span class="text-xs text-slate-500">默认开启</span>
      </div>
      <div class="card-body space-y-3">
        <ToggleField
          v-model="draft.quote_reply"
          label="回答时引用触发它的那条消息 quote_reply"
          hint="开启后，回复会以「引用」的形式挂在触发消息下面，上下文一目了然；即时回复（占位语）始终不引用。"
        />
        <ul class="space-y-1 text-xs text-slate-400">
          <li>· 这是全局默认值；每个群可以在「会话详情」里单独开关，覆盖表里也能改。</li>
          <li>· 主动消息（含后台手动发送）同样支持引用。</li>
          <li>· 升级前写入的老消息没有平台引用索引，无法被引用。</li>
        </ul>
      </div>
    </section>

    <!-- 与其他机器人共存 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="cpu" :size="16" class="text-violet-400" />
          与其他机器人共存
        </h2>
        <span class="text-xs text-slate-500">默认允许</span>
      </div>
      <div class="card-body space-y-3">
        <ToggleField
          v-model="draft.reply_to_bots"
          label="回复其他机器人的消息 reply_to_bots"
          hint="开启后，群里另一个机器人 @ 我们时会正常回复；关闭则只把它的发言记录进会话时间线，不回应。"
        />
        <ul class="space-y-1 text-xs text-slate-400">
          <li>· 只有明确 @ 到我们的机器人消息才会触发回复；它平时的发言一律只记录、不接话。</li>
          <li>· 连续互回 5 次后会主动停下并等待真人插话，避免两个机器人无限对刷。</li>
          <li>· 我们自己发出的消息（平台回显）始终忽略，不会自问自答。</li>
        </ul>
      </div>
    </section>

<section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="shield" :size="16" class="text-sky-400" />
          名单与屏蔽词
        </h2>
        <span class="text-xs text-slate-500">一行一个，空 = 不限制</span>
      </div>
      <div class="card-body grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div>
          <label class="label" for="allow-groups">群白名单 group_allowlist</label>
          <textarea
            id="allow-groups"
            v-model="draft.group_allowlist"
            class="input mono h-28 resize-y"
            placeholder="留空表示不限制；每行一个 group_openid"
            spellcheck="false"
          />
          <p class="field-hint">已填 {{ parseList(draft.group_allowlist).length }} 项</p>
        </div>
        <div>
          <label class="label" for="allow-members">成员白名单 member_allowlist</label>
          <textarea
            id="allow-members"
            v-model="draft.member_allowlist"
            class="input mono h-28 resize-y"
            placeholder="留空表示不限制；每行一个 member_openid"
            spellcheck="false"
          />
          <p class="field-hint">已填 {{ parseList(draft.member_allowlist).length }} 项</p>
        </div>
        <div>
          <label class="label" for="admin-openids">管理员 OpenID admin_openids</label>
          <textarea
            id="admin-openids"
            v-model="draft.admin_openids"
            class="input mono h-28 resize-y"
            placeholder="每行一个 OpenID，建议填 user_openid（跨群稳定）"
            spellcheck="false"
          />
          <p class="field-hint">
            已填 {{ parseList(draft.admin_openids).length }} 项。
            列在这里的人可以使用管理员专属能力（目前是容器内执行 <span class="mono">run_shell</span>）。
            <strong class="text-slate-300">留空 = 没有管理员</strong>，不是"不限制"。
            填 <span class="mono">member_openid</span> 只在该群生效，建议用
            <span class="mono">user_openid</span>。
            管理员还可以在对话里让 AI 读写本节以外的运行配置（工具
            <span class="mono">settings_read</span> / <span class="mono">settings_write</span>）；
            密钥与这份名单本身只能在这里改。
          </p>
        </div>
        <div>
          <label class="label" for="blocked">屏蔽词 blocked_keywords</label>
          <textarea
            id="blocked"
            v-model="draft.blocked_keywords"
            class="input mono h-28 resize-y"
            placeholder="每行一个关键词；命中后不回复"
            spellcheck="false"
          />
          <p class="field-hint">已填 {{ parseList(draft.blocked_keywords).length }} 项</p>
        </div>
      </div>
    </section>

    <!-- 按群覆盖 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="list" :size="16" class="text-sky-400" />
          按群覆盖 group_overrides
        </h2>
        <button type="button" class="btn btn-sm" @click="addOverride">
          <AppIcon name="plus" :size="14" />
          新增覆盖
        </button>
      </div>

      <div class="card-body">
        <p
          v-if="draft.group_overrides.length === 0"
          class="rounded-lg border border-dashed border-slate-800 px-4 py-8 text-center text-sm text-slate-500"
        >
          暂无按群覆盖，所有群使用全局策略。
        </p>

        <div v-else class="table-wrap">
          <table class="table-base">
            <thead>
              <tr>
                <th class="th min-w-[16rem]">group_openid</th>
                <th class="th">启用</th>
                <th class="th min-w-[10rem]">触发模式</th>
                <th class="th min-w-[10rem]">上下文模式</th>
                <th class="th min-w-[10rem]">上下文范围</th>
                <th class="th min-w-[8rem]">工具 tools</th>
                <th class="th min-w-[8rem]">主动 proactive</th>
                <th class="th min-w-[8rem]">定时 tasks</th>
                <th class="th min-w-[8rem]">记忆 memory</th>
                <th class="th min-w-[8rem]">人味 humanize</th>
                <th class="th min-w-[8rem]">联网 web</th>
                <th class="th min-w-[8rem]">执行 exec</th>
                <th class="th min-w-[8rem]">润色 polish</th>
                <th class="th min-w-[9rem]">机器人共存</th>
                <th class="th min-w-[9rem]">引用回复</th>
                <th class="th min-w-[9rem]">群 QPM 限流</th>
                <th class="th text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(row, index) in draft.group_overrides" :key="index" class="row-hover">
                <td class="td">
                  <input
                    v-model="row.group_openid"
                    class="input mono"
                    type="text"
                    placeholder="群 OpenID"
                    spellcheck="false"
                    :class="row.group_openid.trim() ? '' : 'border-rose-500/60'"
                  />
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.enabled === null || row.enabled === undefined ? '' : String(row.enabled)"
                    @change="setOverrideEnabled(row, ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随默认（启用）</option>
                    <option value="true">启用</option>
                    <option value="false">暂停</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.trigger_mode ?? ''"
                    @change="setOverrideTrigger(row, ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="at_only">仅 @机器人</option>
                    <option value="all_messages">群内任意消息</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.context_mode ?? ''"
                    @change="setOverrideContext(row, ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="per_member">每人独立</option>
                    <option value="shared">整群共享</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.context_scope ?? ''"
                    @change="setOverrideScope(row, ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="addressed_only">仅 @ 对话</option>
                    <option value="include_unaddressed">含未 @ 消息</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.tools === null || row.tools === undefined ? '' : String(row.tools)"
                    @change="setOverrideBool(row, 'tools', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">开启</option>
                    <option value="false">关闭</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.proactive ?? ''"
                    @change="setOverrideLevel(row, 'proactive', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="off">关闭</option>
                    <option value="auto">自动</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.tasks === null || row.tasks === undefined ? '' : String(row.tasks)"
                    @change="setOverrideBool(row, 'tasks', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">开启</option>
                    <option value="false">关闭</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.memory === null || row.memory === undefined ? '' : String(row.memory)"
                    @change="setOverrideBool(row, 'memory', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">开启</option>
                    <option value="false">关闭</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.humanize ?? ''"
                    @change="setOverrideLevel(row, 'humanize', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="off">off</option>
                    <option value="soft">soft</option>
                    <option value="medium">medium</option>
                    <option value="strong">strong</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.web === null || row.web === undefined ? '' : String(row.web)"
                    @change="setOverrideBool(row, 'web', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">开启</option>
                    <option value="false">关闭</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.exec === null || row.exec === undefined ? '' : String(row.exec)"
                    @change="setOverrideBool(row, 'exec', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">开启</option>
                    <option value="false">关闭</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.polish === null || row.polish === undefined ? '' : String(row.polish)"
                    @change="setOverrideBool(row, 'polish', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">开启</option>
                    <option value="false">关闭</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.reply_to_bots === null || row.reply_to_bots === undefined ? '' : String(row.reply_to_bots)"
                    @change="setOverrideBool(row, 'reply_to_bots', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">回复其它 bot</option>
                    <option value="false">不回复 bot</option>
                  </select>
                </td>
                <td class="td">
                  <select
                    class="input"
                    :value="row.quote_reply === null || row.quote_reply === undefined ? '' : String(row.quote_reply)"
                    @change="setOverrideBool(row, 'quote_reply', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">跟随全局</option>
                    <option value="true">引用回复</option>
                    <option value="false">不引用</option>
                  </select>
                </td>
                <td class="td">
                  <input
                    class="input"
                    type="number"
                    min="1"
                    placeholder="未设置"
                    :value="row.rate_limit_group_qpm ?? ''"
                    @input="setOverrideQpm(row, ($event.target as HTMLInputElement).value)"
                  />
                </td>
                <td class="td text-right">
                  <button
                    type="button"
                    class="btn btn-sm btn-danger"
                    title="删除该行"
                    @click="removeOverride(index)"
                  >
                    <AppIcon name="trash" :size="14" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <div class="flex items-center justify-end gap-2">
      <button type="button" class="btn" :disabled="!dirty || saving" @click="reset">撤销修改</button>
      <button type="button" class="btn btn-primary" :disabled="!dirty || saving" @click="save">
        保存策略
      </button>
    </div>
  </div>
</template>
