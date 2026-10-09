<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Eye, EyeOff, Loader2 } from "lucide-vue-next";
import { useAuth } from "@/composables/useAuth";
import { ApiError } from "@/api/client";

const router = useRouter();
const route = useRoute();
const { login } = useAuth();

const password = ref("");
const show = ref(false);
const submitting = ref(false);
const error = ref("");
const inputRef = ref<HTMLInputElement | null>(null);

onMounted(() => inputRef.value?.focus());

async function submit(): Promise<void> {
  if (submitting.value || password.value.length === 0) return;
  error.value = "";
  submitting.value = true;
  try {
    await login(password.value);
    const redirect = route.query.redirect;
    await router.replace(typeof redirect === "string" && redirect.startsWith("/") ? redirect : "/app");
  } catch (err) {
    // 失败保留输入位置，错误内联显示，不使用会自动消失的 Toast。
    error.value = err instanceof ApiError ? err.message : "登录失败，请重试";
    inputRef.value?.focus();
    inputRef.value?.select();
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="grid min-h-dvh place-items-center p-4">
    <div class="w-[min(360px,100%)] rounded-lg border border-border bg-surface p-8 shadow-md">
      <div class="text-center">
        <div class="text-[28px] leading-none">🌱</div>
        <h1 class="mt-3 text-xl font-semibold">PassPal</h1>
        <p class="mt-1 text-[13px] text-ink-3">个人凭据库</p>
      </div>

      <form class="mt-7" novalidate @submit.prevent="submit">
        <label for="admin-password" class="mb-1.5 block text-xs font-medium text-ink-2">
          管理员密码
        </label>
        <div class="relative">
          <input
            id="admin-password"
            ref="inputRef"
            v-model="password"
            :type="show ? 'text' : 'password'"
            class="transition-base h-[38px] w-full rounded-md border border-border-strong bg-surface px-3 pr-[46px] text-[16px] focus:border-brand focus:ring-2 focus:ring-brand-soft focus:outline-none"
            autocomplete="current-password"
            :disabled="submitting"
          />
          <button
            type="button"
            class="absolute top-1.5 right-1.5 grid h-7 w-7 place-items-center rounded-sm text-ink-2 hover:bg-surface-2"
            :aria-label="show ? '隐藏密码' : '显示密码'"
            @click="show = !show"
          >
            <EyeOff v-if="show" :size="15" />
            <Eye v-else :size="15" />
          </button>
        </div>

        <button
          type="submit"
          class="transition-base mt-4 flex h-[38px] w-full items-center justify-center gap-2 rounded-md bg-brand text-[13px] font-medium text-on-brand hover:bg-brand-hover disabled:opacity-60"
          :disabled="submitting || password.length === 0"
        >
          <Loader2 v-if="submitting" :size="15" class="animate-spin" />
          {{ submitting ? "正在解锁…" : "解锁系统" }}
        </button>

        <p v-if="error" role="alert" class="mt-3.5 text-[13px] text-danger">
          {{ error }}
        </p>
      </form>
    </div>
  </div>
</template>
