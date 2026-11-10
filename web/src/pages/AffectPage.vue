<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import NumberField from '@/components/NumberField.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { affectApi, configApi, toMessage } from '@/api'
import type {
  AffectBehavior,
  AffectConversation,
  AffectEvent,
  AffectHistoryPoint,
  AffectPack,
  AffectPackDimension,
  AffectState,
  EditableConfig,
} from '@/api/types'
import {
  assign,
  buildPatch,
  emptyDraft,
  type EditableDraft,
} from '@/composables/configContext'
import { toast } from '@/composables/useToast'
import {
  formatDateTime,
  formatRelative,
  formatShortDateTime,
  shortId,
} from '@/utils/format'

/**
 * 情绪状态（Affect）管理页。
 *
 * 默认关闭：未启用时只显示引导，不请求会话状态（避免给旧后端制造无意义的 404）。
 * 启用后按会话展示 5 维数值、行为摘要、历史曲线、最近事件与 line/full 注入预览。
 * 配置只提交被改动的键（复用 configContext 的脏字段语义）。
 */

interface DimensionView {
  key: string
  label: string
  color: string
  bar: string
}

/** 内容包固定 5 维；后端 pack 只提供 label 覆盖，key 必须稳定。 */
const DIMENSION_FALLBACK: DimensionView[] = [
  { key: 'valence', label: '心情', color: '#38bdf8', bar: 'bg-sky-500/80' },
  { key: 'energy', label: '精力', color: '#fbbf24', bar: 'bg-amber-500/80' },
  { key: 'stress', label: '压力', color: '#fb7185', bar: 'bg-rose-500/80' },
  { key: 'closeness', label: '熟悉度', color: '#34d399', bar: 'bg-emerald-500/80' },
  { key: 'patience', label: '耐心', color: '#a78bfa', bar: 'bg-violet-500/80' },
]

const PRESET_FALLBACK = [
  { id: 'calm', name: '平静' },
  { id: 'focused', name: '专注' },
  { id: 'cheerful', name: '愉快' },
  { id: 'low_battery', name: '低电量' },
]

/** 目录接口只回事件数量；内置 daily 包的事件标签在这里兜底。 */
const EVENT_LABELS_FALLBACK: Record<string, string> = {
  praise: '被夸',
  thanks: '被感谢',
  joke: '说笑',
  help: '帮上忙',
  greet: '打招呼',
  agree: '聊到一起',
  food: '吃饭',
  rest: '休息',
  argue: '起了争执',
  complaint: '抱怨',
  apology: '道歉',
  busy: '赶时间',
}

const INJECT_MODES = [
  { value: 'off', label: 'off · 不注入' },
  { value: 'line', label: 'line · 一行摘要' },
  { value: 'full', label: 'full · 多段注入' },
]

const CLASSIFIERS = [
  { value: 'off', label: 'off · 不分类' },
  { value: 'rules', label: 'rules · 按内容包规则' },
]

const HOUR_OPTIONS: { value: 24 | 168; label: string }[] = [
  { value: 24, label: '24h' },
  { value: 168, label: '7d' },
]

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
const enabled = computed(() => draft.affect_enabled)

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
    await applyConfig(configPatch.value, '情绪配置已保存')
  } catch {
    // 错误已经显示在配置卡片里
  }
}

function discardConfig() {
  assign(draft, loaded.value)
  toast.info('已撤销未保存的情绪配置')
}

/** 一键启用：坐实 affect_enabled=true 且 affect_pack=daily；停用只改开关。 */
async function toggleAffect() {
  if (configSaving.value) return
  const next = !draft.affect_enabled
  // 顶部的开关同时承担「保存」语义：把配置区未保存的改动一起带上，避免一键启用悄悄丢弃它们。
  const patch: Partial<EditableConfig> = {
    ...configPatch.value,
    ...(next ? { affect_enabled: true, affect_pack: 'daily' } : { affect_enabled: false }),
  }
  try {
    await applyConfig(patch, next ? '已启用「日常心境」' : '已停用情绪状态')
    if (next) await loadConversations()
  } catch {
    // 错误已经显示在配置卡片里
  }
}

/* ------------------------------------------------------------------ *
 * 内容包
 * ------------------------------------------------------------------ */

const packs = ref<AffectPack[]>([])
const packsLoading = ref(false)
const packsError = ref('')

const packOptions = computed(() => {
  if (packs.value.length > 0) return packs.value
  return [{ id: 'daily', name: '日常心境', description: '内置内容包' }] as AffectPack[]
})

const currentPack = computed(
  () => packs.value.find((pack) => pack.id === draft.affect_pack) ?? packOptions.value[0],
)

function packName(id?: string | null): string {
  if (!id) return '—'
  return packs.value.find((pack) => pack.id === id)?.name ?? id
}

/** 目录接口只回 key 列表；完整内容包才回带 label 的对象数组。 */
const packDimensionDefs = computed<AffectPackDimension[]>(() => {
  const value = currentPack.value?.dimensions
  if (!Array.isArray(value)) return []
  return value.filter(
    (item): item is AffectPackDimension => typeof item === 'object' && item !== null,
  )
})

const packEvents = computed<{ id: string; label?: string; tone?: string }[]>(() => {
  const value = currentPack.value?.events
  if (!Array.isArray(value)) return []
  return value.filter(
    (item): item is { id: string; label?: string; tone?: string } =>
      typeof item === 'object' && item !== null && 'id' in item,
  )
})

const packPresetList = computed<{ id: string; name: string }[]>(() => {
  const value = currentPack.value?.presets
  if (!Array.isArray(value)) return []
  return value.filter(
    (item): item is { id: string; name: string } =>
      typeof item === 'object' && item !== null && 'id' in item,
  )
})

const dimensions = computed<DimensionView[]>(() => {
  const defs = packDimensionDefs.value
  return DIMENSION_FALLBACK.map((fallback) => ({
    ...fallback,
    label: defs.find((item) => item.key === fallback.key)?.label ?? fallback.label,
  }))
})

async function loadPacks() {
  packsLoading.value = true
  packsError.value = ''
  try {
    packs.value = await affectApi.packs()
    // 后端没有返回当前选中的包时，退回第一个可用包（内置 daily 一定有）。
    if (!packs.value.some((pack) => pack.id === draft.affect_pack) && packs.value.length > 0) {
      draft.affect_pack = packs.value[0].id
    }
  } catch (err) {
    packsError.value = toMessage(err)
  } finally {
    packsLoading.value = false
  }
}

/* ------------------------------------------------------------------ *
 * 会话与状态
 * ------------------------------------------------------------------ */

const conversations = ref<AffectConversation[]>([])
const conversationsLoading = ref(false)
const conversationsError = ref('')
const selected = ref('')

const state = ref<AffectState | null>(null)
const behavior = ref<AffectBehavior | null>(null)
const blocks = ref({ line: '', full: '' })
const history = ref<AffectHistoryPoint[]>([])
const events = ref<AffectEvent[]>([])
const stateLoading = ref(false)
const stateError = ref('')
const hours = ref<24 | 168>(24)
const actionLoading = ref('')
const resetOpen = ref(false)

const pausedUntil = computed(() => state.value?.paused_until ?? '')
const isPaused = computed(() => {
  if (!pausedUntil.value) return false
  const until = new Date(pausedUntil.value).getTime()
  return !Number.isNaN(until) && until > Date.now()
})

function clearState() {
  state.value = null
  behavior.value = null
  blocks.value = { line: '', full: '' }
  history.value = []
  events.value = []
  stateError.value = ''
}

async function loadConversations() {
  conversationsLoading.value = true
  conversationsError.value = ''
  try {
    const raw = (await affectApi.conversations(50)) as unknown as (AffectConversation | string)[]
    // 契约回对象；Service.Conversations 只回字符串 id，这里统一成对象。
    const list: AffectConversation[] = raw.map((item) =>
      typeof item === 'string'
        ? { id: item, pack: '', updated_at: '', dims: {} }
        : item,
    )
    conversations.value = list
    const keep = list.some((item) => item.id === selected.value)
    if (!keep) {
      const previous = selected.value
      selected.value = list[0]?.id ?? ''
      // 选中项没变时 watcher 不会触发，这里补一次加载（刷新按钮的场景）。
      if (previous === selected.value) await loadSelection()
    } else {
      await loadSelection()
    }
  } catch (err) {
    conversationsError.value = toMessage(err)
    clearState()
  } finally {
    conversationsLoading.value = false
  }
}

async function loadSelection() {
  if (!selected.value) {
    clearState()
    return
  }
  stateLoading.value = true
  stateError.value = ''
  try {
    const [stateData, points, eventList] = await Promise.all([
      affectApi.state(selected.value),
      affectApi.history(selected.value, hours.value),
      affectApi.events(selected.value, 50),
    ])
    // 契约形状是 {state, behavior, blocks}；同时容忍后端直接把 Snapshot 摊平返回。
    const flat = stateData as unknown as AffectState
    const rawState = stateData.state ?? (flat.conversation_id ? flat : null)
    state.value = rawState
    behavior.value = stateData.behavior ?? rawState?.behavior ?? null
    blocks.value = {
      line: stateData.blocks?.line ?? '',
      full: stateData.blocks?.full ?? '',
    }
    history.value = points
    events.value = eventList
  } catch (err) {
    stateError.value = toMessage(err)
  } finally {
    stateLoading.value = false
  }
}

watch(selected, (value) => {
  if (value) {
    void loadSelection()
  } else {
    clearState()
  }
})

watch(hours, () => {
  if (selected.value) void loadSelection()
})

function setHours(value: number): void {
  hours.value = value === 168 ? 168 : 24
}

async function runAction(key: string, action: () => Promise<unknown>, success: string) {
  if (actionLoading.value) return
  actionLoading.value = key
  stateError.value = ''
  try {
    await action()
    toast.success(success)
    await loadSelection()
  } catch (err) {
    stateError.value = toMessage(err)
    toast.error(stateError.value)
  } finally {
    actionLoading.value = ''
  }
}

function pause(minutes: number) {
  if (!selected.value) return
  void runAction(`pause-${minutes}`, () => affectApi.pause(selected.value, minutes), `已暂停 ${minutes} 分钟`)
}

function resume() {
  if (!selected.value) return
  void runAction('resume', () => affectApi.resume(selected.value), '已恢复情绪影响')
}

function applyPreset(preset: string) {
  if (!selected.value) return
  void runAction(`preset-${preset}`, () => affectApi.preset(selected.value, preset), `已套用预设「${presetLabel(preset)}」`)
}

async function confirmReset() {
  if (!selected.value) return
  await runAction('reset', () => affectApi.reset(selected.value), '已重置该会话的情绪状态')
  resetOpen.value = false
}

onMounted(async () => {
  void loadPacks()
  await loadConfig()
  // 默认关闭时不请求会话状态：旧后端没有 affect 端点，未启用就没必要制造 404。
  if (enabled.value) await loadConversations()
})

/* ------------------------------------------------------------------ *
 * 展示辅助
 * ------------------------------------------------------------------ */

function dimValue(key: string): number {
  const raw = Number(state.value?.dims?.[key] ?? 0)
  if (Number.isNaN(raw)) return 0
  return Math.max(0, Math.min(100, raw))
}

function dimText(key: string): string {
  return dimValue(key).toFixed(2)
}

function dimWidth(key: string): string {
  return `${Math.max(0, Math.min(100, dimValue(key)))}%`
}

const behaviorCards = computed(() => {
  const value = behavior.value
  return [
    {
      label: '字数系数',
      value: value ? `${value.max_chars_factor.toFixed(2)}×` : '—',
      hint: '回复长度软约束',
    },
    {
      label: '最大分段',
      value: value ? String(value.max_segments) : '—',
      hint: '单次回复最多几段',
    },
    {
      label: '人味系数',
      value: value ? `${value.humanize_scale.toFixed(2)}×` : '—',
      hint: '延迟与节奏缩放',
    },
    {
      label: '主动性系数',
      value: value ? `${value.initiative_factor.toFixed(2)}×` : '—',
      hint: '主动接话概率',
    },
  ]
})

const presetOptions = computed(() => {
  const list = packPresetList.value
  return list.length > 0 ? list : PRESET_FALLBACK
})

function presetLabel(id: string): string {
  return presetOptions.value.find((item) => item.id === id)?.name ?? id
}

function eventLabel(id: string): string {
  const found = packEvents.value.find((item) => item.id === id)
  return found?.label || EVENT_LABELS_FALLBACK[id] || found?.id || id
}

function toneLabel(tone: string): string {
  switch (tone) {
    case 'positive':
      return '正向'
    case 'negative':
      return '负向'
    case 'strong':
      return '强烈'
    default:
      return '中性'
  }
}

function toneClass(tone: string): string {
  switch (tone) {
    case 'positive':
      return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
    case 'negative':
      return 'border-rose-500/40 bg-rose-500/10 text-rose-300'
    case 'strong':
      return 'border-amber-500/40 bg-amber-500/10 text-amber-300'
    default:
      return 'border-slate-700 bg-slate-800/60 text-slate-400'
  }
}

function sourceLabel(source: string): string {
  switch (source) {
    case 'rules':
      return '规则分类'
    case 'preset':
      return '预设'
    case 'reset':
      return '重置'
    case 'system':
      return '系统'
    default:
      return source || '—'
  }
}

function conversationLabel(item: AffectConversation): string {
  const short = shortId(item.id, 8, 6)
  const pack = packName(item.pack)
  return `${short} · ${pack} · ${formatRelative(item.updated_at)}`
}

/* ------------------------------------------------------------------ *
 * 历史 SVG 折线
 * ------------------------------------------------------------------ */

const CHART = { width: 720, height: 240, left: 34, right: 12, top: 12, bottom: 28 }

const chartData = computed(() => {
  const points = history.value.filter((point) => point && point.at)
  if (points.length === 0) return null

  const times = points.map((point) => new Date(point.at).getTime())
  let min = Math.min(...times)
  let max = Math.max(...times)
  if (!Number.isFinite(min) || !Number.isFinite(max)) return null
  if (max === min) max = min + 3_600_000

  const innerW = CHART.width - CHART.left - CHART.right
  const innerH = CHART.height - CHART.top - CHART.bottom
  const xAt = (time: number) =>
    CHART.left + ((time - min) / (max - min)) * innerW
  const yAt = (value: number) =>
    CHART.top + innerH - (Math.max(0, Math.min(100, value)) / 100) * innerH

  const lines = dimensions.value
    .filter((dimension) =>
      points.some((point) => typeof point.dims?.[dimension.key] === 'number'),
    )
    .map((dimension) => {
      const d = points
        .map((point, index) => {
          const x = xAt(times[index] ?? min).toFixed(2)
          const y = yAt(Number(point.dims?.[dimension.key] ?? 0)).toFixed(2)
          return `${index === 0 ? 'M' : 'L'} ${x} ${y}`
        })
        .join(' ')
      return { key: dimension.key, label: dimension.label, color: dimension.color, d }
    })

  const tickIndexes = Array.from(
    new Set([0, Math.floor((points.length - 1) / 2), points.length - 1]),
  ).filter((index) => index >= 0)

  return {
    lines,
    yTicks: [0, 25, 50, 75, 100].map((value) => ({ value, y: yAt(value) })),
    xTicks: tickIndexes.map((index) => ({
      index,
      x: xAt(times[index] ?? min),
      label: formatShortDateTime(points[index]?.at),
    })),
    innerW,
    innerH,
    baseY: yAt(0),
  }
})
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部：开关 + 内容包徽章 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="sparkles" :size="16" class="text-violet-400" />
          情绪状态
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <span class="badge border-violet-500/40 bg-violet-500/10 text-violet-300">
            内容包 {{ packName(draft.affect_pack) }}（{{ draft.affect_pack }}）
          </span>
          <StatusBadge
            :info="
              enabled
                ? { label: '已启用', tone: 'emerald' }
                : { label: '未启用', tone: 'slate' }
            "
            dot
          />
          <button
            type="button"
            class="btn"
            :class="enabled ? 'btn-ghost' : 'btn-primary'"
            :disabled="configSaving || configLoading"
            @click="toggleAffect"
          >
            <AppIcon :name="enabled ? 'pause' : 'sparkles'" :size="15" />
            {{ enabled ? '一键停用' : '一键启用日常心境' }}
          </button>
        </div>
      </div>
      <div class="card-body">
        <p class="text-sm leading-relaxed text-slate-400">
          按内容包积累「心情 / 精力 / 压力 / 熟悉度 / 耐心」5 维数值，并把它作为
          <b class="text-slate-300">语气参考</b>注入提示词、轻微调整回复长度与主动性。
          默认关闭；关闭时现有回复行为零变化。启用按钮会同时写入
          <span class="mono">affect_enabled=true</span> 与
          <span class="mono">affect_pack=daily</span>。
        </p>
        <p v-if="configError" class="mt-2 flex items-center gap-2 text-xs text-rose-300">
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
          <div>
            <label class="label" for="affect-pack">内容包 affect_pack</label>
            <select id="affect-pack" v-model="draft.affect_pack" class="input" :disabled="packsLoading">
              <option v-for="pack in packOptions" :key="pack.id" :value="pack.id">
                {{ pack.name }}（{{ pack.id }}）
              </option>
            </select>
            <p class="field-hint">
              {{ currentPack?.description || '内置内容包；默认「日常心境」' }}
            </p>
          </div>
          <div>
            <label class="label" for="affect-inject-mode">注入档位 affect_inject_mode</label>
            <select id="affect-inject-mode" v-model="draft.affect_inject_mode" class="input">
              <option v-for="mode in INJECT_MODES" :key="mode.value" :value="mode.value">
                {{ mode.label }}
              </option>
            </select>
            <p class="field-hint">off 不注入 / line 一行 / full 多段；token 上限见右侧</p>
          </div>
          <div>
            <label class="label" for="affect-classifier">事件分类器 affect_event_classifier</label>
            <select id="affect-classifier" v-model="draft.affect_event_classifier" class="input">
              <option v-for="item in CLASSIFIERS" :key="item.value" :value="item.value">
                {{ item.label }}
              </option>
            </select>
            <p class="field-hint">rules 按内容包正则匹配；off 时只维护数值不识别事件</p>
          </div>
          <NumberField
            v-model="draft.affect_tick_seconds"
            label="tick 间隔 affect_tick_seconds（秒）"
            :min="60"
            :max="1800"
            hint="后台衰减 / 落盘节奏；60–1800，默认 300"
          />
          <NumberField
            v-model="draft.affect_inject_max_tokens"
            label="注入 token 上限 affect_inject_max_tokens"
            :min="40"
            :max="300"
            hint="单次注入的 token 预算；40–300，默认 120"
          />
        </div>

        <p v-if="packsError" class="text-xs text-amber-300">内容包列表获取失败：{{ packsError }}</p>

        <div class="flex flex-wrap items-center justify-end gap-2 border-t border-slate-800 pt-3">
          <span class="mr-auto text-xs text-slate-500">
            配置走 <span class="mono">PUT /api/config</span>，只发送改动的键。
          </span>
          <button type="button" class="btn btn-sm" :disabled="!configDirty || configSaving" @click="discardConfig">
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

    <!-- 会话选择 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="chat" :size="16" class="text-sky-400" />
          会话
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <select v-model="selected" class="input min-w-[18rem]" :disabled="conversationsLoading || conversations.length === 0">
            <option value="">请选择会话</option>
            <option v-for="item in conversations" :key="item.id" :value="item.id">
              {{ conversationLabel(item) }}
            </option>
          </select>
          <button type="button" class="btn btn-sm" :disabled="conversationsLoading" @click="loadConversations">
            <AppIcon name="refresh" :size="14" :class="conversationsLoading ? 'animate-spin' : ''" />
            刷新
          </button>
        </div>
      </div>
      <div class="card-body">
        <p v-if="conversationsError" class="flex items-center gap-2 text-xs text-rose-300">
          <AppIcon name="warning" :size="14" />
          {{ conversationsError }}
        </p>
        <EmptyState
          v-else-if="!conversationsLoading && conversations.length === 0"
          title="暂无会话情绪状态"
          hint="启用后，机器人收到人类消息会开始为会话积累情绪；先在上方配置区确认内容包与注入档位。"
          icon="inbox"
        />
        <p v-else class="text-xs text-slate-500">
          共 {{ conversations.length }} 个会话有情绪状态；按状态更新时间倒序。
        </p>
      </div>
    </section>

    <!-- 未启用引导 -->
    <EmptyState
      v-if="!enabled"
      title="情绪状态尚未启用"
      hint="默认关闭，现有回复行为完全不变。点击下方按钮会同时写入 affect_enabled=true 与 affect_pack=daily，之后可在这里观察数值与历史。"
    >
      <button type="button" class="btn btn-primary" :disabled="configSaving" @click="toggleAffect">
        <AppIcon name="sparkles" :size="15" />
        一键启用日常心境
      </button>
    </EmptyState>

    <!-- 状态主体 -->
    <template v-else>
      <div
        v-if="stateError"
        class="flex items-center gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
      >
        <AppIcon name="warning" :size="15" class="text-rose-400" />
        {{ stateError }}
      </div>

      <section v-if="stateLoading" class="card">
        <div class="card-body py-10 text-center text-sm text-slate-500">加载情绪状态…</div>
      </section>

      <EmptyState
        v-else-if="selected && !state"
        title="该会话还没有情绪状态"
        hint="收到下一条人类消息并完成分类后会自动创建；也可以稍后刷新。"
        icon="inbox"
      />

      <section v-else-if="state" class="card">
        <div class="card-header">
          <h2 class="card-title">
            <AppIcon name="dashboard" :size="16" class="text-violet-400" />
            5 维数值
            <span class="mono text-xs font-normal text-slate-500">{{ shortId(state.conversation_id, 10, 6) }}</span>
          </h2>
          <div class="flex flex-wrap items-center gap-2">
            <span class="badge border-slate-700 bg-slate-800/60 text-slate-300">
              {{ packName(state.pack_id) }}
            </span>
            <span v-if="isPaused" class="badge border-amber-500/40 bg-amber-500/10 text-amber-300">
              暂停至 {{ formatDateTime(pausedUntil) }}
            </span>
            <span v-else class="badge border-emerald-500/40 bg-emerald-500/10 text-emerald-300">
              生效中
            </span>
            <span class="text-xs text-slate-500">更新于 {{ formatRelative(state.updated_at) }}</span>
          </div>
        </div>
        <div class="card-body space-y-4">
          <div class="grid grid-cols-1 gap-x-6 gap-y-3 lg:grid-cols-2">
            <div v-for="dimension in dimensions" :key="dimension.key" class="space-y-1">
              <div class="flex items-baseline justify-between gap-2 text-sm">
                <span class="text-slate-300">
                  {{ dimension.label }}
                  <span class="mono text-xs text-slate-500">{{ dimension.key }}</span>
                </span>
                <span class="tabular-nums text-slate-200">{{ dimText(dimension.key) }}</span>
              </div>
              <div class="h-2 overflow-hidden rounded-full bg-slate-800">
                <div
                  class="h-full rounded-full transition-all"
                  :class="dimension.bar"
                  :style="{ width: dimWidth(dimension.key) }"
                />
              </div>
            </div>
          </div>

          <div class="border-t border-slate-800 pt-4">
            <h3 class="mb-2 text-xs font-semibold tracking-wide text-slate-400 uppercase">
              行为摘要
            </h3>
            <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
              <div
                v-for="item in behaviorCards"
                :key="item.label"
                class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2"
              >
                <p class="text-xs text-slate-500">{{ item.label }}</p>
                <p class="text-base font-semibold text-slate-200 tabular-nums">{{ item.value }}</p>
                <p class="text-[11px] text-slate-600">{{ item.hint }}</p>
              </div>
            </div>
            <p v-if="behavior?.tone || behavior?.reason" class="field-hint">
              语气：{{ behavior?.tone || '—' }}<span v-if="behavior?.reason"> · {{ behavior.reason }}</span>
            </p>
          </div>

          <div class="flex flex-wrap items-center gap-2 border-t border-slate-800 pt-4">
            <span class="mr-1 text-xs text-slate-500">操作</span>
            <button
              type="button"
              class="btn btn-sm"
              :disabled="!!actionLoading"
              @click="pause(30)"
            >
              <AppIcon name="pause" :size="14" />
              暂停 30 分钟
            </button>
            <button
              type="button"
              class="btn btn-sm"
              :disabled="!!actionLoading"
              @click="pause(120)"
            >
              <AppIcon name="pause" :size="14" />
              暂停 120 分钟
            </button>
            <button
              type="button"
              class="btn btn-sm btn-success"
              :disabled="!!actionLoading || !isPaused"
              @click="resume"
            >
              <AppIcon name="play" :size="14" />
              恢复
            </button>
            <span class="mx-1 h-4 w-px bg-slate-800" />
            <button
              v-for="preset in presetOptions"
              :key="preset.id"
              type="button"
              class="btn btn-sm"
              :disabled="!!actionLoading"
              :title="'套用预设 ' + preset.id"
              @click="applyPreset(preset.id)"
            >
              预设：{{ preset.name }}
            </button>
            <button
              type="button"
              class="btn btn-sm btn-danger ml-auto"
              :disabled="!!actionLoading"
              @click="resetOpen = true"
            >
              <AppIcon name="trash" :size="14" />
              重置
            </button>
          </div>
        </div>
      </section>

      <!-- 历史曲线 -->
      <section v-if="enabled" class="card">
        <div class="card-header">
          <h2 class="card-title">
            <AppIcon name="dashboard" :size="16" class="text-emerald-400" />
            历史曲线
          </h2>
          <div class="flex overflow-hidden rounded-lg border border-slate-700">
            <button
              v-for="option in HOUR_OPTIONS"
              :key="option.value"
              type="button"
              class="cursor-pointer px-3 py-1 text-xs font-medium transition"
              :class="hours === option.value ? 'bg-emerald-600 text-white' : 'bg-slate-900 text-slate-400 hover:bg-slate-800'"
              @click="setHours(option.value)"
            >
              {{ option.label }}
            </button>
          </div>
        </div>
        <div class="card-body">
          <EmptyState
            v-if="!chartData"
            title="暂无历史数据"
            hint="情绪状态每 tick 落一次历史点；启用一段时间后这里会出现曲线。"
            icon="inbox"
          />
          <div v-else class="space-y-2">
            <svg
              :viewBox="`0 0 ${CHART.width} ${CHART.height}`"
              class="block h-auto w-full"
              role="img"
              aria-label="情绪历史折线图"
            >
              <g>
                <template v-for="tick in chartData.yTicks" :key="`y-${tick.value}`">
                  <line
                    :x1="CHART.left"
                    :x2="CHART.left + chartData.innerW"
                    :y1="tick.y"
                    :y2="tick.y"
                    stroke="#1e293b"
                    stroke-width="1"
                  />
                  <text
                    :x="CHART.left - 6"
                    :y="tick.y + 3"
                    text-anchor="end"
                    fill="#64748b"
                    font-size="9"
                  >
                    {{ tick.value }}
                  </text>
                </template>
              </g>
              <g>
                <text
                  v-for="tick in chartData.xTicks"
                  :key="`x-${tick.index}`"
                  :x="tick.x"
                  :y="CHART.height - 8"
                  text-anchor="middle"
                  fill="#64748b"
                  font-size="9"
                >
                  {{ tick.label }}
                </text>
              </g>
              <path
                v-for="line in chartData.lines"
                :key="line.key"
                :d="line.d"
                fill="none"
                :stroke="line.color"
                stroke-width="2"
                stroke-linejoin="round"
                stroke-linecap="round"
              />
            </svg>
            <div class="flex flex-wrap items-center gap-3 px-1 text-xs text-slate-400">
              <span v-for="line in chartData.lines" :key="'legend-' + line.key" class="flex items-center gap-1.5">
                <span class="h-0.5 w-4 rounded" :style="{ backgroundColor: line.color }" />
                {{ line.label }}
              </span>
            </div>
          </div>
        </div>
      </section>

      <!-- 注入预览 -->
      <section v-if="enabled" class="card">
        <div class="card-header">
          <h2 class="card-title">
            <AppIcon name="sparkles" :size="16" class="text-violet-400" />
            注入预览
          </h2>
          <span class="text-xs text-slate-500">只读；来自 GET /api/affect/state 的 blocks</span>
        </div>
        <div class="card-body grid grid-cols-1 gap-4 lg:grid-cols-2">
          <div>
            <label class="label" for="affect-block-line">line 档（≤25 tokens）</label>
            <textarea
              id="affect-block-line"
              class="input mono h-32 resize-y"
              readonly
              :value="blocks.line || '（后端未生成 line 注入块）'"
            />
          </div>
          <div>
            <label class="label" for="affect-block-full">full 档（≤120 tokens）</label>
            <textarea
              id="affect-block-full"
              class="input mono h-32 resize-y"
              readonly
              :value="blocks.full || '（后端未生成 full 注入块）'"
            />
          </div>
        </div>
      </section>

      <!-- 最近事件 -->
      <section v-if="enabled" class="card">
        <div class="card-header">
          <h2 class="card-title">
            <AppIcon name="inbox" :size="16" class="text-sky-400" />
            最近事件
          </h2>
          <span class="text-xs text-slate-500">最近 50 条</span>
        </div>
        <div class="card-body">
          <EmptyState
            v-if="events.length === 0"
            title="还没有事件"
            hint="被夸、被催、深夜聊天等事件会由分类器识别并记录在这里。"
            icon="inbox"
          />
          <ul v-else class="max-h-[24rem] space-y-1.5 overflow-y-auto pr-1 scrollbar-thin">
            <li
              v-for="event in events"
              :key="event.id"
              class="flex flex-wrap items-center gap-2 rounded-lg border border-slate-800 bg-slate-950/40 px-3 py-2 text-sm"
            >
              <span class="badge" :class="toneClass(event.tone)">{{ toneLabel(event.tone) }}</span>
              <span class="font-medium text-slate-200">{{ eventLabel(event.event_id) }}</span>
              <span class="mono text-xs text-slate-500">{{ event.event_id }}</span>
              <span class="text-xs text-slate-500">{{ sourceLabel(event.source) }}</span>
              <span class="ml-auto text-xs text-slate-500">{{ formatDateTime(event.created_at) }}</span>
            </li>
          </ul>
        </div>
      </section>
    </template>

    <ConfirmDialog
      :open="resetOpen"
      title="重置情绪状态"
      message="将把该会话的 5 维数值恢复到内容包基线，并清空事件与历史。此操作不可撤销。"
      confirm-text="重置"
      danger
      :loading="actionLoading === 'reset'"
      @confirm="confirmReset"
      @cancel="resetOpen = false"
    />
  </div>
</template>
