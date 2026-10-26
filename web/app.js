import { $, el, icon, tip, hideTip } from "./dom.js";
import { savePref, state, int, agentColor, agentName, AGENTS, clockTime } from "./core.js";
import { initEvents, loadEvents, resetEventPage } from "./events.js";
import { loadAnalysis, renderAnalysis } from "./analysis.js";
import { initPrices, loadPrices } from "./prices.js";

const TABS = { events: loadEvents, analysis: loadAnalysis, prices: loadPrices };

function reload() {
  TABS[state.tab]().catch((error) => {
    console.error(error);
    $("sync-status").textContent = "出错：" + error.message;
  });
}

// Agents with events, most first, and the selected one even when it has none.
function renderAgents() {
  const counts = state.server?.counts || {};
  const total = Object.values(counts).reduce((sum, count) => sum + count, 0);
  const agents = Object.keys(AGENTS)
    .filter((id) => counts[id] > 0 || id === state.agent)
    .sort((a, b) => (counts[b] || 0) - (counts[a] || 0));
  $("agent-switch").replaceChildren(
    ...[["", "全部", total], ...agents.map((id) => [id, agentName(id), counts[id] || 0])].map(([id, name, count]) =>
      el(
        "button",
        {
          type: "button",
          class: id === state.agent ? "active" : "",
          "aria-pressed": String(id === state.agent),
          onclick: () => {
            if (id === state.agent) return;
            state.agent = id;
            savePref("agent", id);
            resetEventPage();
            renderAgents();
            reload();
          }
        },
        id && el("i", { class: "agent-dot", style: { "--dot": agentColor(id) } }),
        name,
        el("span", { class: "count", text: int(count) })
      )
    )
  );
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

const toLocalInput = (iso) => {
  const at = new Date(iso);
  return new Date(at.getTime() - at.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};

function initRange() {
  const mode = $("range-mode");
  const showCustom = () => {
    const custom = state.range.mode === "custom";
    $("range-custom").classList.toggle("hidden", !custom);
    if (custom) {
      $("range-from").value = toLocalInput(state.range.from);
      $("range-to").value = toLocalInput(state.range.to);
    }
  };
  const apply = (range) => {
    state.range = range;
    savePref("range", range);
    resetEventPage();
    showCustom();
    reload();
  };
  mode.value = state.range.mode;
  mode.addEventListener("change", () => {
    if (mode.value !== "custom") return apply({ mode: mode.value });
    const now = Date.now();
    apply({ mode: "custom", from: new Date(now - 7 * 86400000).toISOString(), to: new Date(now).toISOString() });
  });
  for (const id of ["range-from", "range-to"]) {
    $(id).addEventListener("change", () => {
      const from = new Date($("range-from").value);
      const to = new Date($("range-to").value);
      if (from < to) apply({ mode: "custom", from: from.toISOString(), to: to.toISOString() });
    });
  }
  showCustom();
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

Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
Chart.defaults.font.size = 12;
initTheme();
initRange();
initEvents();
initPrices();
for (const button of document.querySelectorAll("[data-tab]")) button.addEventListener("click", () => selectTab(button.dataset.tab));
selectTab(state.tab);
connect();
