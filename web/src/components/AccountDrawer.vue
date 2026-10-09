<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  Check,
  Copy,
  Eye,
  EyeOff,
  Loader2,
  Pencil,
  Trash2,
  X,
} from "lucide-vue-next";
import {
  api,
  ApiError,
  ACCOUNT_STATUS,
  statusLabel,
  type Account,
  type SecretField,
  type Tag,
} from "@/api/client";
import { useToast } from "@/composables/useToast";
import { useSecrets, DETAIL_SECRET_TTL_MS } from "@/composables/useSecrets";
import { formatDateTime, toIsoDateTime } from "@/utils/datetime";
import ConfirmDialog from "@/components/ConfirmDialog.vue";

const props = defineProps<{
  open: boolean;
  account: Account | null;
  tags: Tag[];
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "saved", account: Account): void;
  (e: "deleted", account: Account): void;
}>();

const toast = useToast();
const secrets = useSecrets();

const mode = ref<"view" | "edit">("view");
const saving = ref(false);
const inlineError = ref("");
const panelRef = ref<HTMLElement | null>(null);
const confirmDelete = ref(false);
const confirmDiscard = ref(false);

/** 编辑表单。秘密字段为 undefined 表示「不修改」，null 表示「清空」。 */
const form = ref({
  email: "",
  username: "",
  password: undefined as string | null | undefined,
  backupEmail: undefined as string | null | undefined,
  f2a: undefined as string | null | undefined,
  credentialJson: undefined as string | null | undefined,
  refreshToken: undefined as string | null | undefined,
  smsLink: undefined as string | null | undefined,
  notes: undefined as string | null | undefined,
  tagIds: [] as number[],
});

const dirty = computed(() => {
  const f = form.value;
  return (
    f.email !== (props.account?.email ?? "") ||
    f.username !== (props.account?.username ?? "") ||
    f.password !== undefined ||
    f.backupEmail !== undefined ||
    f.f2a !== undefined ||
    f.credentialJson !== undefined ||
    f.refreshToken !== undefined ||
    f.smsLink !== undefined ||
    f.notes !== undefined ||
    JSON.stringify([...f.tagIds].sort()) !==
      JSON.stringify([...(props.account?.tag_ids ?? [])].sort())
  );
});

watch(
  () => props.account?.id,
  (id) => {
    if (id === undefined) return;
    secrets.clearAll();
    mode.value = "view";
    inlineError.value = "";
  },
);

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      secrets.clearAll();
      mode.value = "view";
      inlineError.value = "";
      return;
    }
    await nextTick();
    panelRef.value?.focus();
    // 打开即明文：列表已带回的辅助字段直接落位，其余字段主动取密。
    await hydrateSecrets();
  },
);

/** 把该账号的全部可读字段落到已揭示槽位里。 */
async function hydrateSecrets(): Promise<void> {
  const a = props.account;
  if (!a) return;

  // 这三项列表接口已经带回明文，不必再请求。
  if (a.f2a) secrets.set(a.id, "f2a", a.f2a, DETAIL_SECRET_TTL_MS);
  if (a.refresh_token) secrets.set(a.id, "refresh_token", a.refresh_token, DETAIL_SECRET_TTL_MS);
  if (a.sms_link) secrets.set(a.id, "sms_link", a.sms_link, DETAIL_SECRET_TTL_MS);

  // 密码、辅助邮箱、JSON 凭证、备注仍逐字段取密（一次一个字段，各自写审计）。
  const rest: SecretField[] = [];
  if (a.has_password) rest.push("password");
  if (a.has_backup_email) rest.push("backup_email");
  if (a.has_json) rest.push("credential_json");
  if (a.has_notes) rest.push("notes");

  await Promise.all(
    rest.map(async (field) => {
      try {
        const res = await api.post<{ value: string }>(`/api/accounts/${a.id}/reveal`, { field });
        secrets.set(a.id, field, res.value, DETAIL_SECRET_TTL_MS);
      } catch {
        // 单个字段失败不影响其他字段的显示
      }
    }),
  );
}

/** 读取单个字段：能用列表带回的明文就不发请求。 */
async function loadField(a: Account, field: SecretField): Promise<void> {
  if (field === "f2a" && a.f2a) {
    secrets.set(a.id, field, a.f2a, DETAIL_SECRET_TTL_MS);
    return;
  }
  if (field === "refresh_token" && a.refresh_token) {
    secrets.set(a.id, field, a.refresh_token, DETAIL_SECRET_TTL_MS);
    return;
  }
  if (field === "sms_link" && a.sms_link) {
    secrets.set(a.id, field, a.sms_link, DETAIL_SECRET_TTL_MS);
    return;
  }
  const res = await api.post<{ value: string }>(`/api/accounts/${a.id}/reveal`, { field });
  secrets.set(a.id, field, res.value, DETAIL_SECRET_TTL_MS);
}

function startEdit(): void {
  const a = props.account;
  if (!a) return;
  form.value = {
    email: a.email,
    username: a.username,
    password: undefined,
    backupEmail: undefined,
    f2a: undefined,
    credentialJson: undefined,
    refreshToken: undefined,
    smsLink: undefined,
    notes: undefined,
    tagIds: [...a.tag_ids],
  };
  inlineError.value = "";
  mode.value = "edit";
}

function requestClose(): void {
  if (dirty.value && mode.value === "edit") {
    confirmDiscard.value = true;
    return;
  }
  emit("close");
}

async function reveal(field: SecretField): Promise<void> {
  const a = props.account;
  if (!a) return;
  if (secrets.get(a.id, field) !== undefined) {
    secrets.clear(a.id, field);
    return;
  }
  try {
    await loadField(a, field);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "读取失败");
  }
}

async function copy(text: string, label: string): Promise<void> {
  try {
    if (!navigator.clipboard?.writeText) throw new Error("当前浏览器不支持剪贴板写入");
    await navigator.clipboard.writeText(text);
    toast.success(`已复制${label}`);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "复制失败");
  }
}

async function copySecret(field: SecretField, label: string): Promise<void> {
  const a = props.account;
  if (!a) return;
  try {
    const res = await api.post<{ value: string }>(`/api/accounts/${a.id}/reveal`, { field });
    if (!res.value) {
      toast.info(`该账号没有${label}`);
      return;
    }
    await copy(res.value, label);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "复制失败");
  }
}

/** 复制全部：逐个取已存在字段，全部成功才写剪贴板。 */
async function copyAll(): Promise<void> {
  const a = props.account;
  if (!a) return;
  const parts: string[] = [`邮箱：${a.email}`];
  const plan: { field: SecretField; present: boolean; label: string }[] = [
    { field: "password", present: a.has_password, label: "密码" },
    { field: "backup_email", present: a.has_backup_email, label: "辅助邮箱" },
    { field: "f2a", present: a.has_f2a, label: "F2A" },
    { field: "credential_json", present: a.has_json, label: "JSON" },
    { field: "refresh_token", present: a.has_refresh_token, label: "Refresh Token" },
    { field: "sms_link", present: a.has_sms_link, label: "取码链接" },
  ];
  try {
    for (const item of plan) {
      if (!item.present) continue;
      const res = await api.post<{ value: string }>(`/api/accounts/${a.id}/reveal`, {
        field: item.field,
      });
      if (res.value) parts.push(`${item.label}：${res.value}`);
    }
    await copy(parts.join("\n"), "全部");
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "复制失败");
  }
}

async function save(): Promise<void> {
  const a = props.account;
  if (!a || saving.value) return;
  saving.value = true;
  inlineError.value = "";
  try {
    const f = form.value;
    const body: Record<string, unknown> = {
      revision: a.revision,
      email: f.email,
      username: f.username,
      tag_ids: f.tagIds,
    };
    // 只有用户确实动过的秘密字段才发出去，避免把掩码写回。
    if (f.password !== undefined) body.password = f.password;
    if (f.backupEmail !== undefined) body.backup_email = f.backupEmail;
    if (f.f2a !== undefined) body.f2a = f.f2a;
    if (f.credentialJson !== undefined) body.credential_json = f.credentialJson;
    if (f.refreshToken !== undefined) body.refresh_token = f.refreshToken;
    if (f.smsLink !== undefined) body.sms_link = f.smsLink;
    if (f.notes !== undefined) body.notes = f.notes;

    const updated = await api.patch<Account>(`/api/accounts/${a.id}`, body);
    toast.success("已保存");
    emit("saved", updated);
    mode.value = "view";
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      // 版本冲突：保留草稿，提示刷新
      inlineError.value = "该账号已被其他操作修改，请关闭后重新打开再编辑";
    } else {
      inlineError.value = err instanceof Error ? err.message : "保存失败";
    }
  } finally {
    saving.value = false;
  }
}

/** 切换账号状态。走 PATCH + revision 乐观锁。 */
async function setStatus(next: string): Promise<void> {
  const a = props.account;
  if (!a || a.status === next) return;
  try {
    const updated = await api.patch<Account>(`/api/accounts/${a.id}`, {
      revision: a.revision,
      status: next,
    });
    toast.success(`状态已改为「${statusLabel(next)}」`);
    emit("saved", updated);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "切换状态失败");
  }
}

async function doDelete(): Promise<void> {
  const a = props.account;
  if (!a) return;
  try {
    await api.del(`/api/accounts/${a.id}`);
    toast.success("账号已删除");
    confirmDelete.value = false;
    emit("deleted", a);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "删除失败");
    confirmDelete.value = false;
  }
}

function toggleTag(id: number): void {
  const idx = form.value.tagIds.indexOf(id);
  if (idx >= 0) form.value.tagIds.splice(idx, 1);
  else form.value.tagIds.push(id);
}

function maskFor(field: SecretField): string {
  const a = props.account;
  if (!a) return "";
  const value = secrets.get(a.id, field);
  if (value === undefined) return "••••••••";
  return value || "（空）";
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open && account" class="fixed inset-0 z-[200]">
      <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="requestClose" />
      <aside
        ref="panelRef"
        role="dialog"
        aria-modal="true"
        aria-label="账号详情"
        tabindex="-1"
        class="absolute inset-y-0 right-0 flex w-[min(480px,100vw)] flex-col border-l border-border bg-surface shadow-lg focus:outline-none"
        @keydown.esc.stop="requestClose"
      >
        <header class="flex items-center gap-2 border-b border-border px-5 py-4">
          <h2 class="flex-1 text-[15px] font-semibold">
            {{ mode === "edit" ? "编辑账号" : "账号详情" }}
          </h2>
          <button
            class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
            aria-label="关闭"
            @click="requestClose"
          >
            <X :size="16" />
          </button>
        </header>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5">
          <template v-if="mode === 'view'">
            <div class="mb-5">
              <p class="mb-1.5 text-[11.5px] font-semibold tracking-wide text-ink-3 uppercase">邮箱</p>
              <div class="flex items-center gap-2">
                <div class="mono min-w-0 flex-1 rounded-sm border border-border bg-surface-2 px-2.5 py-1.5 break-all">
                  {{ account.email }}
                </div>
                <button
                  class="grid h-8 w-8 shrink-0 place-items-center rounded-sm border border-border-strong text-ink-2 hover:bg-surface-2"
                  aria-label="复制邮箱"
                  @click="copy(account.email, '邮箱')"
                >
                  <Copy :size="14" />
                </button>
              </div>
            </div>

            <div v-if="account.username" class="mb-5">
              <p class="mb-1.5 text-[11.5px] font-semibold tracking-wide text-ink-3 uppercase">用户名</p>
              <div class="flex items-center gap-2">
                <div class="mono min-w-0 flex-1 rounded-sm border border-border bg-surface-2 px-2.5 py-1.5 break-all">
                  {{ account.username }}
                </div>
                <button
                  class="grid h-8 w-8 shrink-0 place-items-center rounded-sm border border-border-strong text-ink-2 hover:bg-surface-2"
                  aria-label="复制用户名"
                  @click="copy(account.username, '用户名')"
                >
                  <Copy :size="14" />
                </button>
              </div>
            </div>

            <div
              v-for="item in [
                { field: 'password' as SecretField, label: '密码', present: account.has_password },
                { field: 'backup_email' as SecretField, label: '辅助邮箱', present: account.has_backup_email },
                { field: 'f2a' as SecretField, label: 'F2A', present: account.has_f2a },
                { field: 'credential_json' as SecretField, label: 'JSON 凭证', present: account.has_json },
                { field: 'refresh_token' as SecretField, label: 'Refresh Token', present: account.has_refresh_token },
                { field: 'sms_link' as SecretField, label: '取码链接', present: account.has_sms_link },
                { field: 'notes' as SecretField, label: '备注', present: account.has_notes },
              ]"
              :key="item.field"
              class="mb-5"
            >
              <p class="mb-1.5 text-[11.5px] font-semibold tracking-wide text-ink-3 uppercase">
                {{ item.label }}
              </p>
              <div class="flex items-center gap-2">
                <div class="mono min-w-0 flex-1 rounded-sm border border-border bg-surface-2 px-2.5 py-1.5 break-all">
                  {{ item.present ? maskFor(item.field) : "—" }}
                </div>
                <button
                  v-if="item.present"
                  class="grid h-8 w-8 shrink-0 place-items-center rounded-sm border border-border-strong text-ink-2 hover:bg-surface-2"
                  :aria-label="`显示或隐藏${item.label}`"
                  @click="reveal(item.field)"
                >
                  <EyeOff v-if="secrets.get(account.id, item.field) !== undefined" :size="14" />
                  <Eye v-else :size="14" />
                </button>
                <button
                  v-if="item.present"
                  class="grid h-8 w-8 shrink-0 place-items-center rounded-sm border border-border-strong text-ink-2 hover:bg-surface-2"
                  :aria-label="`复制${item.label}`"
                  @click="copySecret(item.field, item.label)"
                >
                  <Copy :size="14" />
                </button>
              </div>
            </div>

            <div class="mb-5">
              <p class="mb-1.5 text-[11.5px] font-semibold tracking-wide text-ink-3 uppercase">状态</p>
              <div class="flex flex-wrap gap-1.5">
                <button
                  v-for="(label, key) in ACCOUNT_STATUS"
                  :key="key"
                  class="transition-base rounded-full border px-3 py-1 text-xs"
                  :class="
                    account.status === key
                      ? key === 'normal'
                        ? 'border-ok bg-ok/10 font-medium text-ok'
                        : 'border-danger bg-danger/10 font-medium text-danger'
                      : 'border-border text-ink-2 hover:bg-surface-2'
                  "
                  :aria-pressed="account.status === key"
                  @click="setStatus(String(key))"
                >
                  {{ label }}
                </button>
              </div>
            </div>

            <div class="mb-5">
              <p class="mb-1.5 text-[11.5px] font-semibold tracking-wide text-ink-3 uppercase">标签</p>
              <div class="flex flex-wrap gap-1.5">
                <span
                  v-for="t in account.tags"
                  :key="t.id"
                  class="rounded-full border border-border bg-surface-2 px-2 py-0.5 text-xs text-ink-2"
                >
                  {{ t.name }}
                </span>
                <span v-if="account.tags.length === 0" class="text-[13px] text-ink-3">无</span>
              </div>
            </div>

            <div class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12.5px] text-ink-2">
              <span class="text-ink-3">创建时间</span>
              <time class="mono" :datetime="toIsoDateTime(account.created_at)">
                {{ formatDateTime(account.created_at) }}
              </time>
              <span class="text-ink-3">更新时间</span>
              <time class="mono" :datetime="toIsoDateTime(account.updated_at)">
                {{ formatDateTime(account.updated_at) }}
              </time>
            </div>
          </template>

          <template v-else>
            <div class="space-y-4">
              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink-2">邮箱</label>
                <input v-model="form.email" class="h-8 w-full rounded-md border border-border-strong bg-surface px-2 text-[13px] focus:border-brand focus:outline-none" />
              </div>
              <div>
                <label class="mb-1.5 block text-xs font-medium text-ink-2">用户名</label>
                <input v-model="form.username" class="h-8 w-full rounded-md border border-border-strong bg-surface px-2 text-[13px] focus:border-brand focus:outline-none" />
              </div>

              <div
                v-for="item in [
                  { key: 'password', label: '密码' },
                  { key: 'backupEmail', label: '辅助邮箱' },
                  { key: 'f2a', label: 'F2A' },
                  { key: 'credentialJson', label: 'JSON 凭证' },
                  { key: 'refreshToken', label: 'Refresh Token' },
                  { key: 'smsLink', label: '取码链接' },
                  { key: 'notes', label: '备注' },
                ]"
                :key="item.key"
              >
                <label class="mb-1.5 flex items-center justify-between text-xs font-medium text-ink-2">
                  <span>{{ item.label }}</span>
                  <span v-if="form[item.key as 'password'] === null" class="text-danger">将清空</span>
                  <span v-else-if="form[item.key as 'password'] === undefined" class="text-ink-3">未修改</span>
                </label>
                <div class="flex gap-2">
                  <input
                    :value="form[item.key as 'password'] ?? ''"
                    class="mono h-8 min-w-0 flex-1 rounded-md border border-border-strong bg-surface px-2 text-[12.5px] focus:border-brand focus:outline-none"
                    :placeholder="form[item.key as 'password'] === undefined ? '保持原值不变' : ''"
                    @input="form[item.key as 'password'] = ($event.target as HTMLInputElement).value"
                  />
                  <button
                    class="h-8 shrink-0 rounded-md border border-border-strong px-2 text-xs text-ink-2 hover:bg-surface-2"
                    @click="form[item.key as 'password'] = null"
                  >
                    清空
                  </button>
                </div>
              </div>

              <div>
                <p class="mb-1.5 text-xs font-medium text-ink-2">标签</p>
                <div class="flex flex-wrap gap-1.5">
                  <button
                    v-for="t in tags"
                    :key="t.id"
                    class="rounded-full border px-2 py-0.5 text-xs transition-base"
                    :class="
                      form.tagIds.includes(t.id)
                        ? 'border-brand bg-brand-soft text-brand'
                        : 'border-border text-ink-2 hover:bg-surface-2'
                    "
                    @click="toggleTag(t.id)"
                  >
                    <Check v-if="form.tagIds.includes(t.id)" :size="11" class="mr-0.5 inline" />
                    {{ t.name }}
                  </button>
                  <span v-if="tags.length === 0" class="text-[13px] text-ink-3">该项目还没有标签</span>
                </div>
              </div>
            </div>

            <p v-if="inlineError" role="alert" class="mt-4 text-[13px] text-danger">
              {{ inlineError }}
            </p>
          </template>
        </div>

        <footer class="flex items-center gap-2 border-t border-border px-5 py-3.5">
          <template v-if="mode === 'view'">
            <button
              class="flex h-8 items-center gap-1.5 rounded-md border border-border-strong px-3 text-[13px] text-danger hover:bg-surface-2"
              @click="confirmDelete = true"
            >
              <Trash2 :size="14" /> 删除
            </button>
            <button
              class="ml-auto flex h-8 items-center gap-1.5 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
              @click="copyAll"
            >
              <Copy :size="14" /> 复制全部
            </button>
            <button
              class="flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover"
              @click="startEdit"
            >
              <Pencil :size="14" /> 编辑
            </button>
          </template>
          <template v-else>
            <button
              class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
              @click="requestClose"
            >
              取消
            </button>
            <button
              class="ml-auto flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
              :disabled="saving"
              @click="save"
            >
              <Loader2 v-if="saving" :size="14" class="animate-spin" />
              保存
            </button>
          </template>
        </footer>
      </aside>
    </div>

    <ConfirmDialog
      :open="confirmDelete"
      title="删除这个账号？"
      message="删除后账号进入回收状态，可在设置页恢复。"
      confirm-text="确认删除"
      danger
      @cancel="confirmDelete = false"
      @confirm="doDelete"
    />
    <ConfirmDialog
      :open="confirmDiscard"
      title="放弃未保存的修改？"
      message="当前编辑内容尚未保存，关闭后将丢失。"
      confirm-text="放弃修改"
      danger
      @cancel="confirmDiscard = false"
      @confirm="confirmDiscard = false; emit('close')"
    />
  </Teleport>
</template>
