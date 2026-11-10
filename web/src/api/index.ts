import { request, requestEnvelope } from './client'
import type {
  AffectConversationsData,
  AffectEventsData,
  AffectHistoryData,
  AffectPacksData,
  AffectStateData,
  ConfigData,
  ConfigUpdateResult,
  Conversation,
  ConversationDetail,
  ConversationMessagesQuery,
  ConversationQuery,
  EditableConfig,
  JoinDecisionsData,
  JoinTopicsData,
  LogItem,
  LogList,
  LogQuery,
  LoginResult,
  ManualSendResult,
  MeResult,
  MessageItem,
  MergeableGroupsData,
  MergeGroupResult,
  MessageQuery,
  ModelsData,
  Paged,
  PlatformGroupState,
  Policies,
  QuoteReplyState,
  StatsData,
  MemoryItem,
  ProactiveOverview,
  TaskItem,
  ToolCallItem,
  CommitmentItem,
  StatsRange,
  StatusData,
  SyncCommandsResult,
  TestAiResult,
  TestSendResult,
} from './types'

export * from './client'
export * from './types'

/* ------------------------------------------------------------------ *
 * 1. 认证
 * ------------------------------------------------------------------ */

export const authApi = {
  login(token: string): Promise<LoginResult> {
    return request<LoginResult>('/auth/login', {
      method: 'POST',
      body: { token },
      skipAuthRedirect: true,
    })
  },
  logout(): Promise<unknown> {
    return request<unknown>('/auth/logout', { method: 'POST', skipAuthRedirect: true })
  },
  me(): Promise<MeResult> {
    return request<MeResult>('/auth/me', { skipAuthRedirect: true })
  },
}

/* ------------------------------------------------------------------ *
 * 2. 运行状态 / 7. 统计
 * ------------------------------------------------------------------ */

export const statusApi = {
  get(signal?: AbortSignal): Promise<StatusData> {
    return request<StatusData>('/status', { signal })
  },
}

export const statsApi = {
  get(range: StatsRange = '24h', signal?: AbortSignal): Promise<StatsData> {
    return request<StatsData>('/stats', { query: { range }, signal })
  },
}

/* ------------------------------------------------------------------ *
 * 3. 配置
 * ------------------------------------------------------------------ */

export interface ConfigUpdateOutcome {
  /** 保存后的扁平设置（后端 data.settings）。 */
  editable: EditableConfig
  /** 被跳过的字段说明（例如非法的按群覆盖）。 */
  warnings: string[]
  /** 需要重启才完全生效的字段（契约 §3）。 */
  restartRequired: string[]
}

export const configApi = {
  get(signal?: AbortSignal): Promise<ConfigData> {
    return request<ConfigData>('/config', { signal })
  },
  async update(patch: Partial<EditableConfig>): Promise<ConfigUpdateOutcome> {
    const envelope = await requestEnvelope<ConfigUpdateResult>('/config', {
      method: 'PUT',
      body: patch,
    })
    const data = envelope.data ?? {}
    const topLevel = envelope.restart_required
    const restartRequired = Array.isArray(topLevel)
      ? topLevel.map(String)
      : Array.isArray(data.restart_required)
        ? data.restart_required.map(String)
        : []
    // 后端返回 {settings, warnings, restart_required}，settings 才是扁平的可编辑项。
    // 之前把整个 data 当扁平字段用，导致每个控件都拿到 undefined（保存后全变空）。
    const settings = (data.settings ?? {}) as EditableConfig
    const warnings = Array.isArray(data.warnings) ? data.warnings.map(String) : []
    return { editable: settings, warnings, restartRequired }
  },
}

/* ------------------------------------------------------------------ *
 * 4. 策略
 * ------------------------------------------------------------------ */

export const policiesApi = {
  get(signal?: AbortSignal): Promise<Policies> {
    return request<Policies>('/policies', { signal })
  },
  update(patch: Partial<Policies>, signal?: AbortSignal): Promise<Policies> {
    return request<Policies>('/policies', { method: 'PUT', body: patch, signal })
  },
}

/* ------------------------------------------------------------------ *
 * 5. 会话与上下文
 * ------------------------------------------------------------------ */

export const conversationsApi = {
  list(query: ConversationQuery, signal?: AbortSignal): Promise<Paged<Conversation>> {
    return request<Paged<Conversation>>('/conversations', { query: { ...query }, signal })
  },
  get(id: string, query?: ConversationMessagesQuery, signal?: AbortSignal): Promise<ConversationDetail> {
    return request<ConversationDetail>(`/conversations/${encodeURIComponent(id)}`, {
      query: { ...query },
      signal,
    })
  },
  /** 清空该会话消息但保留会话元数据。 */
  clear(id: string): Promise<unknown> {
    return request<unknown>(`/conversations/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },
  setEnabled(id: string, enabled: boolean): Promise<unknown> {
    return request<unknown>(`/conversations/${encodeURIComponent(id)}/enabled`, {
      method: 'POST',
      body: { enabled },
    })
  },
  /** 清空所有会话，必须带 confirm=CLEAR_ALL。 */
  clearAll(): Promise<unknown> {
    return request<unknown>('/conversations', { query: { confirm: 'CLEAR_ALL' }, method: 'DELETE' })
  },
  /**
   * 以机器人的身份手动发一条消息到该会话对应的群 / 私聊。
   * 一定是主动消息：会消耗平台的主动消息配额，并且需要该群已开启主动消息权限。
   */
  send(id: string, content: string, quoteMessageId?: number): Promise<ManualSendResult> {
    return request<ManualSendResult>(`/conversations/${encodeURIComponent(id)}/messages`, {
      method: 'POST',
      body: { content, quote_message_id: quoteMessageId ?? 0 },
    })
  },
  /** 设置该会话所属群的「引用回复」开关。 */
  setQuoteReply(id: string, enabled: boolean | null): Promise<QuoteReplyState> {
    return request<QuoteReplyState>(`/conversations/${encodeURIComponent(id)}/quote-reply`, {
      method: 'POST',
      body: { enabled },
    })
  },
}

/* ------------------------------------------------------------------ *
 * 6. 消息与日志
 * ------------------------------------------------------------------ */

export const messagesApi = {
  list(query: MessageQuery, signal?: AbortSignal): Promise<Paged<MessageItem>> {
    return request<Paged<MessageItem>>('/messages', { query: { ...query }, signal })
  },
}

export const logsApi = {
  list(query: LogQuery, signal?: AbortSignal): Promise<LogItem[]> {
    return request<LogList>('/logs', { query: { ...query }, signal }).then((data) =>
      Array.isArray(data?.items) ? data.items : [],
    )
  },
}

/* ------------------------------------------------------------------ *
 * 8. 运维动作
 * ------------------------------------------------------------------ */

export const actionsApi = {
  /** 强制重建 WS 连接。 */
  reconnect(): Promise<unknown> {
    return request<unknown>('/actions/reconnect', { method: 'POST', body: {} })
  },
  /** 立即刷新 access_token。 */
  reloadToken(): Promise<unknown> {
    return request<unknown>('/actions/reload-token', { method: 'POST', body: {} })
  },
  /** 清空数据库配置覆盖，回到内置默认值。 */
  /** 清空数据库配置覆盖。返回 warnings：端点/密钥会变为未配置时给出提示。 */
  resetConfig(): Promise<{ settings?: EditableConfig; warnings?: string[] }> {
    return request<{ settings?: EditableConfig; warnings?: string[] }>('/actions/reset-config', {
      method: 'POST',
      body: {},
    })
  },
  /**
   * 立即把 Bot 命令同步到 QQ 指令面板（群 / 私聊）。
   * 失败是 502 upstream_error；成功返回逐面板的结果。
   */
  syncCommands(): Promise<SyncCommandsResult> {
    return request<SyncCommandsResult>('/actions/sync-commands', { method: 'POST', body: {} })
  },
  /** 一次性调用模型。 */
  testAi(prompt: string): Promise<TestAiResult> {
    return request<TestAiResult>('/actions/test-ai', { method: 'POST', body: { prompt } })
  },
  /** 发送主动消息（受平台频控）。 */
  testSend(groupOpenid: string, content: string): Promise<TestSendResult> {
    return request<TestSendResult>('/actions/test-send', {
      method: 'POST',
      body: { group_openid: groupOpenid, content },
    })
  },
}

/* ------------------------------------------------------------------ *
 * 会话合并（把同群的分成员会话并入共享会话）
 * ------------------------------------------------------------------ */

export const mergeApi = {
  /** 列出还有多个会话的群。 */
  candidates(signal?: AbortSignal): Promise<MergeableGroupsData> {
    return request<MergeableGroupsData>('/groups/mergeable', { signal })
  },
  /** 执行（或试运行）合并；服务端会严格校验所有会话都属于该群。 */
  run(groupOpenid: string, targetId?: string, dryRun = false): Promise<MergeGroupResult> {
    return request<MergeGroupResult>(
      `/groups/${encodeURIComponent(groupOpenid)}/conversations/merge`,
      {
        method: 'POST',
        body: { target_id: targetId ?? '', dry_run: dryRun },
      },
    )
  },
}

/* ------------------------------------------------------------------ *
 * 群内平台状态
 * ------------------------------------------------------------------ */

export const groupsApi = {
  /**
   * 读取某群在平台侧的机器人状态（接收设置、是否允许主动消息、自身 openid）。
   * @param refresh 传 true 绕过服务端缓存强制刷新。
   */
  state(groupOpenid: string, refresh = false, signal?: AbortSignal): Promise<PlatformGroupState> {
    return request<PlatformGroupState>(`/groups/${encodeURIComponent(groupOpenid)}/state`, {
      query: refresh ? { refresh: 1 } : undefined,
      signal,
    })
  },
  /**
   * 批量取群名（GET /api/groups/names?openids=a,b,c）。
   *
   * 后端最多接受 100 个 openid，查不到的群不会出现在返回里；
   * 调用方必须自己兜底成 openid —— 群名只是展示优化，不能让它决定页面能不能用。
   */
  names(openids: string[], signal?: AbortSignal): Promise<Record<string, string>> {
    const unique = Array.from(new Set(openids.map((id) => id.trim()).filter(Boolean)))
    if (unique.length === 0) return Promise.resolve({})
    return request<{ names?: Record<string, string> }>('/groups/names', {
      query: { openids: unique.join(',') },
      signal,
    }).then((data) => (data && typeof data.names === 'object' && data.names ? data.names : {}))
  },
}

/* ------------------------------------------------------------------ *
 * 模型目录
 * ------------------------------------------------------------------ */

export const modelsApi = {
  /**
   * 拉取上游 /models 返回的可用模型列表。
   * @param refresh 传 true 绕过服务端 5 分钟缓存强制刷新。
   */
  list(refresh = false, signal?: AbortSignal): Promise<ModelsData> {
    return request<ModelsData>('/models', {
      query: refresh ? { refresh: 1 } : undefined,
      signal,
    })
  },
}

/* ------------------------------------------------------------------ *
 * 工具调用审计
 * ------------------------------------------------------------------ */

export const toolCallsApi = {
  list(limit = 50, beforeID = 0, signal?: AbortSignal): Promise<{ items: ToolCallItem[]; next_before_id: number }> {
    return request('/tool-calls', {
      query: { limit, before_id: beforeID || undefined },
      signal,
    })
  },
}

/* ------------------------------------------------------------------ *
 * 长期记忆
 * ------------------------------------------------------------------ */

export const memoriesApi = {
  list(query: { scope?: string; group_openid?: string; member_openid?: string; limit?: number; offset?: number } = {},
       signal?: AbortSignal): Promise<MemoryItem[]> {
    return request<{ items: MemoryItem[] }>('/memories', { query: { ...query }, signal }).then((d) => d.items ?? [])
  },
  add(body: { scope: string; group_openid?: string; member_openid?: string; text: string }): Promise<{ id: number }> {
    return request<{ id: number }>('/memories', { method: 'POST', body })
  },
  remove(id: number): Promise<{ deleted: number }> {
    return request<{ deleted: number }>(`/memories/${id}`, { method: 'DELETE' })
  },
  forgetGroup(groupOpenID: string): Promise<{ deleted: number }> {
    return request<{ deleted: number }>('/memories', { method: 'DELETE', query: { group_openid: groupOpenID } })
  },
}

/* ------------------------------------------------------------------ *
 * 定时任务
 * ------------------------------------------------------------------ */

export const tasksApi = {
  list(status?: string, signal?: AbortSignal): Promise<TaskItem[]> {
    return request<{ items: TaskItem[] }>('/tasks', {
      query: { status: status || undefined, limit: 100 },
      signal,
    }).then((d) => d.items ?? [])
  },
  cancel(id: number): Promise<{ canceled: boolean }> {
    return request<{ canceled: boolean }>(`/tasks/${id}`, { method: 'DELETE' })
  },
}

/* ------------------------------------------------------------------ *
 * 承诺（open loop）
 * ------------------------------------------------------------------ */

export const commitmentsApi = {
  /** 列出承诺（后端返回完整历史，含 done / cancelled；页面自带状态标签）。 */
  list(signal?: AbortSignal): Promise<CommitmentItem[]> {
    return request<{ items: CommitmentItem[] }>('/commitments', { signal }).then((d) => d.items ?? [])
  },
  /**
   * 取消一条未收口承诺（DELETE /api/commitments/{id}）。
   * 只有 waiting / leased 可取消；已 done / cancelled 返回 409。
   */
  cancel(id: number): Promise<{ canceled: boolean }> {
    return request<{ canceled: boolean }>(`/commitments/${id}`, { method: 'DELETE' })
  },
}

/* ------------------------------------------------------------------ *
 * 自主加入（join_gate）
 * ------------------------------------------------------------------ */

export const joinApi = {
  /**
   * 最近加入决策。groupOpenid 为空 = 查全部群；limit 限 1–200。
   * 后端未配置存储时返回 503，页面按 toMessage 展示即可。
   */
  decisions(groupOpenid = '', limit = 50, signal?: AbortSignal): Promise<JoinDecisionsData['decisions']> {
    return request<JoinDecisionsData>('/join/decisions', {
      query: { group_openid: groupOpenid, limit },
      signal,
    }).then((data) => (Array.isArray(data?.decisions) ? data.decisions : []))
  },
  /** 某个群最近一次话题分析；没有分析结果时后端返回 topics=null。 */
  topics(groupOpenid: string, signal?: AbortSignal): Promise<JoinTopicsData['topics']> {
    return request<JoinTopicsData>('/join/topics', {
      query: { group_openid: groupOpenid },
      signal,
    }).then((data) => data?.topics ?? null)
  },
}

/* ------------------------------------------------------------------ *
 * 自发主动
 * ------------------------------------------------------------------ */

export const proactiveApi = {
  overview(signal?: AbortSignal): Promise<ProactiveOverview> {
    return request<ProactiveOverview>('/proactive', { signal })
  },
}

/* ------------------------------------------------------------------ *
 * 情绪状态（Affect）
 * ------------------------------------------------------------------ */

export const affectApi = {
  /** 可用的内容包（内置包随二进制嵌入）。 */
  packs(signal?: AbortSignal): Promise<AffectPacksData['packs']> {
    return request<AffectPacksData>('/affect/packs', { signal }).then((data) => data.packs ?? [])
  },
  /** 有情绪状态的会话，按更新时间倒序。 */
  conversations(limit = 50, signal?: AbortSignal): Promise<AffectConversationsData['conversations']> {
    return request<AffectConversationsData>('/affect/conversations', {
      query: { limit },
      signal,
    }).then((data) => data.conversations ?? [])
  },
  /** 某会话的状态 + 行为摘要 + 注入预览。 */
  state(conversation: string, signal?: AbortSignal): Promise<AffectStateData> {
    return request<AffectStateData>('/affect/state', {
      query: { conversation },
      signal,
    })
  },
  /** 历史点；hours 支持 24 / 168。 */
  history(
    conversation: string,
    hours = 24,
    signal?: AbortSignal,
  ): Promise<AffectHistoryData['points']> {
    return request<AffectHistoryData>('/affect/history', {
      query: { conversation, hours },
      signal,
    }).then((data) => data.points ?? [])
  },
  /** 最近事件日志。 */
  events(
    conversation: string,
    limit = 50,
    signal?: AbortSignal,
  ): Promise<AffectEventsData['events']> {
    return request<AffectEventsData>('/affect/events', {
      query: { conversation, limit },
      signal,
    }).then((data) => data.events ?? [])
  },
  /** 暂停情绪影响（minutes 分钟）。 */
  pause(conversation: string, minutes: number): Promise<unknown> {
    return request<unknown>('/affect/pause', {
      method: 'POST',
      body: { conversation, minutes },
    })
  },
  /** 立即恢复。 */
  resume(conversation: string): Promise<unknown> {
    return request<unknown>('/affect/resume', {
      method: 'POST',
      body: { conversation },
    })
  },
  /** 套用内容包预设（calm / focused / cheerful / low_battery）。 */
  preset(conversation: string, preset: string): Promise<unknown> {
    return request<unknown>('/affect/preset', {
      method: 'POST',
      body: { conversation, preset },
    })
  },
  /** 重置该会话的情绪状态。 */
  reset(conversation: string): Promise<unknown> {
    return request<unknown>('/affect/reset', {
      method: 'POST',
      body: { conversation },
    })
  },
}
