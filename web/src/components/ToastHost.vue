<script setup lang="ts">
import { computed } from 'vue'
import { dismiss, useToasts } from '@/composables/useToast'
import type { ToastType } from '@/composables/useToast'
import AppIcon from './AppIcon.vue'

const toasts = useToasts()

const STYLES: Record<ToastType, { wrap: string; icon: string; name: string }> = {
  success: {
    wrap: 'border-emerald-500/40 bg-emerald-950/90 text-emerald-100',
    icon: 'text-emerald-400',
    name: 'check',
  },
  error: {
    wrap: 'border-rose-500/40 bg-rose-950/90 text-rose-100',
    icon: 'text-rose-400',
    name: 'warning',
  },
  warn: {
    wrap: 'border-amber-500/40 bg-amber-950/90 text-amber-100',
    icon: 'text-amber-400',
    name: 'warning',
  },
  info: {
    wrap: 'border-sky-500/40 bg-sky-950/90 text-sky-100',
    icon: 'text-sky-400',
    name: 'inbox',
  },
}

const visible = computed(() =>
  toasts.map((item) => ({ ...item, style: STYLES[item.type] ?? STYLES.info })),
)
</script>

<template>
  <Teleport to="body">
    <div
      class="pointer-events-none fixed top-4 right-4 z-[100] flex w-[min(22rem,calc(100vw-2rem))] flex-col gap-2"
      role="status"
      aria-live="polite"
    >
      <TransitionGroup
        enter-active-class="transition duration-200 ease-out"
        enter-from-class="translate-x-4 opacity-0"
        leave-active-class="transition duration-150 ease-in"
        leave-to-class="translate-x-4 opacity-0"
      >
        <div
          v-for="item in visible"
          :key="item.id"
          class="pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3 py-2.5 shadow-lg backdrop-blur"
          :class="item.style.wrap"
        >
          <span :class="item.style.icon" class="mt-0.5">
            <AppIcon :name="item.style.name" :size="16" />
          </span>
          <p class="min-w-0 flex-1 text-sm leading-snug break-words">{{ item.message }}</p>
          <button
            type="button"
            class="shrink-0 cursor-pointer rounded p-0.5 opacity-60 transition hover:opacity-100"
            aria-label="关闭提示"
            @click="dismiss(item.id)"
          >
            <AppIcon name="close" :size="14" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>
