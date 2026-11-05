import { $, icon, svgEl, tip, hideTip } from "./dom.js";
import { savePref, state, clockTime, int, agentName, AGENTS } from "./core.js";
import { initRange } from "./date-range.js";
import { select } from "./controls.js";
import { initEvents, loadEvents, resetEventPage } from "./events.js";
import { initAnalysis, loadAnalysis, renderAnalysis } from "./analysis.js";
import { initPrices, loadPrices } from "./prices.js";

const TABS = { events: loadEvents, analysis: loadAnalysis, prices: loadPrices };

function reload() {
  TABS[state.tab]().catch((error) => {
    console.error(error);
    $("sync-status").textContent = "出错：" + error.message;
  });
}

const agentPicker = select({ label: "Agent", className: "agent-select", onChange: (id) => {
  state.agent = id;
  savePref("agent", id);
  reloadScope();
} });

function renderAgents() {
  const counts = state.server?.counts || {};
  const total = Object.values(counts).reduce((sum, count) => sum + count, 0);
  const agents = Object.keys(AGENTS)
    .filter((id) => counts[id] > 0 || id === state.agent)
    .sort((a, b) => (counts[b] || 0) - (counts[a] || 0));
  agentPicker.update(["", ...agents].map((id) => ({
    value: id,
    label: id ? agentName(id) : "全部 Agent",
    detail: int(id ? counts[id] : total),
    icon: () => svgEl("svg", { class: "agent-icon", "aria-hidden": "true" }, svgEl("use", { href: `agents.svg#${id || "all"}` }))
  })), state.agent);
}

function renderSyncStatus() {
  const { ready, error, updated_at: updated } = state.server;
  const status = $("sync-status");
  status.textContent = !ready ? "正在扫描日志…" : error ? "部分日志读取失败" : updated ? `${clockTime(updated)} 更新` : "";
  tip(status, error);
}

// The server pushes its state on connect and whenever the data changes; a new
// version reloads the current tab.
function connect() {
  const stream = new EventSource("/api/stream");
  stream.onmessage = (message) => {
    const next = JSON.parse(message.data);
    const changed = next.version !== state.server?.version;
    state.server = next;
    renderAgents();
    renderSyncStatus();
    if (changed) reload();
  };
}

function selectTab(tab) {
  state.tab = TABS[tab] ? tab : "events";
  savePref("tab", state.tab);
  for (const button of document.querySelectorAll("[data-tab]")) {
    const active = button.dataset.tab === state.tab;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
  }
  for (const name of Object.keys(TABS)) $("tab-" + name).classList.toggle("hidden", name !== state.tab);
  $("range-control").classList.toggle("hidden", state.tab === "prices");
  hideTip();
  if (state.server) reload();
}

function initTheme() {
  const button = $("theme-button");
  const render = () => button.replaceChildren(icon(document.documentElement.dataset.theme === "dark" ? "sun" : "moon"));
  render();
  button.addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("agent-events:theme", next);
    render();
    if (state.tab === "analysis") renderAnalysis();
  });
}

function reloadScope() {
  resetEventPage();
  reload();
}

initTheme();
$("agent-picker").append(agentPicker.element);
renderAgents();
initRange(reloadScope);
initEvents();
initAnalysis();
initPrices();
for (const button of document.querySelectorAll("[data-tab]")) button.addEventListener("click", () => selectTab(button.dataset.tab));
selectTab(state.tab);
connect();
