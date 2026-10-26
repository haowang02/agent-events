// Shared state, API access, agents, time range and formatting.
import { el } from "./dom.js";

const PREFIX = "agent-events:";

export function pref(key, fallback) {
  try {
    const value = localStorage.getItem(PREFIX + key);
    return value == null ? fallback : JSON.parse(value);
  } catch {
    return fallback;
  }
}

export function savePref(key, value) {
  localStorage.setItem(PREFIX + key, JSON.stringify(value));
}

// server is the latest state pushed by /api/stream.
export const state = {
  agent: pref("agent", ""),
  tab: pref("tab", "events"),
  range: pref("range", { mode: "7d" }),
  server: null
};

export async function api(method, path, body) {
  const response = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined
  });
  if (response.status === 204) return null;
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error || "HTTP " + response.status);
  return value;
}

// Agent colors take categorical slots 1–4 in their validated order; the agent
// name is always shown beside the color.
export const AGENTS = {
  claude: { name: "Claude Code", slot: 2 },
  codex: { name: "Codex", slot: 1 },
  opencode: { name: "OpenCode", slot: 3 },
  antigravity: { name: "Antigravity", slot: 4 }
};
export const AGENT_ORDER = ["claude", "codex", "opencode", "antigravity"];
export const agentName = (id) => AGENTS[id]?.name || id || "未知";
export const agentColor = (id) => `var(--series-${AGENTS[id]?.slot || "other"})`;

export function agentTag(id) {
  return el("span", { class: "agent-tag" }, el("i", { class: "agent-dot", style: { "--dot": agentColor(id) } }), agentName(id));
}

// The selected time range in Unix milliseconds; 0 is unbounded.
export function selectedRange() {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const { mode, from, to } = state.range;
  if (mode === "today") return { from: today, to: 0 };
  if (mode === "yesterday") return { from: today - 86400000, to: today };
  if (mode === "all") return { from: 0, to: 0 };
  if (mode === "custom") return { from: new Date(from).getTime() || 0, to: new Date(to).getTime() || 0 };
  const [, count, unit] = /^(\d+)([hd])$/.exec(mode) || [null, 7, "d"];
  return { from: now.getTime() - count * (unit === "h" ? 3600000 : 86400000), to: 0 };
}

export function rangeParams() {
  const { from, to } = selectedRange();
  const params = new URLSearchParams();
  if (state.agent) params.set("agent", state.agent);
  if (from) params.set("from", String(from));
  if (to) params.set("to", String(to));
  return params;
}

const numberFormat = new Intl.NumberFormat("zh-CN");
export const int = (value) => numberFormat.format(Math.round(Number(value || 0)));

export function tokens(value) {
  const n = Number(value || 0);
  const compact = (scaled, suffix) => String(Number(scaled.toFixed(scaled >= 100 ? 1 : 2))) + suffix;
  if (n >= 1e9) return compact(n / 1e9, "B");
  if (n >= 1e6) return compact(n / 1e6, "M");
  if (n >= 1e3) return compact(n / 1e3, "K");
  return String(Math.round(n));
}

export function usd(value) {
  const n = Number(value || 0);
  if (n === 0) return "$0";
  if (n < 0.01) return "$" + n.toFixed(4);
  if (n < 100) return "$" + n.toFixed(3);
  return "$" + numberFormat.format(Math.round(n * 100) / 100);
}

export const rate = (value) => "$" + String(Number(Number(value || 0).toFixed(4)));
export const percent = (value, digits = 1) => Number(value || 0).toFixed(digits) + "%";
export const tps = (value) => (value == null ? "—" : Number(value).toFixed(1) + " t/s");

export function latency(ms) {
  if (!(ms > 0)) return "—";
  if (ms < 1000) return Math.round(ms) + "ms";
  if (ms < 60000) return (ms / 1000).toFixed(ms < 10000 ? 2 : 1) + "s";
  return Math.floor(ms / 60000) + "m" + String(Math.round((ms % 60000) / 1000)).padStart(2, "0") + "s";
}

const dateFormat = new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" });
const timeFormat = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
export const clockTime = (value) => timeFormat.format(new Date(value));

export function dateTimeLines(ms) {
  return [el("div", { class: "cell-line mono", text: clockTime(ms) }), el("div", { class: "cell-sub", text: dateFormat.format(new Date(ms)) })];
}

export function shortPath(path) {
  const home = state.server?.home;
  if (!path || !home) return path || "";
  return path === home || path.startsWith(home + "/") ? "~" + path.slice(home.length) : path;
}

export const projectName = (path) => shortPath(path).split("/").filter(Boolean).pop() || "";

export const cssVar = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
export const seriesColor = (slot) => cssVar(`--series-${slot}`);

export function priceSource(price) {
  const source = price.source === "custom" ? "自定义价格" : `models.dev · ${price.provider}/${price.model_id}`;
  return price.as ? `按 ${price.as} 计价 · ${source}` : source;
}
