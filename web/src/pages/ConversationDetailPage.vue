<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import CopyButton from '@/components/CopyButton.vue'
import ConversationSidebar from '@/components/ConversationSidebar.vue'
import { conversationsApi, groupsApi, isAbortError, toMessage } from '@/api'
import type { ConversationDetail, ConversationMessage } from '@/api/types'
import { toast } from '@/composables/useToast'
import { enabledBadge, roleLabel, sceneBadge } from '@/utils/badges'
import type { BadgeInfo } from '@/utils/badges'
import { useLiveRefresh } from '@/composables/useLiveRefresh'
import { formatDateTime, formatNumber, formatRelative, shortId, truncate } from '@/utils/format'

const route = useRoute()
const router = useRouter()

const PAGE_LIMIT = 200

const detail = ref<ConversationDetail | null>(null)
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')

const clearOpen = ref(false)
const clearing = ref(false)
const toggling = ref(false)

const conversationId = computed(() => String(route.params.id ?? ''))

/** 平台侧的群接收设置文案。 */
function recvSettingLabel(value?: string | null): string {
  switch (value) {
    case 'all':
      return '接收所有消息'
    case 'only_mention':
      return '仅 @机器人'
    case 'mention_and_context':
      return '仅 @机器人及其引用'
    default:
      return value || '未知'
  }
}

const messages = computed(() => detail.value?.messages ?? [])
/** 消息列表的滚动容器：自动刷新时保持用户的阅读位置。 */
const listRef = ref<HTMLElement | null>(null)
const lastRefreshedAt = ref<Date | null>(null)
const manualRefreshing = ref(false)

/**
 * 新消息到达时是否自动滚动到底部。默认开启；关掉后仅记住关闭状态，
 * 这样刷新页面不会把用户的偏好重置回开启。
 *
 * 注意语义：开启也只是「**本来就在底部**时才跟随」——正在往上翻历史的人
 * 不该被新消息拽走，那种情况交给右下角的「回到底部」按钮。
 */
const AUTO_SCROLL_KEY = 'bot-harness.conversation.autoScroll'
const autoScroll = ref(
  typeof localStorage === 'undefined' ? true : localStorage.getItem(AUTO_SCROLL_KEY) !== '0',
)

/** 贴底判定阈值：留点余量，免得像素级误差让按钮一直闪。 */
const BOTTOM_THRESHOLD_PX = 80

/** 列表当前是否贴在底部。 */
const atBottom = ref(true)
/** 没贴在底部时累计的新消息条数，用于右下角按钮的角标。 */
const pendingNew = ref(0)

function isAtBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_THRESHOLD_PX
}

/** 滚动（用户滚动或程序化滚动）之后重新读一次真实位置。 */
function syncAtBottom() {
  const el = listRef.value
  if (!el) return
  atBottom.value = isAtBottom(el)
  // 已经回到最新，角标清零。
  if (atBottom.value) pendingNew.value = 0
}

function captureScroll(): { top: number } | null {
  const el = listRef.value
  if (!el) return null
  return { top: el.scrollTop }
}

async function restoreScroll(snap: { top: number } | null) {
  if (!snap) return
  await nextTick()
  const el = listRef.value
  if (!el) return
  // 只恢复阅读位置，不做任何「跟随最新」的判断：跟随由调用方决定。
  el.scrollTop = snap.top
}

/** 最后一条消息的 id，用来判断这一轮刷新是否带来了新消息。 */
function lastMessageID(): number {
  const list = messages.value
  return list.length ? list[list.length - 1].id : 0
}

/** 滚到底部；等 DOM 更新完再滚，否则新消息还没进布局。 */
async function scrollToBottom() {
  await nextTick()
  const el = listRef.value
  if (!el) return
  el.scrollTop = el.scrollHeight
  pendingNew.value = 0
  syncAtBottom()
}

/** 右下角按钮：回到最新。 */
function jumpToBottom() {
  void scrollToBottom()
}

/** 角标文案：过百就折叠，免得按钮被撑变形。 */
const pendingNewLabel = computed(() => (pendingNew.value > 99 ? '99+' : String(pendingNew.value)))

watch(autoScroll, (value) => {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(AUTO_SCROLL_KEY, value ? '1' : '0')
  }
  // 重新打开时立刻贴到底部，否则用户还要手动滚一次才知道它生效了。
  if (value) void scrollToBottom()
})
const hasMore = computed(() => messages.value.length >= PAGE_LIMIT)

let controller: AbortController | null = null

async function load(silent = false) {
  if (!conversationId.value) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  if (!silent) loading.value = true
  const scrollSnap = silent ? captureScroll() : null
  const lastIDBefore = lastMessageID()
  // 请求发出时的贴底状态：刷新回来时用户可能已经滚走了，用请求前的判断更稳。
  const wasAtBottom = atBottom.value
  error.value = ''
  try {
    const fresh = await conversationsApi.get(
      conversationId.value,
      { limit: PAGE_LIMIT },
      current.signal,
    )
    const previous = detail.value
    if (silent && previous) {
      // 静默刷新只取最新一页，把已经翻出来的更早消息保留在前面。
      const freshIDs = new Set(fresh.messages.map((item) => item.id))
      const oldestFresh = fresh.messages[0]?.id ?? Number.MAX_SAFE_INTEGER
      const older = previous.messages.filter((item) => !freshIDs.has(item.id) && item.id < oldestFresh)
      detail.value = { ...fresh, messages: [...older, ...fresh.messages] }
    } else {
      detail.value = fresh
    }
    lastRefreshedAt.value = new Date()
  } catch (err) {
    if (isAbortError(err)) return
    error.value = toMessage(err)
    detail.value = null
  } finally {
    if (controller === current) {
      loading.value = false
      controller = null
    }
    const grew = lastMessageID() > lastIDBefore
    // 跟随规则：首次打开直接贴到最新；之后的刷新只在「本来就在底部」时跟随，
    // 这样正在往上翻历史的人不会被新消息拽走，那种情况交给右下角按钮。
    const follow = grew && autoScroll.value && (silent ? wasAtBottom : true)
    if (follow) {
      await scrollToBottom()
    } else {
      if (silent && grew) {
        // 没跟随时把新消息记进角标（只算尾巴上新增的，翻出来的更早消息不计）。
        pendingNew.value += messages.value.filter((item) => item.id > lastIDBefore).length
      }
      await restoreScroll(scrollSnap)
      // 内容变长/变短都可能改变贴底状态，直接读一次真实 DOM。
      syncAtBottom()
    }
  }
}

const platformRefreshing = ref(false)

/** 元数据板块是否展开（默认展开）。 */
const metadataOpen = ref(true)
/** 右侧「群成员」侧栏是否展开（默认折叠）。 */
const membersOpen = ref(false)
/** 成员排序：按发言条数 / 按最近发言。 */
const memberSort = ref<'count' | 'recent'>('count')
/** 消息输入框的草稿。 */
const draft = ref('')
/** 正在发送手动消息。 */
const sending = ref(false)
/** 右键菜单（消息时间线里）。 */
const menu = ref<{ x: number; y: number; message: ConversationMessage } | null>(null)
/** 输入栏当前要引用的那条消息。 */
const quoteTarget = ref<ConversationMessage | null>(null)
/** 正在切换按群「引用回复」开关。 */
const quoteToggling = ref(false)
/** 上一次手动发送的错误（失败时草稿保留，方便改完重发）。 */
const sendError = ref('')

/** 发送目标的名字：写在输入栏里，避免发错群。 */
const sendTargetName = computed(() => {
  const d = detail.value
  if (!d) return '该会话'
  if (d.scene === 'c2c') return d.member_name || '该用户'
  return d.group_name || d.group_openid || '该群'
})

const canSend = computed(() => draft.value.trim().length > 0 && !sending.value)

/* ------------------------------------------------------------------ *
 * 右键引用
 * ------------------------------------------------------------------ */

/** 这个群当前是否允许引用回复（私聊跟随全局默认）。 */
const quoteEnabled = computed(() => detail.value?.quote_reply !== false)

/** 这条消息能不能被引用：后端要有它的平台引用索引。 */
function canQuote(message: ConversationMessage): boolean {
  return quoteEnabled.value && !!message.quote_index
}

function openMenu(event: MouseEvent, message: ConversationMessage) {
  // 菜单尺寸按最宽的情况预留，避免贴边被截断
  const width = 190
  const height = 88
  menu.value = {
    x: Math.min(event.clientX, window.innerWidth - width - 8),
    y: Math.min(event.clientY, window.innerHeight - height - 8),
    message,
  }
}

function closeMenu() {
  menu.value = null
}

function pickQuote() {
  const message = menu.value?.message
  closeMenu()
  if (!message || !canQuote(message)) return
  quoteTarget.value = message
}

async function copyMessage() {
  const message = menu.value?.message
  closeMenu()
  if (!message) return
  try {
    await navigator.clipboard.writeText(message.content)
    toast.success('已复制消息内容')
  } catch {
    toast.error('复制失败，请手动选中')
  }
}

/** 按群切换「引用回复」（写进策略里的按群覆盖）。 */
async function toggleQuoteReply() {
  const current = detail.value
  if (!current || current.scene === 'c2c' || quoteToggling.value) return
  quoteToggling.value = true
  try {
    const next = !quoteEnabled.value
    const state = await conversationsApi.setQuoteReply(current.id, next)
    detail.value = {
      ...current,
      quote_reply: state.quote_reply,
      quote_reply_mode: state.quote_reply_mode ?? current.quote_reply_mode,
      quote_reply_override: state.override ?? null,
      quote_reply_global: state.global,
      quote_reply_global_mode: state.global_mode ?? current.quote_reply_global_mode,
    }
    if (!state.quote_reply) quoteTarget.value = null
    toast.success(state.quote_reply ? '已开启引用回复' : '已关闭引用回复')
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    quoteToggling.value = false
  }
}

function onWindowKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    closeMenu()
  }
}

// 点空白 / 滚轮 / 切走都要把菜单关掉，否则它会一直浮在那儿。
onMounted(() => {
  window.addEventListener('click', closeMenu)
  window.addEventListener('keydown', onWindowKeydown)
})
onBeforeUnmount(() => {
  window.removeEventListener('click', closeMenu)
  window.removeEventListener('keydown', onWindowKeydown)
})

/** 以机器人的身份把输入框里的内容发到该会话（主动消息）。 */
async function sendNow() {
  const content = draft.value.trim()
  if (!content || sending.value) return
  sending.value = true
  sendError.value = ''
  try {
    const res = await conversationsApi.send(conversationId.value, content, quoteTarget.value?.id)
    // 发送成功才清空草稿；失败保留，别让用户白打一遍。
    draft.value = ''
    quoteTarget.value = null
    const suffix = res.segments > 1 ? `（拆成 ${res.segments} 段）` : ''
    toast.success(res.quoted ? `已引用发送${suffix}` : `已发送${suffix}`)
    if (detail.value) detail.value = { ...detail.value, active_today: res.active_today }
    // SSE 也会推事件，但手动刷新一次让时间线立刻出现这条
    await load(true)
  } catch (err) {
    sendError.value = toMessage(err)
    toast.error(sendError.value)
  } finally {
    sending.value = false
  }
}

/**
 * Enter 发送、Shift+Enter 换行。
 *
 * 中文输入法组字时回车是「选词」：isComposing / keyCode 229 必须放过，
 * 否则打拼音打个回车就把半成品发出去了。
 */
function onDraftKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.shiftKey) return
  if (event.isComposing || event.keyCode === 229) return
  event.preventDefault()
  void sendNow()
}

/** 图片放大预览。 */
const previewImage = ref<{ url: string; label: string } | null>(null)

/* ------------------------------------------------------------------ *
 * 群成员聚合
 *
 * 数据来自**已加载的上下文消息**（首屏 200 条，向上翻历史会继续累加），
 * 纯前端聚合，不额外请求后端：谁发过言、发了多少条、最后一次是什么时候。
 * 机器人自己（assistant 轮次）和其他机器人（observed_bot）也各算一个成员，
 * 否则「群里发言过的所有人」会缺一块。
 * ------------------------------------------------------------------ */

type MemberKind = 'human' | 'botSelf' | 'botOther'

interface GroupMember {
  key: string
  name: string
  openid: string
  kind: MemberKind
  count: number
  firstAt: string | null
  lastAt: string | null
}

const speakers = computed<GroupMember[]>(() => {
  // 第一步：昵称 → openid 的映射，用来把「升级前写入、没有 openid 的老消息」
  // 并回同一个人。同名对应多个 openid 时记成 null（有歧义）——宁可不合并，
  // 也不要把两个人的发言算到一个人头上。
  const nameToOpenID = new Map<string, string | null>()
  for (const message of messages.value) {
    if (message.role === 'assistant') continue
    const openid = message.speaker_openid
    const name = message.speaker
    if (!openid || !name) continue
    if (nameToOpenID.has(name) && nameToOpenID.get(name) !== openid) {
      nameToOpenID.set(name, null)
    } else if (!nameToOpenID.has(name)) {
      nameToOpenID.set(name, openid)
    }
  }

  const map = new Map<string, GroupMember>()
  for (const message of messages.value) {
    const kind: MemberKind =
      message.role === 'assistant' ? 'botSelf' : message.kind === 'observed_bot' ? 'botOther' : 'human'
    const openid = message.speaker_openid || ''
    const name =
      kind === 'botSelf' ? '我（机器人）' : message.speaker || (openid ? shortId(openid) : '未知成员')
    // 老消息没有 openid 时，按昵称查出「唯一确定的」那个人并合并进去。
    const resolved = openid || (kind === 'human' || kind === 'botOther' ? (nameToOpenID.get(name) ?? '') : '')
    // 机器人自己只有一个身份，不因老消息缺 openid 而分裂成两行。
    const key = kind === 'botSelf' ? 'botSelf:self' : `${kind}:${resolved || name}`
    const at = message.created_at ?? null
    const existing = map.get(key)
    if (!existing) {
      map.set(key, { key, name, openid: resolved, kind, count: 1, firstAt: at, lastAt: at })
      continue
    }
    existing.count += 1
    if (!existing.openid && resolved) existing.openid = resolved
    if (at) {
      if (!existing.lastAt || at > existing.lastAt) existing.lastAt = at
      if (!existing.firstAt || at < existing.firstAt) existing.firstAt = at
    }
  }
  const out = [...map.values()]
  if (memberSort.value === 'recent') {
    out.sort((a, b) => (b.lastAt ?? '').localeCompare(a.lastAt ?? ''))
  } else {
    out.sort((a, b) => b.count - a.count || (b.lastAt ?? '').localeCompare(a.lastAt ?? ''))
  }
  return out
})

/** 成员类型徽标；模板里只对非人类成员渲染，普通群成员不会挂「群成员」标签。 */
function memberBadge(kind: MemberKind): BadgeInfo {
  if (kind === 'botSelf') return { label: '本机器人', tone: 'sky' }
  if (kind === 'botOther') return { label: '其他机器人', tone: 'violet' }
  return { label: '群成员', tone: 'slate' }
}

const AVATAR_TONES = [
  'border-sky-500/40 bg-sky-500/15 text-sky-300',
  'border-emerald-500/40 bg-emerald-500/15 text-emerald-300',
  'border-amber-500/40 bg-amber-500/15 text-amber-300',
  'border-violet-500/40 bg-violet-500/15 text-violet-300',
  'border-rose-500/40 bg-rose-500/15 text-rose-300',
  'border-cyan-500/40 bg-cyan-500/15 text-cyan-300',
]

/** 用名字散列挑一个稳定的底色，让同一个人每次都是同一个颜色。 */
function avatarTone(seed: string): string {
  let hash = 0
  for (let i = 0; i < seed.length; i += 1) hash = (hash * 31 + seed.charCodeAt(i)) >>> 0
  return AVATAR_TONES[hash % AVATAR_TONES.length] ?? AVATAR_TONES[0]!
}

/** 头像里的首字符；用 Array.from 以免把 emoji 劈成两半。 */
function initialOf(name: string): string {
  const trimmed = name.trim()
  return trimmed ? (Array.from(trimmed)[0] ?? '?') : '?'
}

function imagesOf(message: { attachments?: { kind?: string; media_id?: string; filename?: string }[] }) {
  return (message.attachments ?? []).filter((a) => a.kind === 'image' && a.media_id)
}

function otherFiles(message: { attachments?: { kind?: string; filename?: string; content_type?: string }[] }) {
  return (message.attachments ?? []).filter((a) => a.kind !== 'image')
}

function mediaURL(id: string): string {
  return `/api/media/${id}`
}

function attachmentLabel(a: { kind?: string; filename?: string; content_type?: string }): string {
  const kind = a.kind || 'file'
  const name = a.filename ? ` ${a.filename}` : ''
  switch (kind) {
    case 'voice':
      return `语音${name}`
    case 'video':
      return `视频${name}`
    default:
      return `附件${name}${a.content_type ? ` (${a.content_type})` : ''}`
  }
}

/** 主动向平台要一次该群的机器人状态（权限在 QQ 群里被改过时用得上）。 */
async function refreshPlatformState() {
  const groupOpenID = detail.value?.group_openid
  if (!groupOpenID || platformRefreshing.value) return
  platformRefreshing.value = true
  try {
    const state = await groupsApi.state(groupOpenID, true)
    if (detail.value) detail.value = { ...detail.value, platform: state }
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    platformRefreshing.value = false
  }
}

/** 手动刷新：给出可见反馈。 */
async function refreshNow() {
  manualRefreshing.value = true
  try {
    await load(true)
  } finally {
    manualRefreshing.value = false
  }
}

async function loadOlder() {
  const first = messages.value[0]
  if (!first || loadingMore.value) return
  loadingMore.value = true
  try {
    const older = await conversationsApi.get(conversationId.value, {
      limit: PAGE_LIMIT,
      before_id: first.id,
    })
    const existing = detail.value
    if (existing) {
      const known = new Set(existing.messages.map((item) => item.id))
      const merged = [...(older.messages ?? []).filter((item) => !known.has(item.id)), ...existing.messages]
      detail.value = { ...older, messages: merged }
    }
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    loadingMore.value = false
    // 往前面补历史会让列表变长（通常还伴随位置变化），贴底状态要重算：
    // 于是「回到底部」按钮会如预期地出现在右下角。
    await nextTick()
    syncAtBottom()
  }
}

watch(conversationId, () => void load())

onMounted(() => void load())

/* ------------------------------------------------------------------ *
 * 实时刷新
 * ------------------------------------------------------------------ */

const { enabled: liveRefresh } = useLiveRefresh(() => void load(true), {
  pollIntervalMs: 5000,
  // 只对当前会话相关的事件触发刷新，避免全站任意一条消息都重拉。
  shouldRefresh: (event) => {
    try {
      const payload = JSON.parse((event as MessageEvent).data) as { conversation_id?: string }
      if (!payload?.conversation_id) return true
      return payload.conversation_id === conversationId.value
    } catch {
      return true
    }
  },
})

async function confirmClear() {
  clearing.value = true
  try {
    await conversationsApi.clear(conversationId.value)
    toast.success('已清空该会话上下文')
    clearOpen.value = false
    await load(true)
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    clearing.value = false
  }
}

async function toggleEnabled() {
  if (!detail.value) return
  toggling.value = true
  try {
    const next = !detail.value.enabled
    await conversationsApi.setEnabled(detail.value.id, next)
    detail.value.enabled = next
    toast.success(next ? '会话已恢复' : '会话已暂停')
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    toggling.value = false
  }
}

function goBack() {
  void router.push({ name: 'conversations' })
}
</script>

<template>
  <div class="flex h-full min-h-0 gap-4">
    <!-- 左侧会话切换栏：窗口够宽才显示，窄窗口回退用顶部的「返回列表」。 -->
    <aside class="hidden w-72 shrink-0 lg:flex">
      <ConversationSidebar :active-id="conversationId" />
    </aside>

    <div class="page-fill min-w-0 flex-1">
      <div class="flex shrink-0 flex-wrap items-center gap-3">
        <button type="button" class="btn btn-sm" @click="goBack">
          <AppIcon name="chevronLeft" :size="14" />
          返回列表
        </button>
        <h2 class="flex items-center gap-2 text-sm font-semibold text-slate-200">
          <AppIcon name="chat" :size="16" class="text-sky-400" />
          会话详情
        </h2>
        <span v-if="detail" class="mono text-slate-500">{{ detail.id }}</span>
        <div v-if="detail" class="ml-auto flex flex-wrap items-center gap-2">
          <StatusBadge :info="enabledBadge(detail.enabled)" />
          <button type="button" class="btn btn-sm" :disabled="loading" @click="load()">
            <AppIcon name="refresh" :size="14" :class="loading ? 'animate-spin' : ''" />
            刷新
          </button>
          <button type="button" class="btn btn-sm" :disabled="toggling" @click="toggleEnabled">
            <AppIcon :name="detail.enabled ? 'pause' : 'play'" :size="14" />
            {{ detail.enabled ? '暂停' : '恢复' }}
          </button>
          <button type="button" class="btn btn-sm btn-danger" @click="clearOpen = true">
            <AppIcon name="trash" :size="14" />
            清空上下文
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

      <template v-if="detail">
        <!-- 元数据（固定，不随消息列表滚动；可折叠，默认展开） -->
        <section class="shrink-0 card">
          <div class="card-header">
            <h3 class="card-title">
              <AppIcon name="list" :size="15" class="text-sky-400" />
              元数据
              <span v-if="!metadataOpen" class="truncate text-xs font-normal text-slate-500">
                {{ detail.group_name || detail.group_openid || detail.id }} ·
                {{ formatNumber(detail.message_count) }} 消息 /
                {{ formatNumber(detail.turn_count) }} 轮次
              </span>
            </h3>
            <div class="flex items-center gap-2">
              <StatusBadge :info="sceneBadge(detail.scene)" />
              <button
                type="button"
                class="btn btn-sm"
                :title="metadataOpen ? '折叠元数据' : '展开元数据'"
                :aria-expanded="metadataOpen"
                @click="metadataOpen = !metadataOpen"
              >
                <AppIcon
                  name="chevronRight"
                  :size="14"
                  class="transition-transform"
                  :class="metadataOpen ? 'rotate-90' : ''"
                />
                {{ metadataOpen ? '折叠' : '展开' }}
              </button>
            </div>
          </div>
          <div
            v-show="metadataOpen"
            class="card-body grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2 xl:grid-cols-3"
          >
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">群名称</span>
              <span class="truncate text-slate-200">{{ detail.group_name || '—' }}</span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">群 OpenID</span>
              <span class="flex min-w-0 items-center gap-1">
                <span class="mono truncate text-slate-300" :title="detail.group_openid ?? ''">
                  {{ detail.group_openid || '—' }}
                </span>
                <CopyButton :value="detail.group_openid" label="群 OpenID" />
              </span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">成员</span>
              <span class="truncate text-slate-200">
                {{ detail.member_name || (detail.scene === 'group_shared' ? '整群共享' : '—') }}
              </span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">成员 OpenID</span>
              <span class="flex min-w-0 items-center gap-1">
                <span class="mono truncate text-slate-300" :title="detail.member_openid ?? ''">
                  {{ detail.member_openid || '—' }}
                </span>
                <CopyButton :value="detail.member_openid" label="成员 OpenID" />
              </span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="flex items-center gap-1 text-slate-500">
                引用回复
                <span
                  v-if="detail.scene !== 'c2c' && detail.quote_reply_override == null"
                  class="text-[11px] text-slate-600"
                  >跟随全局</span
                >
              </span>
              <button
                v-if="detail.scene !== 'c2c'"
                type="button"
                class="badge cursor-pointer transition-colors"
                :class="
                  detail.quote_reply
                    ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300 hover:border-emerald-400'
                    : 'border-slate-600/50 bg-slate-700/20 text-slate-300 hover:border-slate-500'
                "
                :disabled="quoteToggling"
                :title="
                  detail.quote_reply
                    ? '回答时按全局档位概率引用触发它的那条消息，点击关闭（只影响本群）'
                    : '回答时不再引用触发消息，点击开启（只影响本群）'
                "
                @click="toggleQuoteReply"
              >
                {{ detail.quote_reply ? '已开启' : '已关闭' }}
              </button>
              <span v-else class="text-slate-300">
                {{ detail.quote_reply ? '跟随全局（开）' : '跟随全局（关）' }}
              </span>
            </div>
            <div
              v-if="detail.platform"
              class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm"
            >
              <span class="flex items-center gap-1 text-slate-500">
                平台接收设置
                <button
                  type="button"
                  class="cursor-pointer rounded p-0.5 text-slate-500 transition-colors hover:bg-slate-700/50 hover:text-slate-200"
                  title="重新向 QQ 平台读取该群的机器人权限（在群里改过设置后点这里）"
                  :disabled="platformRefreshing"
                  @click="refreshPlatformState"
                >
                  <AppIcon name="refresh" :size="12" :class="platformRefreshing ? 'animate-spin' : ''" />
                </button>
              </span>
              <span
                class="badge"
                :class="
                  detail.platform.recv_msg_setting === 'all'
                    ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                    : 'border-amber-500/40 bg-amber-500/10 text-amber-300'
                "
                :title="`recv_msg_setting = ${detail.platform.recv_msg_setting}`"
              >
                {{ recvSettingLabel(detail.platform.recv_msg_setting) }}
              </span>
            </div>
            <div
              v-if="detail.platform"
              class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm"
            >
              <span class="text-slate-500">主动消息</span>
              <span
                class="badge"
                :class="
                  detail.platform.allow_proactive_msg
                    ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                    : 'border-rose-500/40 bg-rose-500/10 text-rose-300'
                "
              >
                {{ detail.platform.allow_proactive_msg ? '允许' : '不允许' }}
              </span>
            </div>
            <div
              v-if="detail.platform?.member_openid"
              class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm"
            >
              <span class="text-slate-500">机器人群内 OpenID</span>
              <span class="flex min-w-0 items-center gap-1">
                <span class="mono truncate text-slate-300" :title="detail.platform.member_openid">
                  {{ detail.platform.member_openid }}
                </span>
                <CopyButton :value="detail.platform.member_openid" label="机器人群内 OpenID" />
              </span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">消息 / 轮次</span>
              <span class="text-slate-200 tabular-nums">
                {{ formatNumber(detail.message_count) }} / {{ formatNumber(detail.turn_count) }}
              </span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">tokens 估算</span>
              <span class="text-slate-200 tabular-nums">{{ formatNumber(detail.token_estimate) }}</span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">创建时间</span>
              <span class="text-slate-200">{{ formatDateTime(detail.created_at) }}</span>
            </div>
            <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
              <span class="text-slate-500">最近活跃</span>
              <span class="text-slate-200">{{ formatRelative(detail.last_active_at) }}</span>
            </div>
          </div>
        </section>

        <!-- 消息气泡 -->
        <section class="card-fill">
          <div class="card-header">
            <h3 class="card-title">
              <AppIcon name="mail" :size="15" class="text-sky-400" />
              上下文消息
              <span class="text-xs font-normal text-slate-500">共 {{ messages.length }} 条</span>
            </h3>
            <div class="flex flex-wrap items-center gap-2">
              <span v-if="lastRefreshedAt" class="text-xs text-slate-500">
                最后更新 {{ lastRefreshedAt.toLocaleTimeString('zh-CN', { hour12: false }) }}
              </span>
              <button
                type="button"
                class="btn btn-sm"
                :disabled="manualRefreshing"
                title="立即刷新消息列表"
                @click="refreshNow"
              >
                <AppIcon name="refresh" :size="14" :class="manualRefreshing ? 'animate-spin' : ''" />
                刷新
              </button>
              <button
                type="button"
                class="btn btn-sm"
                :class="liveRefresh ? 'text-emerald-300' : ''"
                :title="liveRefresh ? '正在实时刷新，点击暂停' : '已暂停实时刷新，点击开启'"
                @click="liveRefresh = !liveRefresh"
              >
                <span
                  class="h-2 w-2 rounded-full"
                  :class="liveRefresh ? 'bg-emerald-400 pulse-dot' : 'bg-slate-500'"
                />
                {{ liveRefresh ? '实时刷新中' : '实时刷新已暂停' }}
              </button>
              <button
                type="button"
                class="btn btn-sm"
                :class="autoScroll ? 'text-emerald-300' : ''"
                :title="autoScroll ? '有新消息时自动滚到底部，点击关闭' : '已关闭自动滚动，点击开启'"
                @click="autoScroll = !autoScroll"
              >
                <AppIcon name="arrowDown" :size="14" />
                {{ autoScroll ? '自动滚动' : '自动滚动已关' }}
              </button>
              <button
                v-if="hasMore"
                type="button"
                class="btn btn-sm"
                :disabled="loadingMore"
                @click="loadOlder"
              >
                <AppIcon name="refresh" :size="14" :class="loadingMore ? 'animate-spin' : ''" />
                加载更早
              </button>
              <button
                type="button"
                class="btn btn-sm"
                :class="membersOpen ? 'text-emerald-300' : ''"
                :aria-expanded="membersOpen"
                :title="membersOpen ? '收起右侧群成员侧栏' : '展开右侧群成员侧栏（默认折叠）'"
                @click="membersOpen = !membersOpen"
              >
                <AppIcon name="users" :size="14" />
                群成员 {{ speakers.length }}
              </button>
            </div>
          </div>

          <div class="flex min-h-0 flex-1">
            <!-- 消息列：列表 + 回到底部浮层 + 输入栏 -->
            <div class="flex min-w-0 flex-1 flex-col">
              <div class="relative flex min-h-0 flex-1 flex-col">
                <div ref="listRef" class="card-body scroll-y" @scroll.passive="syncAtBottom">
                  <EmptyState
                    v-if="messages.length === 0"
                    title="上下文为空"
                    hint="该会话尚未积累消息，或已被清空。"
                  />
                  <ul v-else class="space-y-3">
                    <li
                      v-for="message in messages"
                      :key="message.id"
                      class="flex"
                      :class="[
                        message.role === 'assistant' ? 'justify-end' : 'justify-start',
                        quoteTarget?.id === message.id ? 'rounded-xl ring-1 ring-sky-500/40' : '',
                      ]"
                      @contextmenu.prevent="openMenu($event, message)"
                    >
                      <div
                        class="max-w-[min(46rem,85%)] rounded-xl border px-3 py-2"
                        :class="
                          message.kind === 'manual'
                            ? 'border-cyan-500/40 bg-cyan-500/10'
                            : message.role === 'assistant'
                              ? 'border-sky-500/30 bg-sky-500/10'
                              : message.kind === 'observed' || message.kind === 'observed_bot'
                                ? 'border-dashed border-slate-700/70 bg-slate-900/40'
                                : 'border-slate-700 bg-slate-800/40'
                        "
                      >
                        <div class="mb-1 flex flex-wrap items-center gap-2 text-[11px] text-slate-400">
                          <span
                            class="font-medium"
                            :class="
                              message.kind === 'manual'
                                ? 'text-cyan-300'
                                : message.role === 'assistant'
                                  ? 'text-sky-300'
                                  : message.kind === 'observed' || message.kind === 'observed_bot'
                                    ? 'text-slate-400'
                                    : 'text-slate-200'
                            "
                          >
                            {{ message.speaker || roleLabel(message.role) }}
                          </span>
                          <span
                            v-if="message.speaker_openid"
                            class="flex items-center gap-0.5"
                            :title="`${message.role === 'assistant' ? '机器人' : '发言人'} OpenID：${message.speaker_openid}`"
                          >
                            <span class="mono text-slate-500">{{ shortId(message.speaker_openid) }}</span>
                            <CopyButton
                              :value="message.speaker_openid"
                              :label="message.role === 'assistant' ? '机器人 OpenID' : '发言人 OpenID'"
                              :size="12"
                            />
                          </span>
                          <span
                            v-if="message.kind === 'observed'"
                            class="badge border-slate-600/50 bg-slate-700/20 text-slate-400"
                            title="群成员没有 @ 机器人：仅记录在会话时间线里，不会触发回复，也不进入模型上下文"
                          >
                            未 @ 机器人
                          </span>
                          <span
                            v-else-if="message.kind === 'observed_bot'"
                            class="badge border-violet-500/40 bg-violet-500/10 text-violet-300"
                            title="群里另一个机器人的发言：仅记录，不会被当作对你的 @，我们也不会回复"
                          >
                            其他机器人
                          </span>
                          <span
                            v-else-if="message.kind === 'manual'"
                            class="badge border-cyan-500/40 bg-cyan-500/10 text-cyan-300"
                            title="管理员在后台以机器人身份发出：会消耗平台主动消息配额，并作为机器人的发言进入上下文"
                          >
                            后台发送
                          </span>
                          <span class="tabular-nums">#{{ message.id }}</span>
                          <span v-if="message.tokens">{{ formatNumber(message.tokens) }} tokens</span>
                          <span>{{ formatDateTime(message.created_at) }}</span>
                        </div>
                        <div v-if="imagesOf(message).length" class="mb-1.5 flex flex-wrap gap-2">
                          <button
                            v-for="img in imagesOf(message)"
                            :key="img.media_id"
                            type="button"
                            class="group relative overflow-hidden rounded-lg border border-slate-700 transition hover:border-sky-500/60"
                            :title="img.filename || '点击放大'"
                            @click="previewImage = { url: mediaURL(img.media_id!), label: img.filename || '图片' }"
                          >
                            <img
                              :src="mediaURL(img.media_id!)"
                              :alt="img.filename || '图片'"
                              class="max-h-48 max-w-[16rem] object-contain"
                              loading="lazy"
                            />
                          </button>
                        </div>
                        <p
                          v-if="message.content && !(imagesOf(message).length && message.content === '[附件消息]')"
                          class="text-sm leading-relaxed break-words whitespace-pre-wrap text-slate-200"
                          >{{ message.content }}</p
                        >
                        <ul v-if="otherFiles(message).length" class="mt-1 space-y-0.5 text-xs text-slate-400">
                          <li v-for="(f, i) in otherFiles(message)" :key="i">📎 {{ attachmentLabel(f) }}</li>
                        </ul>
                      </div>
                    </li>
                  </ul>
                </div>
                <!-- 往上翻历史时浮在右下角：一键回到最新；期间来的新消息用角标计数。 -->
                <button
                  v-if="!atBottom && messages.length > 0"
                  type="button"
                  class="absolute right-4 bottom-4 z-10 flex items-center gap-1.5 rounded-full border border-slate-700 bg-slate-900/95 px-3 py-2 text-xs text-slate-200 shadow-lg backdrop-blur transition-colors hover:border-sky-500/60 hover:text-sky-300"
                  :title="pendingNew > 0 ? `有 ${pendingNew} 条新消息，点击回到最新` : '回到最新'"
                  @click="jumpToBottom"
                >
                  <AppIcon name="arrowDown" :size="15" />
                  回到底部
                  <span
                    v-if="pendingNew > 0"
                    class="badge min-w-[1.5rem] justify-center border-sky-500/40 bg-sky-500/20 tabular-nums text-sky-200"
                  >
                    {{ pendingNewLabel }}
                  </span>
                </button>

              </div>

              <!-- 消息输入栏：以机器人身份发一条主动消息 -->
              <div class="shrink-0 border-t border-slate-800 bg-slate-900/40 px-3 py-2">
                <div
                  v-if="quoteTarget"
                  class="mb-1.5 flex items-start gap-1.5 rounded-lg border border-sky-500/30 bg-sky-500/5 px-2 py-1.5 text-[11px]"
                >
                  <AppIcon name="chatAlt" :size="12" class="mt-0.5 shrink-0 text-sky-400" />
                  <span class="min-w-0 flex-1">
                    <span class="text-slate-400">
                      引用 {{ quoteTarget.speaker || roleLabel(quoteTarget.role) }}：
                    </span>
                    <span class="text-slate-300">{{ truncate(quoteTarget.content, 60) }}</span>
                  </span>
                  <button
                    type="button"
                    class="shrink-0 cursor-pointer rounded p-0.5 text-slate-500 transition-colors hover:bg-slate-700/50 hover:text-slate-200"
                    title="取消引用"
                    @click="quoteTarget = null"
                  >
                    <AppIcon name="close" :size="12" />
                  </button>
                </div>
                <div class="flex items-end gap-2">
                  <textarea
                    v-model="draft"
                    rows="2"
                    class="input max-h-40 min-h-[2.5rem] flex-1 resize-y py-2 text-sm"
                    placeholder="输入要发送到群里的消息…（Enter 发送，Shift+Enter 换行）"
                    :disabled="sending"
                    @keydown="onDraftKeydown"
                  ></textarea>
                  <button
                    type="button"
                    class="btn btn-primary shrink-0"
                    :disabled="!canSend"
                    :title="`以机器人身份发送到「${sendTargetName}」（主动消息）`"
                    @click="sendNow"
                  >
                    <AppIcon name="send" :size="15" :class="sending ? 'animate-pulse' : ''" />
                    {{ sending ? '发送中…' : '发送' }}
                  </button>
                </div>
                <p
                  v-if="sendError"
                  class="mt-1.5 flex items-start gap-1 text-[11px] text-rose-300"
                >
                  <AppIcon name="warning" :size="12" class="mt-0.5 shrink-0" />
                  <span>{{ sendError }}</span>
                </p>
                <p class="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-slate-500">
                  <span class="text-slate-400">
                    将以机器人身份发送到「<span class="text-slate-300">{{ sendTargetName }}</span
                    >」
                  </span>
                  <span class="text-amber-400/80">主动消息</span>
                  <span :class="quoteEnabled ? 'text-slate-400' : 'text-slate-500'">
                    {{ quoteEnabled ? '可右键消息引用' : '引用回复已关闭' }}
                  </span>
                  <span v-if="detail?.scene !== 'c2c'" class="tabular-nums">
                    今日已发 {{ detail?.active_today ?? 0 }} 条
                  </span>
                  <span class="tabular-nums">{{ draft.length }} 字</span>
                </p>
              </div>
            </div>

            <!-- 右侧「群成员」侧栏：聚合已加载消息里发言过的人（默认折叠） -->
            <aside
              v-if="membersOpen"
              class="flex w-56 shrink-0 flex-col border-l border-slate-800"
            >
              <div
                class="flex shrink-0 items-center justify-between gap-2 border-b border-slate-800 px-3 py-2"
              >
                <span class="flex items-center gap-1.5 text-xs font-semibold text-slate-300">
                  <AppIcon name="users" :size="13" class="text-sky-400" />
                  发言成员
                  <span class="tabular-nums text-slate-500">{{ speakers.length }}</span>
                </span>
                <button
                  type="button"
                  class="btn btn-sm"
                  :title="
                    memberSort === 'count'
                      ? '当前按发言条数排序，点击改为按最近发言'
                      : '当前按最近发言排序，点击改为按发言条数'
                  "
                  @click="memberSort = memberSort === 'count' ? 'recent' : 'count'"
                >
                  {{ memberSort === 'count' ? '按发言数' : '按最近' }}
                </button>
              </div>

              <div class="scroll-y scrollbar-thin px-2 py-2">
                <p v-if="speakers.length === 0" class="px-2 py-4 text-center text-xs text-slate-500">
                  还没有发言记录
                </p>
                <ul v-else class="space-y-0.5">
                  <li v-for="member in speakers" :key="member.key" class="px-1.5 py-1.5">
                    <div class="flex items-center gap-2">
                      <span
                        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-xs font-medium"
                        :class="avatarTone(member.name)"
                      >
                        {{ initialOf(member.name) }}
                      </span>
                      <span class="min-w-0 flex-1">
                        <span class="flex items-center gap-1">
                          <span class="min-w-0 truncate text-xs text-slate-200" :title="member.name">
                            {{ member.name }}
                          </span>
                          <StatusBadge
                            v-if="member.kind !== 'human'"
                            :info="memberBadge(member.kind)"
                          />
                        </span>
                        <span class="flex items-center gap-1 text-[11px] text-slate-500">
                          <span class="tabular-nums">{{ member.count }} 条</span>
                          <span>·</span>
                          <span class="truncate">{{ formatRelative(member.lastAt) }}</span>
                        </span>
                      </span>
                      <CopyButton
                        v-if="member.openid"
                        :value="member.openid"
                        label="发言人 OpenID"
                        :size="12"
                      />
                    </div>
                    <p
                      v-if="member.openid"
                      class="mono truncate pl-9 text-[10px] text-slate-600"
                      :title="member.openid"
                    >
                      {{ member.openid }}
                    </p>
                  </li>
                </ul>
              </div>

              <p class="shrink-0 border-t border-slate-800 px-3 py-2 text-[11px] text-slate-500">
                基于已加载的 {{ messages.length }} 条消息统计；向上翻历史会继续累加。
              </p>
            </aside>
          </div>
        </section>
      </template>


      <div v-else-if="loading" class="card card-body flex items-center justify-center gap-2 py-12 text-sm text-slate-400">
        <span class="h-4 w-4 animate-spin rounded-full border-2 border-slate-600 border-t-sky-400" />
        加载中…
      </div>

      <ConfirmDialog
        :open="clearOpen"
        title="清空会话上下文"
        danger
        confirm-text="清空"
        :loading="clearing"
        message="将删除该会话的全部消息记录（保留会话元数据），此操作不可撤销。"
        @cancel="clearOpen = false"
        @confirm="confirmClear"
      />

      <!-- 图片放大预览 -->
      <ModalDialog
        :open="previewImage !== null"
        :title="previewImage?.label || '图片'"
        width="max-w-4xl"
        @close="previewImage = null"
      >
        <img
          v-if="previewImage"
          :src="previewImage.url"
          :alt="previewImage.label"
          class="mx-auto max-h-[70vh] rounded-lg object-contain"
        />
        <p v-if="previewImage" class="mt-2 text-center text-xs text-slate-500">
          图片由 QQ 平台转发，已在本机缓存；原链接带时效，缓存保证历史消息里的图片仍然可见。
        </p>
      </ModalDialog>

      <!-- 消息右键菜单：引用 / 复制 -->
      <div
        v-if="menu"
        class="fixed z-50 w-[190px] overflow-hidden rounded-lg border border-slate-700 bg-slate-900 py-1 text-xs shadow-xl"
        :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"
        @click.stop
        @contextmenu.prevent
      >
        <button
          type="button"
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left transition-colors"
          :class="
            canQuote(menu.message)
              ? 'cursor-pointer text-slate-200 hover:bg-slate-800'
              : 'cursor-not-allowed text-slate-600'
          "
          :disabled="!canQuote(menu.message)"
          :title="
            canQuote(menu.message)
              ? '发送时引用这条消息'
              : quoteEnabled
                ? '这条消息没有平台引用索引（升级前写入的），无法引用'
                : '该群已关闭引用回复，可在上方元数据里打开'
          "
          @click="pickQuote"
        >
          <AppIcon name="chatAlt" :size="13" />
          引用这条消息
        </button>
        <button
          type="button"
          class="flex w-full cursor-pointer items-center gap-2 px-3 py-1.5 text-left text-slate-200 transition-colors hover:bg-slate-800"
          @click="copyMessage"
        >
          <AppIcon name="copy" :size="13" />
          复制内容
        </button>
      </div>
    </div>
  </div>
</template>
