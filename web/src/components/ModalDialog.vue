<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import AppIcon from './AppIcon.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    subtitle?: string
    /** 面板最大宽度（Tailwind 类） */
    width?: string
    closeOnBackdrop?: boolean
  }>(),
  { width: 'max-w-lg', closeOnBackdrop: true, subtitle: '' },
)

const emit = defineEmits<{ close: [] }>()

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && props.open) emit('close')
}

/** 允许在无 DOM 环境（SSR / 单测）下安全求值。 */
const hasDom = typeof window !== 'undefined' && typeof document !== 'undefined'

watch(
  () => props.open,
  (open) => {
    if (!hasDom) return
    if (open) {
      window.addEventListener('keydown', onKeydown)
      document.body.style.overflow = 'hidden'
    } else {
      window.removeEventListener('keydown', onKeydown)
      document.body.style.overflow = ''
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (!hasDom) return
  window.removeEventListener('keydown', onKeydown)
  document.body.style.overflow = ''
})
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div v-if="open" class="fixed inset-0 z-[90] flex items-start justify-center overflow-y-auto p-4 sm:p-6">
        <div
          class="fixed inset-0 bg-slate-950/80 backdrop-blur-sm"
          @click="closeOnBackdrop && emit('close')"
        />
        <div
          class="relative z-10 my-8 w-full rounded-xl border border-slate-700 bg-slate-900 shadow-2xl"
          :class="width"
          role="dialog"
          aria-modal="true"
        >
          <div class="flex items-start justify-between gap-4 border-b border-slate-800 px-4 py-3">
            <div class="min-w-0">
              <h3 class="truncate text-sm font-semibold text-slate-100">{{ title }}</h3>
              <p v-if="subtitle" class="mt-0.5 text-xs text-slate-400">{{ subtitle }}</p>
            </div>
            <button
              type="button"
              class="cursor-pointer rounded-md p-1 text-slate-400 transition hover:bg-slate-800 hover:text-slate-200"
              aria-label="关闭"
              @click="emit('close')"
            >
              <AppIcon name="close" :size="16" />
            </button>
          </div>

          <div class="max-h-[70vh] overflow-y-auto px-4 py-4 scrollbar-thin">
            <slot />
          </div>

          <div
            v-if="$slots.footer"
            class="flex flex-wrap items-center justify-end gap-2 border-t border-slate-800 px-4 py-3"
          >
            <slot name="footer" />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
