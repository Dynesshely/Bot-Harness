<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import AppIcon from './AppIcon.vue'

/**
 * 一键复制按钮。
 *
 * 注意：navigator.clipboard 只在安全上下文（HTTPS / localhost）可用，
 * 后台常以内网 HTTP 访问，因此保留 execCommand 兜底。
 */
const props = withDefaults(
  defineProps<{
    value?: string | null
    /** 复制成功提示里的名称，如「群 OpenID」。 */
    label?: string
    size?: number
    /** 是否在按钮旁显示文字。 */
    showText?: boolean
  }>(),
  { value: '', label: '', size: 13, showText: false },
)

const copied = ref(false)
let timer: number | undefined

async function writeClipboard(text: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text)
    return
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.top = '-1000px'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  const ok = document.execCommand('copy')
  document.body.removeChild(area)
  if (!ok) throw new Error('execCommand copy rejected')
}

async function copy() {
  const text = (props.value ?? '').trim()
  if (!text) return
  try {
    await writeClipboard(text)
    copied.value = true
    if (timer) window.clearTimeout(timer)
    timer = window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    /* 失败时不打断操作，用户仍可手动选中文本 */
  }
}

onBeforeUnmount(() => {
  if (timer) window.clearTimeout(timer)
})
</script>

<template>
  <button
    type="button"
    class="inline-flex shrink-0 cursor-pointer items-center gap-1 rounded p-1 text-slate-500 transition-colors hover:bg-slate-700/50 hover:text-slate-200"
    :class="copied ? 'text-emerald-400 hover:text-emerald-300' : ''"
    :disabled="!value"
    :title="copied ? '已复制' : `复制${label || ''}`"
    :aria-label="`复制${label || ''}`"
    @click.stop.prevent="copy"
  >
    <AppIcon :name="copied ? 'check' : 'copy'" :size="size" />
    <span v-if="showText" class="text-xs">{{ copied ? '已复制' : '复制' }}</span>
  </button>
</template>
