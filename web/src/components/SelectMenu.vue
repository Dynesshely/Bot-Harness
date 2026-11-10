<script setup lang="ts" generic="T extends string | number">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import type { SelectMenuOption } from '@/components/selectMenu'

/**
 * 同屏可能有十几个下拉（按群覆盖卡片里每个字段一个）。
 * 打开一个新的就广播一次，其它还开着的自己关上 —— 否则会叠出好几层弹层。
 */
const OPEN_EVENT = 'select-menu:open'

/**
 * 暗色自定义下拉：替代原生 <select>。
 *
 * 为什么不用原生 select：暗色主题下操作系统会把下拉弹层渲染成浅色系统菜单，
 * 与后台整体风格割裂，而且无法给选项加说明文字。这里用 button + listbox 自己画。
 *
 * 交互：点击展开 / 点击外部关闭 / Escape 关闭 / ArrowUp·Down 移动 / Enter 选择 /
 * Home·End 首尾 / Tab 关闭。焦点始终留在触发按钮上，用 aria-activedescendant
 * 指向高亮项（标准 combobox 模式），因此不需要把焦点移进弹层。
 */
const props = withDefaults(
  defineProps<{
    modelValue: T | null | undefined
    options: SelectMenuOption<T>[]
    label?: string
    placeholder?: string
    hint?: string
    disabled?: boolean
    /** sm = 紧凑尺寸（筛选栏、分页条、卡片内密集字段）。 */
    size?: 'sm' | 'md'
    /** 没有可见 label 时的无障碍名称。 */
    ariaLabel?: string
    id?: string
  }>(),
  {
    label: '',
    placeholder: '请选择',
    hint: '',
    disabled: false,
    size: 'md',
    ariaLabel: '',
    id: '',
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: T]
  change: [value: T]
  open: []
  close: []
}>()

const uid = useId()
/**
 * useId 只在同一个应用实例内唯一，单测里每个用例各挂一个 app 就会撞 id；
 * 所以再拼一段随机后缀，保证 aria-controls / aria-activedescendant 永远指得准。
 */
const autoId = `select-${uid}-${Math.random().toString(36).slice(2, 8)}`
const root = ref<HTMLElement | null>(null)
const listEl = ref<HTMLElement | null>(null)
const open = ref(false)
const dropUp = ref(false)
const activeIndex = ref(-1)

const triggerId = computed(() => props.id || autoId)
const labelId = computed(() => `${triggerId.value}-label`)
const listboxId = computed(() => `${triggerId.value}-listbox`)
const optionId = (index: number) => `${triggerId.value}-option-${index}`
const activeId = computed(() =>
  open.value && activeIndex.value >= 0 ? optionId(activeIndex.value) : undefined,
)

/** 值与选项比较：允许 20 与 '20' 这类数字/字符串混用（分页条传数字）。 */
function sameValue(a: T | null | undefined, b: T | null | undefined): boolean {
  if (a === null || a === undefined || b === null || b === undefined) return false
  return String(a) === String(b)
}

const selectedIndex = computed(() =>
  props.options.findIndex((option) => sameValue(option.value, props.modelValue)),
)
const selectedOption = computed<SelectMenuOption<T> | null>(() =>
  selectedIndex.value >= 0 ? props.options[selectedIndex.value] ?? null : null,
)
const displayLabel = computed(() => selectedOption.value?.label ?? props.placeholder)
const showPlaceholder = computed(() => selectedOption.value === null)

/** 空间不够就向上弹，避免被卡片 / 页面容器的 overflow 裁掉。 */
function measure() {
  const el = root.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const below = window.innerHeight - rect.bottom
  dropUp.value = below < 260 && rect.top > below
}

function scrollActiveIntoView() {
  const list = listEl.value
  if (!list || activeIndex.value < 0) return
  const node = list.children[activeIndex.value] as HTMLElement | undefined
  // jsdom 没有 scrollIntoView：用可选调用保住单测。
  node?.scrollIntoView?.({ block: 'nearest' })
}

function openMenu() {
  if (props.disabled || open.value) return
  // 让同屏其它下拉先收起（事件里带上自己的 uid，忽略自己）。
  document.dispatchEvent(new CustomEvent(OPEN_EVENT, { detail: triggerId.value }))
  measure()
  open.value = true
  activeIndex.value = props.options.length === 0 ? -1 : Math.max(0, selectedIndex.value)
  emit('open')
  void nextTick(scrollActiveIntoView)
}

function closeMenu() {
  if (!open.value) return
  open.value = false
  activeIndex.value = -1
  emit('close')
}

function toggle() {
  if (props.disabled) return
  if (open.value) closeMenu()
  else openMenu()
}

function moveTo(index: number) {
  const count = props.options.length
  if (count === 0) {
    activeIndex.value = -1
    return
  }
  activeIndex.value = ((index % count) + count) % count
  void nextTick(scrollActiveIntoView)
}

function move(delta: number) {
  if (props.options.length === 0) return
  const base = activeIndex.value < 0 ? (delta > 0 ? -1 : 0) : activeIndex.value
  moveTo(base + delta)
}

function choose(option: SelectMenuOption<T> | undefined) {
  if (!option || props.disabled) return
  const changed = !sameValue(option.value, props.modelValue)
  emit('update:modelValue', option.value)
  if (changed) emit('change', option.value)
  closeMenu()
}

function onKeydown(event: KeyboardEvent) {
  if (props.disabled) return
  const option = props.options[activeIndex.value]
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault()
      if (open.value) move(1)
      else openMenu()
      break
    case 'ArrowUp':
      event.preventDefault()
      if (open.value) move(-1)
      else openMenu()
      break
    case 'Home':
      if (!open.value) return
      event.preventDefault()
      moveTo(0)
      break
    case 'End':
      if (!open.value) return
      event.preventDefault()
      moveTo(props.options.length - 1)
      break
    case 'Enter':
      event.preventDefault()
      if (!open.value) openMenu()
      else choose(option)
      break
    case ' ':
    case 'Spacebar':
      event.preventDefault()
      if (!open.value) openMenu()
      else choose(option)
      break
    case 'Escape':
      if (!open.value) return
      event.preventDefault()
      closeMenu()
      break
    case 'Tab':
      closeMenu()
      break
    default:
      break
  }
}

/** 点击外部关闭（pointerdown 捕获阶段，避免和选项 click 抢先后顺序）。 */
function onDocumentPointerDown(event: Event) {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && root.value?.contains(target)) return
  closeMenu()
}

function onDocumentKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) closeMenu()
}

function onPeerOpen(event: Event) {
  const detail = (event as CustomEvent<string>).detail
  if (detail !== triggerId.value) closeMenu()
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown, true)
  document.addEventListener('keydown', onDocumentKeydown)
  document.addEventListener(OPEN_EVENT, onPeerOpen)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  document.removeEventListener('keydown', onDocumentKeydown)
  document.removeEventListener(OPEN_EVENT, onPeerOpen)
})
</script>

<template>
  <div ref="root" class="relative">
    <span v-if="label" :id="labelId" class="label">{{ label }}</span>
    <button
      :id="triggerId"
      type="button"
      role="combobox"
      class="input flex w-full min-w-0 items-center justify-between gap-2 text-left"
      :class="[
        size === 'sm' ? 'px-2 py-1 text-xs' : '',
        disabled ? 'cursor-not-allowed' : 'cursor-pointer',
        open ? 'border-sky-500 ring-1 ring-sky-500' : '',
      ]"
      :disabled="disabled"
      :title="displayLabel"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="listboxId"
      :aria-activedescendant="activeId"
      :aria-labelledby="label ? labelId : undefined"
      :aria-label="!label ? ariaLabel || placeholder : undefined"
      @click="toggle"
      @keydown="onKeydown"
    >
      <span
        class="min-w-0 flex-1 truncate"
        :class="showPlaceholder ? 'text-slate-500' : 'text-slate-100'"
      >
        {{ displayLabel }}
      </span>
      <AppIcon
        name="arrowDown"
        :size="14"
        class="shrink-0 text-slate-500 transition-transform"
        :class="open ? 'rotate-180' : ''"
      />
    </button>

    <div
      v-if="open"
      class="absolute inset-x-0 z-30"
      :class="dropUp ? 'bottom-full mb-1' : 'top-full mt-1'"
    >
      <ul
        :id="listboxId"
        ref="listEl"
        role="listbox"
        :aria-labelledby="label ? labelId : undefined"
        class="scrollbar-thin max-h-64 overflow-y-auto rounded-lg border border-slate-700 bg-slate-900 py-1 shadow-xl shadow-black/40"
      >
        <li
          v-if="options.length === 0"
          class="px-3 py-2 text-xs text-slate-500"
          role="presentation"
        >
          （无可选项）
        </li>
        <li
          v-for="(option, index) in options"
          :id="optionId(index)"
          :key="`${String(option.value)}-${index}`"
          role="option"
          :aria-selected="sameValue(option.value, modelValue)"
          class="flex cursor-pointer flex-col gap-0.5 px-3 py-1.5 text-sm transition-colors"
          :class="[
            index === activeIndex ? 'bg-sky-500/15 text-sky-200' : 'text-slate-300',
            sameValue(option.value, modelValue) ? 'font-medium' : '',
          ]"
          @click="choose(option)"
          @mousemove="activeIndex = index"
        >
          <span class="truncate">{{ option.label }}</span>
          <span v-if="option.desc" class="text-[11px] font-normal text-slate-500">
            {{ option.desc }}
          </span>
        </li>
      </ul>
    </div>

    <p v-if="hint" class="field-hint">{{ hint }}</p>
  </div>
</template>
