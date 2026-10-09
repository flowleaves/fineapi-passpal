<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { ArrowLeft, Download, HardDriveDownload, Loader2, RotateCcw, Trash2 } from "lucide-vue-next";
import { api, ApiError, type BackupInfo, type Project, type Account, type Tag } from "@/api/client";
import { useAuth } from "@/composables/useAuth";
import { useTheme } from "@/composables/useTheme";
import { useToast } from "@/composables/useToast";
import { formatDateTime } from "@/utils/datetime";
import ConfirmDialog from "@/components/ConfirmDialog.vue";

const router = useRouter();
const { logout } = useAuth();
const { theme, density, setTheme, setDensity } = useTheme();
const toast = useToast();

const settings = ref<Record<string, string>>({});
const keyInfo = ref<{ current_key_version: number; available_versions: number[] } | null>(null);
const backups = ref<BackupInfo[]>([]);
const busy = ref("");
const reloading = ref(false);

const trashProjects = ref<Project[]>([]);
const trashTags = ref<Tag[]>([]);
const trashAccounts = ref<Account[]>([]);

const passwordPrompt = ref<{ open: boolean; purpose: "backup" | "export"; name?: string }>({
  open: false,
  purpose: "backup",
});
const adminPassword = ref("");
const exportScope = ref("all");
const exportFormat = ref("json");

const logoutConfirm = ref(false);

const ttlHours = ref(12);
const maxAttempts = ref(5);
const lockMinutes = ref(15);
const retainDays = ref(7);

async function loadAll(): Promise<void> {
  reloading.value = true;
  try {
    const res = await api.get<{
      settings: Record<string, string>;
      encryption: { current_key_version: number; available_versions: number[] };
    }>("/api/settings");
    settings.value = res.settings;
    keyInfo.value = res.encryption;
    ttlHours.value = Number(res.settings.session_ttl_hours ?? 12);
    maxAttempts.value = Number(res.settings.login_max_attempts ?? 5);
    lockMinutes.value = Number(res.settings.login_lock_minutes ?? 15);
    retainDays.value = Number(res.settings.backup_retain_days ?? 7);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "加载设置失败");
  } finally {
    reloading.value = false;
  }
}

async function loadBackups(): Promise<void> {
  try {
    const res = await api.get<{ items: BackupInfo[] }>("/api/backup/list");
    backups.value = res.items;
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "加载备份清单失败");
  }
}

async function loadTrash(): Promise<void> {
  try {
    const res = await api.get<{
      projects: Project[];
      tags: Tag[];
      accounts: { items: Account[] };
    }>("/api/trash");
    trashProjects.value = res.projects ?? [];
    trashTags.value = res.tags ?? [];
    trashAccounts.value = res.accounts?.items ?? [];
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "加载回收站失败");
  }
}

async function runBackup(): Promise<void> {
  if (busy.value) return;
  busy.value = "backup";
  try {
    const info = await api.post<BackupInfo>("/api/backup");
    toast.success(`备份完成：${info.name}`);
    await loadBackups();
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "备份失败");
  } finally {
    busy.value = "";
  }
}

function askPassword(purpose: "backup" | "export", name?: string): void {
  adminPassword.value = "";
  passwordPrompt.value = { open: true, purpose, name };
}

async function submitPassword(): Promise<void> {
  const prompt = passwordPrompt.value;
  if (!adminPassword.value) return;
  busy.value = prompt.purpose;
  try {
    if (prompt.purpose === "backup") {
      await downloadBackup(prompt.name!);
    } else {
      await downloadExport();
    }
    passwordPrompt.value = { open: false, purpose: "backup" };
    adminPassword.value = "";
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      toast.error("管理员密码不正确");
    } else {
      toast.error(err instanceof Error ? err.message : "操作失败");
    }
  } finally {
    busy.value = "";
  }
}

async function downloadBackup(name: string): Promise<void> {
  const res = await fetch(`/api/backup/${encodeURIComponent(name)}/download`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ admin_password: adminPassword.value }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.code ?? "error", body?.error?.message ?? "下载失败");
  }
  await saveBlob(await res.blob(), name);
}

async function downloadExport(): Promise<void> {
  const res = await fetch("/api/export", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({
      scope: exportScope.value,
      format: exportFormat.value,
      admin_password: adminPassword.value,
    }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new ApiError(res.status, body?.error?.code ?? "error", body?.error?.message ?? "导出失败");
  }
  const disposition = res.headers.get("Content-Disposition") ?? "";
  const match = /filename="([^"]+)"/.exec(disposition);
  await saveBlob(await res.blob(), match?.[1] ?? "passpal-export");
  toast.success("导出完成");
}

async function saveBlob(blob: Blob, filename: string): Promise<void> {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

async function restoreItem(kind: "projects" | "tags" | "accounts", id: number): Promise<void> {
  try {
    await api.post(`/api/${kind}/${id}/restore`);
    toast.success("已恢复");
    await loadTrash();
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "恢复失败");
  }
}

async function savePreferences(): Promise<void> {
  busy.value = "prefs";
  try {
    await api.patch("/api/settings", {
      session_ttl_hours: ttlHours.value,
      login_max_attempts: maxAttempts.value,
      login_lock_minutes: lockMinutes.value,
      backup_retain_days: retainDays.value,
    });
    toast.success("设置已保存");
    await loadAll();
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "保存失败");
  } finally {
    busy.value = "";
  }
}

function fmtSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

async function doLogout(): Promise<void> {
  await logout();
  await router.replace({ name: "login" });
}

const trashCount = computed(
  () => trashProjects.value.length + trashTags.value.length + trashAccounts.value.length,
);

onMounted(async () => {
  await loadAll();
  await Promise.all([loadBackups(), loadTrash()]);
});
</script>

<template>
  <div class="min-h-dvh">
    <header class="sticky top-0 z-30 flex h-12 items-center gap-3 border-b border-border bg-surface px-3 sm:px-4">
      <router-link
        to="/app"
        class="flex h-8 items-center gap-1.5 rounded-sm px-2 text-[13px] text-ink-2 hover:bg-surface-2"
      >
        <ArrowLeft :size="15" /> 返回工作台
      </router-link>
      <h1 class="text-[15px] font-semibold">设置</h1>
      <Loader2 v-if="reloading" :size="15" class="animate-spin text-ink-3" />
    </header>

    <div class="mx-auto w-full max-w-[820px] space-y-6 px-4 py-6 sm:px-6">
      <!-- 备份 -->
      <section class="rounded-lg border border-border bg-surface p-5 shadow-sm">
        <div class="flex items-center gap-2">
          <HardDriveDownload :size="16" class="text-ink-2" />
          <h2 class="text-[15px] font-semibold">备份</h2>
        </div>
        <p class="mt-1.5 text-[13px] text-ink-2">
          用 SQLite 在线备份（VACUUM INTO）生成一致性快照，再连同<strong class="font-medium">数据加密密钥</strong>一起打包成
          <span class="mono">.tar.gz</span> 归档 —— 恢复时只需要这一个文件，不必再找密钥。
          代价是归档本身等同于明文凭据，请放在受控的地方。
        </p>
        <div class="mt-3 flex flex-wrap gap-2">
          <button
            class="flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
            :disabled="busy === 'backup'"
            @click="runBackup"
          >
            <Loader2 v-if="busy === 'backup'" :size="14" class="animate-spin" />
            立即备份
          </button>
          <button
            class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
            @click="loadBackups"
          >
            刷新清单
          </button>
        </div>

        <ul v-if="backups.length" class="mt-4 divide-y divide-border rounded-md border border-border">
          <li v-for="b in backups" :key="b.name" class="flex items-center gap-2 px-3 py-2">
            <div class="min-w-0 flex-1">
              <div class="mono truncate">{{ b.name }}</div>
              <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-ink-3">
                <span>{{ formatDateTime(b.created_at) }}</span>
                <span>·</span>
                <span>{{ fmtSize(b.size) }}</span>
                <span>·</span>
                <span v-if="b.self_contained" class="text-brand" title="内含密钥，恢复时不需要额外提供密钥">
                  自包含可恢复
                </span>
                <span v-else class="text-danger" title="旧版快照，敏感字段仍是密文；恢复时必须另外提供当时的数据加密密钥">
                  旧格式 · 恢复需外部密钥
                </span>
              </div>
            </div>
            <button
              class="flex h-7 items-center gap-1 rounded-sm border border-border-strong px-2 text-xs text-ink-2 hover:bg-surface-2"
              @click="askPassword('backup', b.name)"
            >
              <Download :size="12" /> 下载
            </button>
          </li>
        </ul>
        <p v-else class="mt-3 text-[13px] text-ink-3">还没有备份文件。</p>
      </section>

      <!-- 导出 -->
      <section class="rounded-lg border border-border bg-surface p-5 shadow-sm">
        <h2 class="text-[15px] font-semibold">导出</h2>
        <p class="mt-1.5 text-[13px] text-ink-2">
          导出是高风险操作，需要重新输入管理员密码。JSON 无损，CSV 会转义公式前缀。
        </p>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <select
            v-model="exportScope"
            class="h-8 rounded-md border border-border-strong bg-surface px-2 text-[13px]"
            aria-label="导出范围"
          >
            <option value="all">全部账号</option>
            <option value="project">当前项目</option>
            <option value="tag">当前标签</option>
          </select>
          <select
            v-model="exportFormat"
            class="h-8 rounded-md border border-border-strong bg-surface px-2 text-[13px]"
            aria-label="导出格式"
          >
            <option value="json">JSON</option>
            <option value="csv">CSV</option>
          </select>
          <button
            class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
            @click="askPassword('export')"
          >
            导出并下载
          </button>
        </div>
      </section>

      <!-- 密钥状态 -->
      <section class="rounded-lg border border-border bg-surface p-5 shadow-sm">
        <h2 class="text-[15px] font-semibold">加密密钥</h2>
        <div v-if="keyInfo" class="mt-2 text-[13px] text-ink-2">
          <p>当前写入版本：<span class="mono">{{ keyInfo.current_key_version }}</span></p>
          <p>
            可用版本：<span class="mono">{{ keyInfo.available_versions.join(", ") }}</span>
          </p>
          <p class="mt-2 text-warn">
            密钥由环境变量提供，不写入数据库。任一仍被数据引用的版本密钥丢失，对应密文将无法恢复。
          </p>
        </div>
      </section>

      <!-- 回收站 -->
      <section class="rounded-lg border border-border bg-surface p-5 shadow-sm">
        <div class="flex items-center gap-2">
          <Trash2 :size="16" class="text-ink-2" />
          <h2 class="text-[15px] font-semibold">回收站</h2>
          <span class="text-xs text-ink-3">{{ trashCount }} 项</span>
        </div>

        <p v-if="trashCount === 0" class="mt-2 text-[13px] text-ink-3">回收站是空的。</p>

        <div v-else class="mt-3 space-y-4">
          <div v-if="trashProjects.length">
            <p class="mb-1.5 text-xs font-medium text-ink-2">项目</p>
            <ul class="divide-y divide-border rounded-md border border-border">
              <li v-for="p in trashProjects" :key="p.id" class="flex items-center gap-2 px-3 py-2">
                <span class="min-w-0 flex-1 truncate text-[13px]">{{ p.name }}</span>
                <button
                  class="flex h-7 items-center gap-1 rounded-sm border border-border-strong px-2 text-xs text-ink-2 hover:bg-surface-2"
                  @click="restoreItem('projects', p.id)"
                >
                  <RotateCcw :size="12" /> 恢复
                </button>
              </li>
            </ul>
          </div>

          <div v-if="trashTags.length">
            <p class="mb-1.5 text-xs font-medium text-ink-2">标签</p>
            <ul class="divide-y divide-border rounded-md border border-border">
              <li v-for="t in trashTags" :key="t.id" class="flex items-center gap-2 px-3 py-2">
                <span class="min-w-0 flex-1 truncate text-[13px]">{{ t.name }}</span>
                <button
                  class="flex h-7 items-center gap-1 rounded-sm border border-border-strong px-2 text-xs text-ink-2 hover:bg-surface-2"
                  @click="restoreItem('tags', t.id)"
                >
                  <RotateCcw :size="12" /> 恢复
                </button>
              </li>
            </ul>
          </div>

          <div v-if="trashAccounts.length">
            <p class="mb-1.5 text-xs font-medium text-ink-2">账号</p>
            <ul class="divide-y divide-border rounded-md border border-border">
              <li v-for="a in trashAccounts" :key="a.id" class="flex items-center gap-2 px-3 py-2">
                <span class="mono min-w-0 flex-1 truncate">{{ a.email }}</span>
                <button
                  class="flex h-7 items-center gap-1 rounded-sm border border-border-strong px-2 text-xs text-ink-2 hover:bg-surface-2"
                  @click="restoreItem('accounts', a.id)"
                >
                  <RotateCcw :size="12" /> 恢复
                </button>
              </li>
            </ul>
          </div>
        </div>
      </section>

      <!-- 偏好 -->
      <section class="rounded-lg border border-border bg-surface p-5 shadow-sm">
        <h2 class="text-[15px] font-semibold">偏好</h2>

        <div class="mt-3 flex items-center gap-3">
          <span class="w-24 text-[13px] text-ink-2">主题</span>
          <div class="flex gap-1.5">
            <button
              class="rounded-full border px-3 py-1 text-[13px] transition-base"
              :class="theme === 'light' ? 'border-brand bg-brand-soft text-brand' : 'border-border text-ink-2'"
              @click="setTheme('light')"
            >
              浅色
            </button>
            <button
              class="rounded-full border px-3 py-1 text-[13px] transition-base"
              :class="theme === 'dark' ? 'border-brand bg-brand-soft text-brand' : 'border-border text-ink-2'"
              @click="setTheme('dark')"
            >
              深色
            </button>
          </div>
        </div>

        <div class="mt-3 flex items-center gap-3">
          <span class="w-24 text-[13px] text-ink-2">列表密度</span>
          <div class="flex gap-1.5">
            <button
              class="rounded-full border px-3 py-1 text-[13px] transition-base"
              :class="density === 'compact' ? 'border-brand bg-brand-soft text-brand' : 'border-border text-ink-2'"
              @click="setDensity('compact')"
            >
              紧凑
            </button>
            <button
              class="rounded-full border px-3 py-1 text-[13px] transition-base"
              :class="density === 'comfortable' ? 'border-brand bg-brand-soft text-brand' : 'border-border text-ink-2'"
              @click="setDensity('comfortable')"
            >
              舒适
            </button>
          </div>
        </div>

        <div class="mt-5 grid gap-3 sm:grid-cols-2">
          <label class="text-[13px] text-ink-2">
            会话有效期（小时）
            <input v-model.number="ttlHours" type="number" min="1" class="mt-1 h-8 w-full rounded-md border border-border-strong bg-surface px-2" />
          </label>
          <label class="text-[13px] text-ink-2">
            登录失败上限（次）
            <input v-model.number="maxAttempts" type="number" min="1" class="mt-1 h-8 w-full rounded-md border border-border-strong bg-surface px-2" />
          </label>
          <label class="text-[13px] text-ink-2">
            锁定时长（分钟）
            <input v-model.number="lockMinutes" type="number" min="1" class="mt-1 h-8 w-full rounded-md border border-border-strong bg-surface px-2" />
          </label>
          <label class="text-[13px] text-ink-2">
            备份保留（天）
            <input v-model.number="retainDays" type="number" min="1" class="mt-1 h-8 w-full rounded-md border border-border-strong bg-surface px-2" />
          </label>
        </div>

        <div class="mt-4 flex gap-2">
          <button
            class="flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
            :disabled="busy === 'prefs'"
            @click="savePreferences"
          >
            <Loader2 v-if="busy === 'prefs'" :size="14" class="animate-spin" />
            保存设置
          </button>
          <button
            class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-danger hover:bg-surface-2"
            @click="logoutConfirm = true"
          >
            退出登录
          </button>
        </div>
      </section>
    </div>

    <!-- 重新认证 -->
    <Teleport to="body">
      <div v-if="passwordPrompt.open" class="fixed inset-0 z-[215] grid place-items-center p-4">
        <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="passwordPrompt.open = false" />
        <div role="dialog" aria-modal="true" class="relative w-[min(400px,100%)] rounded-lg border border-border bg-surface p-5 shadow-lg">
          <h2 class="text-[15px] font-semibold">重新输入管理员密码</h2>
          <p class="mt-1.5 text-[13px] text-ink-2">
            {{ passwordPrompt.purpose === "backup" ? "下载备份文件前需要重新认证。" : "导出凭据前需要重新认证。" }}
          </p>
          <input
            v-model="adminPassword"
            type="password"
            class="mt-3 h-9 w-full rounded-md border border-border-strong bg-surface px-3 text-[16px] focus:border-brand focus:outline-none"
            placeholder="管理员密码"
            autocomplete="current-password"
            @keydown.enter="submitPassword"
          />
          <div class="mt-4 flex justify-end gap-2">
            <button
              class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
              @click="passwordPrompt.open = false"
            >
              取消
            </button>
            <button
              class="flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
              :disabled="!adminPassword || busy !== ''"
              @click="submitPassword"
            >
              <Loader2 v-if="busy === 'backup' || busy === 'export'" :size="14" class="animate-spin" />
              确认
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="logoutConfirm"
      title="退出登录？"
      message="退出后会清除浏览器中的会话与已揭示的秘密。"
      confirm-text="退出"
      @cancel="logoutConfirm = false"
      @confirm="doLogout"
    />
  </div>
</template>
