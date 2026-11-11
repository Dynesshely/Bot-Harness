<script setup lang="ts">
import { useConfig } from '@/composables/configContext'
import { formatMs, formatNumber } from '@/utils/format'
import AppIcon from '@/components/AppIcon.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import TextField from '@/components/TextField.vue'

const { config, readonlyEntries, actionLoading, resetConfigOpen, aiOpen, closeTestAi, openResetConfig, cancelResetConfig, aiPrompt, aiRunning, aiResult, aiError, sendGroup, sendContent, sending, sendResult, sendError, loggingOut, runAction, openTestAi, runTestAi, runTestSend, onLogout } = useConfig()
</script>

<template>
  <div class="space-y-4">
    <!-- 只读快照 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="lock" :size="16" class="text-slate-400" />
          只读配置
        </h2>
        <span class="text-xs text-slate-500">
          启动期信息（存储引擎 / 连接 / 版本）·
          数据库覆盖 <span class="mono">{{ config?.source?.overrides_from_db ?? 0 }}</span> 项
        </span>
      </div>
      <div class="card-body grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2 xl:grid-cols-3">
        <div
          v-for="entry in readonlyEntries"
          :key="entry.key"
          class="flex items-center justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm"
        >
          <span class="text-slate-500" :title="entry.key">{{ entry.label }}</span>
          <span class="mono truncate text-slate-300">{{ entry.value }}</span>
        </div>
        <p v-if="readonlyEntries.length === 0" class="text-sm text-slate-500">暂无只读配置</p>
      </div>
    </section>
    <!-- 运维动作 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="bolt" :size="16" class="text-sky-400" />
          运维动作
        </h2>
        <span class="text-xs text-slate-500">直接作用于运行中的机器人</span>
      </div>
      <div class="card-body grid grid-cols-1 gap-4 lg:grid-cols-2">
        <!-- 基础动作 -->
        <div class="space-y-3">
          <div class="flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="btn"
              :disabled="actionLoading !== ''"
              @click="runAction('reconnect')"
            >
              <AppIcon name="bolt" :size="15" />
              {{ actionLoading === 'reconnect' ? '重连中…' : '强制重连 WS' }}
            </button>
            <button
              type="button"
              class="btn"
              :disabled="actionLoading !== ''"
              @click="runAction('token')"
            >
              <AppIcon name="key" :size="15" />
              {{ actionLoading === 'token' ? '刷新中…' : '刷新 access_token' }}
            </button>
            <button
              type="button"
              class="btn btn-danger"
              :disabled="actionLoading !== ''"
              @click="openResetConfig()"
            >
              <AppIcon name="refresh" :size="15" />
              重置配置覆盖
            </button>
          </div>
          <p class="field-hint">
            「重置配置覆盖」会清空数据库 settings 表中的覆盖项，回到<span class="mono">内置默认值</span>。
            <strong class="text-amber-400/90">注意</strong>：模型端点与 API Key 没有内置默认值，
            重置后会变成「未配置」，需要重新填写。
          </p>
          <div class="flex flex-wrap items-center gap-2 border-t border-slate-800 pt-3">
            <button type="button" class="btn" @click="openTestAi">
              <AppIcon name="cpu" :size="15" />
              测试 AI
            </button>
            <span class="text-xs text-slate-500">一次性调用模型，返回文本与耗时</span>
          </div>
        </div>

        <!-- 测试发送 -->
        <div class="space-y-2 rounded-xl border border-slate-800 bg-slate-950/40 p-3">
          <div class="flex items-center gap-2">
            <AppIcon name="send" :size="15" class="text-sky-400" />
            <span class="text-sm font-medium text-slate-200">测试发送群消息</span>
            <span
              class="badge border-amber-500/50 bg-amber-500/15 text-amber-300"
              title="主动消息不依赖用户触发，受平台频控限制"
            >
              <AppIcon name="warning" :size="12" />
              主动消息，受平台频控
            </span>
          </div>
          <TextField v-model="sendGroup" label="群 OpenID group_openid" mono placeholder="B2C3D4..." />
          <div>
            <label class="label" for="send-content">消息内容</label>
            <textarea
              id="send-content"
              v-model="sendContent"
              class="input h-20 resize-y"
              placeholder="要发送的纯文本内容"
            />
          </div>
          <button
            type="button"
            class="btn btn-primary"
            :disabled="sending || !sendGroup.trim() || !sendContent.trim()"
            @click="runTestSend"
          >
            <span
              v-if="sending"
              class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/40 border-t-white"
            />
            <AppIcon v-else name="send" :size="15" />
            发送主动消息（受平台频控）
          </button>
          <p v-if="sendResult" class="mono text-xs text-emerald-300">
            发送成功，message_id：{{ sendResult }}
          </p>
          <p v-if="sendError" class="mono text-xs text-rose-300">发送失败：{{ sendError }}</p>
        </div>
      </div>
    </section>

    <!-- 会话 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="logout" :size="16" class="text-slate-400" />
          当前会话
        </h2>
      </div>
      <div class="card-body flex flex-wrap items-center justify-between gap-3">
        <p class="text-sm text-slate-400">
          退出后需要重新输入 Admin Token 才能访问管理后台。
        </p>
        <button type="button" class="btn btn-danger" :disabled="loggingOut" @click="onLogout">
          <AppIcon name="logout" :size="15" />
          退出登录
        </button>
      </div>
    </section>
    <!-- 测试 AI 弹窗 -->
    <ModalDialog
      :open="aiOpen"
      title="测试 AI 调用"
      subtitle="POST /api/actions/test-ai"
      width="max-w-2xl"
      @close="closeTestAi()"
    >
      <div class="space-y-3">
        <div>
          <label class="label" for="ai-prompt">Prompt</label>
          <textarea
            id="ai-prompt"
            v-model="aiPrompt"
            class="input h-24 resize-y"
            placeholder="输入要发送给模型的 prompt"
          />
        </div>

        <div v-if="aiRunning" class="flex items-center gap-2 text-sm text-slate-400">
          <span class="h-4 w-4 animate-spin rounded-full border-2 border-slate-600 border-t-sky-400" />
          调用中…
        </div>

        <p
          v-if="aiError"
          class="flex items-center gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
        >
          <AppIcon name="warning" :size="15" class="text-rose-400" />
          {{ aiError }}
        </p>

        <div v-if="aiResult" class="space-y-2">
          <div class="flex flex-wrap items-center gap-2 text-xs text-slate-400">
            <span class="badge border-slate-700 bg-slate-800/60 text-slate-300">
              模型 {{ aiResult.model ?? '—' }}
            </span>
            <span class="badge border-sky-500/40 bg-sky-500/10 text-sky-300">
              耗时 {{ formatMs(aiResult.latency_ms) }}
            </span>
            <span v-if="aiResult.usage" class="badge border-slate-700 bg-slate-800/60 text-slate-400">
              prompt {{ formatNumber(aiResult.usage.prompt_tokens) }} · completion
              {{ formatNumber(aiResult.usage.completion_tokens) }}
            </span>
          </div>
          <div class="rounded-lg border border-slate-800 bg-slate-950 p-3">
            <p class="text-sm leading-relaxed break-words whitespace-pre-wrap text-slate-200">{{
              aiResult.text
            }}</p>
          </div>
        </div>
      </div>

      <template #footer>
        <button type="button" class="btn" :disabled="aiRunning" @click="closeTestAi()">关闭</button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="aiRunning || !aiPrompt.trim()"
          @click="runTestAi"
        >
          {{ aiRunning ? '调用中…' : '发送测试' }}
        </button>
      </template>
    </ModalDialog>
    <ConfirmDialog
      :open="resetConfigOpen"
      title="重置配置覆盖"
      danger
      confirm-text="重置配置"
      :loading="actionLoading === 'reset'"
      message="将清空数据库中保存的所有配置覆盖项，运行中的配置会回到内置默认值。模型端点与 API Key 没有内置默认值，重置后会变为未配置，需要重新填写。"
      @cancel="cancelResetConfig()"
      @confirm="runAction('reset')"
    />
  </div>
</template>
