<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    /** null = 配置还没加载到（显示空输入框 + 占位提示）。 */
    modelValue: string | null
    label: string
    hint?: string
    placeholder?: string
    type?: string
    mono?: boolean
    disabled?: boolean
  }>(),
  { hint: '', placeholder: '', type: 'text', mono: false, disabled: false },
)

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

function onInput(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).value)
}
</script>

<template>
  <div>
    <label class="label">{{ label }}</label>
    <input
      class="input"
      :class="mono ? 'mono' : ''"
      :type="type"
      :value="modelValue ?? ''"
      :placeholder="placeholder"
      :disabled="disabled"
      spellcheck="false"
      @input="onInput"
    />
    <p v-if="hint" class="field-hint">{{ hint }}</p>
  </div>
</template>
