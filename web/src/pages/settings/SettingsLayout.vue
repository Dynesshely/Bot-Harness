<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import {
  MODEL_PARAM_KEYS,
  READONLY_LABELS,
  assign,
  buildPatch,
  emptyDraft,
  provideConfig,
  validateDraft,
  type ConfigContext,
  type EditableDraft,
} from '@/composables/configContext'
import { useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { actionsApi, configApi, modelsApi, toMessage } from '@/api'
import type { ConfigData, TestAiResult } from '@/api/types'
import { logout } from '@/stores/auth'
import { toast } from '@/composables/useToast'

const router = useRouter()

/* ------------------------------------------------------------------ *
 * 配置
 * ------------------------------------------------------------------ */





const config = ref<ConfigData | null>(null)
const draft = reactive<EditableDraft>(emptyDraft())
const loaded = ref<EditableDraft>(emptyDraft())
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const restartRequired = ref<string[]>([])


async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await configApi.get()
    config.value = data
    const editable = data.editable ?? {}
    assign(draft, editable)
    assign(loaded.value, editable)
    draft.openai_api_key = ''
    restartRequired.value = []
  } catch (err) {
    error.value = toMessage(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  void loadModels(false)
})

/* ------------------------------------------------------------------ *
 * 模型列表（GET /api/models）
 * ------------------------------------------------------------------ */

const modelIds = ref<string[]>([])
const modelsBaseUrl = ref('')
const modelsFetchedAt = ref('')
const modelsLoading = ref(false)
const modelsError = ref('')
/** 上游没返回列表或需要填非标准模型名时，退化为自由输入。 */
const customModel = ref(false)

const modelMissingFromList = computed(
  () => modelIds.value.length > 0 && !!draft.model && !modelIds.value.includes(draft.model),
)

async function loadModels(refresh: boolean) {
  modelsLoading.value = true
  modelsError.value = ''
  try {
    const data = await modelsApi.list(refresh)
    modelIds.value = data.ids ?? data.models?.map((m) => m.id) ?? []
    modelsBaseUrl.value = data.base_url ?? ''
    modelsFetchedAt.value = (data.fetched_at ?? '').replace('T', ' ').replace(/(\+|Z).*$/, '')
    // 列表里没有当前模型时自动切到手动输入，避免下拉框显示成空。
    if (modelIds.value.length && draft.model && !modelIds.value.includes(draft.model)) {
      customModel.value = true
    }
  } catch (err) {
    modelsError.value = `获取模型列表失败：${toMessage(err)}`
  } finally {
    modelsLoading.value = false
  }
}

/**
 * 只提交被修改的字段；数字字段留空视为「不修改」。
 * 具体语义（null / 布尔 false / 秘密字段）在 configContext.buildPatch 里，有单测钉住。
 */
const patch = computed(() => buildPatch(draft, loaded.value))

const dirty = computed(() => Object.keys(patch.value).length > 0)

/**
 * 保存前的本地校验：后端 validateRanges 会拒的情况，先在界面上拦住。
 *
 * 目前只有一条：开了「提前安抚」却没有文案 —— 后端报的是
 * `early_reply_enabled requires a non-empty early_reply_text`，
 * 与其保存时弹错，不如直接把保存按钮禁用掉。
 */
const validationError = computed(() => validateDraft(draft))

/** 系统提示词的大屏编辑弹窗。 */
const promptOpen = ref(false)

/** 「模型参数」分组默认折叠。 */
const modelParamsOpen = ref(false)

/**
 * 折叠进「模型参数」里的字段（顺序即界面顺序）。
 * 列表同时用来算「有几项改了还没保存」——折叠着也能看出里面有未保存的改动。
 */
const modelParamsDirtyCount = computed(
  () => MODEL_PARAM_KEYS.filter((key) => draft[key] !== loaded.value[key]).length,
)

/** 密钥来源（后端只告诉我们有没有、从哪来，永远不下发密钥本身）。 */
const apiKeySource = computed(() => config.value?.openai_api_key_source ?? 'none')

const readonlyEntries = computed(() =>
  Object.entries(config.value?.readonly ?? {}).map(([key, value]) => ({
    key,
    label: READONLY_LABELS[key] ?? key,
    value: formatReadonly(value),
  })),
)

function formatReadonly(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  if (typeof value === 'boolean') return value ? '是' : '否'
  return String(value)
}

async function save() {
  if (!dirty.value || saving.value) return
  if (validationError.value) {
    toast.error(validationError.value)
    return
  }
  saving.value = true
  restartRequired.value = []
  try {
    const baseUrlChanged = patch.value.openai_base_url !== undefined
    const keyChanged = patch.value.openai_api_key !== undefined
    const outcome = await configApi.update(patch.value)
    // 先用响应里的权威值就地对齐，再整体重新拉一次：
    // 保存涉及默认值补全、按群覆盖、只读派生项，只有重新 GET 才能保证界面与后端一致。
    assign(draft, outcome.editable)
    assign(loaded.value, outcome.editable)
    await load()
    restartRequired.value = outcome.restartRequired
    if (outcome.warnings.length > 0) {
      toast.warn(`已保存，但：${outcome.warnings.join('；')}`)
    }
    if (baseUrlChanged || keyChanged) {
      // 旧列表是从旧端点/旧密钥拉的，改了必须重新拉一次（很可能上一次就是 401）。
      void loadModels(true)
    }
    if (outcome.restartRequired.length > 0) {
      toast.warn(`已保存，但以下字段需要重启服务后完全生效：${outcome.restartRequired.join('、')}`)
    } else if (outcome.warnings.length === 0) {
      toast.success('配置已保存')
    }
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    saving.value = false
  }
}

/**
 * 清掉保存的密钥（= 变成未配置）。
 *
 * 密钥字段是「只进不出」的：接口永远不下发它，所以也没法靠「清空输入框」来表达，
 * 必须走这个显式动作。
 */
async function clearStoredKey() {
  if (saving.value) return
  saving.value = true
  try {
    const outcome = await configApi.update({ openai_api_key: '' })
    const editable = outcome.editable
    assign(draft, editable)
    assign(loaded.value, editable)
    draft.openai_api_key = ''
    await load()
    toast.warn('已清除密钥：在重新填写之前，模型调用都会失败')
    void loadModels(true)
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    saving.value = false
  }
}

function discard() {
  assign(draft, loaded.value)
  toast.info('已撤销未保存的修改')
}

/* ------------------------------------------------------------------ *
 * 运维动作
 * ------------------------------------------------------------------ */

const actionLoading = ref<'' | 'reconnect' | 'token' | 'reset'>('')
const resetConfigOpen = ref(false)

async function runAction(kind: 'reconnect' | 'token' | 'reset') {
  actionLoading.value = kind
  try {
    if (kind === 'reconnect') {
      await actionsApi.reconnect()
      toast.success('已请求强制重建 WS 连接')
    } else if (kind === 'token') {
      await actionsApi.reloadToken()
      toast.success('已请求刷新 access_token')
    } else {
      const outcome = await actionsApi.resetConfig()
      // 端点/密钥没有内置默认值兜底，重置后可能直接变成未配置 —— 后端会把后果写进 warnings。
      const warns = outcome?.warnings ?? []
      if (warns.length) {
        toast.warn(warns[0])
      } else {
        toast.success('已清空数据库配置覆盖，回到内置默认值')
      }
      resetConfigOpen.value = false
      await load()
    }
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    actionLoading.value = ''
  }
}

/* 测试 AI */
const aiOpen = ref(false)
const aiPrompt = ref('你好，请用一句话自我介绍。')
const aiRunning = ref(false)
const aiResult = ref<TestAiResult | null>(null)
const aiError = ref('')

async function runTestAi() {
  const prompt = aiPrompt.value.trim()
  if (!prompt || aiRunning.value) return
  aiRunning.value = true
  aiError.value = ''
  aiResult.value = null
  try {
    aiResult.value = await actionsApi.testAi(prompt)
  } catch (err) {
    aiError.value = toMessage(err)
  } finally {
    aiRunning.value = false
  }
}

function openTestAi() {
  aiOpen.value = true
  aiError.value = ''
  aiResult.value = null
}

/* 测试发送消息 */
const sendGroup = ref('')
const sendContent = ref('这是一条来自管理后台的测试消息。')
const sending = ref(false)
const sendResult = ref('')
const sendError = ref('')

async function runTestSend() {
  const groupOpenid = sendGroup.value.trim()
  const content = sendContent.value.trim()
  if (!groupOpenid || !content) {
    toast.error('请填写群 OpenID 与消息内容')
    return
  }
  sending.value = true
  sendResult.value = ''
  sendError.value = ''
  try {
    const result = await actionsApi.testSend(groupOpenid, content)
    sendResult.value = result?.message_id ?? ''
    toast.success('主动消息已提交')
  } catch (err) {
    sendError.value = toMessage(err)
  } finally {
    sending.value = false
  }
}

/* 退出登录 */
const loggingOut = ref(false)
async function onLogout() {
  loggingOut.value = true
  try {
    await logout()
    toast.success('已退出登录')
    await router.replace({ name: 'login' })
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    loggingOut.value = false
  }
}

// 子页面共用这一份状态：ref 放进 reactive 容器后读取时自动解包。
provideConfig(
  reactive({
    draft, loaded, config: config as unknown as ConfigContext['config'], error, loading, saving,
    dirty, validationError, restartRequired, apiKeySource, readonlyEntries, modelIds, modelsBaseUrl, modelsFetchedAt,
    modelsLoading, modelsError, customModel, modelMissingFromList, promptOpen, modelParamsOpen,
    modelParamsDirtyCount, actionLoading, resetConfigOpen, aiOpen, aiPrompt, aiRunning, aiResult,
    aiError, sendGroup, sendContent, sending, sendResult, sendError, loggingOut,
    save, discard, reload: load, clearStoredKey, loadModels, runAction, openTestAi, runTestAi,
    runTestSend, onLogout,
  }) as unknown as ConfigContext,
)

/** 设置页的子页面（嵌套路由）。 */
const SECTIONS = [
  { to: '/settings/provider', label: '模型与提示词', icon: 'cpu' },
  { to: '/settings/reply', label: '回复与人味', icon: 'chat' },
  { to: '/settings/capability', label: '能力', icon: 'bolt' },
  { to: '/settings/proactive', label: '主动消息', icon: 'clock' },
  { to: '/settings/connection', label: '接入与运维', icon: 'key' },
  { to: '/settings/system', label: '系统与诊断', icon: 'cog' },
] as const

</script>

<template>
  <div class="space-y-4">
    <nav class="flex flex-wrap gap-1 rounded-xl border border-slate-800 bg-slate-900/60 p-1">
      <RouterLink
        v-for="item in SECTIONS"
        :key="item.to"
        :to="item.to"
        class="flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm text-slate-400 transition hover:bg-slate-800 hover:text-slate-200"
        active-class="bg-slate-800 text-slate-100"
      >
        <AppIcon :name="item.icon" :size="14" />
        {{ item.label }}
      </RouterLink>
    </nav>

    <p v-if="error" class="rounded-lg border border-rose-500/40 bg-rose-950/30 px-3 py-2 text-sm text-rose-300">
      {{ error }}
    </p>

    <div
      v-if="restartRequired.length > 0"
      class="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-200"
    >
      <AppIcon name="warning" :size="15" class="mt-0.5 text-amber-400" />
      <span>
        配置已保存，但以下字段需要重启服务后才会完全生效：
        <span class="mono">{{ restartRequired.join('、') }}</span>
      </span>
    </div>

    <p v-if="loading" class="py-10 text-center text-sm text-slate-500">加载中…</p>
    <RouterView v-else />

    <p
      v-if="validationError"
      class="flex items-start gap-2 rounded-lg border border-rose-500/40 bg-rose-950/30 px-3 py-2 text-xs text-rose-200"
    >
      <AppIcon name="warning" :size="15" class="mt-0.5 text-rose-400" />
      <span>{{ validationError }}（保存已禁用）</span>
    </p>

    <div class="sticky bottom-0 z-20 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-slate-700 bg-slate-900/95 px-3 py-2 backdrop-blur">
      <span class="text-xs" :class="dirty ? 'text-amber-300' : 'text-slate-500'">
        {{ dirty ? '有改动尚未保存' : '所有配置均已保存' }}
      </span>
      <div class="flex items-center gap-2">
        <button type="button" class="btn btn-ghost btn-sm" :disabled="!dirty || saving" @click="discard">
          放弃修改
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="loading || saving" @click="load">
          <AppIcon name="refresh" :size="14" />
          重新加载
        </button>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="!dirty || saving || !!validationError"
          @click="save"
        >
          {{ saving ? '保存中…' : '保存修改' }}
        </button>
      </div>
    </div>
  </div>
</template>
