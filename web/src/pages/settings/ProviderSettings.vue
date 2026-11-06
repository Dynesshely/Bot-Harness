<script setup lang="ts">
import { MODEL_PARAM_KEYS, useConfig } from '@/composables/configContext'
import ModalDialog from '@/components/ModalDialog.vue'
import NumberField from '@/components/NumberField.vue'
import ToggleField from '@/components/ToggleField.vue'

const { draft, loaded, saving, apiKeySource, modelIds, modelsBaseUrl, modelsFetchedAt, modelsLoading, modelsError, customModel, modelMissingFromList, promptOpen, modelParamsOpen, modelParamsDirtyCount, clearStoredKey, loadModels } = useConfig()
</script>

<template>
  <div class="space-y-4">
        <!-- 系统提示词：正文在页面里预览，大屏编辑走弹窗 -->
        <div>
          <div class="flex flex-wrap items-center justify-between gap-2">
            <label class="label mb-0" for="cfg-prompt">系统提示词 system_prompt</label>
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              title="在弹窗里大屏编辑（Esc 关闭）"
              @click="promptOpen = true"
            >
              <AppIcon name="editSquare" :size="14" />
              大屏编辑
            </button>
          </div>
          <textarea
            id="cfg-prompt"
            v-model="draft.system_prompt"
            class="input mt-1 h-40 resize-y leading-relaxed"
            placeholder="你是……"
          />
          <p class="field-hint">会作为 system 消息注入每次对话，注意上下文长度。</p>
        </div>

        <!-- 模型 Provider：端点 / 密钥 / 模型名，永远只来自数据库 -->
        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-slate-200">模型 Provider</h3>
            <span class="text-xs text-slate-500">端点 · 密钥 · 模型</span>
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <div class="sm:col-span-2 xl:col-span-3">
              <label class="label" for="cfg-base-url">模型端点 openai_base_url</label>
              <input
                id="cfg-base-url"
                v-model="draft.openai_base_url"
                class="input mono"
                type="text"
                spellcheck="false"
                placeholder="https://your-endpoint.example/v1"
              />
              <p class="field-hint">
                任何 OpenAI 兼容服务的根地址（要带版本段，如 <span class="mono">/v1</span>）。
                改完保存立即生效，不用重启。<strong class="text-slate-300">这是端点的唯一来源</strong>：
                <span class="mono">.env</span> 里已经不再读取它，留空表示未配置（模型调用会直接报错）。
                换了端点记得用右边的「刷新列表」重新拉模型。
              </p>
            </div>

            <div class="sm:col-span-2 xl:col-span-3">
              <label class="label" for="cfg-api-key">模型 API Key openai_api_key</label>
              <div class="flex flex-wrap items-center gap-2">
                <input
                  id="cfg-api-key"
                  v-model="draft.openai_api_key"
                  class="input mono min-w-56 flex-1"
                  type="password"
                  autocomplete="new-password"
                  spellcheck="false"
                  :placeholder="
                    apiKeySource === 'db' ? '已配置（留空则保持不变）' : '尚未配置，请填写 API Key'
                  "
                />
                <button
                  v-if="apiKeySource === 'db'"
                  type="button"
                  class="btn btn-ghost btn-sm"
                  :disabled="saving"
                  title="删掉保存的密钥（删掉后将无法调用模型，直到重新填写）"
                  @click="clearStoredKey"
                >
                  清除后台密钥
                </button>
              </div>
              <p class="field-hint">
                密钥<strong class="text-slate-300">只进不出</strong>：保存后接口永远不会再把它发回浏览器，
                只告诉你「有没有配」。它和端点一样只存在数据库里，<span class="mono">.env</span> 已不再读取。
                换成别的服务商时通常要一起换掉这把 key；留空 = 保持现状。
              </p>
            </div>

            <div class="sm:col-span-2 xl:col-span-3">
              <label class="label" for="cfg-model">模型 model</label>
              <div class="flex flex-wrap items-center gap-2">
                <select
                  v-if="!customModel"
                  id="cfg-model"
                  v-model="draft.model"
                  class="input mono min-w-56 flex-1"
                >
                  <option v-if="!modelIds.length" :value="draft.model">{{ draft.model || '（先刷新模型列表）' }}</option>
                  <option v-else-if="modelMissingFromList" :value="draft.model">
                    {{ draft.model }}（不在列表中）
                  </option>
                  <option v-for="id in modelIds" :key="id" :value="id">{{ id }}</option>
                </select>
                <input
                  v-else
                  id="cfg-model"
                  v-model="draft.model"
                  class="input mono flex-1"
                  placeholder="手动输入模型名，例如 vendor/model-name"
                />
                <button
                  type="button"
                  class="btn btn-ghost btn-sm"
                  :disabled="modelsLoading"
                  @click="loadModels(true)"
                >
                  <AppIcon name="refresh" :size="14" :class="modelsLoading ? 'animate-spin' : ''" />
                  {{ modelsLoading ? '获取中…' : '刷新列表' }}
                </button>
                <button type="button" class="btn btn-ghost btn-sm" @click="customModel = !customModel">
                  {{ customModel ? '从列表选择' : '手动输入' }}
                </button>
              </div>
              <p class="field-hint">
                <template v-if="modelsError">
                  <span class="text-rose-400">{{ modelsError }}</span>
                </template>
                <template v-else-if="modelIds.length">
                  共 {{ modelIds.length }} 个可用模型，来源
                  <span class="mono">{{ modelsBaseUrl || '（端点未配置）' }}</span>
                  <span v-if="modelsFetchedAt">（{{ modelsFetchedAt }} 获取）</span>
                </template>
                <template v-else>尚未获取模型列表，点击「刷新列表」从上游 /models 拉取。</template>
              </p>
              <p v-if="!customModel && modelMissingFromList" class="field-hint text-amber-400">
                当前模型「{{ draft.model }}」不在上游返回的列表中，保存后可能在调用时报错。
              </p>
            </div>
          </div>
        </div>

        <!-- 模型参数：默认折叠，标题上能看到有没有未保存的改动 -->
        <div class="rounded-xl border border-slate-800 bg-slate-950/40">
          <button
            type="button"
            class="flex w-full cursor-pointer items-center justify-between gap-2 px-4 py-3 text-left"
            :aria-expanded="modelParamsOpen"
            :title="modelParamsOpen ? '折叠模型参数' : '展开模型参数'"
            @click="modelParamsOpen = !modelParamsOpen"
          >
            <span class="flex items-center gap-2 text-sm font-medium text-slate-200">
              <AppIcon name="cpu" :size="14" class="text-sky-400" />
              模型参数
            </span>
            <span class="flex items-center gap-2 text-xs text-slate-500">
              <span v-if="modelParamsDirtyCount" class="text-amber-400">
                {{ modelParamsDirtyCount }} 项未保存
              </span>
              <span v-else>{{ MODEL_PARAM_KEYS.length }} 项</span>
              <AppIcon
                name="chevronRight"
                :size="14"
                class="transition-transform"
                :class="modelParamsOpen ? 'rotate-90' : ''"
              />
            </span>
          </button>
          <div v-show="modelParamsOpen" class="px-4 pb-4">
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
              <NumberField
                v-model="draft.temperature"
                label="采样温度 temperature"
                :step="0.1"
                :min="0"
                :max="2"
                hint="0 ~ 2，越低越稳定"
              />
              <NumberField
                v-model="draft.max_tokens"
                label="单次回复上限 max_tokens"
                :min="1"
                :max="10000000"
                hint="默认 1024；范围 1 ~ 10000000（上限为预留）"
              />
              <NumberField
                v-model="draft.request_timeout_seconds"
                label="请求超时 request_timeout_seconds（秒）"
                :min="1"
              />
              <NumberField v-model="draft.context_max_turns" label="上下文最大轮次 context_max_turns" :min="1" />
              <NumberField
                v-model="draft.context_max_tokens"
                label="上下文最大 tokens context_max_tokens"
                :min="256"
                :max="10000000"
                hint="默认 256000；范围 256 ~ 10000000"
              />
              <NumberField v-model="draft.history_ttl_hours" label="历史保留 history_ttl_hours（小时）" :min="1" />
              <NumberField v-model="draft.reply_max_chars" label="回复最大字数 reply_max_chars" :min="1" />
              <NumberField v-model="draft.reply_max_segments" label="回复最大分段 reply_max_segments" :min="1" />
              <NumberField v-model="draft.rate_limit_user_qpm" label="每人 QPM 限流 rate_limit_user_qpm" :min="0" />
              <NumberField v-model="draft.rate_limit_group_qpm" label="每群 QPM 限流 rate_limit_group_qpm" :min="1" />
              <NumberField
                v-model="draft.group_cooldown_seconds"
                label="群冷却 group_cooldown_seconds（秒）"
                :min="0"
              />
              <NumberField
                v-model="draft.max_concurrent_llm"
                label="最大并发 max_concurrent_llm"
                :min="1"
                hint="修改后可能需要重启服务才会完全生效"
              />
              <ToggleField
                v-model="draft.reply_footer_enabled"
                label="回复末尾追加用时 / Token 估算 reply_footer_enabled"
                hint="每条 AI 回复末尾追加「—— 用时 3.2s · 约 128 tokens」"
              />
              <ToggleField
                v-model="draft.vision_enabled"
                label="让模型看图 vision_enabled"
                hint="本轮消息带图片时，把图片作为多模态输入发给模型（仅本轮，不回看历史图片）"
              />
              <NumberField
                v-model="draft.vision_max_images"
                label="单条消息最多几张图 vision_max_images"
                :min="1"
                :max="6"
                hint="图片会按 4MB 上限缓存到本地，过大或下载失败的图片只保留占位说明"
              />
            </div>
          </div>
        </div>
    <!-- 系统提示词大屏编辑：直接改同一份 draft，关闭即生效（仍需点保存提交） -->
    <ModalDialog
      :open="promptOpen"
      title="编辑系统提示词"
      subtitle="会作为 system 消息注入每次对话；这里只是放大编辑，仍需点「保存修改」才会提交"
      width="max-w-5xl"
      @close="promptOpen = false"
    >
      <textarea
        v-model="draft.system_prompt"
        class="input h-[60vh] resize-y font-mono text-sm leading-relaxed"
        placeholder="你是……"
        spellcheck="false"
      ></textarea>
      <div class="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-slate-500">
        <span class="tabular-nums">{{ draft.system_prompt.length }} 字符</span>
        <span v-if="draft.system_prompt !== loaded.system_prompt" class="text-amber-400">
          已修改，关闭后记得点「保存修改」提交
        </span>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="promptOpen = false">完成</button>
      </template>
    </ModalDialog>
  </div>
</template>
