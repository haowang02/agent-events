// Shared state, API access, agents, time range and formatting.
import { el, svgEl, cssVar, seriesColor } from "./dom.js";

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
  try {
    localStorage.setItem(PREFIX + key, JSON.stringify(value));
  } catch {
    // Preferences are a convenience; the page works without them.
  }
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

export const AGENTS = {
  codex: "Codex",
  claude: "Claude Code",
  opencode: "OpenCode",
  antigravity: "Antigravity",
  gemini: "Gemini CLI",
  copilot: "Copilot CLI",
  kilo: "Kilo Code",
  qwen: "Qwen Code",
  amp: "Amp",
  codebuff: "Codebuff",
  droid: "Droid",
  goose: "Goose",
  grok: "Grok Build",
  hermes: "Hermes",
  kimi: "Kimi",
  openclaw: "OpenClaw",
  pi: "Pi",
  zcode: "ZCode"
};
export const agentName = (id) => AGENTS[id] || id || "未知";
export const agentIcon = (id) => svgEl("svg", { class: "agent-icon", "aria-hidden": "true" }, svgEl("use", { href: `agents.svg#${id in AGENTS ? id : "all"}` }));

// Agents with events take the categorical color slots in the order above;
// the name is always shown beside the color.
export function agentSlot(id) {
  const counts = state.server?.counts || {};
  const index = Object.keys(AGENTS).filter((agent) => counts[agent] > 0).indexOf(id);
  return index >= 0 && index < 7 ? index + 1 : null;
}
export const agentColor = (id) => (agentSlot(id) ? seriesColor(agentSlot(id)) : cssVar("--series-other"));

export const agentTag = (id) => el("span", { class: "agent-tag" }, agentIcon(id), el("span", { text: agentName(id) }));

export const TIME_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

export const RANGE_PRESETS = {
  today: "今天",
  yesterday: "昨天",
  "24h": "最近 24 小时",
  "7d": "最近 7 天",
  "30d": "最近 30 天",
  "90d": "最近 90 天",
  all: "全部时间"
};

// The selected time range in Unix milliseconds; 0 is unbounded.
export function selectedRange() {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const { mode, from, to } = state.range;
  if (mode === "today") return { from: today, to: 0 };
  if (mode === "yesterday") return { from: new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1).getTime(), to: today };
  if (mode === "all") return { from: 0, to: 0 };
  if (mode === "custom") return { from: new Date(from).getTime() || 0, to: new Date(to).getTime() || 0 };
  const [, count, unit] = /^(\d+)([hd])$/.exec(mode) || [null, 7, "d"];
  return { from: now.getTime() - count * (unit === "h" ? 3600000 : 86400000), to: 0 };
}

export function rangeParams() {
  const { from, to } = selectedRange();
  const params = new URLSearchParams({ tz: TIME_ZONE });
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

const dollars = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });
const smallDollars = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumSignificantDigits: 2 });
// Two decimals; below $0.1, two significant digits so small costs never read $0.00.
export function usd(value) {
  const n = Number(value || 0);
  return (n && Math.abs(n) < 0.0995 ? smallDollars : dollars).format(n);
}

export const rate = (value) => "$" + String(Number(Number(value || 0).toFixed(4)));
export const percent = (value, digits = 1) => (value == null || !Number.isFinite(value) ? "—" : Number(value).toFixed(digits) + "%");
export const tps = (value) => (value == null || !(value > 0) ? "—" : Number(value).toFixed(1) + " t/s");
export const promptTokens = (row) => row.input_tokens + row.cache_read_tokens + row.cache_write_tokens;
export const cacheRate = (row) => (promptTokens(row) ? (row.cache_read_tokens * 100) / promptTokens(row) : null);

export function latency(ms) {
  if (!(ms > 0)) return "—";
  if (ms < 1000) return Math.round(ms) + "ms";
  if (ms < 60000) return (ms / 1000).toFixed(ms < 10000 ? 2 : 1) + "s";
  const seconds = Math.round(ms / 1000);
  return Math.floor(seconds / 60) + "m" + String(seconds % 60).padStart(2, "0") + "s";
}

const pad = (n) => String(n).padStart(2, "0");
export const monthDay = (at) => `${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
export const hourMinute = (at) => `${pad(at.getHours())}:${pad(at.getMinutes())}`;
export const dateText = (at) => `${at.getFullYear()}-${monthDay(at)}`;
export const clockTime = (value) => {
  const at = new Date(value);
  return `${hourMinute(at)}:${pad(at.getSeconds())}`;
};
export const dateTime = (ms) => `${dateText(new Date(ms))} ${clockTime(ms)}`;

export function rangeLabel({ mode, from, to }) {
  const at = (value) => `${dateText(new Date(value))} ${hourMinute(new Date(value))}`;
  return RANGE_PRESETS[mode] || `${at(from)} — ${at(to)}`;
}

// A span of time naming its day once when both ends share it.
export function timeSpan(from, to, time = clockTime) {
  const a = new Date(from), b = new Date(to), day = monthDay(a);
  return `${day} ${time(a)} – ${monthDay(b) === day ? "" : monthDay(b) + " "}${time(b)}`;
}

const collator = new Intl.Collator("zh-CN", { numeric: true });

// Text compares by locale; missing values sort last in either direction.
export function compareValues(a, b, direction = 1) {
  if (a == null || b == null) return (a == null) - (b == null);
  return direction * (typeof a === "string" ? collator.compare(a, b) : a - b);
}

export function shortPath(path) {
  const home = state.server?.home;
  if (!path || !home) return path || "";
  return path === home || path.startsWith(home + "/") ? "~" + path.slice(home.length) : path;
}

export const projectName = (path) => shortPath(path).split("/").filter(Boolean).pop() || "";

export function priceSource(price) {
  const source = price.source === "custom" ? "自定义价格" : `models.dev · ${price.provider}/${price.model_id}`;
  return price.as ? `按 ${price.as} 计价 · ${source}` : source;
}

export const costNotes = (cost) => [cost.long_context && "长上下文", cost.multiplier && `优先处理 ×${cost.multiplier}`].filter(Boolean);

// Joins two pieces of text, spacing Chinese from Latin letters and digits.
export function joinText(a, b) {
  const latin = (char) => /[A-Za-z0-9]/.test(char || "");
  return a + (latin(a.at(-1)) !== latin(b[0]) ? " " : "") + b;
}

// Dimensions that split usage, and how their keys read.
export const DIMENSIONS = { models: "模型", agents: "Agent", projects: "项目", providers: "提供商" };

export function dimensionLabel(dimension, key) {
  if (dimension === "agents") return agentName(key);
  if (dimension === "projects") return projectName(key) || "未知项目";
  return key || (dimension === "models" ? "未知模型" : "未知");
}
