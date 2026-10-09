import { ref, onScopeDispose } from "vue";
import type { SecretField } from "@/api/client";

/**
 * 已揭示秘密的生命周期管理。
 *
 * 规则（doc §9.5）：
 *  - 显示值最多保留 30 秒；
 *  - 隐藏、关闭面板、切换账号、页面进入后台、登出时立即清除；
 *  - 秘密只保存在内存，绝不写入 localStorage / sessionStorage。
 */

const SECRET_TTL_MS = 30_000;

/** 详情页打开期间的保留时长：够看完一屏，又不至于无限期驻留。 */
export const DETAIL_SECRET_TTL_MS = 10 * 60 * 1000;

interface Slot {
  value: string;
  timer: number;
}

const slots = ref<Record<string, Slot>>({});

function slotKey(accountId: number, field: SecretField): string {
  return `${accountId}:${field}`;
}

function drop(key: string): void {
  const slot = slots.value[key];
  if (!slot) return;
  window.clearTimeout(slot.timer);
  delete slots.value[key];
  // 触发响应式更新
  slots.value = { ...slots.value };
}

export function useSecrets() {
  function get(accountId: number, field: SecretField): string | undefined {
    return slots.value[slotKey(accountId, field)]?.value;
  }

  /** 写入一个已揭示的值；ttlMs 可覆盖默认的 30 秒（详情页用更长的保留时间）。 */
  function set(accountId: number, field: SecretField, value: string, ttlMs = SECRET_TTL_MS): void {
    const key = slotKey(accountId, field);
    drop(key);
    const timer = window.setTimeout(() => drop(key), ttlMs);
    slots.value = { ...slots.value, [key]: { value, timer } };
  }

  function clear(accountId: number, field: SecretField): void {
    drop(slotKey(accountId, field));
  }

  /** 切换账号或关闭面板时清掉该账号的全部已揭示值。 */
  function clearAccount(accountId: number): void {
    for (const key of Object.keys(slots.value)) {
      if (key.startsWith(`${accountId}:`)) drop(key);
    }
  }

  function clearAll(): void {
    for (const key of Object.keys(slots.value)) drop(key);
    slots.value = {};
  }

  return { get, set, clear, clearAccount, clearAll };
}

// 页面进入后台或会话失效时统一清除。
if (typeof document !== "undefined") {
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      useSecrets().clearAll();
    }
  });
}

/** 组件卸载时清理自己的定时器。 */
export function useSecretsScopeCleanup(): void {
  onScopeDispose(() => useSecrets().clearAll());
}
