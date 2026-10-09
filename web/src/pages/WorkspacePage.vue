<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import {
  ChevronDown,
  ChevronRight,
  Copy,
  Download,
  Loader2,
  LogOut,
  Menu,
  Moon,
  Plus,
  Search,
  Settings,
  Sun,
  Upload,
  X,
  KeyRound,
  Download as DownloadIcon,
} from "lucide-vue-next";
import {
  api,
  type Account,
  type ListResult,
  type Project,
  type RefreshTokenEntry,
  type Tag,
} from "@/api/client";
import { useAuth } from "@/composables/useAuth";
import { useTheme } from "@/composables/useTheme";
import { useToast } from "@/composables/useToast";
import AccountTable from "@/components/AccountTable.vue";
import AccountDrawer from "@/components/AccountDrawer.vue";
import ImportModal from "@/components/ImportModal.vue";
import ProjectPanel from "@/components/ProjectPanel.vue";
import ConfirmDialog from "@/components/ConfirmDialog.vue";

const router = useRouter();
const { logout } = useAuth();
const { theme, toggleTheme } = useTheme();
const toast = useToast();

const LAST_PROJECT_KEY = "passpal.lastProject";
const LAST_TAG_KEY = "passpal.lastTag";

const projects = ref<Project[]>([]);
const tags = ref<Tag[]>([]);
const accounts = ref<Account[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(100);
const sort = ref("newest");
const loading = ref(false);
const listError = ref("");

const activeProjectId = ref<number | null>(null);
const activeTagId = ref<number | null>(null);
const expandedProjects = ref<Set<number>>(new Set());

const globalQuery = ref("");
const localQuery = ref("");
const searching = ref(false);

const sidebarOpen = ref(false);
const drawerOpen = ref(false);
const activeAccount = ref<Account | null>(null);
const importOpen = ref(false);
const projectPanelOpen = ref(false);
const logoutConfirm = ref(false);

const selectedIds = ref<number[]>([]);
const rtExport = ref<{ open: boolean; count: number; text: string; items: RefreshTokenEntry[] }>({
  open: false,
  count: 0,
  text: "",
  items: [],
});
const exportingRT = ref(false);

const newAccountOpen = ref(false);
const newAccountEmail = ref("");
const newAccountPassword = ref("");
const creatingAccount = ref(false);

let searchTimer = 0;
let requestSeq = 0;

const activeProject = computed(() => projects.value.find((p) => p.id === activeProjectId.value) ?? null);
const activeTag = computed(() => tags.value.find((t) => t.id === activeTagId.value) ?? null);
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)));

function persistSelection(): void {
  try {
    if (activeProjectId.value) localStorage.setItem(LAST_PROJECT_KEY, String(activeProjectId.value));
    if (activeTagId.value) localStorage.setItem(LAST_TAG_KEY, String(activeTagId.value));
  } catch {
    // 忽略存储异常
  }
}

function readPersisted(): { projectId: number | null; tagId: number | null } {
  try {
    const p = Number(localStorage.getItem(LAST_PROJECT_KEY) || 0);
    const t = Number(localStorage.getItem(LAST_TAG_KEY) || 0);
    return { projectId: p || null, tagId: t || null };
  } catch {
    return { projectId: null, tagId: null };
  }
}

async function loadProjects(): Promise<void> {
  try {
    const res = await api.get<{ items: Project[] }>("/api/projects");
    projects.value = res.items;
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "加载项目失败");
  }
}

async function loadTags(projectId: number): Promise<void> {
  try {
    const res = await api.get<{ items: Tag[] }>(`/api/projects/${projectId}/tags`);
    tags.value = res.items;
  } catch (err) {
    tags.value = [];
    toast.error(err instanceof Error ? err.message : "加载标签失败");
  }
}

/** 列表加载：页码、每页条数、排序、筛选都真正改变查询。 */
async function loadAccounts(): Promise<void> {
  const seq = ++requestSeq;
  loading.value = true;
  listError.value = "";
  try {
    const params = new URLSearchParams();
    if (globalQuery.value.trim()) {
      params.set("q", globalQuery.value.trim());
    } else {
      if (activeProjectId.value) params.set("project_id", String(activeProjectId.value));
      if (activeTagId.value) params.set("tag_id", String(activeTagId.value));
      if (localQuery.value.trim()) params.set("q", localQuery.value.trim());
    }
    params.set("sort", sort.value);
    params.set("page", String(page.value));
    params.set("page_size", String(pageSize.value));

    const path = globalQuery.value.trim() ? "/api/search" : "/api/accounts";
    const res = await api.get<ListResult<Account>>(`${path}?${params.toString()}`);
    if (seq !== requestSeq) return; // 旧响应不得覆盖新结果
    accounts.value = res.items;
    total.value = res.total;
    page.value = res.page;
  } catch (err) {
    if (seq !== requestSeq) return;
    listError.value = err instanceof Error ? err.message : "加载账号失败";
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

async function selectProject(projectId: number): Promise<void> {
  activeProjectId.value = projectId;
  activeTagId.value = null;
  expandedProjects.value = new Set([...expandedProjects.value, projectId]);
  page.value = 1;
  localQuery.value = "";
  sidebarOpen.value = false;
  selectedIds.value = [];
  persistSelection();
  await loadTags(projectId);
  await loadAccounts();
}

async function selectTag(tagId: number): Promise<void> {
  activeTagId.value = tagId;
  page.value = 1;
  localQuery.value = "";
  sidebarOpen.value = false;
  selectedIds.value = [];
  persistSelection();
  await loadAccounts();
}

function toggleProject(projectId: number): void {
  const next = new Set(expandedProjects.value);
  if (next.has(projectId)) next.delete(projectId);
  else next.add(projectId);
  expandedProjects.value = next;
}

watch(globalQuery, () => {
  window.clearTimeout(searchTimer);
  searching.value = true;
  searchTimer = window.setTimeout(async () => {
    page.value = 1;
    await loadAccounts();
    searching.value = false;
  }, 200);
});

watch(localQuery, () => {
  window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(async () => {
    page.value = 1;
    await loadAccounts();
  }, 200);
});

function openAccount(account: Account): void {
  activeAccount.value = account;
  drawerOpen.value = true;
}

function onSaved(updated: Account): void {
  activeAccount.value = updated;
  void loadAccounts();
}

function onDeleted(): void {
  drawerOpen.value = false;
  activeAccount.value = null;
  void loadProjects();
  void loadAccounts();
}

async function createAccount(): Promise<void> {
  if (!activeProjectId.value) {
    toast.error("请先选择项目");
    return;
  }
  const email = newAccountEmail.value.trim();
  if (!email || creatingAccount.value) return;
  creatingAccount.value = true;
  try {
    const body: Record<string, unknown> = { project_id: activeProjectId.value, email };
    if (newAccountPassword.value) body.password = newAccountPassword.value;
    if (activeTagId.value) body.tag_ids = [activeTagId.value];
    await api.post("/api/accounts", body);
    toast.success("账号已创建");
    newAccountOpen.value = false;
    newAccountEmail.value = "";
    newAccountPassword.value = "";
    await loadProjects();
    await loadAccounts();
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "创建失败");
  } finally {
    creatingAccount.value = false;
  }
}

/** 批量导出选中账号的 refresh token。 */
async function exportSelectedRT(): Promise<void> {
  if (selectedIds.value.length === 0 || exportingRT.value) return;
  exportingRT.value = true;
  try {
    const res = await api.post<{
      requested: number;
      exported: number;
      text: string;
      items: RefreshTokenEntry[];
    }>("/api/accounts/export-refresh-tokens", { ids: selectedIds.value });
    rtExport.value = { open: true, count: res.exported, text: res.text, items: res.items };
    if (res.exported === 0) {
      toast.info("选中的账号都没有 Refresh Token");
    }
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "导出失败");
  } finally {
    exportingRT.value = false;
  }
}

async function copyRTText(): Promise<void> {
  try {
    if (!navigator.clipboard?.writeText) throw new Error("当前浏览器不支持剪贴板写入");
    await navigator.clipboard.writeText(rtExport.value.text);
    toast.success(`已复制 ${rtExport.value.count} 条`);
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "复制失败");
  }
}

function downloadRTText(): void {
  const blob = new Blob([rtExport.value.text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `passpal-refresh-tokens-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "")}.txt`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

async function doLogout(): Promise<void> {
  await logout();
  logoutConfirm.value = false;
  await router.replace({ name: "login" });
}

function onKeydown(event: KeyboardEvent): void {
  const target = event.target as HTMLElement | null;
  const typing =
    target &&
    (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable);
  if (typing || event.metaKey || event.ctrlKey || event.altKey) return;

  if (event.key === "/") {
    event.preventDefault();
    document.getElementById("global-search")?.focus();
  } else if (event.key === "n" || event.key === "N") {
    event.preventDefault();
    if (activeProjectId.value) newAccountOpen.value = true;
  } else if (event.key === "i" || event.key === "I") {
    event.preventDefault();
    if (activeProjectId.value) importOpen.value = true;
  }
}

onMounted(async () => {
  document.addEventListener("keydown", onKeydown);
  await loadProjects();
  const persisted = readPersisted();
  const first = projects.value.find((p) => p.id === persisted.projectId) ?? projects.value[0];
  if (first) {
    activeProjectId.value = first.id;
    expandedProjects.value = new Set([first.id]);
    await loadTags(first.id);
    if (persisted.tagId && tags.value.some((t) => t.id === persisted.tagId)) {
      activeTagId.value = persisted.tagId;
    }
  }
  await loadAccounts();
});

onBeforeUnmount(() => document.removeEventListener("keydown", onKeydown));
</script>

<template>
  <div class="flex min-h-dvh flex-col">
    <!-- 顶栏 -->
    <header class="sticky top-0 z-30 flex h-12 shrink-0 items-center gap-3 border-b border-border bg-surface px-3 sm:px-4">
      <button
        class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2 lg:hidden"
        aria-label="打开项目导航"
        @click="sidebarOpen = true"
      >
        <Menu :size="17" />
      </button>

      <router-link to="/app" class="flex shrink-0 items-center gap-1.5 text-[13px] font-semibold">
        <span>🌱</span>
        <span class="hidden sm:inline">PassPal</span>
      </router-link>

      <div class="relative min-w-0 flex-1 sm:max-w-[420px]">
        <Search :size="14" class="pointer-events-none absolute top-2.5 left-2.5 text-ink-3" />
        <input
          id="global-search"
          v-model="globalQuery"
          class="h-8 w-full rounded-md border border-border bg-bg pr-7 pl-8 text-[13px] focus:border-brand focus:outline-none"
          placeholder="搜索账号、项目、标签…（按 / 聚焦）"
          aria-label="全局搜索"
        />
        <Loader2 v-if="searching" :size="13" class="absolute top-2.5 right-2.5 animate-spin text-ink-3" />
        <button
          v-else-if="globalQuery"
          class="absolute top-2 right-2 grid h-4 w-4 place-items-center text-ink-3 hover:text-ink"
          aria-label="清空搜索"
          @click="globalQuery = ''"
        >
          <X :size="13" />
        </button>
      </div>

      <div class="ml-auto flex items-center gap-0.5">
        <button
          class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
          :aria-label="theme === 'dark' ? '切换到浅色主题' : '切换到深色主题'"
          @click="toggleTheme"
        >
          <Sun v-if="theme === 'dark'" :size="16" />
          <Moon v-else :size="16" />
        </button>
        <router-link
          to="/settings"
          class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
          aria-label="设置"
        >
          <Settings :size="16" />
        </router-link>
        <button
          class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
          aria-label="退出登录"
          @click="logoutConfirm = true"
        >
          <LogOut :size="16" />
        </button>
      </div>
    </header>

    <div class="flex min-h-0 flex-1">
      <!-- 侧栏（桌面固定 / 手机抽屉） -->
      <aside
        class="fixed inset-y-0 left-0 z-40 w-[248px] shrink-0 overflow-y-auto border-r border-border bg-bg p-3 transition-transform lg:static lg:translate-x-0"
        :class="sidebarOpen ? 'translate-x-0' : '-translate-x-full'"
        aria-label="项目与标签"
      >
        <div class="mb-2 flex items-center justify-between px-2">
          <h2 class="text-[11px] font-semibold tracking-wide text-ink-3 uppercase">项目</h2>
          <div class="flex gap-0.5">
            <button
              class="grid h-6 w-6 place-items-center rounded-sm text-ink-3 hover:bg-surface-2"
              aria-label="管理项目与标签"
              @click="projectPanelOpen = true"
            >
              <Plus :size="14" />
            </button>
            <button
              class="grid h-6 w-6 place-items-center rounded-sm text-ink-3 hover:bg-surface-2 lg:hidden"
              aria-label="关闭导航"
              @click="sidebarOpen = false"
            >
              <X :size="14" />
            </button>
          </div>
        </div>

        <p v-if="projects.length === 0" class="px-2 py-3 text-[12.5px] text-ink-3">
          还没有项目，点右上角 + 创建。
        </p>

        <div v-for="p in projects" :key="p.id">
          <div
            class="transition-base flex h-8 cursor-pointer items-center gap-1.5 rounded-sm px-2 text-[13px]"
            :class="activeProjectId === p.id ? 'bg-brand-soft font-medium text-brand' : 'text-ink-2 hover:bg-surface-2'"
            @click="selectProject(p.id)"
          >
            <button
              class="grid h-4 w-4 place-items-center text-ink-3"
              :aria-label="expandedProjects.has(p.id) ? '收起' : '展开'"
              @click.stop="toggleProject(p.id)"
            >
              <ChevronDown v-if="expandedProjects.has(p.id)" :size="12" />
              <ChevronRight v-else :size="12" />
            </button>
            <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
            <span class="text-xs text-ink-3 tabular-nums">{{ p.account_count }}</span>
          </div>

          <template v-if="expandedProjects.has(p.id) && activeProjectId === p.id">
            <div
              v-for="t in tags"
              :key="t.id"
              class="transition-base ml-4 flex h-7 cursor-pointer items-center gap-2 rounded-sm px-2 text-[12.5px]"
              :class="activeTagId === t.id ? 'bg-brand-soft font-medium text-brand' : 'text-ink-2 hover:bg-surface-2'"
              @click="selectTag(t.id)"
            >
              <span class="h-1.5 w-1.5 shrink-0 rounded-full" :style="{ background: t.color || 'var(--ink-3)' }" />
              <span class="min-w-0 flex-1 truncate">{{ t.name }}</span>
              <span class="text-xs text-ink-3 tabular-nums">{{ t.account_count }}</span>
            </div>
            <p v-if="tags.length === 0" class="ml-4 px-2 py-1.5 text-[12px] text-ink-3">暂无标签</p>
          </template>
        </div>
      </aside>

      <div
        v-if="sidebarOpen"
        class="fixed inset-0 z-30 bg-[rgb(28_25_23/0.32)] lg:hidden"
        @click="sidebarOpen = false"
      />

      <!-- 内容区 -->
      <main class="min-w-0 flex-1">
        <div class="mx-auto w-full max-w-[1440px] px-4 py-5 sm:px-6 lg:px-8">
          <div class="mb-4 flex flex-wrap items-baseline gap-2">
            <h1 class="text-xl font-semibold">
              {{ globalQuery ? "搜索结果" : (activeTag?.name ?? activeProject?.name ?? "全部账号") }}
            </h1>
            <span class="text-[13px] text-ink-3">
              <template v-if="!globalQuery && activeTag && activeProject">
                {{ activeProject.name }} ·
              </template>
              {{ total }} 个账号
            </span>
          </div>

          <div class="mb-3.5 flex flex-wrap items-center gap-2">
            <button
              class="transition-base flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-50"
              :disabled="!activeProjectId"
              @click="importOpen = true"
            >
              <Upload :size="14" /> 批量导入
            </button>
            <button
              class="transition-base flex h-8 items-center gap-1.5 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2 disabled:opacity-50"
              :disabled="!activeProjectId"
              @click="newAccountOpen = true"
            >
              <Plus :size="14" /> 添加账号
            </button>
            <router-link
              to="/settings"
              class="transition-base flex h-8 items-center gap-1.5 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
            >
              <Download :size="14" /> 导出
            </router-link>
            <button
              v-if="selectedIds.length > 0"
              class="transition-base flex h-8 items-center gap-1.5 rounded-md border border-brand bg-brand-soft px-3 text-[13px] font-medium text-brand disabled:opacity-60"
              :disabled="exportingRT"
              @click="exportSelectedRT"
            >
              <Loader2 v-if="exportingRT" :size="14" class="animate-spin" />
              <KeyRound v-else :size="14" />
              导出选中 RT（{{ selectedIds.length }}）
            </button>
            <div class="ml-auto">
              <select
                v-model="sort"
                class="h-8 rounded-md border border-border-strong bg-surface px-2 text-[13px] text-ink-2 focus:border-brand focus:outline-none"
                aria-label="排序方式"
                @change="page = 1; loadAccounts()"
              >
                <option value="newest">最新导入</option>
                <option value="oldest">最早导入</option>
                <option value="email_asc">邮箱 A-Z</option>
                <option value="email_desc">邮箱 Z-A</option>
                <option value="recent_used">最近使用</option>
              </select>
            </div>
          </div>

          <div v-if="!globalQuery" class="relative mb-3">
            <Search :size="14" class="pointer-events-none absolute top-2.5 left-2.5 text-ink-3" />
            <input
              v-model="localQuery"
              class="h-8 w-full rounded-md border border-border bg-surface pr-3 pl-8 text-[13px] focus:border-brand focus:outline-none"
              :placeholder="activeTag ? `在「${activeTag.name}」中搜索邮箱、用户名…` : '搜索邮箱、用户名…'"
              aria-label="在当前范围内搜索"
            />
          </div>

          <p v-if="listError" role="alert" class="mb-3 text-[13px] text-danger">{{ listError }}</p>

          <AccountTable
            v-model:selected="selectedIds"
            :accounts="accounts"
            :loading="loading"
            @open="openAccount"
            @edit="openAccount"
            @delete="openAccount"
          />

          <div v-if="total > 0" class="mt-4 flex flex-wrap items-center gap-2 text-[13px] text-ink-2">
            <button
              class="h-7 rounded-sm border border-border-strong px-2 hover:bg-surface-2 disabled:opacity-50"
              :disabled="page <= 1"
              @click="page--; loadAccounts()"
            >
              上一页
            </button>
            <span class="tabular-nums">第 {{ page }} / {{ totalPages }} 页</span>
            <span v-if="selectedIds.length > 0" class="text-brand">
              已选 {{ selectedIds.length }} 项
              <button class="ml-1 underline hover:no-underline" @click="selectedIds = []">清除</button>
            </span>
            <button
              class="h-7 rounded-sm border border-border-strong px-2 hover:bg-surface-2 disabled:opacity-50"
              :disabled="page >= totalPages"
              @click="page++; loadAccounts()"
            >
              下一页
            </button>
            <select
              v-model.number="pageSize"
              class="ml-auto h-7 rounded-sm border border-border-strong bg-surface px-1.5 text-[12.5px]"
              aria-label="每页条数"
              @change="page = 1; loadAccounts()"
            >
              <option :value="50">每页 50</option>
              <option :value="100">每页 100</option>
            </select>
          </div>
        </div>
      </main>
    </div>

    <!-- 新增账号 -->
    <Teleport to="body">
      <div v-if="newAccountOpen" class="fixed inset-0 z-[205] grid place-items-center p-4">
        <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="newAccountOpen = false" />
        <div role="dialog" aria-modal="true" class="relative w-[min(420px,100%)] rounded-lg border border-border bg-surface p-5 shadow-lg">
          <h2 class="text-[15px] font-semibold">添加账号</h2>
          <p class="mt-1 text-xs text-ink-3">项目：{{ activeProject?.name }}</p>
          <div class="mt-4 space-y-3">
            <div>
              <label for="new-email" class="mb-1.5 block text-xs font-medium text-ink-2">邮箱</label>
              <input
                id="new-email"
                v-model="newAccountEmail"
                class="mono h-8 w-full rounded-md border border-border-strong bg-surface px-2 text-[12.5px] focus:border-brand focus:outline-none"
                placeholder="name@example.com"
              />
            </div>
            <div>
              <label for="new-password" class="mb-1.5 block text-xs font-medium text-ink-2">
                密码（可留空）
              </label>
              <input
                id="new-password"
                v-model="newAccountPassword"
                type="text"
                class="mono h-8 w-full rounded-md border border-border-strong bg-surface px-2 text-[12.5px] focus:border-brand focus:outline-none"
              />
            </div>
          </div>
          <div class="mt-5 flex justify-end gap-2">
            <button
              class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
              @click="newAccountOpen = false"
            >
              取消
            </button>
            <button
              class="flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
              :disabled="creatingAccount || !newAccountEmail.trim()"
              @click="createAccount"
            >
              <Loader2 v-if="creatingAccount" :size="14" class="animate-spin" />
              创建
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <AccountDrawer
      :open="drawerOpen"
      :account="activeAccount"
      :tags="tags"
      @close="drawerOpen = false"
      @saved="onSaved"
      @deleted="onDeleted"
    />

    <ImportModal
      :open="importOpen"
      :project-id="activeProjectId ?? 0"
      :tag-ids="activeTagId ? [activeTagId] : []"
      :project-name="activeProject?.name ?? ''"
      @close="importOpen = false"
      @committed="loadProjects(); loadTags(activeProjectId!); loadAccounts()"
    />

    <ProjectPanel
      :open="projectPanelOpen"
      :projects="projects"
      @close="projectPanelOpen = false"
      @changed="loadProjects(); activeProjectId && loadTags(activeProjectId); loadAccounts()"
    />

    <!-- 批量导出 Refresh Token 结果 -->
    <Teleport to="body">
      <div v-if="rtExport.open" class="fixed inset-0 z-[208] grid place-items-center p-4">
        <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="rtExport.open = false" />
        <div
          role="dialog"
          aria-modal="true"
          aria-label="导出 Refresh Token"
          class="relative flex max-h-[80dvh] w-[min(680px,100%)] flex-col rounded-lg border border-border bg-surface shadow-lg"
        >
          <header class="flex items-center border-b border-border px-5 py-3.5">
            <h2 class="flex-1 text-[15px] font-semibold">
              已导出 {{ rtExport.count }} 条 Refresh Token
            </h2>
            <button
              class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
              aria-label="关闭"
              @click="rtExport.open = false"
            >
              <X :size="16" />
            </button>
          </header>
          <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
            <p class="mb-2 text-[13px] text-ink-2">
              每行格式为 <span class="mono">邮箱----refresh_token</span>，没有 RT 的账号已跳过。
            </p>
            <textarea
              readonly
              :value="rtExport.text"
              class="mono h-[260px] w-full resize-y rounded-md border border-border-strong bg-surface-2 p-3 text-[12px] leading-relaxed focus:border-brand focus:outline-none"
            />
          </div>
          <footer class="flex justify-end gap-2 border-t border-border px-5 py-3.5">
            <button
              class="flex h-8 items-center gap-1.5 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
              @click="downloadRTText"
            >
              <DownloadIcon :size="14" /> 下载 .txt
            </button>
            <button
              class="flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover"
              @click="copyRTText"
            >
              <Copy :size="14" /> 复制全部
            </button>
          </footer>
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
