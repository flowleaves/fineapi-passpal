import { ref } from "vue";
import { api, setCsrfToken, ApiError } from "@/api/client";

const authenticated = ref(false);
const ready = ref(false);

export function useAuth() {
  /** 探测会话状态；有效会话时会补发 CSRF cookie。 */
  async function refresh(): Promise<boolean> {
    try {
      const data = await api.get<{ authenticated: boolean; csrf_token?: string }>("/api/auth/session", {
        allowUnauthorized: true,
      });
      authenticated.value = data.authenticated;
      if (data.csrf_token) setCsrfToken(data.csrf_token);
      return data.authenticated;
    } catch {
      authenticated.value = false;
      return false;
    } finally {
      ready.value = true;
    }
  }

  /** 登录；失败时抛出 ApiError，由页面显示内联错误。 */
  async function login(password: string): Promise<void> {
    const data = await api.post<{ csrf_token: string; expires_at: number }>(
      "/api/auth/login",
      { password },
      { allowUnauthorized: true },
    );
    setCsrfToken(data.csrf_token);
    authenticated.value = true;
  }

  async function logout(): Promise<void> {
    try {
      await api.post("/api/auth/logout");
    } catch (err) {
      // 会话已失效也视为登出成功
      if (!(err instanceof ApiError)) throw err;
    } finally {
      setCsrfToken("");
      authenticated.value = false;
    }
  }

  return { authenticated, ready, refresh, login, logout };
}
