<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from './AppIcon.vue'
import SelectMenu from './SelectMenu.vue'

const props = withDefaults(
  defineProps<{
    page: number
    pageSize: number
    total: number
    pageSizeOptions?: number[]
  }>(),
  { pageSizeOptions: () => [20, 50, 100] },
)

const emit = defineEmits<{
  'update:page': [page: number]
  'update:pageSize': [size: number]
}>()

/** 每页条数下拉的选项（数字 value，SelectMenu 支持 string | number）。 */
const pageSizeSelectOptions = computed(() =>
  props.pageSizeOptions.map((size) => ({ value: size, label: `${size} 条/页` })),
)

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.pageSize))))
const rangeStart = computed(() => (props.total === 0 ? 0 : (props.page - 1) * props.pageSize + 1))
const rangeEnd = computed(() => Math.min(props.total, props.page * props.pageSize))

function goto(page: number) {
  const next = Math.min(Math.max(1, page), totalPages.value)
  if (next !== props.page) emit('update:page', next)
}
</script>

<template>
  <div class="flex flex-wrap items-center justify-between gap-3 text-sm text-slate-400">
    <div class="tabular-nums">
      共 <span class="text-slate-200">{{ total }}</span> 条 · 当前
      <span class="text-slate-200">{{ rangeStart }}-{{ rangeEnd }}</span>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <div class="flex w-32 items-center gap-1.5 text-xs">
        <span class="shrink-0 text-slate-500">每页</span>
        <SelectMenu
          class="min-w-0 flex-1"
          :model-value="pageSize"
          size="sm"
          aria-label="每页条数"
          :options="pageSizeSelectOptions"
          @update:model-value="emit('update:pageSize', Number($event))"
        />
      </div>

      <div class="flex items-center gap-1">
        <button
          type="button"
          class="btn btn-sm"
          :disabled="page <= 1"
          aria-label="上一页"
          @click="goto(page - 1)"
        >
          <AppIcon name="chevronLeft" :size="14" />
          上一页
        </button>
        <span class="px-2 text-xs tabular-nums text-slate-300">
          {{ Math.min(page, totalPages) }} / {{ totalPages }}
        </span>
        <button
          type="button"
          class="btn btn-sm"
          :disabled="page >= totalPages"
          aria-label="下一页"
          @click="goto(page + 1)"
        >
          下一页
          <AppIcon name="chevronRight" :size="14" />
        </button>
      </div>
    </div>
  </div>
</template>
