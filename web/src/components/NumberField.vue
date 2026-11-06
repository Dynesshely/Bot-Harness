<script setup lang="ts">
withDefaults(
  defineProps<{
    modelValue: number | null
    label: string
    hint?: string
    placeholder?: string
    step?: number | string
    min?: number
    max?: number
    disabled?: boolean
  }>(),
  { hint: '', placeholder: '未设置', step: 1, min: undefined, max: undefined, disabled: false },
)

const emit = defineEmits<{ 'update:modelValue': [value: number | null] }>()

function onInput(event: Event) {
  const raw = (event.target as HTMLInputElement).value.trim()
  if (raw === '') {
    emit('update:modelValue', null)
    return
  }
  const parsed = Number(raw)
  emit('update:modelValue', Number.isNaN(parsed) ? null : parsed)
}
</script>

<template>
  <div>
    <label class="label">{{ label }}</label>
    <input
      class="input tabular-nums"
      :class="disabled ? 'cursor-not-allowed opacity-60' : ''"
      type="number"
      :value="modelValue ?? ''"
      :placeholder="placeholder"
      :step="step"
      :min="min"
      :max="max"
      :disabled="disabled"
      @input="onInput"
    />
    <p v-if="hint" class="field-hint">{{ hint }}</p>
  </div>
</template>
