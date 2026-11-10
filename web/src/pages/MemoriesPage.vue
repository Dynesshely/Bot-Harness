<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import { memoriesApi, toMessage } from '@/api'
import type { MemoryItem } from '@/api/types'
import { toast } from '@/composables/useToast'
import { formatDateTime } from '@/utils/format'

const SCOPES: { value: string; label: string }[] = [
  { value: '', label: '全部作用域' },
  { value: 'global', label: '全局（管理员）' },
  { value: 'group', label: '群记忆' },
  { value: 'member', label: '个人记忆' },
]

/** 写入表单的作用域：必须给出明确作用域，所以没有「全部」这一项。 */
const DRAFT_SCOPES: { value: string; label: string }[] = [
  { value: 'group', label: '群记忆' },
  { value: 'member', label: '个人记忆' },
  { value: 'global', label: '全局' },
]

const scope = ref('')
const items = ref<MemoryItem[]>([])
const loading = ref(false)
const error = ref('')
const draftScope = ref('group')
const draftGroup = ref('')
const draftMember = ref('')
const draftText = ref('')
const saving = ref(false)

const canSave = computed(() => draftText.value.trim() !== '' && (draftScope.value !== 'group' || draftGroup.value.trim() !== '')
  && (draftScope.value !== 'member' || draftMember.value.trim() !== ''))

async function load() {
  loading.value = true
  error.value = ''
  try {
    items.value = await memoriesApi.list({ scope: scope.value || undefined, limit: 200 })
  } catch (e) {
    error.value = toMessage(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function add() {
  if (!canSave.value) return
  saving.value = true
  try {
    await memoriesApi.add({
      scope: draftScope.value,
      group_openid: draftScope.value === 'group' ? draftGroup.value.trim() : undefined,
      member_openid: draftScope.value === 'member' ? draftMember.value.trim() : undefined,
      text: draftText.value.trim(),
    })
    draftText.value = ''
    toast.success('已写入记忆')
    await load()
  } catch (e) {
    toast.error(toMessage(e))
  } finally {
    saving.value = false
  }
}

async function remove(item: MemoryItem) {
  try {
    await memoriesApi.remove(item.id)
    items.value = items.value.filter((m) => m.id !== item.id)
    toast.success('已删除')
  } catch (e) {
    toast.error(toMessage(e))
  }
}

function scopeBadge(s: string) {
  if (s === 'global') return 'border-violet-500/40 bg-violet-500/10 text-violet-300'
  if (s === 'group') return 'border-sky-500/40 bg-sky-500/10 text-sky-300'
  return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
}

function scopeLabel(s: string) {
  return s === 'global' ? '全局' : s === 'group' ? '群' : '个人'
}

function owner(item: MemoryItem) {
  if (item.scope === 'global') return '—'
  return item.group_openid || item.member_openid
}
</script>

<template>
  <div class="space-y-4">
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="database" :size="16" class="text-emerald-400" />
          长期记忆
        </h2>
        <div class="flex items-center gap-2">
          <div class="w-40">
            <SelectMenu
              v-model="scope"
              size="sm"
              aria-label="按作用域筛选"
              placeholder="全部作用域"
              :options="SCOPES"
              @change="load"
            />
          </div>
          <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
            <AppIcon name="refresh" :size="14" :class="loading ? 'animate-spin' : ''" />
            刷新
          </button>
        </div>
      </div>

      <div class="card-body space-y-4">
        <p class="text-xs text-slate-500">
          群记忆只在该群可见，个人记忆只在该成员私聊可见，全局记忆所有会话可见（只有管理员能写）。
          模型通过 remember / recall 工具读写，也可以在下面手工维护。
        </p>

        <div class="rounded-xl border border-slate-800 bg-slate-950/40 p-4">
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-4">
            <SelectMenu v-model="draftScope" size="sm" aria-label="写入作用域" :options="DRAFT_SCOPES" />
            <input
              v-if="draftScope === 'group'"
              v-model="draftGroup"
              class="input mono"
              placeholder="group_openid"
            />
            <input
              v-else-if="draftScope === 'member'"
              v-model="draftMember"
              class="input mono"
              placeholder="member_openid"
            />
            <div v-else class="flex items-center text-xs text-slate-500">所有会话可见</div>
            <input v-model="draftText" class="input sm:col-span-2" placeholder="要记住的一句话事实" />
            <button type="button" class="btn btn-primary" :disabled="!canSave || saving" @click="add">
              {{ saving ? '写入中…' : '写入记忆' }}
            </button>
          </div>
        </div>

        <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

        <EmptyState
          v-else-if="!items.length && !loading"
          title="还没有记忆"
          description="模型调用 remember 时会写到这里；你也可以在上面手工添加一条。"
        />

        <div v-else class="table-wrap scroll-y sticky-head">
          <table class="table-base">
            <thead>
              <tr>
                <th>作用域</th>
                <th>归属</th>
                <th>内容</th>
                <th>权重</th>
                <th>引用</th>
                <th>写入时间</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in items" :key="item.id">
                <td>
                  <span class="badge" :class="scopeBadge(item.scope)">{{ scopeLabel(item.scope) }}</span>
                </td>
                <td class="mono max-w-[14rem] truncate text-xs text-slate-500">{{ owner(item) }}</td>
                <td class="max-w-[26rem] whitespace-normal break-words">{{ item.text }}</td>
                <td class="tabular-nums text-slate-400">{{ item.salience.toFixed(1) }}</td>
                <td class="tabular-nums text-slate-400">{{ item.hits }}</td>
                <td class="whitespace-nowrap text-slate-400">{{ formatDateTime(item.created_at) }}</td>
                <td>
                  <button type="button" class="btn btn-ghost btn-sm text-rose-400" @click="remove(item)">
                    <AppIcon name="trash" :size="14" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>
  </div>
</template>
