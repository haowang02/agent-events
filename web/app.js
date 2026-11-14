import { $, el, icon } from "./dom.js";
import { tip, hideTip } from "./tooltip.js";
import { api, savePref, state, rangeParams, int, clockTime, dateTime, agentName, agentIcon, AGENTS } from "./core.js";
import { initRange } from "./date-range.js";
import { select } from "./controls.js";
import { initEvents, loadEvents, resetEvents } from "./events.js";
import { initAnalysis, loadAnalysis, renderAnalysis } from "./analysis.js";
import { initPrices, loadPrices, setPriceStatus } from "./prices.js";

const TABS = { events: loadEvents, analysis: loadAnalysis, prices: loadPrices };

function showNotice(error) {
  console.error(error);
  $("notice").replaceChildren(icon("circle-alert"), el("span", { text: "加载失败：" + error.message }));
  $("notice").classList.remove("hidden");
}

// The events tab shows how many events the selected agent made in the range.
let countSequence = 0;
async function loadCount() {
  const sequence = ++countSequence;
  const { events } = await api("GET", "/api/count?" + rangeParams());
  if (sequence !== countSequence) return;
  $("events-count").textContent = int(events);
  $("events-count").classList.remove("hidden");
}

// Loads the current tab. Pushed updates coalesce: while one load runs, later
// pushes collapse into a single follow-up load. interactive marks loads the
// user asked for, which show progress.
let running = null, again = false;
function reload(options) {
  return Promise.all([TABS[state.tab](options), loadCount()]).then(() => $("notice").classList.add("hidden"), showNotice);
}
function refresh() {
  if (running) {
    again = true;
    return;
  }
  running = reload().finally(() => {
    running = null;
    if (again) {
      again = false;
      refresh();
    }
  });
}

const agentPicker = select({
  label: "Agent",
  searchable: true,
  className: "agent-select",
  onChange: (id) => {
    state.agent = id;
    savePref("agent", id);
    reloadScope();
  }
});

function renderAgents() {
  const counts = state.server?.counts || {};
  const agents = Object.keys(AGENTS)
    .filter((id) => counts[id] > 0 || id === state.agent)
    .sort((a, b) => (counts[b] || 0) - (counts[a] || 0));
  agentPicker.update(
    ["", ...agents].map((id) => ({
      value: id,
      label: id ? agentName(id) : "全部 Agent",
      icon: () => (id ? agentIcon(id) : icon("layout-grid", { class: "agent-icon" }))
    })),
    state.agent
  );
}

// The sync chip: scanning, live with the last update, partly failed, or
// disconnected while the stream reconnects.
function renderSync(connected = true) {
  const chip = $("sync-status");
  const server = state.server;
  let kind, content;
  if (!connected) [kind, content] = ["bad", [icon("circle-alert"), "连接已断开"]];
  else if (!server) [kind, content] = ["", [icon("rotate-cw", { class: "spin" }), "正在连接…"]];
  else if (!server.ready) [kind, content] = ["", [icon("rotate-cw", { class: "spin" }), "正在扫描日志…"]];
  else if (server.error) [kind, content] = ["warn", [icon("circle-alert"), "部分日志读取失败"]];
  else [kind, content] = ["live", [el("i", { class: "live-dot" }), server.updated_at ? clockTime(server.updated_at) + " 更新" : "实时同步"]];
  chip.className = "sync-chip " + kind;
  chip.replaceChildren(...content);
  tip(chip, () => [
    el("div", { class: "tip-title", text: connected ? "数据同步" : "正在重新连接本地服务" }),
    el(
      "dl",
      { class: "tip-fields" },
      server?.updated_at && [el("dt", { text: "最近更新" }), el("dd", { text: dateTime(new Date(server.updated_at).getTime()) })],
      server?.data_dir && [el("dt", { text: "数据目录" }), el("dd", { class: "mono", text: server.data_dir })],
      server?.error && [el("dt", { text: "读取失败" }), el("dd", { class: "mono", text: server.error })]
    )
  ]);
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
    renderSync();
    if (changed) refresh();
  };
  stream.onerror = () => renderSync(stream.readyState === EventSource.OPEN);
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

const darkScheme = matchMedia("(prefers-color-scheme: dark)");
function initTheme() {
  let theme = localStorage.getItem("agent-events:theme");
  if (!["dark", "light"].includes(theme)) theme = "auto";
  const apply = () => {
    const next = theme === "auto" ? (darkScheme.matches ? "dark" : "light") : theme;
    if (document.documentElement.dataset.theme === next) return;
    document.documentElement.dataset.theme = next;
    if (state.tab === "analysis") renderAnalysis();
  };
  const themeSelect = select({
    label: "主题",
    value: theme,
    align: "end",
    showSelectedDetail: false,
    options: [
      { value: "auto", label: "跟随系统", shortLabel: "", icon: () => icon("sun-moon") },
      { value: "light", label: "浅色", shortLabel: "", icon: () => icon("sun") },
      { value: "dark", label: "深色", shortLabel: "", icon: () => icon("moon") }
    ],
    onChange: (value) => {
      theme = value;
      try {
        if (value === "auto") localStorage.removeItem("agent-events:theme");
        else localStorage.setItem("agent-events:theme", value);
      } catch {
        // The choice then lasts for this page only.
      }
      apply();
    }
  });
  themeSelect.button.classList.add("theme-button");
  themeSelect.button.querySelector(".select-chevron").remove();
  $("theme-control").append(themeSelect.element);
  darkScheme.addEventListener("change", apply);
  apply();
}

function reloadScope() {
  resetEvents();
  reload({ interactive: true });
}

initTheme();
$("agent-picker").append(agentPicker.element);
renderAgents();
renderSync();
initRange(reloadScope);
initEvents();
initAnalysis();
initPrices();
for (const button of document.querySelectorAll("[data-tab]")) button.addEventListener("click", () => selectTab(button.dataset.tab));
document.addEventListener("navigate", ({ detail }) => {
  if (detail.priceStatus) setPriceStatus(detail.priceStatus);
  selectTab(detail.tab);
});
selectTab(state.tab);
connect();
