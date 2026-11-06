<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { StatsPoint } from '@/api/types'
import { formatMs, formatNumber, formatShortDateTime } from '@/utils/format'

/**
 * 纯手写 SVG 折线图（零图表依赖）。
 * 宽度通过 ResizeObserver 实测，因此坐标即像素，文字不会被拉伸变形。
 */
const props = withDefaults(
  defineProps<{
    points: StatsPoint[]
    height?: number
  }>(),
  { height: 260 },
)

const PAD = { top: 14, right: 14, bottom: 30, left: 48 }

const container = ref<HTMLElement | null>(null)
const width = ref(760)
const hover = ref<number | null>(null)
const visible = ref({ in: true, out: true, errors: true })

let observer: ResizeObserver | null = null

onMounted(() => {
  if (!container.value) return
  const measure = () => {
    const w = container.value?.clientWidth ?? 0
    width.value = Math.max(280, Math.round(w))
  }
  measure()
  observer = new ResizeObserver(measure)
  observer.observe(container.value)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})

const chartHeight = computed(() => props.height)
const innerW = computed(() => Math.max(10, width.value - PAD.left - PAD.right))
const innerH = computed(() => Math.max(10, chartHeight.value - PAD.top - PAD.bottom))
const count = computed(() => props.points.length)
const hasData = computed(() => count.value > 0)

const maxValue = computed(() => {
  let max = 0
  for (const point of props.points) {
    max = Math.max(max, point.in ?? 0, point.out ?? 0, visible.value.errors ? (point.errors ?? 0) : 0)
  }
  if (max <= 0) return 1
  return Math.ceil(max * 1.15)
})

const baseY = computed(() => PAD.top + innerH.value)

function xAt(index: number): number {
  if (count.value <= 1) return PAD.left + innerW.value / 2
  return PAD.left + (index / (count.value - 1)) * innerW.value
}

function yAt(value: number): number {
  const ratio = Math.max(0, Math.min(1, value / maxValue.value))
  return PAD.top + innerH.value - ratio * innerH.value
}

function buildLine(key: 'in' | 'out'): string {
  if (!hasData.value) return ''
  return props.points
    .map((point, index) => {
      const x = xAt(index).toFixed(2)
      const y = yAt(Number(point[key] ?? 0)).toFixed(2)
      return `${index === 0 ? 'M' : 'L'} ${x} ${y}`
    })
    .join(' ')
}

const lineIn = computed(() => (visible.value.in ? buildLine('in') : ''))
const lineOut = computed(() => (visible.value.out ? buildLine('out') : ''))

const areaIn = computed(() => {
  if (!visible.value.in || !hasData.value) return ''
  const first = xAt(0).toFixed(2)
  const last = xAt(count.value - 1).toFixed(2)
  return `${buildLine('in')} L ${last} ${baseY.value.toFixed(2)} L ${first} ${baseY.value.toFixed(2)} Z`
})

const yTicks = computed(() => {
  const steps = 4
  const ticks: { value: number; y: number; label: string }[] = []
  for (let i = 0; i <= steps; i += 1) {
    const value = (maxValue.value / steps) * i
    ticks.push({ value, y: yAt(value), label: formatNumber(Math.round(value)) })
  }
  return ticks
})

const xTicks = computed(() => {
  if (!hasData.value) return []
  const target = Math.max(2, Math.min(6, Math.floor(innerW.value / 110)))
  const step = Math.max(1, Math.ceil(count.value / target))
  const ticks: { index: number; x: number; label: string }[] = []
  for (let i = 0; i < count.value; i += step) {
    ticks.push({ index: i, x: xAt(i), label: formatShortDateTime(props.points[i]?.ts) })
  }
  return ticks
})

const barWidth = computed(() => Math.max(1.5, Math.min(10, innerW.value / Math.max(1, count.value) - 1)))

const hovered = computed(() => (hover.value === null ? null : (props.points[hover.value] ?? null)))

const tooltipLeft = computed(() => {
  if (hover.value === null) return 0
  const raw = xAt(hover.value)
  return Math.min(Math.max(raw, 96), Math.max(96, width.value - 96))
})

function onMove(event: MouseEvent) {
  const target = event.currentTarget as SVGRectElement
  const rect = target.getBoundingClientRect()
  if (rect.width <= 0 || count.value === 0) return
  const ratio = (event.clientX - rect.left) / rect.width
  const index = Math.round(ratio * (count.value - 1))
  hover.value = Math.min(Math.max(0, index), count.value - 1)
}
</script>

<template>
  <div ref="container" class="relative w-full">
    <div v-if="!hasData" class="flex h-56 items-center justify-center text-sm text-slate-500">
      暂无统计数据
    </div>

    <svg
      v-else
      :width="width"
      :height="chartHeight"
      :viewBox="`0 0 ${width} ${chartHeight}`"
      class="block max-w-full"
      role="img"
      aria-label="收发消息时序图"
    >
      <defs>
        <linearGradient id="ts-area-in" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="#0ea5e9" stop-opacity="0.32" />
          <stop offset="100%" stop-color="#0ea5e9" stop-opacity="0" />
        </linearGradient>
      </defs>

      <!-- 网格与 Y 轴 -->
      <g>
        <template v-for="tick in yTicks" :key="`y-${tick.value}`">
          <line
            :x1="PAD.left"
            :x2="PAD.left + innerW"
            :y1="tick.y"
            :y2="tick.y"
            stroke="#1e293b"
            stroke-width="1"
          />
          <text
            :x="PAD.left - 8"
            :y="tick.y + 4"
            text-anchor="end"
            fill="#64748b"
            font-size="10"
            class="tabular-nums"
          >
            {{ tick.label }}
          </text>
        </template>
      </g>

      <!-- X 轴标签 -->
      <g>
        <text
          v-for="tick in xTicks"
          :key="`x-${tick.index}`"
          :x="tick.x"
          :y="chartHeight - 10"
          text-anchor="middle"
          fill="#64748b"
          font-size="10"
        >
          {{ tick.label }}
        </text>
      </g>

      <!-- 错误柱 -->
      <g v-if="visible.errors">
        <rect
          v-for="(point, index) in points"
          :key="`e-${index}`"
          :x="xAt(index) - barWidth / 2"
          :y="yAt(point.errors ?? 0)"
          :width="barWidth"
          :height="Math.max(0, baseY - yAt(point.errors ?? 0))"
          fill="#f43f5e"
          opacity="0.35"
          rx="1"
        />
      </g>

      <!-- 折线与面积 -->
      <path v-if="areaIn" :d="areaIn" fill="url(#ts-area-in)" />
      <path
        v-if="lineIn"
        :d="lineIn"
        fill="none"
        stroke="#38bdf8"
        stroke-width="2"
        stroke-linejoin="round"
        stroke-linecap="round"
      />
      <path
        v-if="lineOut"
        :d="lineOut"
        fill="none"
        stroke="#34d399"
        stroke-width="2"
        stroke-linejoin="round"
        stroke-linecap="round"
      />

      <!-- hover 指示线 -->
      <g v-if="hover !== null">
        <line
          :x1="xAt(hover)"
          :x2="xAt(hover)"
          :y1="PAD.top"
          :y2="baseY"
          stroke="#94a3b8"
          stroke-width="1"
          stroke-dasharray="3 3"
        />
      </g>

      <!-- 交互热区 -->
      <rect
        :x="PAD.left"
        :y="PAD.top"
        :width="innerW"
        :height="innerH"
        fill="transparent"
        @mousemove="onMove"
        @mouseleave="hover = null"
      />
    </svg>

    <div
      v-if="hasData && hovered"
      class="pointer-events-none absolute top-2 z-10 w-48 -translate-x-1/2 rounded-lg border border-slate-700 bg-slate-900/95 px-3 py-2 text-xs shadow-xl"
      :style="{ left: `${tooltipLeft}px` }"
    >
      <p class="mb-1 font-medium text-slate-200">{{ formatShortDateTime(hovered.ts) }}</p>
      <p class="flex justify-between text-sky-300">
        <span>接收</span><span class="tabular-nums">{{ formatNumber(hovered.in) }}</span>
      </p>
      <p class="flex justify-between text-emerald-300">
        <span>发送</span><span class="tabular-nums">{{ formatNumber(hovered.out) }}</span>
      </p>
      <p class="flex justify-between text-rose-300">
        <span>错误</span><span class="tabular-nums">{{ formatNumber(hovered.errors) }}</span>
      </p>
      <p class="mt-1 flex justify-between border-t border-slate-800 pt-1 text-slate-400">
        <span>平均耗时</span><span class="tabular-nums">{{ formatMs(hovered.avg_latency_ms) }}</span>
      </p>
      <p class="flex justify-between text-slate-400">
        <span>tokens</span><span class="tabular-nums">{{ formatNumber(hovered.tokens) }}</span>
      </p>
    </div>

    <!-- 图例 -->
    <div v-if="hasData" class="mt-2 flex flex-wrap items-center gap-4 px-1 text-xs text-slate-400">
      <button
        type="button"
        class="flex cursor-pointer items-center gap-1.5 transition hover:text-slate-200"
        :class="visible.in ? '' : 'opacity-40'"
        @click="visible.in = !visible.in"
      >
        <span class="h-0.5 w-4 rounded bg-sky-400" />接收
      </button>
      <button
        type="button"
        class="flex cursor-pointer items-center gap-1.5 transition hover:text-slate-200"
        :class="visible.out ? '' : 'opacity-40'"
        @click="visible.out = !visible.out"
      >
        <span class="h-0.5 w-4 rounded bg-emerald-400" />发送
      </button>
      <button
        type="button"
        class="flex cursor-pointer items-center gap-1.5 transition hover:text-slate-200"
        :class="visible.errors ? '' : 'opacity-40'"
        @click="visible.errors = !visible.errors"
      >
        <span class="h-2.5 w-2.5 rounded-sm bg-rose-500/50" />错误
      </button>
    </div>
  </div>
</template>
