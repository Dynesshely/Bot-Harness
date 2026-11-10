<script setup lang="ts">
import { useConfig } from '@/composables/configContext'
import AppIcon from '@/components/AppIcon.vue'
import NumberField from '@/components/NumberField.vue'
import TextField from '@/components/TextField.vue'
import ToggleField from '@/components/ToggleField.vue'

const { draft } = useConfig()
</script>

<template>
  <div class="space-y-4">
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">工具调用</h3>
            <ToggleField
              v-model="draft.tools_enabled"
              label="启用 tools_enabled"
              class="min-w-[10rem]"
            />
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.tools_max_steps"
              label="最多轮次 tools_max_steps"
              :min="1"
              :max="12"
              hint="一次回复里最多几轮模型调用（每轮都可能执行工具）"
            />
            <NumberField
              v-model="draft.tools_total_budget_seconds"
              label="总预算 tools_total_budget_seconds（秒）"
              :min="10"
              :max="300"
              hint="整个工具循环的墙钟上限，超时立即收尾"
            />
            <NumberField
              v-model="draft.tools_result_max_chars"
              label="结果截断 tools_result_max_chars"
              :min="500"
              :max="20000"
              hint="单个工具结果进入模型前截断到多少字符"
            />
          </div>
          <p class="field-hint mt-2">
            关掉等于纯聊天：一个工具定义都不下发，模型只回文本。范围与后端校验一致
            （轮次 1–12、预算 10–300 秒、截断 500–20000 字符）。
            按群覆盖在「策略配置」里，可单独给某个群开或关。
          </p>
        </div>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">联网与执行</h3>
            <div class="flex flex-wrap items-center gap-4">
              <ToggleField v-model="draft.web_enabled" label="联网 web_enabled" class="min-w-[9rem]" />
              <ToggleField
                v-model="draft.exec_enabled"
                label="执行 run_shell"
                class="min-w-[10rem]"
              />
            </div>
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <div>
              <label class="label" for="cfg-search-provider">搜索后端 web_search_provider</label>
              <select id="cfg-search-provider" v-model="draft.web_search_provider" class="input mono">
                <option value="">（不启用搜索）</option>
                <option value="searxng">searxng</option>
              </select>
              <p class="field-hint">只影响 web_search；web_fetch 只依赖「联网」开关。</p>
            </div>
            <div class="sm:col-span-2">
              <label class="label" for="cfg-search-url">搜索服务地址 web_search_base_url</label>
              <input
                id="cfg-search-url"
                v-model="draft.web_search_base_url"
                class="input mono"
                placeholder="https://search.internal.example"
              />
              <p class="field-hint">SearxNG 根地址，会请求 <span class="mono">/search?format=json</span>。</p>
            </div>
            <NumberField
              v-model="draft.exec_timeout_seconds"
              label="执行超时 exec_timeout_seconds（秒）"
              :min="3"
              :max="60"
              hint="超时后整组进程被 SIGKILL"
            />
            <NumberField
              v-model="draft.exec_max_output_bytes"
              label="输出上限 exec_max_output_bytes"
              :min="512"
              :max="65536"
              hint="stdout+stderr 合计，超出截断"
            />
          </div>
          <p class="field-hint mt-2">
            执行工具在<b>容器内</b>运行：工作目录每次清空、环境变量只保留 PATH/HOME/LANG/TZ、
            有 CPU/内存/进程数限制。默认关闭。<b>即使打开，也只有「策略配置」里
            admin_openids 列出的人能触发</b>——非管理员连工具定义都不会下发，模型也无从调用。
            联网工具会拒绝内网、回环与云元数据地址（含每次重定向）。
          </p>
        </div>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">长期记忆</h3>
            <ToggleField
              v-model="draft.memory_enabled"
              label="启用 memory_enabled"
              class="min-w-[10rem]"
            />
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.memory_inject_max"
              label="每次注入条数 memory_inject_max"
              :min="1"
              :max="20"
              hint="每轮对话最多把几条相关事实贴进系统提示词"
            />
            <NumberField
              v-model="draft.memory_max_per_scope"
              label="每作用域上限 memory_max_per_scope"
              :min="20"
              :max="5000"
              hint="达到上限时淘汰权重最低的一条"
            />
            <NumberField
              v-model="draft.memory_extract_every"
              label="自动抽取间隔 memory_extract_every（轮）"
              :min="1"
              :max="50"
              hint="每 N 轮用一次便宜调用提取事实；关闭抽取时无效"
            />
            <ToggleField
              v-model="draft.memory_extract_enabled"
              label="后台自动抽取 memory_extract_enabled"
              hint="每 N 轮额外调用一次模型，把对话里的长期事实抽出来存下（默认关，会产生额外 token 消耗）"
            />
          </div>
          <p class="field-hint mt-2">
            群记忆只在该群可见，个人记忆只在该成员私聊可见；全局记忆只有管理员明确要求时才会写入。
            记忆内容可在「长期记忆」页查看与删除。
          </p>
        </div>

        <!-- 情绪状态：默认关，内容包默认日常心境；详细配置与状态预览在独立页面 -->
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="flex items-center gap-2 text-sm font-medium text-slate-200">
              情绪状态
              <span
                class="badge"
                :class="
                  draft.affect_enabled
                    ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                    : 'border-slate-700 bg-slate-800/60 text-slate-400'
                "
              >
                {{ draft.affect_enabled ? '已启用' : '默认关闭' }}
              </span>
            </h3>
            <RouterLink :to="{ name: 'affect' }" class="btn btn-sm">
              前往情绪状态页
              <AppIcon name="chevronRight" :size="14" />
            </RouterLink>
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
            <div>
              <p class="label">内容包 affect_pack</p>
              <p class="text-sm text-slate-200">
                {{ draft.affect_pack === 'daily' ? 'daily · 日常心境' : (draft.affect_pack || 'daily') }}
              </p>
              <p class="field-hint">默认使用内置的「日常心境」内容包</p>
            </div>
            <div>
              <p class="label">注入档位 affect_inject_mode</p>
              <p class="text-sm text-slate-200">{{ draft.affect_inject_mode }}</p>
              <p class="field-hint">off 不注入 / line 一行 / full 多段</p>
            </div>
            <div>
              <p class="label">事件分类器 affect_event_classifier</p>
              <p class="text-sm text-slate-200">{{ draft.affect_event_classifier }}</p>
              <p class="field-hint">rules 按内容包正则匹配，默认开启</p>
            </div>
          </div>
          <p class="field-hint mt-2">
            根据对话内容维护 5 维情绪数值，并把状态作为语气参考注入提示词、轻微调整回复长度与主动性。
            <b>默认关闭</b>，关闭时现有行为零变化；内容包默认「日常心境」（daily）。
            开关、数值、历史曲线、暂停/预设与注入预览都在
            <RouterLink :to="{ name: 'affect' }" class="link">情绪状态</RouterLink> 页。
          </p>
        </div>

        <!-- 人类状态感知：自动推测聊天对象是群聊还是私聊，用来调整回复姿态 -->
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">人类状态感知</h3>
            <ToggleField
              v-model="draft.human_status_enabled"
              label="启用 human_status_enabled"
              class="min-w-[12rem]"
            />
          </div>
          <p class="mb-3 text-xs text-slate-500">
            自动推测当前聊天对象的状态（群里 / 私聊、是不是在等回复），
            用来调整回复的姿态与时机。推测结果只供模型参考，不会直接发给用户。
          </p>
          <div
            class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
            :class="draft.human_status_enabled ? '' : 'opacity-60'"
          >
            <NumberField
              v-model="draft.human_status_trigger_messages"
              label="触发条数 human_status_trigger_messages"
              :min="5"
              :max="200"
              :disabled="!draft.human_status_enabled"
              hint="累积多少条新消息推测一次；5–200，默认 20"
            />
            <NumberField
              v-model="draft.human_status_ttl_minutes"
              label="缓存有效期 human_status_ttl_minutes（分钟）"
              :min="10"
              :max="1440"
              :disabled="!draft.human_status_enabled"
              hint="推测结果缓存多久；10–1440，默认 120"
            />
            <NumberField
              v-model="draft.human_status_timeline_messages"
              label="时间线条数 human_status_timeline_messages"
              :min="20"
              :max="400"
              :disabled="!draft.human_status_enabled"
              hint="状态推测时取最近多少条消息构建成员时间线；20–400，默认 120"
            />
            <ToggleField
              v-model="draft.human_status_vision_enabled"
              label="允许图片分析 human_status_vision_enabled"
              :disabled="!draft.human_status_enabled"
              hint="允许模型自主调用图片分析工具；默认开启"
            />
            <TextField
              v-model="draft.human_status_vision_model"
              label="视觉模型 human_status_vision_model"
              mono
              placeholder="留空 = 主模型"
              :disabled="!draft.human_status_enabled || !draft.human_status_vision_enabled"
              hint="图片分析使用哪个视觉模型；留空则沿用主模型"
            />
          </div>
          <p v-if="!draft.human_status_enabled" class="field-hint mt-2">
            已关闭：不再推测聊天对象状态。上面的参数暂时不生效，但会原样保留。
          </p>
          <p v-else class="field-hint mt-2">
            触发条数越小越灵敏，但会多消耗模型调用；缓存有效期越长越省，状态也越可能过时。
            时间线条数决定推测时能看到多少条近期消息；关闭「允许图片分析」后，视觉模型输入框会被禁用，
            模型也不会自主调用图片分析工具。
          </p>
        </div>
  </div>
</template>
