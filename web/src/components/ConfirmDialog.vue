<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ModalDialog from './ModalDialog.vue'
import AppIcon from './AppIcon.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    message?: string
    confirmText?: string
    cancelText?: string
    danger?: boolean
    loading?: boolean
    /** 设置后要求用户逐字输入该文本才能确认（用于「清空全部」等破坏性操作） */
    requireText?: string
  }>(),
  {
    message: '',
    confirmText: '确认',
    cancelText: '取消',
    danger: false,
    loading: false,
    requireText: '',
  },
)

const emit = defineEmits<{ confirm: []; cancel: [] }>()

const typed = ref('')

watch(
  () => props.open,
  (open) => {
    if (open) typed.value = ''
  },
)

const canConfirm = computed(() => {
  if (props.loading) return false
  if (!props.requireText) return true
  return typed.value.trim() === props.requireText
})
</script>

<template>
  <ModalDialog
    :open="open"
    :title="title"
    width="max-w-md"
    :close-on-backdrop="!loading"
    @close="emit('cancel')"
  >
    <div class="space-y-3">
      <div
        v-if="danger"
        class="flex items-start gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
      >
        <AppIcon name="warning" :size="16" class="mt-0.5 text-rose-400" />
        <span>此操作不可撤销，请确认后继续。</span>
      </div>
      <p v-if="message" class="text-sm leading-relaxed text-slate-300">{{ message }}</p>
      <slot />
      <div v-if="requireText" class="space-y-1">
        <label class="label" for="confirm-require-input">
          请输入 <span class="font-mono text-rose-300">{{ requireText }}</span> 以确认
        </label>
        <input
          id="confirm-require-input"
          v-model="typed"
          class="input font-mono"
          type="text"
          autocomplete="off"
          :placeholder="requireText"
        />
      </div>
    </div>

    <template #footer>
      <button type="button" class="btn" :disabled="loading" @click="emit('cancel')">
        {{ cancelText }}
      </button>
      <button
        type="button"
        class="btn"
        :class="danger ? 'btn-danger' : 'btn-primary'"
        :disabled="!canConfirm"
        @click="emit('confirm')"
      >
        <span
          v-if="loading"
          class="h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/40 border-t-white"
        />
        {{ confirmText }}
      </button>
    </template>
  </ModalDialog>
</template>
