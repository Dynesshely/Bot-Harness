<script setup lang="ts">
import { useConfig } from '@/composables/configContext'
import NumberField from '@/components/NumberField.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import ToggleField from '@/components/ToggleField.vue'

const PROACTIVE_LEVELS = [
  { value: 'off', label: 'off · 不主动' },
  { value: 'auto', label: 'auto · 攒够额度可开口' },
]

const { draft } = useConfig()
</script>

<template>
  <div class="space-y-4">
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">自发主动</h3>
            <div class="flex items-center gap-2">
              <span class="text-xs text-slate-500">按群 opt-in，默认关</span>
              <div class="w-44">
                <SelectMenu
                  v-model="draft.proactive_level"
                  size="sm"
                  aria-label="自发主动档位 proactive_level"
                  :options="PROACTIVE_LEVELS"
                />
              </div>
            </div>
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.proactive_ratio"
              label="每条额度需要多少人类消息 proactive_ratio"
              :min="3"
              :max="100"
              hint="默认 10：每 10 条人类消息换 1 次主动开口"
            />
            <NumberField
              v-model="draft.proactive_max_credits"
              label="最多攒几点 proactive_max_credits"
              :min="1"
              :max="10"
              hint="防止长时间沉默后一次爆发"
            />
            <NumberField
              v-model="draft.proactive_min_interval_minutes"
              label="最小间隔（分钟）proactive_min_interval_minutes"
              :min="1"
              :max="240"
            />
            <NumberField
              v-model="draft.proactive_quiet_start"
              label="静默开始（点）proactive_quiet_start"
              :min="0"
              :max="23"
            />
            <NumberField
              v-model="draft.proactive_quiet_end"
              label="静默结束（点）proactive_quiet_end"
              :min="0"
              :max="23"
              hint="可跨午夜（23 → 8）"
            />
          </div>
          <p class="field-hint mt-2">
            这是最容易惹人烦的能力：只有<b>按群开启</b>才会生效，且要同时满足额度、静默时段、最小间隔、
            最近 30 分钟有人说话；连续两次主动发言没人接话，该群会自动降级回「只做委托提醒」。
            每条都会记下模型给出的「为什么现在说」，可在「定时任务」页看到。
          </p>
        </div>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">主动提醒</h3>
            <ToggleField
              v-model="draft.tasks_enabled"
              label="允许定时提醒 tasks_enabled"
              class="min-w-[12rem]"
            />
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.tasks_max_per_member"
              label="每人待发上限 tasks_max_per_member"
              :min="1"
              :max="50"
              hint="同一个会话里还没发出的提醒条数"
            />
            <NumberField
              v-model="draft.tasks_max_delay_hours"
              label="最长提前量 tasks_max_delay_hours"
              :min="1"
              :max="720"
              hint="能安排多远之后的提醒"
            />
          </div>
          <p class="field-hint mt-2">
            这是<b>唯一能在无人说话时发消息</b>的能力，只由用户明确要求触发（「X 之后提醒我」）。
            到点用主动消息发送，消耗平台的主动消息配额。停机超过 10 分钟的提醒标记失败、不补发。
            任务可到「定时任务」页查看与取消。
          </p>
        </div>
  </div>
</template>
