/**
 * 时间显示工具。
 *
 * 后端所有时间字段（accounts.created_at / updated_at / last_used_at、
 * backups.created_at 等）都是 Unix 秒的 int64，这里统一按浏览器本地时区
 * 渲染成 `YYYY-MM-DD HH:mm`，保证列表、详情、设置页三处格式完全一致。
 *
 * 之所以抽成独立模块：此前 AccountDrawer 与 SettingsPage 各有一份同名 `fmt`，
 * 新增列表列时若再抄一份，后续改格式必然漏改其中一处。
 */

/** 把 Unix 秒格式化为 `YYYY-MM-DD HH:mm`（本地时区）。无效值返回占位符 `—`。 */
export function formatDateTime(ts: number | null | undefined): string {
  if (ts === null || ts === undefined || !Number.isFinite(ts) || ts <= 0) return "—";
  const d = new Date(ts * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** 只要日期的短格式 `YYYY-MM-DD`。空间紧张时用。 */
export function formatDate(ts: number | null | undefined): string {
  if (ts === null || ts === undefined || !Number.isFinite(ts) || ts <= 0) return "—";
  const d = new Date(ts * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/**
 * 悬停提示用的完整时间（含秒 + 时区偏移）。
 * 表格列只显示到分钟，鼠标悬停时给出精确值，便于核对。
 */
export function formatDateTimeFull(ts: number | null | undefined): string {
  if (ts === null || ts === undefined || !Number.isFinite(ts) || ts <= 0) return "";
  const d = new Date(ts * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  const offsetMin = -d.getTimezoneOffset();
  const sign = offsetMin >= 0 ? "+" : "-";
  const abs = Math.abs(offsetMin);
  const tz = `UTC${sign}${p(Math.floor(abs / 60))}:${p(abs % 60)}`;
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())} ${tz}`;
}

/** 供 `<time datetime="...">` 使用的 ISO 8601 值（机器可读）。 */
export function toIsoDateTime(ts: number | null | undefined): string {
  if (ts === null || ts === undefined || !Number.isFinite(ts) || ts <= 0) return "";
  return new Date(ts * 1000).toISOString();
}
