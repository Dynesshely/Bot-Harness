<script setup lang="ts">
import { useConfig } from '@/composables/configContext'
import NumberField from '@/components/NumberField.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import TextField from '@/components/TextField.vue'
import ToggleField from '@/components/ToggleField.vue'

const HUMANIZE_LEVELS = [
  { value: 'off', label: 'off · 立即回复' },
  { value: 'soft', label: 'soft · 轻微停顿' },
  { value: 'medium', label: 'medium · 中等（默认）' },
  { value: 'strong', label: 'strong · 更慢、更爱沉默' },
]

const { draft } = useConfig()
</script>

<template>
  <div class="space-y-4">
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">人味</h3>
            <span class="text-xs text-slate-500">回复节奏与说话方式</span>
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <SelectMenu
              id="cfg-humanize"
              v-model="draft.humanize_level"
              label="档位 humanize_level"
              :options="HUMANIZE_LEVELS"
              hint="中等档：基础 0.8s + 每 10 字 0.6s（上限 8s），分段间隔约 0.65s，未 @ 时 10% 概率不插话。"
            />
            <NumberField
              v-model="draft.humanize_delay_scale"
              label="停顿倍数 humanize_delay_scale"
              :step="0.1"
              :min="0.5"
              :max="3"
              hint="0.5–3；乘在所有停顿上，觉得太慢就调小"
            />
            <div class="sm:col-span-2 xl:col-span-1">
              <label class="label" for="cfg-persona">人设 persona</label>
              <input
                id="cfg-persona"
                v-model="draft.persona"
                class="input"
                placeholder="例如：说话简短，偶尔用颜文字，不主动说教"
              />
              <p class="field-hint">一句话描述角色，会附加到系统提示词。</p>
            </div>
          </div>
          <p class="field-hint mt-2">
            停顿按「整轮耗时」计算：模型已经想了好几秒的话术，不会再多等，避免变成拖沓。
            @ 机器人的消息永远会回，沉默只作用于群里的普通消息。
          </p>
        </div>

        <!-- 两阶段生成：先出草稿，再用润色模型打磨一遍 -->
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">两阶段生成</h3>
            <ToggleField
              v-model="draft.polish_enabled"
              label="启用润色 polish_enabled"
              class="min-w-[12rem]"
            />
          </div>
          <p class="mb-3 text-xs text-slate-500">
            主模型先出一版草稿，再交给润色模型<strong class="text-slate-300">只改措辞、不改事实</strong>。
            润色超时、报错或返回空内容时直接发草稿，用户不会因为第二段失败而收不到回复。
            只有草稿会进第二段，不重发整段上下文，所以比「整轮再生成一次」便宜得多。
            关闭则回到一次生成；按群覆盖在「策略配置」的覆盖表里（润色列）。
          </p>
          <div
            class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
            :class="draft.polish_enabled ? '' : 'opacity-60'"
          >
            <div class="sm:col-span-2 xl:col-span-1">
              <TextField
                v-model="draft.polish_model"
                label="润色模型 polish_model"
                placeholder="留空 = 使用主模型"
                :disabled="!draft.polish_enabled"
                hint="与主模型同一个端点，所以必须是该端点支持的模型名；留空 = 复用主模型"
              />
            </div>
            <NumberField
              v-model="draft.polish_temperature"
              label="润色温度 polish_temperature"
              :step="0.1"
              :min="0"
              :max="2"
              :disabled="!draft.polish_enabled"
              hint="0–2，默认 0.7；只求措辞，不需要太发散"
            />
            <NumberField
              v-model="draft.polish_max_tokens"
              label="润色输出上限 polish_max_tokens"
              :min="0"
              :max="8192"
              :disabled="!draft.polish_enabled"
              hint="0 = 自动（按草稿长度估算）；范围 0–8192"
            />
            <NumberField
              v-model="draft.polish_timeout_seconds"
              label="润色超时 polish_timeout_seconds（秒）"
              :min="5"
              :max="120"
              :disabled="!draft.polish_enabled"
              hint="5–120，默认 20；超时就直接把草稿发出去"
            />
            <NumberField
              v-model="draft.polish_min_chars"
              label="最短润色长度 polish_min_chars"
              :min="0"
              :max="2000"
              :disabled="!draft.polish_enabled"
              hint="草稿短于该字数就跳过润色（省一次调用）；0 = 不设下限"
            />
          </div>
          <p v-if="!draft.polish_enabled" class="field-hint mt-2">
            已关闭：回复仍由一次模型调用生成。上面的润色参数暂时不生效，但会原样保留（不会被清空）。
          </p>
          <p v-else class="field-hint mt-2">
            关掉润色 = 省掉一次调用和它的延迟；开启后建议把 temperature 留在 0.7 左右，
            并让「润色输出上限」保持 0（自动），免得把长回复截断。
          </p>
        </div>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">回复格式</h3>
          </div>
          <ToggleField
            v-model="draft.markdown_enabled"
            label="用 Markdown 发送 markdown_enabled"
            hint="以 msg_type=2 发送，**加粗** / 列表 / 引用会渲染出来。需要在 QQ 开放平台单独申请 Markdown 模板权限；没权限时发送会自动降级为纯文本并冷却 30 分钟，不会丢消息。"
          />
        </div>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">后台手动发送（会话详情页的输入栏）</h3>
            <ToggleField
              v-model="draft.manual_send_enabled"
              label="允许手动发送 manual_send_enabled"
              class="min-w-[12rem]"
            />
          </div>
          <p class="mb-3 text-xs text-slate-500">
            手动发送走的是<strong class="text-slate-300">主动消息</strong>：需要该群已开启主动消息权限，
            并消耗平台的主动消息配额（未认证 30 条/分钟、单群 1000 条/天）。
            配额与机器人自己的「提前回复 / 被动超时兜底」共用，发太多会先把机器人的自动回复顶掉。
            0 表示不限制。
          </p>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.manual_send_per_minute"
              label="全局每分钟上限 manual_send_per_minute"
              :min="0"
              :max="600"
              hint="所有会话加起来，0 = 不限"
            />
            <NumberField
              v-model="draft.manual_send_per_conversation_qpm"
              label="单会话每分钟上限 manual_send_per_conversation_qpm"
              :min="0"
              :max="600"
              hint="0 = 不限"
            />
            <NumberField
              v-model="draft.manual_send_max_segments"
              label="单次最大分段 manual_send_max_segments"
              :min="1"
              :max="10"
              hint="超过就拒绝并提示，绝不静默截断；每段单独消耗一条主动消息"
            />
          </div>
        </div>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">提前回复（模型思考期间的安抚语）</h3>
            <ToggleField
              v-model="draft.early_reply_enabled"
              label="启用 early_reply_enabled"
              class="min-w-[10rem]"
            />
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.early_reply_delay_seconds"
              label="触发延迟 early_reply_delay_seconds（秒）"
              :min="1"
              :max="60"
              hint="超过该时长仍无回复时，先用被动回复安抚用户"
            />
            <div class="sm:col-span-1 xl:col-span-2">
              <TextField
                v-model="draft.early_reply_text"
                label="提前回复文案 early_reply_text"
                placeholder="有点复杂，让我多想会儿～稍后直接把答案发给你"
                hint="该条消息不会追加用时 / Token 页脚"
              />
            </div>
          </div>
          <div class="mt-3">
            <ToggleField
              v-model="draft.early_reply_recall"
              label="正式答复发出后撤回即时回复 early_reply_recall"
              hint="避免群里一直留着「让我多想会儿」；平台限制 2 分钟内才能撤回，超时则保留原消息。若正式答复发送失败，会保留即时回复以免用户什么都看不到。"
            />
          </div>
          <p class="field-hint mt-2">
            启用后：先占用一次被动回复告知用户，待模型给出结果时改用
            <span class="text-slate-300">主动消息</span>推送正式答案（受平台主动消息频控约束）。
          </p>
        </div>

        <!-- 承诺追问：提醒发出后没回应时，模型按常识决定何时再问，直到收口 -->
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">承诺追问</h3>
            <ToggleField
              v-model="draft.commitment_enabled"
              label="启用 commitment_enabled"
              class="min-w-[12rem]"
            />
          </div>
          <p class="mb-3 text-xs text-slate-500">
            提醒发出后用户没有回应时，由模型按常识判断什么时候再问一次，
            直到用户明确说<strong class="text-slate-300">完成 / 延后 / 取消</strong>才收口。
            每次追问仍是一条正常的回复，受频控与人味节奏约束。
          </p>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <NumberField
              v-model="draft.commitment_max_open_per_member"
              label="未收口上限 commitment_max_open_per_member"
              :min="1"
              :max="50"
              :disabled="!draft.commitment_enabled"
              hint="每人最多同时保留几条未收口承诺；1–50，默认 10"
            />
          </div>
          <p v-if="!draft.commitment_enabled" class="field-hint mt-2">
            已关闭：提醒发完就结束，不会再追问。上面的上限暂时不生效，但会原样保留。
          </p>
          <p v-else class="field-hint mt-2">
            达到上限后不再接受新的承诺，先让用户把旧的收口（完成 / 延后 / 取消）。
          </p>
        </div>
  </div>
</template>
