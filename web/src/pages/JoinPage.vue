<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import NumberField from '@/components/NumberField.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TextField from '@/components/TextField.vue'
import ToggleField from '@/components/ToggleField.vue'
import { configApi, conversationsApi, joinApi, toMessage } from '@/api'
import type {
  Conversation,
  EditableConfig,
  JoinDecisionItem,
  JoinGateTopic,
  JoinTopicsState,
} from '@/api/types'
import {
  assign,
  buildPatch,
  emptyDraft,
  type EditableDraft,
} from '@/composables/configContext'
import { toast } from '@/composables/useToast'
import { formatDateTime, formatRelative, shortId } from '@/utils/format'

/**
 * 自主加入（join_gate）管理页。
 *
 * 默认关闭：未启用时只显示引导与配置，不把「没有决策」误报成故障。
 * 决策列表与话题面板来自后端只读接口；配置只提交被改动的键（复用
 * configContext 的脏字段语义，数字留空 = 不提交）。
 */

const MODE_OPTIONS = [
  { value: 'llm', label: 'llm · 交给模型判断' },
  { value: 'off', label: 'off · 只记录，不评估' },
]

/** 三分类判定的展示文案：join 插话 / wait 等一等 / skip 不插话。 */
const ACTION_LABELS: Record<string, string> = {
  join: '插话',
  wait: '等一等',
  skip: '不插话',
}
const ACTION_CLASSES: Record<string, string> = {
  join: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  wait: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  skip: 'border-slate-700 bg-slate-800/60 text-slate-400',
}

/** 话题类型（模型输出，可选；缺省按 other）。 */
const TOPIC_TYPE_LABELS: Record<string, string> = {
  help: '求助',
  chat: '闲聊',
  question: '提问',
  plan: '计划',
  conflict: '冲突',
  joke: '玩笑',
  other: '其他',
}

/**
 * 话题生命周期（后端会按最后一条消息的时间只降不升）。
 * 无 status（契约 v2 之前的老数据）按 active 展示，绝不能把空值渲染成空白徽章。
 */
const TOPIC_STATUS_LABELS: Record<string, string> = {
  active: '进行中',
  dormant: '暂歇',
  closed: '已结束',
}
const TOPIC_STATUS_CLASSES: Record<string, string> = {
  active: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  dormant: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  closed: 'border-slate-600/50 bg-slate-600/10 text-slate-400',
}

/** 判定的 addressed_to：模型在判断这条消息是冲着谁说的（接口有才展示）。 */
const ADDRESSED_LABELS: Record<string, string> = {
  bot: '对机器人',
  group: '对群里',
  someone_else: '对别人',
  unknown: '对象不明',
}
const ADDRESSED_CLASSES: Record<string, string> = {
  bot: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  group: 'border-sky-500/40 bg-sky-500/10 text-sky-300',
  someone_else: 'border-slate-700 bg-slate-800/60 text-slate-400',
  unknown: 'border-slate-700 bg-slate-800/60 text-slate-400',
}

/**
 * 决策的 action：契约 v2 之后后端直接返回；
 * 老数据里只有 joined（没有 action 列），按 joined 兜底成 join / skip。
 */
function decisionAction(decision: JoinDecisionItem): string {
  const action = (decision.action ?? '').trim()
  if (action === 'join' || action === 'wait' || action === 'skip') return action
  return decision.joined ? 'join' : 'skip'
}

function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

function actionClass(action: string): string {
  return ACTION_CLASSES[action] ?? ACTION_CLASSES.skip
}

function topicTypeLabel(type?: string | null): string {
  if (!type) return '其他'
  return TOPIC_TYPE_LABELS[type] ?? type
}

/** 归一化话题状态：空值 / 老数据 / 未知值都按 active（后端解析时也这样兜底）。 */
function topicStatus(topic: JoinGateTopic): 'active' | 'dormant' | 'closed' {
  const raw = (topic.status ?? '').trim().toLowerCase()
  if (raw === 'dormant' || raw === 'closed') return raw
  return 'active'
}

function topicStatusLabel(status: string): string {
  return TOPIC_STATUS_LABELS[status] ?? TOPIC_STATUS_LABELS.active
}

function topicStatusClass(status: string): string {
  return TOPIC_STATUS_CLASSES[status] ?? TOPIC_STATUS_CLASSES.active
}

/**
 * resumable 标记：只有 dormant 需要明确「能不能被新消息接回」。
 * closed 永远不可接回（后端强制），用「不要提」表达；active 不需要标记。
 */
function topicResumableMark(topic: JoinGateTopic): string {
  switch (topicStatus(topic)) {
    case 'dormant':
      return topic.resumable ? '可接回' : '不可接回'
    case 'closed':
      return '不要提'
    default:
      return ''
  }
}

/** 话题的一句话状态：暂歇，可接回 / 已结束，不要提。 */
function topicStatusSummary(topic: JoinGateTopic): string {
  const label = topicStatusLabel(topicStatus(topic))
  const mark = topicResumableMark(topic)
  return mark ? `${label}，${mark}` : label
}

function addressedToLabel(value?: string | null): string {
  const raw = (value ?? '').trim()
  if (!raw) return ''
  return ADDRESSED_LABELS[raw] ?? raw
}

function addressedToClass(value?: string | null): string {
  const raw = (value ?? '').trim()
  return ADDRESSED_CLASSES[raw] ?? ADDRESSED_CLASSES.unknown
}

/* ------------------------------------------------------------------ *
 * 配置（PUT /api/config）
 * ------------------------------------------------------------------ */

const draft = reactive<EditableDraft>(emptyDraft())
const loaded = ref<EditableDraft>(emptyDraft())
const configLoading = ref(false)
const configSaving = ref(false)
const configError = ref('')

const configPatch = computed(() => buildPatch(draft, loaded.value))
const configDirty = computed(() => Object.keys(configPatch.value).length > 0)
const enabled = computed(() => draft.join_gate_enabled)

async function loadConfig() {
  configLoading.value = true
  configError.value = ''
  try {
    const data = await configApi.get()
    const editable = data.editable ?? {}
    assign(draft, editable)
    assign(loaded.value, editable)
  } catch (err) {
    configError.value = toMessage(err)
  } finally {
    configLoading.value = false
  }
}

async function applyConfig(patch: Partial<EditableConfig>, success: string) {
  configSaving.value = true
  configError.value = ''
  try {
    const outcome = await configApi.update(patch)
    assign(draft, outcome.editable)
    assign(loaded.value, outcome.editable)
    if (outcome.warnings.length > 0) {
      toast.warn(`已保存，但：${outcome.warnings.join('；')}`)
    } else {
      toast.success(success)
    }
  } catch (err) {
    configError.value = toMessage(err)
    toast.error(configError.value)
    throw err
  } finally {
    configSaving.value = false
  }
}

async function saveConfig() {
  if (!configDirty.value || configSaving.value) return
  try {
    await applyConfig(configPatch.value, '自主加入配置已保存')
  } catch {
    // 错误已经显示在配置卡片里
  }
}

function discardConfig() {
  assign(draft, loaded.value)
  toast.info('已撤销未保存的自主加入配置')
}

/**
 * 一键启用 / 停用。
 *
 * 顶部按钮同时承担「保存」语义：把配置区未保存的改动一起带上，
 * 避免一键操作悄悄丢弃用户刚调过的参数。启用时如果模式是 off，
 * 顺手切回 llm（否则「已启用」但永远不会评估）。
 */
async function toggleGate() {
  if (configSaving.value) return
  const next = !draft.join_gate_enabled
  const patch: Partial<EditableConfig> = {
    ...configPatch.value,
    join_gate_enabled: next,
  }
  if (next && draft.join_gate_mode === 'off') patch.join_gate_mode = 'llm'
  try {
    await applyConfig(patch, next ? '已启用自主加入' : '已停用自主加入')
    if (next) await loadData()
  } catch {
    // 错误已经显示在配置卡片里
  }
}

/* ------------------------------------------------------------------ *
 * 群选择（从会话去重 group_openid）
 * ------------------------------------------------------------------ */

interface GroupOption {
  openid: string
  name: string
  conversations: number
}

const conversations = ref<Conversation[]>([])
const conversationsLoading = ref(false)
const conversationsError = ref('')

const groupOptions = computed<GroupOption[]>(() => {
  const map = new Map<string, GroupOption>()
  for (const conversation of conversations.value) {
    const openid = conversation.group_openid ?? ''
    if (!openid) continue
    const existing = map.get(openid)
    if (existing) {
      existing.conversations += 1
      if (!existing.name && conversation.group_name) existing.name = conversation.group_name
      continue
    }
    map.set(openid, {
      openid,
      name: conversation.group_name ?? '',
      conversations: 1,
    })
  }
  return Array.from(map.values()).sort((a, b) =>
    (a.name || a.openid).localeCompare(b.name || b.openid, 'zh-CN'),
  )
})

const selectedGroup = ref('')
const manualGroup = ref('')

/** 手输优先：允许选一个会话里不存在的群号（历史群 / 平台群）。 */
const effectiveGroup = computed(() => manualGroup.value.trim() || selectedGroup.value)

const selectedGroupLabel = computed(() => {
  const openid = effectiveGroup.value
  if (!openid) return '全部群'
  const found = groupOptions.value.find((item) => item.openid === openid)
  return found?.name ? `${found.name}（${shortId(openid, 8, 6)}）` : shortId(openid, 8, 6)
})

/** 群下拉的选项：空值 = 不筛选，其余按 group_openid 去重。 */
const groupSelectOptions = computed(() => [
  { value: '', label: '全部群（不筛选）' },
  ...groupOptions.value.map((group) => ({
    value: group.openid,
    label: `${group.name || shortId(group.openid, 10, 6)}（${group.conversations} 个会话）`,
    desc: group.name ? group.openid : undefined,
  })),
])

async function loadGroups() {
  conversationsLoading.value = true
  conversationsError.value = ''
  try {
    const data = await conversationsApi.list({ page: 1, page_size: 200 })
    conversations.value = Array.isArray(data?.items) ? data.items : []
  } catch (err) {
    conversationsError.value = toMessage(err)
  } finally {
    conversationsLoading.value = false
  }
}

/* ------------------------------------------------------------------ *
 * 最近的加入决策与话题
 * ------------------------------------------------------------------ */

const decisions = ref<JoinDecisionItem[]>([])
const decisionsLoading = ref(false)
const decisionsError = ref('')

const topicsState = ref<JoinTopicsState | null>(null)
const topicsLoading = ref(false)
const topicsError = ref('')

/** 可选布尔字段容错：后端是 bool，JSON 字符串化 / 老数据里可能是 "true" / "false"。 */
function optionalBool(raw: unknown): boolean | undefined {
  if (typeof raw === 'boolean') return raw
  if (raw === 'true') return true
  if (raw === 'false') return false
  return undefined
}

function parseTopicList(raw: unknown): JoinGateTopic[] {
  let value = raw
  if (typeof value === 'string') {
    const text = value.trim()
    if (!text) return []
    try {
      value = JSON.parse(text)
    } catch {
      return []
    }
  }
  // 兼容 {topics:[…]} / {data:{topics:[…]}} 两种包装。
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    const record = value as Record<string, unknown>
    value = record.topics ?? record.data ?? []
  }
  if (!Array.isArray(value)) return []
  return value.flatMap((item): JoinGateTopic[] => {
    if (!item || typeof item !== 'object') return []
    const record = item as Record<string, unknown>
    const ids = Array.isArray(record.message_ids)
      ? record.message_ids
          .map((id) => Number(id))
          .filter((id) => Number.isFinite(id))
      : []
    return [
      {
        id: String(record.id ?? ''),
        title: String(record.title ?? ''),
        summary: String(record.summary ?? ''),
        message_ids: ids,
        // type / status / resumable 都可选：老数据没有这些字段，渲染时按
        // other / active / 未标记 兜底，绝不能因为多一个未知枚举丢整条记录。
        type: record.type ? String(record.type) : undefined,
        status: record.status ? String(record.status) : undefined,
        resumable: optionalBool(record.resumable),
      },
    ]
  })
}

const latestTopics = computed(() => parseTopicList(topicsState.value?.topics))

function topicsOf(decision: JoinDecisionItem): JoinGateTopic[] {
  return parseTopicList(decision.topics)
}

function confidenceText(value: number): string {
  const number = Number(value)
  if (!Number.isFinite(number)) return '—'
  return `${Math.round(number * 100)}%`
}

function decisionTopicsLabel(decision: JoinDecisionItem): string {
  const topics = topicsOf(decision)
  if (topics.length === 0) return ''
  return topics
    .map(
      (topic) =>
        `${topic.title || topic.id}（${topicTypeLabel(topic.type)} · ${topicStatusSummary(topic)}）`,
    )
    .filter(Boolean)
    .join(' · ')
}

async function loadDecisions() {
  decisionsLoading.value = true
  decisionsError.value = ''
  try {
    decisions.value = await joinApi.decisions(effectiveGroup.value, 50)
  } catch (err) {
    decisionsError.value = toMessage(err)
    decisions.value = []
  } finally {
    decisionsLoading.value = false
  }
}

async function loadTopics() {
  const group = effectiveGroup.value
  if (!group) {
    topicsState.value = null
    topicsError.value = ''
    return
  }
  topicsLoading.value = true
  topicsError.value = ''
  try {
    topicsState.value = await joinApi.topics(group)
  } catch (err) {
    topicsError.value = toMessage(err)
    topicsState.value = null
  } finally {
    topicsLoading.value = false
  }
}

async function loadData() {
  await Promise.all([loadDecisions(), loadTopics()])
}

function refreshData() {
  void loadData()
}

function onGroupChange() {
  void loadData()
}

onMounted(async () => {
  await loadConfig()
  void loadGroups()
  await loadData()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部：状态 / 一键开关 / 说明 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="users" :size="16" class="text-sky-400" />
          自主加入
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <StatusBadge
            :info="
              enabled
                ? { label: '已启用', tone: 'emerald' }
                : { label: '默认关闭', tone: 'slate' }
            "
            dot
          />
          <button
            type="button"
            class="btn"
            :class="enabled ? 'btn-ghost' : 'btn-primary'"
            :disabled="configSaving || configLoading"
            @click="toggleGate"
          >
            <AppIcon :name="enabled ? 'pause' : 'sparkles'" :size="15" />
            {{ enabled ? '一键停用' : '一键启用' }}
          </button>
        </div>
      </div>
      <div class="card-body space-y-2">
        <p class="text-sm leading-relaxed text-slate-400">
          群里没有 @ 机器人的消息，可以交给模型判断「现在插话有没有价值」：被点名求助、
          话题延续且它能补充时加入；闲聊、争吵、广告与无价值内容默认不插话。
          <b class="text-slate-300">默认关闭</b>，关闭时与旧版本行为完全一致。
        </p>
        <ul class="space-y-1 text-xs text-slate-500">
          <li>· 只在群聊生效；私聊、命令、空消息与其他机器人的发言不评估。</li>
          <li>
            · 每次评估最多调用一次模型（temperature 0.1、输出只有 JSON、不生成回复）；
            同群有评估冷却，另有每小时 / 每天加入上限。
          </li>
          <li>· 按群覆盖在「策略配置」的群卡片里（join_gate 字段）：全局关时，按群开启不会越过总开关。</li>
        </ul>
        <p v-if="configError" class="flex items-center gap-2 text-xs text-rose-300">
          <AppIcon name="warning" :size="14" />
          {{ configError }}
        </p>
      </div>
    </section>

    <!-- 配置区：脏字段保存 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="cog" :size="16" class="text-sky-400" />
          配置
        </h2>
        <span class="text-xs" :class="configDirty ? 'text-amber-300' : 'text-slate-500'">
          {{ configDirty ? '有改动尚未保存' : '只提交被改动的字段' }}
        </span>
      </div>
      <div class="card-body space-y-4">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <ToggleField
            v-model="draft.join_gate_enabled"
            label="启用 join_gate_enabled"
            hint="全局总开关；关闭后所有群都不再评估（按群覆盖只能进一步收窄）"
          />
          <SelectMenu
            id="join-mode"
            v-model="draft.join_gate_mode"
            label="模式 join_gate_mode"
            :options="MODE_OPTIONS"
            hint="off 仍会记录消息，但不调用模型评估"
          />
          <TextField
            v-model="draft.join_gate_model"
            label="评估模型 join_gate_model"
            mono
            placeholder="留空 = 主模型"
            hint="建议用更便宜的模型；留空沿用主模型"
          />
          <NumberField
            v-model="draft.join_gate_window"
            label="消息窗口 join_gate_window"
            :min="5"
            :max="50"
            hint="评估时取最近多少条群消息（5–50，默认 20）"
          />
          <NumberField
            v-model="draft.join_gate_confidence"
            label="最低置信度 join_gate_confidence"
            :min="0"
            :max="1"
            :step="0.01"
            hint="模型给出的置信度低于它就不加入（0–1，默认 0.7）"
          />
          <NumberField
            v-model="draft.join_gate_eval_cooldown_seconds"
            label="评估冷却 join_gate_eval_cooldown_seconds（秒）"
            :min="10"
            :max="3600"
            hint="同群两次评估的最小间隔（10–3600，默认 60）"
          />
          <NumberField
            v-model="draft.join_gate_wait_retry_seconds"
            label="wait 重试 join_gate_wait_retry_seconds（秒）"
            :min="10"
            :max="300"
            hint="上次判定「等一等」时，满这个间隔可由该群的下一条新消息提前重评估（10–300，默认 30）"
          />
          <NumberField
            v-model="draft.join_gate_topic_dormant_minutes"
            label="话题休眠 join_gate_topic_dormant_minutes（分钟）"
            :min="5"
            :max="240"
            hint="话题超过这么久没有新消息就不再算「进行中」（5–240，默认 20）"
          />
          <NumberField
            v-model="draft.join_gate_topic_closed_minutes"
            label="话题结束 join_gate_topic_closed_minutes（分钟）"
            :min="30"
            :max="1440"
            hint="话题超过这么久没有新消息算已结束（30–1440，默认 180，必须大于休眠阈值）"
          />
          <NumberField
            v-model="draft.join_gate_min_interval_minutes"
            label="加入最小间隔 join_gate_min_interval_minutes（分钟）"
            :min="1"
            :max="240"
            hint="两次加入之间的最小间隔（1–240，默认 5）"
          />
          <NumberField
            v-model="draft.join_gate_max_per_hour"
            label="每小时上限 join_gate_max_per_hour"
            :min="1"
            :max="30"
            hint="每小时最多加入几次（1–30，默认 3）"
          />
          <NumberField
            v-model="draft.join_gate_max_per_day"
            label="每天上限 join_gate_max_per_day"
            :min="1"
            :max="100"
            hint="每天最多加入几次（1–100，默认 12）"
          />
        </div>

        <p class="field-hint">
          保存走 <span class="mono">PUT /api/config</span>，只发送改动的键；数字框留空 = 不提交，
          不会把后端已有值清成 0。范围与后端校验一致。
        </p>

        <div class="flex flex-wrap items-center justify-end gap-2 border-t border-slate-800 pt-3">
          <span class="mr-auto text-xs text-slate-500">
            评估成本：每次最多一次模型调用；冷却与上限先在本地挡住，超限时不调模型。
          </span>
          <button
            type="button"
            class="btn btn-sm"
            :disabled="!configDirty || configSaving"
            @click="discardConfig"
          >
            放弃修改
          </button>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="!configDirty || configSaving"
            @click="saveConfig"
          >
            {{ configSaving ? '保存中…' : '保存配置' }}
          </button>
        </div>
      </div>
    </section>

    <!-- 群选择 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="users" :size="16" class="text-sky-400" />
          群选择
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="btn btn-sm"
            :disabled="conversationsLoading || decisionsLoading"
            @click="loadGroups(); refreshData()"
          >
            <AppIcon
              name="refresh"
              :size="14"
              :class="conversationsLoading || decisionsLoading ? 'animate-spin' : ''"
            />
            刷新
          </button>
        </div>
      </div>
      <div class="card-body space-y-3">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <SelectMenu
              id="join-group"
              v-model="selectedGroup"
              label="从会话中选择群"
              placeholder="全部群（不筛选）"
              :options="groupSelectOptions"
              :disabled="conversationsLoading"
              :hint="`来自「会话管理」的群会话，按 group_openid 去重；当前：${selectedGroupLabel}`"
              @change="onGroupChange"
            />
          </div>
          <div>
            <label class="label" for="join-manual">或手动输入 group_openid</label>
            <input
              id="join-manual"
              v-model="manualGroup"
              class="input mono"
              type="text"
              placeholder="手输优先于上面的选择"
              spellcheck="false"
              @change="onGroupChange"
            />
            <p class="field-hint">留空则使用上方选择；用于还没有会话记录的历史群</p>
          </div>
        </div>
        <p v-if="conversationsError" class="flex items-center gap-2 text-xs text-rose-300">
          <AppIcon name="warning" :size="14" />
          会话列表获取失败：{{ conversationsError }}
        </p>
        <p v-else-if="!conversationsLoading && groupOptions.length === 0" class="text-xs text-slate-500">
          暂无群会话记录，可直接在右侧手动输入 group_openid。
        </p>
      </div>
    </section>

    <!-- 最新话题面板 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="sparkles" :size="16" class="text-violet-400" />
          最新话题
        </h2>
        <span class="text-xs text-slate-500">
          {{ topicsState?.analyzed_at ? `分析于 ${formatRelative(topicsState.analyzed_at)}` : '仅展示该群最近一次分析' }}
        </span>
      </div>
      <div class="card-body">
        <p v-if="!effectiveGroup" class="text-xs text-slate-500">
          选择一个群后显示它最近一次话题切分（标题 / 摘要 / 消息 id）。
        </p>
        <p v-else-if="topicsLoading" class="text-xs text-slate-500">加载最新话题…</p>
        <p v-else-if="topicsError" class="flex items-center gap-2 text-xs text-rose-300">
          <AppIcon name="warning" :size="14" />
          {{ topicsError }}
        </p>
        <EmptyState
          v-else-if="latestTopics.length === 0"
          title="还没有话题分析"
          hint="开启自主加入并触发评估后，模型会在同一次调用里切分话题；这里显示最近一次结果。"
          icon="inbox"
        />
        <div v-else class="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <div
            v-for="topic in latestTopics"
            :key="topic.id || topic.title"
            class="rounded-lg border border-slate-800 bg-slate-950/40 px-3 py-2"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-sm font-medium text-slate-200">{{ topic.title || '（无标题）' }}</span>
              <span class="badge border-violet-500/40 bg-violet-500/10 text-violet-300">
                {{ topicTypeLabel(topic.type) }}
              </span>
              <span class="badge" :class="topicStatusClass(topicStatus(topic))">
                {{ topicStatusLabel(topicStatus(topic)) }}
              </span>
              <span
                v-if="topicResumableMark(topic)"
                class="badge"
                :class="topicStatus(topic) === 'closed'
                  ? 'border-slate-700 bg-slate-800/60 text-slate-400'
                  : 'border-sky-500/40 bg-sky-500/10 text-sky-300'"
              >
                {{ topicResumableMark(topic) }}
              </span>
              <span class="mono ml-auto text-xs text-slate-500">{{ topic.id }}</span>
            </div>
            <p class="mt-1 text-xs text-slate-400">{{ topic.summary || '（无摘要）' }}</p>
            <p class="mt-1 text-[11px] text-slate-500">
              消息 id：{{ topic.message_ids.length ? topic.message_ids.join(', ') : '—' }}
            </p>
          </div>
        </div>
      </div>
    </section>

    <!-- 最近决策 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="list" :size="16" class="text-sky-400" />
          最近决策
        </h2>
        <span class="text-xs text-slate-500">
          {{ effectiveGroup ? selectedGroupLabel : '全部群' }} · 最近 50 条
        </span>
      </div>
      <div class="card-body">
        <p v-if="decisionsLoading" class="text-xs text-slate-500">加载决策…</p>
        <p v-else-if="decisionsError" class="flex items-center gap-2 text-xs text-rose-300">
          <AppIcon name="warning" :size="14" />
          {{ decisionsError }}
        </p>
        <EmptyState
          v-else-if="decisions.length === 0"
          title="还没有加入决策"
          :hint="
            enabled
              ? '收到未 @ 机器人的群消息后，模型会评估是否加入；每次评估（含跳过）都会记录在这里。'
              : '自主加入尚未启用。点击上方「一键启用」，或先调整配置后保存。'
          "
          icon="inbox"
        >
          <button
            v-if="!enabled"
            type="button"
            class="btn btn-primary"
            :disabled="configSaving"
            @click="toggleGate"
          >
            <AppIcon name="sparkles" :size="15" />
            一键启用自主加入
          </button>
        </EmptyState>
        <ul v-else class="space-y-2">
          <li
            v-for="decision in decisions"
            :key="decision.id"
            class="rounded-lg border border-slate-800 bg-slate-950/40 px-3 py-2"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span class="badge" :class="actionClass(decisionAction(decision))">
                {{ actionLabel(decisionAction(decision)) }}
                <span class="mono text-[11px] opacity-70">{{ decisionAction(decision) }}</span>
              </span>
              <span class="text-xs text-slate-400">置信度 {{ confidenceText(decision.confidence) }}</span>
              <span
                v-if="addressedToLabel(decision.addressed_to)"
                class="badge"
                :class="addressedToClass(decision.addressed_to)"
                :title="`addressed_to: ${decision.addressed_to}`"
              >
                {{ addressedToLabel(decision.addressed_to) }}
              </span>
              <span v-if="decision.speaker" class="text-xs text-slate-500">{{ decision.speaker }}</span>
              <span class="ml-auto text-xs text-slate-500" :title="formatDateTime(decision.created_at)">
                {{ formatRelative(decision.created_at) }}
              </span>
            </div>
            <p class="mt-1 text-sm text-slate-200">{{ decision.preview || '（空消息）' }}</p>
            <p v-if="decision.reason" class="mt-1 text-xs text-slate-400">原因：{{ decision.reason }}</p>
            <p class="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-slate-500">
              <span class="mono">{{ shortId(decision.group_openid, 8, 6) }}</span>
              <span v-if="decision.model" class="mono">{{ decision.model }}</span>
              <span v-if="decision.latency_ms">{{ decision.latency_ms }} ms</span>
              <span v-if="decisionTopicsLabel(decision)" class="text-violet-300">
                话题：{{ decisionTopicsLabel(decision) }}
              </span>
            </p>
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>
