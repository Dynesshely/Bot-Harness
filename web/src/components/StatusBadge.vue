<script setup lang="ts">
import { computed } from 'vue'
import type { BadgeInfo, Tone } from '@/utils/badges'
import { TONE_CLASS } from '@/utils/badges'

const props = defineProps<{
  info: BadgeInfo
  dot?: boolean
}>()

const klass = computed(() => TONE_CLASS[props.info.tone as Tone] ?? TONE_CLASS.slate)
</script>

<template>
  <span class="badge" :class="klass">
    <span v-if="dot" class="h-1.5 w-1.5 rounded-full bg-current opacity-80" />
    {{ info.label }}
  </span>
</template>
