<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { Copy, ExternalLink, MoreHorizontal, Pencil, Trash2, Eye, KeyRound } from "lucide-vue-next";
import type { Account } from "@/api/client";
import { api, statusLabel } from "@/api/client";
import { useToast } from "@/composables/useToast";
import { useTheme } from "@/composables/useTheme";
import { formatDateTime, formatDateTimeFull, toIsoDateTime } from "@/utils/datetime";

const props = defineProps<{ accounts: Account[]; loading?: boolean }>();
const emit = defineEmits<{
  (e: "open", account: Account): void;
  (e: "edit", account: Account): void;
  (e: "delete", account: Account): void;
  (e: "status-changed", account: Account): void;
}>();

/** 已选中的账号 ID（多选）。 */
const selected = defineModel<number[]>("selected", { default: () => [] });

const toast = useToast();
const { density } = useTheme();

const openMenuId = ref<number | null>(null);
const menuPos = ref({ x: 0, y: 0 });
const menuAccount = ref<Account | null>(null);
const busyId = ref<number | null>(null);

const allSelected = computed(
  () => props.accounts.length > 0 && props.accounts.every((a) => selected.value.includes(a.id)),
);

function rowHeight(): string {
  return density.value === "compact" ? "h-8" : "h-11";
}

function toggleAll(): void {
  selected.value = allSelected.value ? [] : props.accounts.map((a) => a.id);
}

function toggleOne(id: number): void {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((x) => x !== id)
    : [...selected.value, id];
}

async function copyText(text: string, label: string): Promise<void> {
  if (!text) {
    toast.info(`该账号没有${label}`);
    return;
  }
  try {
    if (!navigator.clipboard?.writeText) throw new Error("当前浏览器不支持剪贴板写入");
    await navigator.clipboard.writeText(text);
    toast.success(`已复制${label}`);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : `复制${label}失败`);
  }
}

/** 密码仍走 reveal：列表不下发密码明文。 */
async function copyPassword(account: Account): Promise<void> {
  if (busyId.value) return;
  busyId.value = account.id;
  try {
    const res = await api.post<{ value: string }>(`/api/accounts/${account.id}/reveal`, {
      field: "password",
    });
    if (!res.value) {
      toast.info("该账号没有密码");
      return;
    }
    await copyText(res.value, "密码");
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "读取密码失败");
  } finally {
    busyId.value = null;
  }
}

/** 行内直接切换状态，先改 UI 再发请求，失败回滚。 */
async function toggleStatus(account: Account): Promise<void> {
  const next = account.status === "abnormal" ? "normal" : "abnormal";
  const prev = account.status;
  account.status = next;
  try {
    const updated = await api.patch<Account>(`/api/accounts/${account.id}`, {
      revision: account.revision,
      status: next,
    });
    Object.assign(account, updated);
    emit("status-changed", account);
    toast.success(`已标记为「${statusLabel(next)}」`);
  } catch (err) {
    account.status = prev;
    toast.error(err instanceof Error ? err.message : "切换状态失败");
  }
}

/** 取码链接存的是「手机号|URL」，这里取出可点击的 URL。 */
function smsUrl(link: string): string {
  const idx = link.indexOf("http");
  return idx >= 0 ? link.slice(idx) : link;
}

function smsHost(link: string): string {
  try {
    return new URL(smsUrl(link)).host;
  } catch {
    return link.slice(0, 24);
  }
}

function truncate(v: string, n: number): string {
  if (!v) return "";
  return v.length > n ? v.slice(0, n) + "…" : v;
}

function toggleMenu(account: Account, event: MouseEvent): void {
  if (openMenuId.value === account.id) {
    openMenuId.value = null;
    return;
  }
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  menuPos.value = { x: rect.right, y: rect.bottom + 4 };
  menuAccount.value = account;
  openMenuId.value = account.id;
}

function closeMenu(): void {
  openMenuId.value = null;
}

function onDocClick(): void {
  closeMenu();
}

function runMenu(action: (a: Account) => void): void {
  const account = menuAccount.value;
  closeMenu();
  if (account) action(account);
}

onMounted(() => document.addEventListener("click", onDocClick));
onBeforeUnmount(() => document.removeEventListener("click", onDocClick));
</script>

<template>
  <div>
    <!-- 桌面：表格 -->
    <div class="hidden overflow-hidden rounded-md border border-border bg-surface shadow-sm md:block">
      <div class="overflow-x-auto">
        <table class="w-full min-w-[1310px] border-collapse">
          <thead>
            <tr class="bg-surface-2 text-left text-xs font-medium text-ink-2">
              <th class="w-9 border-b border-border px-3 py-2">
                <input
                  type="checkbox"
                  class="h-3.5 w-3.5 accent-[var(--brand)]"
                  :checked="allSelected"
                  aria-label="全选本页账号"
                  @change="toggleAll"
                />
              </th>
              <th class="w-[84px] border-b border-border px-3 py-2">状态</th>
              <th class="min-w-[220px] border-b border-border px-3 py-2">邮箱</th>
              <th class="w-[104px] border-b border-border px-3 py-2">密码</th>
              <th class="w-[232px] border-b border-border px-3 py-2">F2A</th>
              <th class="w-[168px] border-b border-border px-3 py-2">Refresh Token</th>
              <th class="w-[136px] border-b border-border px-3 py-2">取码链接</th>
              <th class="w-[132px] border-b border-border px-3 py-2">创建时间</th>
              <th class="w-[48px] border-b border-border px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="9" class="px-3 py-8 text-center text-[13px] text-ink-3">正在加载…</td>
            </tr>
            <tr v-else-if="accounts.length === 0">
              <td colspan="9" class="px-3 py-10 text-center text-[13px] text-ink-3">这里还没有账号</td>
            </tr>
            <tr
              v-for="a in accounts"
              v-else
              :key="a.id"
              :class="[
                rowHeight(),
                'transition-base border-b border-border last:border-b-0 hover:bg-surface-2',
                selected.includes(a.id) ? 'bg-brand-soft' : '',
              ]"
            >
              <td class="px-3" @click.stop>
                <input
                  type="checkbox"
                  class="h-3.5 w-3.5 accent-[var(--brand)]"
                  :checked="selected.includes(a.id)"
                  :aria-label="`选择 ${a.email}`"
                  @change="toggleOne(a.id)"
                />
              </td>

              <td class="px-3" @click.stop>
                <button
                  class="transition-base inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[12px] whitespace-nowrap hover:bg-surface"
                  :class="a.status === 'abnormal' ? 'border-danger/40 text-danger' : 'border-border text-ink-2'"
                  :title="a.status === 'abnormal' ? '点击标记为正常' : '点击标记为异常'"
                  :aria-label="`状态 ${statusLabel(a.status)}，点击切换`"
                  @click="toggleStatus(a)"
                >
                  <span
                    class="h-1.5 w-1.5 shrink-0 rounded-full"
                    :style="{ background: a.status === 'abnormal' ? 'var(--danger)' : 'var(--ok)' }"
                  />
                  {{ statusLabel(a.status) }}
                </button>
              </td>

              <td class="min-w-0 px-3" @click="emit('open', a)">
                <div class="flex items-center gap-1.5">
                  <span class="mono truncate" :title="a.email">{{ a.email }}</span>
                  <button
                    class="shrink-0 rounded-sm p-0.5 text-ink-3 hover:bg-surface hover:text-ink"
                    :aria-label="`复制 ${a.email}`"
                    @click.stop="copyText(a.email, '邮箱')"
                  >
                    <Copy :size="13" />
                  </button>
                </div>
              </td>

              <td class="px-3" @click.stop>
                <div class="flex items-center gap-1.5">
                  <span class="mono text-ink-3">{{ a.has_password ? "••••••" : "—" }}</span>
                  <button
                    class="shrink-0 rounded-sm p-0.5 text-ink-3 hover:bg-surface hover:text-ink disabled:opacity-40"
                    :disabled="!a.has_password || busyId === a.id"
                    :aria-label="a.has_password ? '复制密码' : '该账号没有密码'"
                    @click="copyPassword(a)"
                  >
                    <Copy :size="13" />
                  </button>
                </div>
              </td>

              <td class="px-3" @click.stop>
                <div class="flex items-center gap-1.5">
                  <span class="mono truncate" :title="a.f2a || ''">{{ a.f2a || "—" }}</span>
                  <button
                    class="shrink-0 rounded-sm p-0.5 text-ink-3 hover:bg-surface hover:text-ink disabled:opacity-40"
                    :disabled="!a.f2a"
                    :aria-label="a.f2a ? '复制 F2A' : '该账号没有 F2A'"
                    @click="copyText(a.f2a ?? '', 'F2A')"
                  >
                    <Copy :size="13" />
                  </button>
                </div>
              </td>

              <td class="px-3" @click.stop>
                <div class="flex items-center gap-1.5">
                  <span class="mono truncate text-ink-2" :title="a.refresh_token || ''">
                    {{ truncate(a.refresh_token ?? "", 18) || "—" }}
                  </span>
                  <button
                    class="shrink-0 rounded-sm p-0.5 text-ink-3 hover:bg-surface hover:text-ink disabled:opacity-40"
                    :disabled="!a.refresh_token"
                    :aria-label="a.refresh_token ? '复制 Refresh Token' : '该账号没有 Refresh Token'"
                    @click="copyText(a.refresh_token ?? '', 'Refresh Token')"
                  >
                    <Copy :size="13" />
                  </button>
                </div>
              </td>

              <td class="px-3" @click.stop>
                <div v-if="a.sms_link" class="flex items-center gap-1.5">
                  <a
                    :href="smsUrl(a.sms_link)"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="mono inline-flex min-w-0 items-center gap-1 text-brand hover:underline"
                    :title="a.sms_link"
                  >
                    <ExternalLink :size="12" class="shrink-0" />
                    <span class="truncate">{{ smsHost(a.sms_link) }}</span>
                  </a>
                  <button
                    class="shrink-0 rounded-sm p-0.5 text-ink-3 hover:bg-surface hover:text-ink"
                    aria-label="复制取码链接"
                    @click="copyText(a.sms_link, '取码链接')"
                  >
                    <Copy :size="13" />
                  </button>
                </div>
                <span v-else class="text-[13px] text-ink-3">—</span>
              </td>

              <!-- 创建时间：来源 accounts.created_at（Unix 秒），本地时区渲染为 YYYY-MM-DD HH:mm -->
              <td class="px-3" @click.stop>
                <time
                  class="mono text-[12.5px] whitespace-nowrap text-ink-2"
                  :datetime="toIsoDateTime(a.created_at)"
                  :title="formatDateTimeFull(a.created_at)"
                >
                  {{ formatDateTime(a.created_at) }}
                </time>
              </td>

              <td class="px-3 text-right" @click.stop>
                <button
                  class="rounded-sm p-0.5 text-ink-3 hover:bg-surface hover:text-ink"
                  aria-label="更多操作"
                  @click="toggleMenu(a, $event)"
                >
                  <MoreHorizontal :size="15" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 手机：卡片 -->
    <div class="space-y-2 md:hidden">
      <p v-if="loading" class="py-6 text-center text-[13px] text-ink-3">正在加载…</p>
      <p v-else-if="accounts.length === 0" class="py-8 text-center text-[13px] text-ink-3">
        这里还没有账号
      </p>
      <div
        v-for="a in accounts"
        v-else
        :key="a.id"
        class="rounded-md border border-border bg-surface p-3 shadow-sm"
        :class="selected.includes(a.id) ? 'ring-1 ring-brand' : ''"
      >
        <div class="flex items-start gap-2">
          <input
            type="checkbox"
            class="mt-0.5 h-4 w-4 shrink-0 accent-[var(--brand)]"
            :checked="selected.includes(a.id)"
            :aria-label="`选择 ${a.email}`"
            @change="toggleOne(a.id)"
          />
          <div class="min-w-0 flex-1">
            <button
              class="transition-base inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[12px]"
              :class="a.status === 'abnormal' ? 'border-danger/40 text-danger' : 'border-border text-ink-2'"
              @click="toggleStatus(a)"
            >
              <span
                class="h-1.5 w-1.5 rounded-full"
                :style="{ background: a.status === 'abnormal' ? 'var(--danger)' : 'var(--ok)' }"
              />
              {{ statusLabel(a.status) }}
            </button>
            <div class="mono mt-1.5 break-all" @click="emit('open', a)">{{ a.email }}</div>
          </div>
        </div>

        <dl class="mt-2 space-y-1 text-[12px]">
          <div v-if="a.f2a" class="flex items-center gap-2">
            <dt class="w-10 shrink-0 text-ink-3">F2A</dt>
            <dd class="mono min-w-0 flex-1 break-all">{{ a.f2a }}</dd>
            <button class="shrink-0 text-ink-3" aria-label="复制 F2A" @click="copyText(a.f2a ?? '', 'F2A')">
              <Copy :size="13" />
            </button>
          </div>
          <div v-if="a.refresh_token" class="flex items-center gap-2">
            <dt class="w-10 shrink-0 text-ink-3">RT</dt>
            <dd class="mono min-w-0 flex-1 truncate">{{ truncate(a.refresh_token, 24) }}</dd>
            <button
              class="shrink-0 text-ink-3"
              aria-label="复制 Refresh Token"
              @click="copyText(a.refresh_token ?? '', 'Refresh Token')"
            >
              <Copy :size="13" />
            </button>
          </div>
          <div v-if="a.sms_link" class="flex items-center gap-2">
            <dt class="w-10 shrink-0 text-ink-3">取码</dt>
            <dd class="min-w-0 flex-1 truncate">
              <a
                :href="smsUrl(a.sms_link)"
                target="_blank"
                rel="noopener noreferrer"
                class="text-brand hover:underline"
              >
                {{ smsHost(a.sms_link) }}
              </a>
            </dd>
            <button
              class="shrink-0 text-ink-3"
              aria-label="复制取码链接"
              @click="copyText(a.sms_link, '取码链接')"
            >
              <Copy :size="13" />
            </button>
          </div>
          <div class="flex items-center gap-2">
            <dt class="w-10 shrink-0 text-ink-3">创建</dt>
            <dd class="mono min-w-0 flex-1 text-ink-2">{{ formatDateTime(a.created_at) }}</dd>
          </div>
        </dl>

        <div class="mt-2.5 flex gap-2">
          <button
            class="flex h-11 flex-1 items-center justify-center gap-1 rounded-md border border-border-strong text-[13px] text-ink-2"
            @click="copyText(a.email, '邮箱')"
          >
            <Copy :size="14" /> 邮箱
          </button>
          <button
            class="flex h-11 flex-1 items-center justify-center gap-1 rounded-md border border-border-strong text-[13px] text-ink-2 disabled:opacity-40"
            :disabled="!a.has_password"
            @click="copyPassword(a)"
          >
            <KeyRound :size="14" /> 密码
          </button>
          <button
            class="flex h-11 flex-1 items-center justify-center gap-1 rounded-md border border-border-strong text-[13px] text-ink-2"
            @click="emit('open', a)"
          >
            <Eye :size="14" /> 详情
          </button>
        </div>
      </div>
    </div>

    <!-- 行操作菜单 -->
    <Teleport to="body">
      <div
        v-if="openMenuId !== null"
        class="fixed z-[220] w-[184px] overflow-hidden rounded-md border border-border bg-surface py-1 shadow-lg"
        :style="{ left: `${Math.max(8, menuPos.x - 184)}px`, top: `${menuPos.y}px` }"
        @click.stop
      >
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2"
          @click="runMenu((a) => emit('open', a))"
        >
          <Eye :size="14" /> 查看详情
        </button>
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2"
          @click="runMenu((a) => emit('edit', a))"
        >
          <Pencil :size="14" /> 编辑
        </button>
        <div class="my-1 border-t border-border" />
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2"
          @click="runMenu((a) => copyText(a.email, '邮箱'))"
        >
          <Copy :size="14" /> 复制邮箱
        </button>
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2 disabled:opacity-40"
          :disabled="!menuAccount?.has_password"
          @click="runMenu(copyPassword)"
        >
          <Copy :size="14" /> 复制密码
        </button>
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2 disabled:opacity-40"
          :disabled="!menuAccount?.f2a"
          @click="runMenu((a) => copyText(a.f2a ?? '', 'F2A'))"
        >
          <Copy :size="14" /> 复制 F2A
        </button>
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2 disabled:opacity-40"
          :disabled="!menuAccount?.refresh_token"
          @click="runMenu((a) => copyText(a.refresh_token ?? '', 'Refresh Token'))"
        >
          <Copy :size="14" /> 复制 Refresh Token
        </button>
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] hover:bg-surface-2 disabled:opacity-40"
          :disabled="!menuAccount?.sms_link"
          @click="runMenu((a) => copyText(a.sms_link ?? '', '取码链接'))"
        >
          <Copy :size="14" /> 复制取码链接
        </button>
        <div class="my-1 border-t border-border" />
        <button
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-[13px] text-danger hover:bg-surface-2"
          @click="runMenu((a) => emit('delete', a))"
        >
          <Trash2 :size="14" /> 删除
        </button>
      </div>
    </Teleport>
  </div>
</template>
