import { ref } from "vue";

export type ToastKind = "success" | "info" | "error";

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
  /** 错误提示不自动消失，必须由用户关闭。 */
  sticky: boolean;
}

const items = ref<Toast[]>([]);
let seq = 0;

const AUTO_DISMISS_MS = 2500;

export function useToast() {
  function push(kind: ToastKind, message: string, sticky = false): number {
    const id = ++seq;
    items.value.push({ id, kind, message, sticky });
    if (!sticky) {
      window.setTimeout(() => dismiss(id), AUTO_DISMISS_MS);
    }
    return id;
  }

  function dismiss(id: number): void {
    items.value = items.value.filter((t) => t.id !== id);
  }

  return {
    items,
    push,
    dismiss,
    success: (message: string) => push("success", message),
    info: (message: string) => push("info", message),
    error: (message: string) => push("error", message, true),
  };
}
