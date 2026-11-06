<script setup lang="ts">
withDefaults(
  defineProps<{
    title?: string
    hint?: string
    icon?: string
  }>(),
  { title: '暂无数据', hint: '', icon: 'inbox' },
)
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-2 px-4 py-10 text-center">
    <slot name="icon">
      <span class="rounded-full border border-slate-800 bg-slate-900 p-3 text-slate-500">
        <svg
          width="22"
          height="22"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.6"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path
            v-if="icon === 'inbox'"
            d="M2.25 13.5h3.86a2.25 2.25 0 0 1 2.012 1.244l.256.512a2.25 2.25 0 0 0 2.013 1.244h3.218a2.25 2.25 0 0 0 2.013-1.244l.256-.512a2.25 2.25 0 0 1 2.013-1.244h3.859M4.5 19.5h15a2.25 2.25 0 0 0 2.25-2.25V6.75A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25v10.5a2.25 2.25 0 0 0 2.25 2.25Z"
          />
          <path
            v-else
            d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 5.625c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125"
          />
        </svg>
      </span>
    </slot>
    <p class="text-sm font-medium text-slate-400">{{ title }}</p>
    <p v-if="hint" class="max-w-md text-xs leading-relaxed text-slate-500">{{ hint }}</p>
    <slot />
  </div>
</template>
