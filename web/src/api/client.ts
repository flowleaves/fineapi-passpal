/**
 * PassPal API 客户端。
 *
 * 约定：
 *  - 所有请求同源，凭据走 Cookie；
 *  - 写请求带 X-CSRF-Token（双提交）；
 *  - 秘密值只通过 reveal 接口逐字段获取，绝不批量预取。
 */

export interface ApiErrorBody {
  error: { code: string; message: string };
}

export class ApiError extends Error {
  status: number;
  code: string;
  retryAfter: number;

  constructor(status: number, code: string, message: string, retryAfter = 0) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

let csrfToken = "";

export function setCsrfToken(token: string): void {
  csrfToken = token;
}

export function getCsrfToken(): string {
  if (csrfToken) return csrfToken;
  return readCookie("pp_csrf");
}

function readCookie(name: string): string {
  const prefix = name + "=";
  for (const part of document.cookie.split(";")) {
    const item = part.trim();
    if (item.startsWith(prefix)) {
      return decodeURIComponent(item.slice(prefix.length));
    }
  }
  return "";
}

interface RequestOptions {
  /** 跳过 401 的全局处理（登录与会话探测用）。 */
  allowUnauthorized?: boolean;
  signal?: AbortSignal;
  body?: unknown;
}

async function request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {};
  let body: BodyInit | undefined;

  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(options.body);
  }

  if (method !== "GET" && method !== "HEAD") {
    const token = getCsrfToken();
    if (token) headers["X-CSRF-Token"] = token;
  }

  const response = await fetch(path, {
    method,
    headers,
    body,
    credentials: "same-origin",
    signal: options.signal,
  });

  const text = await response.text();
  let parsed: unknown = undefined;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = undefined;
    }
  }

  if (!response.ok) {
    const errBody = (parsed as ApiErrorBody | undefined)?.error;
    const retryAfter = Number(response.headers.get("Retry-After") || 0);
    throw new ApiError(
      response.status,
      errBody?.code ?? "unknown_error",
      errBody?.message ?? `请求失败（${response.status}）`,
      Number.isFinite(retryAfter) ? retryAfter : 0,
    );
  }

  if (parsed && typeof parsed === "object" && "data" in parsed) {
    return (parsed as { data: T }).data;
  }
  return parsed as T;
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>("GET", path, options),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>("POST", path, { ...options, body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>("PUT", path, { ...options, body }),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>("PATCH", path, { ...options, body }),
  del: <T>(path: string, options?: RequestOptions) => request<T>("DELETE", path, options),
};

// ── 领域类型 ──────────────────────────────────────────────

export interface Project {
  id: number;
  name: string;
  description: string;
  icon: string;
  sort_order: number;
  account_count: number;
  tag_count: number;
  created_at: number;
  updated_at: number;
}

export interface Tag {
  id: number;
  project_id: number;
  name: string;
  color: string;
  sort_order: number;
  account_count: number;
  created_at: number;
  updated_at: number;
}

export interface Account {
  id: number;
  project_id: number;
  email: string;
  username: string;
  status: string;
  extra_json: string;
  revision: number;
  tags: Tag[];
  tag_ids: number[];
  has_password: boolean;
  has_backup_email: boolean;
  has_f2a: boolean;
  has_json: boolean;
  has_notes: boolean;
  has_refresh_token: boolean;
  has_sms_link: boolean;
  /** 以下三项随列表一并返回明文，便于列表内直接查看与复制。 */
  f2a?: string;
  refresh_token?: string;
  sms_link?: string;
  created_at: number;
  updated_at: number;
  last_used_at: number | null;
}

export interface ListResult<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
}

export interface ImportSummary {
  total: number;
  valid_new: number;
  duplicates: number;
  invalid: number;
  password_empty: number;
}

export interface CommitSummary {
  total: number;
  created: number;
  updated: number;
  skipped: number;
  invalid: number;
  duplicates: number;
}

export interface PreviewRow {
  row_id: number;
  line: number;
  email: string;
  username: string;
  has_password: boolean;
  has_backup_email: boolean;
  has_f2a: boolean;
  has_json: boolean;
  has_notes: boolean;
  state: "new" | "duplicate" | "invalid";
  issue_codes: { code: string; field?: string; message: string }[];
  skip: boolean;
  duplicate_count?: number;
}

export interface Preview {
  preview_id: string;
  expires_at: number;
  preview_revision: number;
  commit_key: string;
  format?: string;
  summary: ImportSummary;
  strategy: string;
  items: PreviewRow[];
  page: number;
  page_size: number;
  total: number;
}

export interface RefreshTokenEntry {
  email: string;
  refresh_token?: string;
}

export interface BackupInfo {
  name: string;
  size: number;
  created_at: number;
  /** 是否自带密钥（归档格式）。旧版 .sqlite 快照为 false，恢复时需另外提供密钥。 */
  self_contained: boolean;
}

export interface Stats {
  projects: number;
  tags: number;
  accounts: number;
  added_today: number;
}

export type SecretField =
  | "password"
  | "backup_email"
  | "f2a"
  | "credential_json"
  | "notes"
  | "refresh_token"
  | "sms_link";

/** 账号状态。当前只暴露两个值，枚举保留扩展空间。 */
export const ACCOUNT_STATUS = {
  normal: "正常",
  abnormal: "异常",
} as const;

export type AccountStatus = keyof typeof ACCOUNT_STATUS;

export function statusLabel(status: string): string {
  return ACCOUNT_STATUS[status as AccountStatus] ?? status;
}
