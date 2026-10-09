<script setup lang="ts">
import { ref, watch } from "vue";
import { ChevronDown, ChevronRight, Plus, Trash2, X } from "lucide-vue-next";
import { api, type Project, type Tag } from "@/api/client";
import { useToast } from "@/composables/useToast";
import ConfirmDialog from "@/components/ConfirmDialog.vue";

const props = defineProps<{ open: boolean; projects: Project[] }>();
const emit = defineEmits<{ (e: "close"): void; (e: "changed"): void }>();

const toast = useToast();

const expanded = ref<number | null>(null);
const tagsByProject = ref<Record<number, Tag[]>>({});
const newProjectName = ref("");
const newTagName = ref<Record<number, string>>({});
const busy = ref(false);

const deleting = ref<{ kind: "project" | "tag"; id: number; name: string; accountCount: number } | null>(null);

watch(
  () => props.open,
  async (open) => {
    if (!open) return;
    expanded.value = null;
    tagsByProject.value = {};
    newProjectName.value = "";
  },
);

async function loadTags(projectId: number): Promise<void> {
  try {
    const res = await api.get<{ items: Tag[] }>(`/api/projects/${projectId}/tags`);
    tagsByProject.value = { ...tagsByProject.value, [projectId]: res.items };
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "加载标签失败");
  }
}

async function toggle(projectId: number): Promise<void> {
  if (expanded.value === projectId) {
    expanded.value = null;
    return;
  }
  expanded.value = projectId;
  if (!tagsByProject.value[projectId]) await loadTags(projectId);
}

async function createProject(): Promise<void> {
  const name = newProjectName.value.trim();
  if (!name || busy.value) return;
  busy.value = true;
  try {
    await api.post("/api/projects", { name });
    newProjectName.value = "";
    toast.success("项目已创建");
    emit("changed");
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "创建项目失败");
  } finally {
    busy.value = false;
  }
}

async function createTag(projectId: number): Promise<void> {
  const name = (newTagName.value[projectId] ?? "").trim();
  if (!name || busy.value) return;
  busy.value = true;
  try {
    await api.post(`/api/projects/${projectId}/tags`, { name });
    newTagName.value = { ...newTagName.value, [projectId]: "" };
    await loadTags(projectId);
    toast.success("标签已创建");
    emit("changed");
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "创建标签失败");
  } finally {
    busy.value = false;
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleting.value;
  if (!target) return;
  busy.value = true;
  try {
    if (target.kind === "project") {
      await api.del(`/api/projects/${target.id}`);
    } else {
      await api.del(`/api/tags/${target.id}`);
    }
    toast.success("已移入回收站");
    deleting.value = null;
    if (target.kind === "tag" && expanded.value) await loadTags(expanded.value);
    emit("changed");
  } catch (err) {
    toast.error(err instanceof Error ? err.message : "删除失败");
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-[205] grid place-items-center p-4">
      <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="emit('close')" />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="项目与标签"
        class="relative flex max-h-[86dvh] w-[min(560px,100%)] flex-col rounded-lg border border-border bg-surface shadow-lg"
      >
        <header class="flex items-center border-b border-border px-5 py-3.5">
          <h2 class="flex-1 text-[15px] font-semibold">项目与标签</h2>
          <button
            class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
            aria-label="关闭"
            @click="emit('close')"
          >
            <X :size="16" />
          </button>
        </header>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <div class="mb-4 flex gap-2">
            <input
              v-model="newProjectName"
              placeholder="新建项目名称"
              class="h-8 min-w-0 flex-1 rounded-md border border-border-strong bg-surface px-2 text-[13px] focus:border-brand focus:outline-none"
              @keydown.enter="createProject"
            />
            <button
              class="flex h-8 items-center gap-1 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
              :disabled="busy || !newProjectName.trim()"
              @click="createProject"
            >
              <Plus :size="14" /> 项目
            </button>
          </div>

          <p v-if="projects.length === 0" class="py-6 text-center text-[13px] text-ink-3">
            还没有项目，先创建一个。
          </p>

          <div v-for="p in projects" :key="p.id" class="mb-2 rounded-md border border-border">
            <div class="flex items-center gap-2 px-3 py-2">
              <button
                class="grid h-6 w-6 place-items-center rounded-sm text-ink-3 hover:bg-surface-2"
                :aria-label="expanded === p.id ? '收起标签' : '展开标签'"
                @click="toggle(p.id)"
              >
                <ChevronDown v-if="expanded === p.id" :size="14" />
                <ChevronRight v-else :size="14" />
              </button>
              <span class="flex-1 truncate text-[13px] font-medium">{{ p.name }}</span>
              <span class="text-xs text-ink-3 tabular-nums">{{ p.account_count }} 账号</span>
              <button
                class="grid h-6 w-6 place-items-center rounded-sm text-ink-3 hover:bg-surface-2 hover:text-danger"
                aria-label="删除项目"
                @click="deleting = { kind: 'project', id: p.id, name: p.name, accountCount: p.account_count }"
              >
                <Trash2 :size="13" />
              </button>
            </div>

            <div v-if="expanded === p.id" class="border-t border-border px-3 py-2.5">
              <div class="mb-2 flex flex-wrap gap-1.5">
                <span
                  v-for="t in tagsByProject[p.id] ?? []"
                  :key="t.id"
                  class="flex items-center gap-1 rounded-full border border-border bg-surface-2 px-2 py-0.5 text-xs text-ink-2"
                >
                  {{ t.name }}
                  <button
                    class="text-ink-3 hover:text-danger"
                    :aria-label="`删除标签 ${t.name}`"
                    @click="deleting = { kind: 'tag', id: t.id, name: t.name, accountCount: 0 }"
                  >
                    <X :size="11" />
                  </button>
                </span>
                <span v-if="!(tagsByProject[p.id] ?? []).length" class="text-xs text-ink-3">暂无标签</span>
              </div>
              <div class="flex gap-2">
                <input
                  v-model="newTagName[p.id]"
                  placeholder="新建标签"
                  class="h-7 min-w-0 flex-1 rounded-md border border-border-strong bg-surface px-2 text-xs focus:border-brand focus:outline-none"
                  @keydown.enter="createTag(p.id)"
                />
                <button
                  class="h-7 rounded-md border border-border-strong px-2 text-xs text-ink-2 hover:bg-surface-2"
                  @click="createTag(p.id)"
                >
                  添加
                </button>
              </div>
            </div>
          </div>
        </div>

        <footer class="flex justify-end border-t border-border px-5 py-3.5">
          <button
            class="h-8 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover"
            @click="emit('close')"
          >
            完成
          </button>
        </footer>
      </div>
    </div>

    <ConfirmDialog
      :open="deleting !== null"
      :title="deleting?.kind === 'project' ? '删除项目？' : '删除标签？'"
      :message="
        deleting?.kind === 'project'
          ? `项目「${deleting?.name}」下的 ${deleting?.accountCount} 个账号将一并移入回收状态。`
          : `标签「${deleting?.name}」将被移入回收站，账号本身不受影响。`
      "
      confirm-text="确认删除"
      danger
      :busy="busy"
      @cancel="deleting = null"
      @confirm="confirmDelete"
    />
  </Teleport>
</template>
