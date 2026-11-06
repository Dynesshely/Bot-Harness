<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { toMessage } from '@/api'
import { login } from '@/stores/auth'
import { toast } from '@/composables/useToast'

const route = useRoute()
const router = useRouter()

const token = ref('')
const showToken = ref(false)
const submitting = ref(false)
const error = ref('')

function redirectAfterLogin() {
  const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
  void router.replace(target)
}

async function submit() {
  const value = token.value.trim()
  if (!value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    await login(value)
    toast.success('登录成功')
    redirectAfterLogin()
  } catch (err) {
    error.value = toMessage(err)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div
    class="flex min-h-screen items-center justify-center bg-slate-950 p-4"
    style="
      background-image:
        radial-gradient(ellipse 60% 50% at 50% -10%, rgba(14, 165, 233, 0.18), transparent),
        radial-gradient(ellipse 40% 40% at 90% 100%, rgba(139, 92, 246, 0.12), transparent);
    "
  >
    <div class="w-full max-w-sm">
      <div class="mb-6 flex flex-col items-center gap-2 text-center">
        <span class="rounded-2xl bg-sky-500/15 p-3 text-sky-400">
          <AppIcon name="sparkles" :size="28" />
        </span>
        <h1 class="text-xl font-semibold text-slate-100">Bot Harness</h1>
        <p class="text-sm text-slate-500">管理后台 · 请输入 Admin Token 登录</p>
      </div>

      <form class="card space-y-4 p-5" @submit.prevent="submit">
        <div>
          <label class="label" for="admin-token">Admin Token</label>
          <div class="relative">
            <input
              id="admin-token"
              v-model="token"
              class="input pr-10"
              :type="showToken ? 'text' : 'password'"
              placeholder="ADMIN_TOKEN"
              autocomplete="current-password"
              spellcheck="false"
              autofocus
            />
            <button
              type="button"
              class="absolute inset-y-0 right-0 flex cursor-pointer items-center px-3 text-slate-500 transition hover:text-slate-300"
              :aria-label="showToken ? '隐藏 Token' : '显示 Token'"
              @click="showToken = !showToken"
            >
              <AppIcon name="key" :size="16" />
            </button>
          </div>
          <p class="field-hint">对应服务端环境变量 ADMIN_TOKEN，登录成功后写入 HttpOnly Cookie。</p>
        </div>

        <p
          v-if="error"
          class="flex items-start gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
        >
          <AppIcon name="warning" :size="15" class="mt-0.5 shrink-0 text-rose-400" />
          <span>{{ error }}</span>
        </p>

        <button
          type="submit"
          class="btn btn-primary w-full"
          :disabled="submitting || !token.trim()"
        >
          <span
            v-if="submitting"
            class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/40 border-t-white"
          />
          {{ submitting ? '登录中…' : '登录' }}
        </button>
      </form>

      <p class="mt-4 text-center text-xs text-slate-600">
        接口契约：docs/ADMIN-API.md · 所有请求走 <span class="mono">/api</span> 同源前缀
      </p>
    </div>
  </div>
</template>
