<script setup lang="ts">
import { nextTick, ref, watch } from "vue";
import { Loader2 } from "lucide-vue-next";

const props = withDefaults(
  defineProps<{
    open: boolean;
    title: string;
    message?: string;
    detail?: string;
    confirmText?: string;
    cancelText?: string;
    danger?: boolean;
    busy?: boolean;
    /** 需要用户原样输入才允许确认（用于高危操作）。 */
    requireText?: string;
  }>(),
  {
    message: "",
    detail: "",
    confirmText: "确认",
    cancelText: "取消",
    danger: false,
    busy: false,
    requireText: "",
  },
);

const emit = defineEmits<{ (e: "confirm"): void; (e: "cancel"): void }>();

const panelRef = ref<HTMLElement | null>(null);
const typed = ref("");

watch(
  () => props.open,
  async (open) => {
    typed.value = "";
    if (open) {
      await nextTick();
      panelRef.value?.focus();
    }
  },
);

function canConfirm(): boolean {
  if (props.busy) return false;
  if (props.requireText) return typed.value.trim() === props.requireText;
  return true;
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") {
    event.stopPropagation();
    if (!props.busy) emit("cancel");
    return;
  }
  if (event.key === "Enter" && canConfirm()) {
    event.preventDefault();
    emit("confirm");
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-[210] grid place-items-center p-4">
      <div class="absolute inset-0 bg-[rgb(28_25_23/0.32)]" @click="!busy && emit('cancel')" />
      <div
        ref="panelRef"
        role="dialog"
        aria-modal="true"
        tabindex="-1"
        class="relative w-[min(440px,100%)] rounded-lg border border-border bg-surface p-5 shadow-lg focus:outline-none"
        @keydown="onKeydown"
      >
        <h2 class="text-[15px] font-semibold">{{ title }}</h2>
        <p v-if="message" class="mt-2 text-[13px] whitespace-pre-line text-ink-2">{{ message }}</p>
        <p v-if="detail" class="mono mt-2 rounded-sm bg-surface-2 p-2 text-ink-2">{{ detail }}</p>

        <div v-if="requireText" class="mt-3">
          <label class="mb-1.5 block text-xs text-ink-2">
            请输入 <span class="mono">{{ requireText }}</span> 以确认
          </label>
          <input
            v-model="typed"
            class="h-8 w-full rounded-md border border-border-strong bg-surface px-2 text-[13px] focus:border-brand focus:outline-none"
          />
        </div>

        <div class="mt-5 flex justify-end gap-2">
          <button
            class="transition-base h-8 rounded-md border border-border-strong px-3 text-[13px] text-ink-2 hover:bg-surface-2 disabled:opacity-60"
            :disabled="busy"
            @click="emit('cancel')"
          >
            {{ cancelText }}
          </button>
          <button
            class="transition-base flex h-8 items-center gap-1.5 rounded-md px-3 text-[13px] font-medium disabled:opacity-60"
            :class="danger ? 'bg-danger text-white' : 'bg-brand text-on-brand hover:bg-brand-hover'"
            :disabled="!canConfirm()"
            @click="emit('confirm')"
          >
            <Loader2 v-if="busy" :size="14" class="animate-spin" />
            {{ confirmText }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
