<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    modelValue: boolean
    label: string
    hint?: string
    disabled?: boolean
  }>(),
  { hint: '', disabled: false },
)

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

function onChange(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).checked)
}
</script>

<template>
  <div>
    <label class="label">{{ label }}</label>
    <label
      class="flex cursor-pointer items-center gap-2 rounded-lg border border-slate-800 bg-slate-950/60 px-3 py-2 transition-colors hover:border-slate-700"
      :class="disabled ? 'cursor-not-allowed opacity-60' : ''"
    >
      <input
        type="checkbox"
        class="h-4 w-4 accent-sky-500"
        :checked="modelValue"
        :disabled="disabled"
        @change="onChange"
      />
      <span class="text-sm text-slate-300">{{ modelValue ? '已开启' : '已关闭' }}</span>
    </label>
    <p v-if="props.hint" class="field-hint">{{ hint }}</p>
  </div>
</template>
