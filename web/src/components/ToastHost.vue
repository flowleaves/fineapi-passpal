<script setup lang="ts">
import { Check, Info, AlertTriangle, X } from "lucide-vue-next";
import { useToast } from "@/composables/useToast";

const { items, dismiss } = useToast();
</script>

<template>
  <div
    class="pointer-events-none fixed inset-x-0 top-0 z-[300] flex flex-col items-end gap-2 p-4"
    aria-live="polite"
    aria-atomic="false"
  >
    <div
      v-for="t in items"
      :key="t.id"
      class="pointer-events-auto flex max-w-[min(420px,90vw)] items-start gap-2 rounded-md bg-ink px-3 py-2 text-[13px] text-bg shadow-md"
      role="status"
    >
      <Check v-if="t.kind === 'success'" :size="15" class="mt-0.5 shrink-0" />
      <Info v-else-if="t.kind === 'info'" :size="15" class="mt-0.5 shrink-0" />
      <AlertTriangle v-else :size="15" class="mt-0.5 shrink-0" />
      <span class="min-w-0 break-words">{{ t.message }}</span>
      <button
        v-if="t.sticky"
        class="ml-1 shrink-0 rounded-sm opacity-70 hover:opacity-100"
        aria-label="关闭提示"
        @click="dismiss(t.id)"
      >
        <X :size="14" />
      </button>
    </div>
  </div>
</template>
