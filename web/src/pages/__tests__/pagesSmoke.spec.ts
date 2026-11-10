import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import appRouter from '@/router'
import { ApiError } from '@/api'
import DashboardPage from '@/pages/DashboardPage.vue'
import PoliciesPage from '@/pages/PoliciesPage.vue'
import ConversationsPage from '@/pages/ConversationsPage.vue'
import MessagesPage from '@/pages/MessagesPage.vue'
import LogsPage from '@/pages/LogsPage.vue'
import TasksPage from '@/pages/TasksPage.vue'
import MemoriesPage from '@/pages/MemoriesPage.vue'
import ToolsPage from '@/pages/ToolsPage.vue'
import LoginPage from '@/pages/LoginPage.vue'
import AffectPage from '@/pages/AffectPage.vue'
import JoinPage from '@/pages/JoinPage.vue'
import SettingsLayout from '@/pages/settings/SettingsLayout.vue'

/**
 * 页面挂载冒烟。
 *
 * 背景：设置页拆成 6 个二级页之后，前端唯一的质量门是 vue-tsc + build ——
 * 这两样都拦不住「模板在运行期炸」「某个控件没渲染」「挂载时打出 Vue 警告」。
 * 这里用 jsdom 逐页真挂载（不依赖浏览器、不装 Playwright），把「至少能渲染」钉住：
 *   - 挂载不抛错；
 *   - 关键标题 / 控件出现；
 *   - 不产生 console.error / warn（白名单之外）。
 * API 全部 mock，绝不发真请求。
 */

const api = vi.hoisted(() => ({
  auth: { me: vi.fn(), login: vi.fn(), logout: vi.fn() },
  status: { get: vi.fn() },
  stats: { get: vi.fn() },
  config: { get: vi.fn(), update: vi.fn() },
  policies: { get: vi.fn(), update: vi.fn() },
  conversations: {
    list: vi.fn(),
    get: vi.fn(),
    clear: vi.fn(),
    setEnabled: vi.fn(),
    clearAll: vi.fn(),
    send: vi.fn(),
    setQuoteReply: vi.fn(),
  },
  messages: { list: vi.fn() },
  logs: { list: vi.fn() },
  actions: {
    reconnect: vi.fn(),
    reloadToken: vi.fn(),
    resetConfig: vi.fn(),
    syncCommands: vi.fn(),
    testAi: vi.fn(),
    testSend: vi.fn(),
  },
  merge: { candidates: vi.fn(), run: vi.fn() },
  groups: { state: vi.fn() },
  models: { list: vi.fn() },
  toolCalls: { list: vi.fn() },
  memories: { list: vi.fn(), add: vi.fn(), remove: vi.fn(), forgetGroup: vi.fn() },
  tasks: { list: vi.fn(), cancel: vi.fn() },
  commitments: { list: vi.fn(), cancel: vi.fn() },
  proactive: { overview: vi.fn() },
  join: { decisions: vi.fn(), topics: vi.fn() },
  affect: {
    packs: vi.fn(),
    conversations: vi.fn(),
    state: vi.fn(),
    history: vi.fn(),
    events: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    preset: vi.fn(),
    reset: vi.fn(),
  },
}))

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    authApi: api.auth,
    statusApi: api.status,
    statsApi: api.stats,
    configApi: api.config,
    policiesApi: api.policies,
    conversationsApi: api.conversations,
    messagesApi: api.messages,
    logsApi: api.logs,
    actionsApi: api.actions,
    mergeApi: api.merge,
    groupsApi: api.groups,
    modelsApi: api.models,
    toolCallsApi: api.toolCalls,
    memoriesApi: api.memories,
    tasksApi: api.tasks,
    commitmentsApi: api.commitments,
    proactiveApi: api.proactive,
    joinApi: api.join,
    affectApi: api.affect,
  }
})

const statusFixture = {
  version: '9.9.9-smoke',
  started_at: '2026-07-21T10:00:00Z',
  uptime_seconds: 3600,
  transport: 'websocket',
  trigger_mode: 'at_only',
  context_mode: 'per_member',
  platform: { known_groups: 1, full_access_groups: [], limited_groups: [] },
  ws: { state: 'open', connected: true },
  ai: { model: 'smoke-model', base_url: 'https://one-api.internal.example/v1' },
  store: {},
  counters: {},
}

const statsFixture = {
  range: '24h',
  bucket: 'hour',
  series: [{ ts: '2026-07-21T10:00:00Z', in: 3, out: 2, errors: 0, tokens: 120 }],
  top_groups: [{ group_openid: 'GROUP_A', group_name: '测试群', messages: 3 }],
  totals: { in: 3, out: 2, errors: 0, tokens: 120 },
}

const policiesFixture = {
  trigger_mode: 'at_only',
  context_mode: 'per_member',
  context_scope: 'addressed_only',
  group_allowlist: [],
  member_allowlist: [],
  admin_openids: [],
  blocked_keywords: [],
  reply_to_bots: true,
  quote_reply: true,
  group_overrides: [
    {
      group_openid: 'GROUP_A',
      enabled: true,
      tools: false,
      proactive: 'auto',
      tasks: true,
      memory: false,
      humanize: 'soft',
      web: true,
      exec: false,
      polish: false,
    },
  ],
  platform: { known_groups: 0, full_access_groups: [], limited_groups: [] },
  warnings: [],
}

const joinDecisionsFixture = [
  {
    id: 128,
    group_openid: 'GROUP_A',
    conversation_id: 'conv-1',
    message_id: 'm_1',
    speaker: '小明',
    preview: '有人懂这家店吗？周六想约个饭。',
    joined: true,
    confidence: 0.82,
    reason: '明确求助且 bot 参与过话题',
    topics: [
      { id: 't1', title: '周末聚餐', summary: '在约周六晚饭。', message_ids: [12, 13] },
    ],
    model: 'smoke-model',
    latency_ms: 840,
    created_at: '2026-07-21T10:00:00Z',
  },
  {
    id: 127,
    group_openid: 'GROUP_A',
    conversation_id: 'conv-1',
    message_id: 'm_2',
    speaker: '小红',
    preview: '哈哈哈哈这也太好笑了',
    joined: false,
    confidence: 0.18,
    reason: '闲聊，没有插话价值',
    topics: [],
    model: 'smoke-model',
    latency_ms: 620,
    created_at: '2026-07-21T09:58:00Z',
  },
]

const joinTopicsFixture = {
  group_openid: 'GROUP_A',
  topics: [
    {
      id: 't1',
      title: '周末聚餐',
      summary: '几个人在约周六晚饭的时间和地点。',
      message_ids: [12, 13, 14],
    },
    {
      id: 't2',
      title: '模型选型',
      summary: '在比较本地部署与调用云端的成本。',
      message_ids: [15, 16],
    },
  ],
  analyzed_at: '2026-07-21T10:00:00Z',
}

const configFixture = {
  source: { overrides_from_db: 3, overridden_keys: ['model', 'temperature', 'http_addr'] },
  readonly: {
    driver: 'postgres',
    storage: 'postgres://bot@postgres:5432/bot',
    version: '9.9.9-smoke',
  },
  editable: {
    model: 'smoke-model',
    openai_base_url: 'https://one-api.internal.example/v1',
    temperature: 0.5,
    max_tokens: 512,
    early_reply_enabled: true,
    early_reply_text: '让我想想',
    tools_enabled: true,
    tools_max_steps: 6,
    tools_total_budget_seconds: 100,
    tools_result_max_chars: 8000,
    memory_inject_max: 6,
    memory_max_per_scope: 300,
    memory_extract_every: 10,
    qq_intents: 33554432,
    command_panel_enabled: true,
    http_addr: ':8080',
    media_max_bytes: 4194304,
    log_level: 'info',
    log_buffer_size: 5000,
    humanize_level: 'medium',
    humanize_delay_scale: 1,
    polish_enabled: true,
    polish_model: '',
    polish_temperature: 0.7,
    polish_max_tokens: 0,
    polish_timeout_seconds: 20,
    polish_min_chars: 0,
    proactive_level: 'off',
    proactive_ratio: 10,
    proactive_max_credits: 3,
    proactive_quiet_start: 23,
    proactive_quiet_end: 8,
    proactive_min_interval_minutes: 10,
    tasks_enabled: true,
    tasks_max_per_member: 5,
    tasks_max_delay_hours: 168,
    commitment_enabled: true,
    commitment_max_open_per_member: 10,
    human_status_enabled: true,
    human_status_trigger_messages: 20,
    human_status_ttl_minutes: 120,
    human_status_timeline_messages: 120,
    human_status_vision_enabled: true,
    human_status_vision_model: '',
    affect_enabled: true,
    affect_pack: 'daily',
    affect_inject_mode: 'line',
    affect_event_classifier: 'rules',
    affect_tick_seconds: 300,
    affect_inject_max_tokens: 120,
    join_gate_enabled: true,
    join_gate_mode: 'llm',
    join_gate_model: '',
    join_gate_window: 20,
    join_gate_confidence: 0.7,
    join_gate_eval_cooldown_seconds: 60,
    join_gate_min_interval_minutes: 5,
    join_gate_max_per_hour: 3,
    join_gate_max_per_day: 12,
    web_enabled: true,
    exec_enabled: false,
  },
  openai_api_key_set: true,
  openai_api_key_source: 'db',
  qq_app_secret_set: true,
  admin_token_set: true,
}

const modelsFixture = {
  base_url: 'https://one-api.internal.example/v1',
  count: 2,
  models: [{ id: 'smoke-model' }, { id: 'other-model' }],
  ids: ['smoke-model', 'other-model'],
  current: 'smoke-model',
  current_in_list: true,
  cached: false,
  fetched_at: '2026-07-21T10:00:00Z',
}

const commitmentsFixture = [
  {
    id: 7,
    conversation_id: 'conv-1',
    group_openid: 'GROUP_A',
    member_openid: 'MEMBER_A',
    creator_openid: 'MEMBER_A',
    text: '明天把方案发到群里',
    status: 'waiting',
    priority: 1,
    deadline_at: '',
    next_check_at: '2026-07-21T12:00:00Z',
    last_nudge_at: '2026-07-21T10:00:00Z',
    nudge_count: 1,
    ack_at: '',
    ack_note: '',
    meta: '',
    created_at: '2026-07-21T09:00:00Z',
  },
  {
    id: 8,
    conversation_id: 'conv-1',
    group_openid: 'GROUP_A',
    member_openid: 'MEMBER_A',
    creator_openid: 'MEMBER_A',
    text: '昨天的会议纪要',
    status: 'done',
    priority: 0,
    deadline_at: '',
    next_check_at: '2026-07-20T12:00:00Z',
    last_nudge_at: '2026-07-20T10:00:00Z',
    nudge_count: 2,
    ack_at: '2026-07-20T11:00:00Z',
    ack_note: '已经发了',
    meta: '',
    created_at: '2026-07-19T09:00:00Z',
  },
]

const affectPacksFixture = [
  {
    id: 'daily',
    name: '日常心境',
    version: '1.0.0',
    description: '内置的日常情绪包',
    dimensions: [
      { key: 'valence', label: '心情', min: 0, max: 100, baseline: 55 },
      { key: 'energy', label: '精力', min: 0, max: 100, baseline: 50 },
      { key: 'stress', label: '压力', min: 0, max: 100, baseline: 35 },
      { key: 'closeness', label: '熟悉度', min: 0, max: 100, baseline: 45 },
      { key: 'patience', label: '耐心', min: 0, max: 100, baseline: 60 },
    ],
    events: [{ id: 'praise', label: '被夸', tone: 'positive' }],
    presets: [
      { id: 'calm', name: '平静' },
      { id: 'focused', name: '专注' },
    ],
  },
]

const affectConversationsFixture = [
  {
    id: 'conv-1',
    pack: 'daily',
    updated_at: '2026-07-21T09:59:00Z',
    dims: { valence: 62.5, energy: 48, stress: 31, closeness: 55, patience: 70 },
  },
]

const affectStateFixture = {
  conversation_id: 'conv-1',
  pack_id: 'daily',
  dims: { valence: 62.5, energy: 48, stress: 31, closeness: 55, patience: 70 },
  events: [{ id: 'praise', at: '2026-07-21T09:58:00Z' }],
  paused_until: '',
  ticked_at: '2026-07-21T09:55:00Z',
  updated_at: '2026-07-21T09:59:00Z',
  version: 3,
}

const affectBehaviorFixture = {
  max_chars_factor: 1.1,
  max_segments: 3,
  humanize_scale: 0.9,
  silence_boost: 0.05,
  initiative_factor: 1.15,
  tone: '轻松',
  reason: '心情好、精力一般',
}

const affectHistoryFixture = [
  { at: '2026-07-21T08:00:00Z', dims: { valence: 55, energy: 50, stress: 35, closeness: 45, patience: 60 } },
  { at: '2026-07-21T09:00:00Z', dims: { valence: 62.5, energy: 48, stress: 31, closeness: 55, patience: 70 } },
]

const affectEventsFixture = [
  {
    id: 1,
    conversation_id: 'conv-1',
    event_id: 'praise',
    tone: 'positive',
    source: 'rules',
    created_at: '2026-07-21T09:58:00Z',
  },
]

const conversationsFixture = {
  total: 1,
  page: 1,
  page_size: 20,
  items: [
    {
      id: 'conv-1',
      scene: 'group_member',
      group_openid: 'GROUP_A',
      group_name: '测试群',
      member_openid: 'MEMBER_A',
      member_name: '测试成员',
      enabled: true,
      message_count: 12,
      turn_count: 3,
      token_estimate: 900,
      last_active_at: '2026-07-21T09:59:00Z',
      created_at: '2026-07-21T09:00:00Z',
    },
  ],
}

/** Vue / 路由 / jsdom 自身的噪音，不算页面缺陷。 */
const CONSOLE_ALLOWLIST = [
  'VUE_ROUTER_R0103',
  'Not implemented:',
  'Failed to resolve component',
  '[Vue warn]: Failed to resolve component',
  'Extraneous non-props attributes',
]

let consoleErrors: string[] = []
let consoleWarns: string[] = []

function unexpected(messages: string[]): string[] {
  return messages.filter((message) => !CONSOLE_ALLOWLIST.some((allowed) => message.includes(allowed)))
}

function resetApiMocks(): void {
  for (const group of Object.values(api)) {
    for (const fn of Object.values(group)) fn.mockReset()
  }
  api.auth.me.mockResolvedValue({ authenticated: true, username: 'admin', auth_mode: 'token' })
  api.auth.logout.mockResolvedValue(undefined)
  api.status.get.mockResolvedValue(statusFixture)
  api.stats.get.mockResolvedValue(statsFixture)
  api.policies.get.mockResolvedValue(policiesFixture)
  api.policies.update.mockImplementation(async (patch) => ({ ...policiesFixture, ...patch }))
  api.conversations.list.mockResolvedValue(conversationsFixture)
  api.conversations.setQuoteReply.mockResolvedValue({ group_openid: 'GROUP_A', quote_reply: true })
  api.merge.candidates.mockResolvedValue({ groups: [] })
  api.config.get.mockResolvedValue(configFixture)
  api.config.update.mockImplementation(async (patch) => ({
    editable: { ...configFixture.editable, ...patch },
    warnings: [],
    restartRequired: [],
  }))
  api.models.list.mockResolvedValue(modelsFixture)
  api.memories.list.mockResolvedValue([])
  api.tasks.list.mockResolvedValue([])
  api.commitments.list.mockResolvedValue(commitmentsFixture)
  api.commitments.cancel.mockResolvedValue({ canceled: true })
  api.toolCalls.list.mockResolvedValue({ items: [], next_before_id: 0 })
  api.proactive.overview.mockResolvedValue({
    items: [],
    count: 0,
    level: 'off',
    ratio: 10,
    max_credits: 3,
    quiet_hours: [23, 8],
    min_interval_minutes: 10,
  })
  api.join.decisions.mockResolvedValue(joinDecisionsFixture)
  api.join.topics.mockResolvedValue(joinTopicsFixture)
  api.logs.list.mockResolvedValue([])
  api.messages.list.mockResolvedValue({ total: 0, page: 1, page_size: 20, items: [] })
  api.affect.packs.mockResolvedValue(affectPacksFixture)
  api.affect.conversations.mockResolvedValue(affectConversationsFixture)
  api.affect.state.mockResolvedValue({
    state: affectStateFixture,
    behavior: affectBehaviorFixture,
    blocks: {
      line: '[此刻的状态｜仅作语气参考] 心情不错，语气轻快一点。',
      full: '[此刻的状态｜仅作语气参考] 心情不错。\n[自述] 今天还算有精神。',
    },
  })
  api.affect.history.mockResolvedValue(affectHistoryFixture)
  api.affect.events.mockResolvedValue(affectEventsFixture)
  api.affect.pause.mockResolvedValue({})
  api.affect.resume.mockResolvedValue({})
  api.affect.preset.mockResolvedValue({})
  api.affect.reset.mockResolvedValue({})
}

beforeEach(() => {
  resetApiMocks()
  consoleErrors = []
  consoleWarns = []
  vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
    consoleErrors.push(args.map(String).join(' '))
  })
  vi.spyOn(console, 'warn').mockImplementation((...args: unknown[]) => {
    consoleWarns.push(args.map(String).join(' '))
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

const mountOptions = {
  global: {
    plugins: [appRouter],
    stubs: { AppIcon: true, CopyButton: true, StatusBadge: true },
  },
}

async function mountRoute(component: Parameters<typeof mount>[0], path: string) {
  await appRouter.push(path)
  const wrapper = mount(component, mountOptions)
  await flushPromises()
  await flushPromises()
  return wrapper
}

describe('一级页面挂载冒烟', () => {
  it('概览：状态与统计渲染出来，没有控制台噪音', async () => {
    const wrapper = await mountRoute(DashboardPage, '/')
    const text = wrapper.text()
    expect(text).toContain('9.9.9-smoke')
    expect(text).toContain('smoke-model')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('策略配置：白名单与按群覆盖表渲染出来', async () => {
    const wrapper = await mountRoute(PoliciesPage, '/policies')
    const text = wrapper.text()
    expect(text).toContain('群白名单 group_allowlist')
    expect(text).toContain('按群覆盖 group_overrides')
    // 覆盖行里的群号是 input 的 value（不在 text 里）
    const groupInput = wrapper
      .findAll<HTMLInputElement>('tbody input.mono')
      .find((input) => input.element.value === 'GROUP_A')
    expect(groupInput, '覆盖表没有渲染出 fixture 里的 GROUP_A 行').toBeTruthy()
    // 新增的七列能力覆盖都在表头里
    for (const label of ['工具 tools', '主动 proactive', '自主加入 join_gate', '定时 tasks', '记忆 memory', '人味 humanize', '联网 web', '执行 exec', '润色 polish']) {
      expect(text, '覆盖表缺少列：' + label).toContain(label)
    }
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('登录页：品牌为 Bot Harness，旧品牌不再出现', async () => {
    const wrapper = await mountRoute(LoginPage, '/login')
    expect(wrapper.find('h1').text()).toBe('Bot Harness')
    expect(wrapper.text()).not.toContain('QQ AI 机器人')
    wrapper.unmount()
  })

  it('会话列表：拿到 fixture 后渲染出群名', async () => {
    const wrapper = await mountRoute(ConversationsPage, '/conversations')
    expect(wrapper.text()).toContain('测试群')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('消息记录：状态筛选包含「命令」（command）', async () => {
    const wrapper = await mountRoute(MessagesPage, '/messages')
    const command = wrapper
      .findAll<HTMLOptionElement>('#msg-status option')
      .find((option) => option.element.value === 'command')
    expect(command, '状态筛选缺少 command 选项').toBeTruthy()
    expect(command?.text()).toBe('命令')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('定时任务：承诺渲染后端字段、状态中文标签与取消按钮，取消走 DELETE', async () => {
    const wrapper = await mountRoute(TasksPage, '/tasks')
    const text = wrapper.text()
    expect(text).toContain('承诺追问')
    expect(text).toContain('明天把方案发到群里')
    // 后端状态 waiting / done → 中文标签
    expect(text).toContain('等待追问')
    expect(text).toContain('已完成')
    // 列头齐全（追问次数 / 下次追问取自 nudge_count / next_check_at）
    for (const header of ['事项', '状态', '已追问', '下次追问时间', '创建时间']) {
      expect(text).toContain(header)
    }
    const cancelButtons = wrapper
      .findAll('button')
      .filter((button) => button.attributes('title') === '取消这条承诺')
    // 只有 waiting / leased 可取消；done 行不渲染按钮。
    expect(cancelButtons.length, '终态承诺不应渲染取消按钮').toBe(1)
    await cancelButtons[0]?.trigger('click')
    await flushPromises()
    expect(api.commitments.cancel).toHaveBeenCalledWith(7)
    expect(api.commitments.list).toHaveBeenCalledTimes(2)
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  const listPages: { label: string; component: Parameters<typeof mount>[0]; path: string; anchor: string }[] = [
    { label: '消息记录', component: MessagesPage, path: '/messages', anchor: '消息记录' },
    { label: '日志', component: LogsPage, path: '/logs', anchor: '日志' },
    { label: '定时任务', component: TasksPage, path: '/tasks', anchor: '任务' },
    { label: '长期记忆', component: MemoriesPage, path: '/memories', anchor: '记忆' },
    { label: '工具调用', component: ToolsPage, path: '/tools', anchor: '工具' },
    { label: '情绪状态', component: AffectPage, path: '/affect', anchor: '情绪状态' },
    { label: '自主加入', component: JoinPage, path: '/join', anchor: '自主加入' },
    { label: '登录页', component: LoginPage, path: '/login', anchor: '登录' },
  ]

  for (const item of listPages) {
    it(item.label + '：挂载成功、无控制台噪音', async () => {
      const wrapper = await mountRoute(item.component, item.path)
      // 页面渲染出东西（不是空白），且正文里有关键词
      expect(wrapper.text().length).toBeGreaterThan(10)
      expect(wrapper.text()).toContain(item.anchor)
      expect(unexpected(consoleErrors)).toEqual([])
      expect(unexpected(consoleWarns)).toEqual([])
      wrapper.unmount()
    })
  }

  it('自主加入：渲染加入/跳过决策、置信度、原因与最新话题', async () => {
    const wrapper = await mountRoute(JoinPage, '/join')
    let text = wrapper.text()
    // 未选群时查全部群，但不请求话题接口。
    expect(api.join.decisions).toHaveBeenCalledWith('', 50)
    expect(api.join.topics).not.toHaveBeenCalled()
    expect(text).toContain('有人懂这家店吗？周六想约个饭。')
    expect(text).toContain('哈哈哈哈这也太好笑了')
    expect(text).toContain('明确求助且 bot 参与过话题')
    expect(text).toContain('闲聊，没有插话价值')
    expect(text).toContain('置信度 82%')
    expect(text).toContain('置信度 18%')
    expect(text).toContain('加入')
    expect(text).toContain('跳过')
    expect(text).toContain('周末聚餐')
    expect(text).toContain('选择一个群后显示它最近一次话题切分')

    // 选一个群后拉取该群话题，标题 / 摘要 / message_ids 都渲染出来。
    await wrapper.find('#join-group').setValue('GROUP_A')
    await flushPromises()
    await flushPromises()
    expect(api.join.topics).toHaveBeenCalledWith('GROUP_A')
    expect(api.join.decisions).toHaveBeenLastCalledWith('GROUP_A', 50)
    text = wrapper.text()
    expect(text).toContain('几个人在约周六晚饭的时间和地点。')
    expect(text).toContain('在比较本地部署与调用云端的成本。')
    expect(text).toContain('12, 13, 14')
    expect(text).toContain('15, 16')

    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('自主加入：未启用时显示空状态与一键启用引导', async () => {
    api.config.get.mockResolvedValue({
      ...configFixture,
      editable: { ...configFixture.editable, join_gate_enabled: false },
    })
    api.join.decisions.mockResolvedValue([])
    api.join.topics.mockResolvedValue({ group_openid: 'GROUP_A', topics: [], analyzed_at: '' })

    const wrapper = await mountRoute(JoinPage, '/join')
    const text = wrapper.text()
    expect(text).toContain('默认关闭')
    expect(text).toContain('还没有加入决策')
    expect(text).toContain('自主加入尚未启用')
    expect(text).toContain('一键启用自主加入')
    // 配置区仍在，且说明默认关与成本。
    expect(text).toContain('join_gate_enabled')
    expect(text).toContain('每次评估最多调用一次模型')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })
})

describe('设置页 6 个二级页挂载冒烟', () => {
  const cases: { path: string; label: string; anchors: string[] }[] = [
    { path: '/settings/provider', label: '模型与提示词', anchors: ['模型端点 openai_base_url', '模型 API Key openai_api_key'] },
    {
      path: '/settings/reply',
      label: '回复与人味',
      anchors: [
        '档位 humanize_level',
        '人设 persona',
        '两阶段生成',
        '润色模型 polish_model',
        '润色温度 polish_temperature',
        '润色输出上限 polish_max_tokens',
        '润色超时 polish_timeout_seconds',
        '最短润色长度 polish_min_chars',
        '承诺追问',
        'commitment_enabled',
        'commitment_max_open_per_member',
      ],
    },
    {
      path: '/settings/capability',
      label: '能力',
      anchors: [
        '情绪状态',
        '默认关闭',
        '日常心境',
        '前往情绪状态页',
        '工具调用',
        'tools_enabled',
        'tools_max_steps',
        'tools_total_budget_seconds',
        'tools_result_max_chars',
        '自主加入',
        '前往自主加入页',
        'join_gate_enabled',
        '人类状态感知',
        'human_status_enabled',
        'human_status_trigger_messages',
        'human_status_ttl_minutes',
        'human_status_timeline_messages',
        'human_status_vision_enabled',
        'human_status_vision_model',
      ],
    },
    { path: '/settings/proactive', label: '主动消息', anchors: ['自发主动'] },
    {
      path: '/settings/connection',
      label: '接入与运维',
      anchors: ['事件通道 qq_transport', '后台口令 admin_token', 'QQ 指令面板', '立即同步'],
    },
    {
      path: '/settings/system',
      label: '系统与诊断',
      anchors: ['重置配置覆盖', '数据库覆盖', '数据库引擎', '连接地址'],
    },
  ]

  for (const item of cases) {
    it(item.label + '：挂载成功、关键控件在场、无控制台噪音', async () => {
      const wrapper = await mountRoute(SettingsLayout, item.path)
      const text = wrapper.text()
      for (const anchor of item.anchors) {
        expect(text, item.label + ' 缺少：' + anchor).toContain(anchor)
      }
      // 6 个二级页 tab 常驻
      for (const tab of ['模型与提示词', '回复与人味', '能力', '主动消息', '接入与运维', '系统与诊断']) {
        expect(text).toContain(tab)
      }
      expect(unexpected(consoleErrors)).toEqual([])
      expect(unexpected(consoleWarns)).toEqual([])
      wrapper.unmount()
    })
  }

  it('两阶段生成：polish_enabled=false 时润色控件被禁用而不是隐藏', async () => {
    api.config.get.mockResolvedValue({
      ...configFixture,
      editable: { ...configFixture.editable, polish_enabled: false },
    })
    const wrapper = await mountRoute(SettingsLayout, '/settings/reply')
    const model = wrapper.find<HTMLInputElement>('input[placeholder="留空 = 使用主模型"]')
    expect(model.exists()).toBe(true)
    expect(model.element.disabled).toBe(true)
    // 4 个数字输入（temperature / max_tokens / timeout / min_chars）一起禁用；
    // 本页其它数字输入（人味、手动发送、提前回复）不受影响。
    const disabledNumbers = wrapper
      .findAll<HTMLInputElement>('input[type="number"]')
      .filter((input) => input.element.disabled)
    expect(disabledNumbers).toHaveLength(4)
    expect(wrapper.text()).toContain('已关闭')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('系统与诊断：只读块报「引擎 + 连接 + 版本」，不回显落盘细节', async () => {
    const wrapper = await mountRoute(SettingsLayout, '/settings/system')
    const text = wrapper.text()
    // 正向：启动期信息以 store 的中性表述出现
    expect(text).toContain('数据库引擎')
    expect(text).toContain('连接地址')
    expect(text).toContain('postgres')
    expect(text).toContain('postgres://bot@postgres:5432/bot')
    expect(text).toContain('9.9.9-smoke')
    // 负向：界面不得回显具体引擎名 / 落盘路径 / 文件名
    expect(text).not.toContain('SQLite')
    expect(text).not.toContain('db_path')
    expect(text).not.toContain('bot.db')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('QQ 指令面板：点「立即同步」调用动作并显示 group/c2c 结果', async () => {
    api.actions.syncCommands.mockResolvedValue({
      results: [
        { scope: 'group', panel_id: 'PANEL_GROUP', action: 'created', version: 0 },
        { scope: 'c2c', panel_id: 'PANEL_C2C', action: 'unchanged', version: 1 },
      ],
    })
    const wrapper = await mountRoute(SettingsLayout, '/settings/connection')
    const button = wrapper.findAll('button').find((item) => item.text().includes('立即同步'))
    expect(button, '找不到「立即同步」按钮').toBeTruthy()
    await button?.trigger('click')
    await flushPromises()

    const text = wrapper.text()
    expect(api.actions.syncCommands).toHaveBeenCalledTimes(1)
    // 逐面板结果：scope（group / c2c）、panel_id、action 都渲染出来
    expect(text).toContain('PANEL_GROUP')
    expect(text).toContain('PANEL_C2C')
    expect(text).toContain('created')
    expect(text).toContain('unchanged')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('QQ 指令面板：同步失败显示错误（upstream_error），不渲染结果表', async () => {
    api.actions.syncCommands.mockRejectedValue(
      new ApiError('upstream_error', '平台指令面板接口返回 502', 502),
    )
    const wrapper = await mountRoute(SettingsLayout, '/settings/connection')
    const button = wrapper.findAll('button').find((item) => item.text().includes('立即同步'))
    expect(button, '找不到「立即同步」按钮').toBeTruthy()
    await button?.trigger('click')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('同步失败')
    expect(text).toContain('平台指令面板接口返回 502')
    expect(text).not.toContain('panel_id')
    expect(unexpected(consoleErrors)).toEqual([])
    expect(unexpected(consoleWarns)).toEqual([])
    wrapper.unmount()
  })

  it('配置加载完成后控件显示的是后端值（不是占位/空值）', async () => {
    const wrapper = await mountRoute(SettingsLayout, '/settings/provider')
    const baseUrl = wrapper.find<HTMLInputElement>('#cfg-base-url')
    expect(baseUrl.exists()).toBe(true)
    expect(baseUrl.element.value).toBe('https://one-api.internal.example/v1')
    // 枚举控件同样落到后端值（回复与人味 → 档位 humanize_level = medium）
    await appRouter.push('/settings/reply')
    await flushPromises()
    const humanize = wrapper.find<HTMLSelectElement>('#cfg-humanize')
    expect(humanize.exists()).toBe(true)
    expect(humanize.element.value).toBe('medium')
    wrapper.unmount()
  })

  it('保存只提交被改动的字段（patch 不含未改动项）', async () => {
    const wrapper = await mountRoute(SettingsLayout, '/settings/provider')
    const baseUrl = wrapper.find<HTMLInputElement>('#cfg-base-url')
    expect(baseUrl.element.value).toBe('https://one-api.internal.example/v1')

    await baseUrl.setValue('https://changed.internal.example/v1')
    const saveButton = wrapper.findAll('button').find((button) => button.text().includes('保存修改'))
    expect(saveButton, '找不到「保存修改」按钮').toBeTruthy()
    await saveButton?.trigger('click')
    await flushPromises()

    expect(api.config.update).toHaveBeenCalledTimes(1)
    expect(api.config.update).toHaveBeenCalledWith({
      openai_base_url: 'https://changed.internal.example/v1',
    })
    wrapper.unmount()
  })
})
