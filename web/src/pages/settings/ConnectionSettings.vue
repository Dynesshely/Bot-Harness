<script setup lang="ts">
import { computed, ref } from 'vue'
import { useConfig } from '@/composables/configContext'
import { actionsApi, toMessage } from '@/api'
import type { SyncCommandItem } from '@/api/types'
import { toast } from '@/composables/useToast'
import AppIcon from '@/components/AppIcon.vue'
import NumberField from '@/components/NumberField.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import TextField from '@/components/TextField.vue'
import ToggleField from '@/components/ToggleField.vue'

const { draft, config } = useConfig()

const qqSecretSet = computed(() => config.value?.qq_app_secret_set === true)
const adminTokenSet = computed(() => config.value?.admin_token_set === true)

/* ------------------------------------------------------------------ *
 * QQ 指令面板：启动时自动同步开关 + 「立即同步」动作
 * ------------------------------------------------------------------ */

const SYNC_SCOPE_LABELS: Record<string, string> = {
  group: '群 group',
  c2c: '私聊 c2c',
}
const SYNC_ACTION_LABELS: Record<string, string> = {
  created: '新建 created',
  updated: '更新 updated',
  unchanged: '无变化 unchanged',
}
const SYNC_ACTION_CLASSES: Record<string, string> = {
  created: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  updated: 'border-sky-500/40 bg-sky-500/10 text-sky-300',
  unchanged: 'border-slate-700 bg-slate-800/60 text-slate-400',
}

function syncScopeLabel(scope: string): string {
  return SYNC_SCOPE_LABELS[scope] ?? scope
}
function syncActionLabel(action: string): string {
  return SYNC_ACTION_LABELS[action] ?? action
}
function syncActionClass(action: string): string {
  return SYNC_ACTION_CLASSES[action] ?? 'border-slate-700 bg-slate-800/60 text-slate-300'
}

/** 「立即同步」的状态：按钮 loading、失败原因、逐面板结果。 */
const syncing = ref(false)
const syncError = ref('')
const syncResults = ref<SyncCommandItem[]>([])

async function syncCommandsNow() {
  if (syncing.value) return
  syncing.value = true
  syncError.value = ''
  syncResults.value = []
  try {
    const outcome = await actionsApi.syncCommands()
    syncResults.value = Array.isArray(outcome?.results) ? outcome.results : []
    if (syncResults.value.length === 0) {
      toast.info('同步完成：平台没有返回任何指令面板')
    } else {
      toast.success(`已同步 ${syncResults.value.length} 个指令面板`)
    }
  } catch (err) {
    syncError.value = toMessage(err)
    toast.error('指令面板同步失败：' + syncError.value)
  } finally {
    syncing.value = false
  }
}

const TRANSPORTS = [
  { value: 'websocket', label: 'websocket（默认，无需公网 IP）' },
  { value: 'webhook', label: 'webhook（需要公网 HTTPS）' },
  { value: 'both', label: 'both（两条通道同时开）' },
]

const LOG_LEVELS = ['debug', 'info', 'warn', 'error'].map((level) => ({
  value: level,
  label: level,
}))
</script>

<template>
  <div class="space-y-4">
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="key" :size="16" class="text-sky-400" />
          QQ 开放平台接入
        </h2>
        <span class="text-xs text-slate-500">改动凭证后需要重连（「系统与诊断」里的按钮）</span>
      </div>
      <div class="card-body grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <TextField
          v-model="draft.qq_app_id"
          label="AppID qq_app_id"
          placeholder="在 QQ 机器人开放平台获取"
          hint="改完保存即可，不必重新部署"
        />
        <div>
          <label class="label" for="cfg-qq-secret">AppSecret qq_app_secret</label>
          <input
            id="cfg-qq-secret"
            v-model="draft.qq_app_secret"
            class="input mono"
            type="password"
            autocomplete="new-password"
            spellcheck="false"
            :placeholder="qqSecretSet ? '已配置（留空则保持不变）' : '尚未配置，请填写 AppSecret'"
          />
          <p class="field-hint">
            <template v-if="qqSecretSet">已配置，密钥只进不出；留空 = 保持现状。</template>
            <template v-else>尚未配置：机器人无法连接平台。</template>
          </p>
        </div>
        <SelectMenu
          id="cfg-qq-transport"
          v-model="draft.qq_transport"
          label="事件通道 qq_transport"
          :options="TRANSPORTS"
          hint="切换通道需要重启进程才会重新装配接收端。"
        />
        <ToggleField
          v-model="draft.qq_sandbox"
          label="沙箱环境 qq_sandbox"
          hint="开启后 REST 与网关都指向 QQ 沙箱环境"
        />
        <NumberField
          v-model="draft.qq_intents"
          label="事件订阅位 qq_intents"
          :min="0"
          :max="4294967295"
          placeholder="未加载"
          hint="平台事件订阅位掩码，一般不用改；留空 = 不修改"
        />
        <NumberField v-model="draft.qq_shard_count" label="分片数 qq_shard_count" :min="1" :max="32" />
        <TextField
          v-model="draft.qq_webhook_path"
          label="Webhook 路径 qq_webhook_path"
          placeholder="/qq/webhook"
          hint="仅 webhook / both 通道使用"
        />
        <TextField
          v-model="draft.qq_bot_openid"
          label="自身 OpenID qq_bot_openid"
          placeholder="可选，bot_state 不可用时兜底"
          hint="用于在群里判断「哪条消息是 @ 我」"
        />
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="bolt" :size="16" class="text-emerald-400" />
          QQ 指令面板
        </h2>
        <span class="text-xs text-slate-500">把 Bot 命令注册到群 / 私聊的指令面板</span>
      </div>
      <div class="card-body space-y-4">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <ToggleField
            v-model="draft.command_panel_enabled"
            label="启动时自动同步 command_panel_enabled"
            hint="开启后启动时自动把命令注册到群/私聊指令面板；关闭后仍可手动同步。"
          />
          <div>
            <span class="label">手动同步</span>
            <button
              type="button"
              class="btn"
              :disabled="syncing"
              @click="syncCommandsNow"
            >
              <span
                v-if="syncing"
                class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/40 border-t-white"
              />
              <AppIcon v-else name="refresh" :size="15" />
              {{ syncing ? '同步中…' : '立即同步' }}
            </button>
            <p class="field-hint">
              立即把当前命令注册表推到平台，不用等重启；失败会显示上游错误（upstream_error）。
            </p>
          </div>
        </div>

        <p
          v-if="syncError"
          class="flex items-start gap-2 rounded-lg border border-rose-500/40 bg-rose-950/30 px-3 py-2 text-xs text-rose-200"
        >
          <AppIcon name="warning" :size="15" class="mt-0.5 text-rose-400" />
          <span>同步失败：<span class="mono">{{ syncError }}</span></span>
        </p>

        <div v-else-if="syncResults.length > 0" class="table-wrap overflow-x-auto">
          <table class="table-base">
            <thead>
              <tr>
                <th>范围 scope</th>
                <th>指令面板 panel_id</th>
                <th>动作 action</th>
                <th>版本 version</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in syncResults" :key="item.scope + ':' + item.panel_id">
                <td>{{ syncScopeLabel(item.scope) }}</td>
                <td class="mono">{{ item.panel_id }}</td>
                <td>
                  <span class="badge" :class="syncActionClass(item.action)">
                    {{ syncActionLabel(item.action) }}
                  </span>
                </td>
                <td class="mono">{{ item.version }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="shield" :size="16" class="text-amber-400" />
          后台与网络
        </h2>
        <span class="text-xs text-slate-500">口令与监听地址都在数据库里，重启后仍然有效</span>
      </div>
      <div class="card-body grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <div>
          <label class="label" for="cfg-admin-token">后台口令 admin_token</label>
          <input
            id="cfg-admin-token"
            v-model="draft.admin_token"
            class="input mono"
            type="password"
            autocomplete="new-password"
            spellcheck="false"
            :placeholder="adminTokenSet ? '已配置（留空则保持不变）' : '尚未配置'"
          />
          <p class="field-hint">
            <template v-if="adminTokenSet">已配置。改口令会立即让所有已登录会话失效。</template>
            <template v-else>尚未配置：下次启动会自动生成并写进数据库。</template>
          </p>
        </div>
        <TextField
          v-model="draft.http_addr"
          label="监听地址 http_addr"
          placeholder="未加载"
          hint="例如 :8080；改完需要重启进程（保存后会在顶部提示）"
        />
        <TextField
          v-model="draft.system_prompt_file"
          label="提示词文件 system_prompt_file"
          placeholder="可选：例如 /app/prompts/system.md"
          hint="数据库里的 system_prompt 优先级更高"
        />
        <div class="sm:col-span-2 xl:col-span-3">
          <label class="label" for="cfg-tls-ca">额外 CA 文件 tls_extra_ca_files</label>
          <textarea
            id="cfg-tls-ca"
            :value="draft.tls_extra_ca_files.join('\n')"
            class="input mono h-20 resize-y"
            spellcheck="false"
            placeholder="/app/certs/extra-ca.pem"
            @input="draft.tls_extra_ca_files = ($event.target as HTMLTextAreaElement).value.split('\n').map((s) => s.trim()).filter(Boolean)"
          ></textarea>
          <p class="field-hint">每行一个路径，用私有 CA 签发的内部端点需要它。改完需要重启。</p>
        </div>
        <ToggleField
          v-model="draft.tls_insecure_verify"
          label="跳过 TLS 校验 tls_insecure_verify"
          hint="仅在不信任 CA 时临时使用，改完需要重启"
        />
        <SelectMenu
          id="cfg-log-level"
          v-model="draft.log_level"
          label="日志级别 log_level"
          :options="LOG_LEVELS"
          hint="保存后立即生效，不用重启。"
        />
        <NumberField
          v-model="draft.log_buffer_size"
          label="日志缓冲容量 log_buffer_size"
          :min="100"
          :max="100000"
          hint="后台日志页保留的条数，需要重启"
        />
        <NumberField
          v-model="draft.media_max_bytes"
          label="附件缓存上限 media_max_bytes"
          :min="65536"
          :max="67108864"
          placeholder="未加载"
          hint="单个图片/文件的缓存上限（字节）"
        />
      </div>
    </section>
  </div>
</template>
