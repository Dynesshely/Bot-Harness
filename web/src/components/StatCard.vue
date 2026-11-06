<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from './AppIcon.vue'

const props = withDefaults(
  defineProps<{
    label: string
    value: string
    hint?: string
    icon?: string
    /** 数值配色：default | emerald | rose | amber | sky */
    tone?: 'default' | 'emerald' | 'rose' | 'amber' | 'sky'
    mono?: boolean
  }>(),
  { hint: '', icon: '', tone: 'default', mono: false },
)

const TONE: Record<string, string> = {
  default: 'text-slate-100',
  emerald: 'text-emerald-300',
  rose: 'text-rose-300',
  amber: 'text-amber-300',
  sky: 'text-sky-300',
}

const valueClass = computed(() => [
  TONE[props.tone] ?? TONE.default,
  props.mono ? 'font-mono text-base' : '',
])
</script>

<template>
  <div class="card px-4 py-3">
    <div class="flex items-center justify-between gap-2">
      <span class="truncate text-xs font-medium text-slate-400">{{ label }}</span>
      <AppIcon v-if="icon" :name="icon" :size="15" class="text-slate-500" />
    </div>
    <p class="mt-1 truncate text-lg font-semibold tabular-nums" :class="valueClass" :title="value">
      {{ value }}
    </p>
    <p v-if="hint" class="mt-0.5 truncate text-xs text-slate-500" :title="hint">{{ hint }}</p>
  </div>
</template>
