<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { AlertTriangle, Check, Loader2, X } from "lucide-vue-next";
import { api, ApiError, type CommitSummary, type Preview } from "@/api/client";
import { useToast } from "@/composables/useToast";

const props = defineProps<{
  open: boolean;
  projectId: number;
  tagIds: number[];
  projectName: string;
}>();

const emit = defineEmits<{ (e: "close"): void; (e: "committed"): void }>();

const toast = useToast();

type Step = "paste" | "preview" | "done";

const step = ref<Step>("paste");
const text = ref("");
const parsing = ref(false);
const committing = ref(false);
const error = ref("");
const preview = ref<Preview | null>(null);
const strategy = ref("skip");
const result = ref<CommitSummary | null>(null);
const batchId = ref<number | null>(null);

const STRATEGY_LABEL: Record<string, string> = {
  skip: "跳过重复",
  overwrite: "覆盖旧账号",
  keep: "全部保留为新账号",
};

watch(
  () => props.open,
  (open) => {
    if (open) {
      step.value = "paste";
      text.value = "";
      preview.value = null;
      result.value = null;
      batchId.value = null;
      error.value = "";
      strategy.value = "skip";
    }
  },
);

const invalidCount = computed(() => preview.value?.summary.invalid ?? 0);
const duplicateCount = computed(() => preview.value?.summary.duplicates ?? 0);
const validCount = computed(() => preview.value?.summary.valid_new ?? 0);

/** 提交按钮上的数量：随策略与逐行修改实时重算。 */
const willCreate = computed(() => {
  const p = preview.value;
  if (!p) return 0;
  if (strategy.value === "skip") return p.summary.valid_new;
  return p.summary.valid_new + p.summary.duplicates;
});

async function parse(): Promise<void> {
  if (parsing.value) return;
  if (!text.value.trim()) {
    error.value = "请先粘贴账号数据";
    return;
  }
  parsing.value = true;
  error.value = "";
  try {
    const data = await api.post<Preview>("/api/import/parse", {
      project_id: props.projectId,
      tag_ids: props.tagIds,
      text: text.value,
    });
    preview.value = data;
    step.value = "preview";
  } catch (err) {
    error.value = err instanceof Error ? err.message : "解析失败";
  } finally {
    parsing.value = false;
  }
}

async function refreshPage(page: number): Promise<void> {
  const p = preview.value;
  if (!p) return;
  try {
    const data = await api.get<Preview>(
      `/api/import/previews/${p.preview_id}?page=${page}&page_size=${p.page_size}`,
    );
    preview.value = data;
  } catch (err) {
    error.value = err instanceof Error ? err.message : "加载预览失败";
  }
}

async function setStrategy(next: string): Promise<void> {
  const p = preview.value;
  if (!p) return;
  try {
    const data = await api.patch<Preview>(`/api/import/previews/${p.preview_id}`, {
      preview_revision: p.preview_revision,
      strategy: next,
    });
    preview.value = data;
    strategy.value = next;
  } catch (err) {
    error.value = err instanceof Error ? err.message : "切换策略失败";
  }
}

async function updateRow(rowId: number, patch: Record<string, unknown>): Promise<void> {
  const p = preview.value;
  if (!p) return;
  try {
    const res = await api.patch<{ preview_revision: number }>(
      `/api/import/previews/${p.preview_id}/rows/${rowId}`,
      { preview_revision: p.preview_revision, ...patch },
    );
    p.preview_revision = res.preview_revision;
    await refreshPage(p.page);
  } catch (err) {
    error.value = err instanceof Error ? err.message : "修改失败";
  }
}

async function commit(): Promise<void> {
  const p = preview.value;
  if (!p || committing.value) return;
  committing.value = true;
  error.value = "";
  try {
    const res = await api.post<{ batch_id: number; summary: CommitSummary; already_committed: boolean }>(
      "/api/import/commit",
      {
        preview_id: p.preview_id,
        preview_revision: p.preview_revision,
        commit_key: p.commit_key,
        strategy: strategy.value,
      },
    );
    result.value = res.summary;
    batchId.value = res.batch_id;
    step.value = "done";
  } catch (err) {
    // 超时或网络中断：用同一 commit_key 查询真实结果，不自动换 key 重试。
    if (err instanceof ApiError && (err.status === 503 || err.status === 0)) {
      await lookupResult();
      return;
    }
    error.value = err instanceof Error ? err.message : "提交失败";
  } finally {
    committing.value = false;
  }
}

async function lookupResult(): Promise<void> {
  const p = preview.value;
  if (!p) return;
  try {
    const res = await api.post<{ status: string; batch_id?: number; summary?: CommitSummary }>(
      "/api/import/result",
      { commit_key: p.commit_key },
    );
    if (res.status === "committed" && res.summary) {
      result.value = res.summary;
      batchId.value = res.batch_id ?? null;
      step.value = "done";
      return;
    }
    error.value = "提交结果待确认，请稍后重试（不会重复写入）";
  } catch (err) {
    error.value = err instanceof Error ? err.message : "查询结果失败";
  }
}

async function cancel(): Promise<void> {
  const p = preview.value;
  if (p) {
    try {
      await api.del(`/api/import/previews/${p.preview_id}`);
    } catch {
      // 取消失败不阻塞关闭
    }
  }
  emit("close");
}

function closeAfterDone(): void {
  emit("committed");
  emit("close");
}

const ISSUE_LABEL: Record<string, string> = {
  email_missing: "缺少邮箱",
  email_invalid: "邮箱格式错误",
  password_empty: "密码为空",
  row_too_large: "行过大",
  field_too_large: "字段过大",
  row_malformed: "格式异常",
};

function issueText(codes: { code: string }[]): string {
  return codes.map((c) => ISSUE_LABEL[c.code] ?? c.code).join("、");
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-[205] grid place-items-center p-4">
      <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="step === 'done' ? closeAfterDone() : cancel()" />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="批量导入"
        class="relative flex max-h-[86dvh] w-[min(680px,100%)] flex-col rounded-lg border border-border bg-surface shadow-lg"
      >
        <header class="flex items-center gap-2 border-b border-border px-5 py-3.5">
          <h2 class="flex-1 text-[15px] font-semibold">批量导入 → {{ projectName }}</h2>
          <button
            class="grid h-8 w-8 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
            aria-label="关闭"
            @click="step === 'done' ? closeAfterDone() : cancel()"
          >
            <X :size="16" />
          </button>
        </header>

        <!-- 步骤指示 -->
        <div class="flex items-center gap-3 border-b border-border px-5 py-3 text-xs">
          <div class="flex items-center gap-2" :class="step === 'paste' ? 'font-medium text-brand' : 'text-ink-3'">
            <span
              class="grid h-5 w-5 place-items-center rounded-full border text-[11px]"
              :class="step === 'paste' ? 'border-brand bg-brand text-on-brand' : 'border-border-strong'"
            >
              <Check v-if="step !== 'paste'" :size="11" />
              <template v-else>1</template>
            </span>
            粘贴
          </div>
          <span class="h-px w-6 bg-border-strong" />
          <div class="flex items-center gap-2" :class="step === 'preview' ? 'font-medium text-brand' : 'text-ink-3'">
            <span
              class="grid h-5 w-5 place-items-center rounded-full border text-[11px]"
              :class="step === 'preview' ? 'border-brand bg-brand text-on-brand' : 'border-border-strong'"
            >
              <Check v-if="step === 'done'" :size="11" />
              <template v-else>2</template>
            </span>
            预览
          </div>
          <span class="h-px w-6 bg-border-strong" />
          <div class="flex items-center gap-2" :class="step === 'done' ? 'font-medium text-brand' : 'text-ink-3'">
            <span class="grid h-5 w-5 place-items-center rounded-full border border-border-strong text-[11px]">3</span>
            完成
          </div>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <!-- ① 粘贴 -->
          <template v-if="step === 'paste'">
            <label for="import-text" class="mb-1.5 block text-xs font-medium text-ink-2">
              粘贴账号数据
            </label>
            <textarea
              id="import-text"
              v-model="text"
              class="mono h-[190px] w-full resize-y rounded-md border border-border-strong bg-surface p-3 text-[12.5px] leading-relaxed focus:border-brand focus:ring-2 focus:ring-brand-soft focus:outline-none"
              placeholder="xxx@gmail.com----password&#10;yyy@gmail.com----password----backup@gmail.com----JBSWY3DPEHPK3PXP"
            />
            <p class="mt-2 text-xs text-ink-3">
              支持：文本 / CSV / JSON / Key-Value / 多行格式。粘贴后点「解析」生成预览，确认前不会写入数据库。
            </p>
          </template>

          <!-- ② 预览 -->
          <template v-else-if="step === 'preview' && preview">
            <div class="mb-4 grid grid-cols-2 gap-2.5 sm:grid-cols-4">
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums text-ok">{{ validCount }}</div>
                <div class="text-[11.5px] text-ink-3">有效非重复</div>
              </div>
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums">{{ duplicateCount }}</div>
                <div class="text-[11.5px] text-ink-3">重复候选</div>
              </div>
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums text-danger">{{ invalidCount }}</div>
                <div class="text-[11.5px] text-ink-3">无效</div>
              </div>
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums text-warn">{{ preview.summary.password_empty }}</div>
                <div class="text-[11.5px] text-ink-3">密码为空</div>
              </div>
            </div>

            <div class="mb-3 flex flex-wrap items-center gap-2 text-xs">
              <span class="text-ink-2">重复处理：</span>
              <button
                v-for="(label, key) in STRATEGY_LABEL"
                :key="key"
                class="rounded-full border px-2.5 py-1 transition-base"
                :class="
                  strategy === key ? 'border-brand bg-brand-soft text-brand' : 'border-border text-ink-2 hover:bg-surface-2'
                "
                @click="setStrategy(String(key))"
              >
                {{ label }}
              </button>
            </div>

            <div class="overflow-hidden rounded-md border border-border">
              <table class="w-full border-collapse text-[12.5px]">
                <thead>
                  <tr class="bg-surface-2 text-left text-xs text-ink-2">
                    <th class="border-b border-border px-2 py-1.5">行</th>
                    <th class="border-b border-border px-2 py-1.5">邮箱</th>
                    <th class="border-b border-border px-2 py-1.5">状态</th>
                    <th class="border-b border-border px-2 py-1.5">附加</th>
                    <th class="border-b border-border px-2 py-1.5">跳过</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="row in preview.items"
                    :key="row.row_id"
                    class="border-b border-border last:border-b-0"
                    :class="row.state === 'invalid' ? 'bg-[color-mix(in_srgb,var(--danger)_8%,transparent)]' : ''"
                  >
                    <td class="px-2 py-1.5 text-ink-3 tabular-nums">{{ row.line }}</td>
                    <td class="px-2 py-1.5">
                      <input
                        :value="row.email"
                        class="mono w-full rounded-sm border border-transparent bg-transparent px-1 py-0.5 hover:border-border focus:border-brand focus:outline-none"
                        @change="updateRow(row.row_id, { email: ($event.target as HTMLInputElement).value })"
                      />
                    </td>
                    <td class="px-2 py-1.5">
                      <span v-if="row.state === 'invalid'" class="text-danger">
                        <AlertTriangle :size="12" class="mr-0.5 inline" />
                        {{ issueText(row.issue_codes) }}
                      </span>
                      <span v-else-if="row.state === 'duplicate'" class="text-warn">重复</span>
                      <span v-else class="text-ink-3">
                        新账号
                        <span v-if="row.issue_codes.length" class="text-warn"> · {{ issueText(row.issue_codes) }}</span>
                      </span>
                    </td>
                    <td class="px-2 py-1.5 whitespace-nowrap text-[11.5px] text-ink-3">
                      <span v-if="row.has_f2a" class="rounded-sm bg-surface-2 px-1">2FA</span>
                      <span v-if="row.has_refresh_token" class="ml-1 rounded-sm bg-surface-2 px-1">RT</span>
                      <span v-if="row.has_json" class="ml-1 rounded-sm bg-surface-2 px-1">JSON</span>
                    </td>
                    <td class="px-2 py-1.5">
                      <input
                        type="checkbox"
                        :checked="row.skip"
                        class="h-3.5 w-3.5 accent-[var(--brand)]"
                        @change="updateRow(row.row_id, { skip: ($event.target as HTMLInputElement).checked })"
                      />
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div class="mt-3 flex items-center gap-2 text-xs text-ink-2">
              <button
                class="rounded-sm border border-border-strong px-2 py-1 hover:bg-surface-2 disabled:opacity-50"
                :disabled="preview.page <= 1"
                @click="refreshPage(preview.page - 1)"
              >
                上一页
              </button>
              <span class="tabular-nums">
                第 {{ preview.page }} 页 / 共 {{ Math.max(1, Math.ceil(preview.total / preview.page_size)) }} 页
                （{{ preview.total }} 行）
              </span>
              <button
                class="rounded-sm border border-border-strong px-2 py-1 hover:bg-surface-2 disabled:opacity-50"
                :disabled="preview.page * preview.page_size >= preview.total"
                @click="refreshPage(preview.page + 1)"
              >
                下一页
              </button>
            </div>
          </template>

          <!-- ③ 完成 -->
          <template v-else-if="step === 'done' && result">
            <div class="py-2 text-center">
              <div class="mx-auto grid h-10 w-10 place-items-center rounded-full bg-brand-soft text-brand">
                <Check :size="20" />
              </div>
              <p class="mt-3 text-[15px] font-semibold">导入完成</p>
              <p v-if="batchId" class="mono mt-1 text-xs text-ink-3">批次 #{{ batchId }}</p>
            </div>
            <div class="mt-4 grid grid-cols-2 gap-2.5 sm:grid-cols-4">
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums text-ok">{{ result.created }}</div>
                <div class="text-[11.5px] text-ink-3">新增</div>
              </div>
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums">{{ result.updated }}</div>
                <div class="text-[11.5px] text-ink-3">覆盖更新</div>
              </div>
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums">{{ result.skipped }}</div>
                <div class="text-[11.5px] text-ink-3">跳过</div>
              </div>
              <div class="rounded-md border border-border bg-surface-2 p-3">
                <div class="text-lg font-semibold tabular-nums text-danger">{{ result.invalid }}</div>
                <div class="text-[11.5px] text-ink-3">无效排除</div>
              </div>
            </div>
          </template>

          <p v-if="error" role="alert" class="mt-3 text-[13px] text-danger">{{ error }}</p>
        </div>

        <footer class="flex items-center gap-2 border-t border-border px-5 py-3.5">
          <template v-if="step === 'paste'">
            <button class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2" @click="cancel">
              取消
            </button>
            <button
              class="ml-auto flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
              :disabled="parsing"
              @click="parse"
            >
              <Loader2 v-if="parsing" :size="14" class="animate-spin" />
              解析
            </button>
          </template>
          <template v-else-if="step === 'preview'">
            <button
              class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2"
              @click="step = 'paste'"
            >
              返回修改
            </button>
            <button class="h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2" @click="cancel">
              取消
            </button>
            <button
              class="ml-auto flex h-8 items-center gap-1.5 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
              :disabled="committing || willCreate === 0"
              @click="commit"
            >
              <Loader2 v-if="committing" :size="14" class="animate-spin" />
              {{ committing ? "正在写入…" : `确认导入 ${willCreate} 条` }}
            </button>
          </template>
          <template v-else>
            <button
              class="ml-auto h-8 rounded-md bg-brand px-3 text-[13px] font-medium text-on-brand hover:bg-brand-hover"
              @click="closeAfterDone"
            >
              完成
            </button>
          </template>
        </footer>
      </div>
    </div>
  </Teleport>
</template>
