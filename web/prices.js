// The model prices tab and the price dialog.
import { $, el, icon } from "./dom.js";
import { tip, hideTip } from "./tooltip.js";
import { segmented } from "./controls.js";
import { api, state, pref, savePref, int, rate, tokens, priceSource, agentName, dateTime } from "./core.js";
import { dataTable } from "./data-table.js";

const RATES = [["input", "输入"], ["output", "输出"], ["cache_read", "缓存读"], ["cache_write", "缓存写"]];
const STATUSES = { all: "全部", unpriced: "未定价", custom: "自定义", reference: "参考价" };
const view = {
  data: null,
  sequence: 0,
  filters: pref("price-filters", { query: "", status: "all" }),
  editing: null,
  onSaved: null,
  searchTimer: 0,
  searchSequence: 0
};
const statusSelect = segmented({ label: "定价状态", onChange: (status) => setFilters({ ...view.filters, status }) });

export async function loadPrices() {
  const sequence = ++view.sequence;
  const data = await api("GET", "/api/prices");
  if (sequence !== view.sequence) return;
  view.data = data;
  renderPrices();
}

export function setPriceStatus(status) {
  $("prices-query").value = "";
  setFilters({ query: "", status });
}

const ownCustom = (price) => price?.source === "custom" && !price.as;
const priceStatus = (price) => (!price ? "unpriced" : price.source === "custom" ? "custom" : "reference");

function sourceTag(price) {
  if (!price) return el("span", { class: "tag warn", text: "未定价" });
  return tip(el("span", { class: "tag " + (price.source === "custom" ? "info" : "ok"), text: price.source === "custom" ? "自定义" : "models.dev" }), priceSource(price));
}

const tiers = (price) => [price, price?.long_context].filter(Boolean);
const rateCell = (key) => (row) => (tiers(row.price).length ? tiers(row.price).map((tier) => el("div", { text: tier[key] == null ? "—" : rate(tier[key]) })) : "—");

const COLUMNS = [
  {
    id: "model",
    label: "模型",
    sort: (row) => row.model,
    cell: (row) => [el("div", { class: "cell-line mono", text: row.model }), el("div", { class: "cell-sub", text: row.agents.map(agentName).join(" · ") })]
  },
  { id: "requests", label: "请求", num: true, sort: (row) => row.requests, cell: (row) => int(row.requests) },
  { id: "total_tokens", label: "Token", num: true, sort: (row) => row.total_tokens, cell: (row) => tokens(row.total_tokens) },
  {
    id: "context",
    label: "上下文",
    className: "muted",
    cell: (row) => row.price?.long_context && [el("div", { text: "≤ " + tokens(row.price.long_context.threshold) }), el("div", { text: "> " + tokens(row.price.long_context.threshold) })]
  },
  ...RATES.map(([key, label]) => ({ id: key, label: label + " $/M", num: true, sort: (row) => row.price?.[key] ?? null, cell: rateCell(key) })),
  {
    id: "source",
    label: "来源",
    sort: (row) => ["unpriced", "custom", "reference"].indexOf(priceStatus(row.price)),
    cell: (row) => [sourceTag(row.price), row.price?.as && el("div", { class: "cell-sub", text: `按 ${row.price.as} 计价` })]
  },
  {
    id: "actions",
    label: "",
    className: "actions-cell",
    cell: (row) =>
      el(
        "div",
        { class: "price-actions" },
        el("button", { class: "link", type: "button", text: row.price ? "编辑" : "设置价格", onclick: () => openPriceDialog(row) }),
        ownCustom(row.price) && el("button", { class: "link danger", type: "button", text: "重置", onclick: () => resetPrice(row) })
      )
  }
];

function renderPrices() {
  if (!view.data) return;
  hideTip();
  const { models, catalog } = view.data;
  tip($("catalog-refresh"), `models.dev 参考价：${catalog.fetched_at ? dateTime(new Date(catalog.fetched_at).getTime()) + " 更新" : "未获取"}` + (catalog.error ? `\n上次更新失败：${catalog.error}` : ""));
  const scope = models.filter((row) => !state.agent || row.agents.includes(state.agent));
  const words = view.filters.query.toLowerCase().trim().split(/\s+/).filter(Boolean);
  const matches = scope.filter(({ model, price }) => {
    const text = [model, price?.as, price?.provider, price?.model_id].join(" ").toLowerCase();
    return words.every((word) => text.includes(word));
  });
  const status = STATUSES[view.filters.status] ? view.filters.status : "all";
  const counts = { all: matches.length, unpriced: 0, custom: 0, reference: 0 };
  for (const row of matches) counts[priceStatus(row.price)]++;
  statusSelect.update(Object.entries(STATUSES).map(([value, label]) => ({ value, label, detail: int(counts[value]) })), status);
  const rows = matches.filter((row) => status === "all" || priceStatus(row.price) === status);
  $("prices-table").replaceChildren(
    dataTable({ id: "prices-grid", columns: COLUMNS, rows, sortPref: "price-sort", initialSort: { key: "requests", order: "desc" } })
  );
  $("prices-empty").classList.toggle("hidden", rows.length > 0);
  const EMPTY = { unpriced: "所有模型都已定价", custom: "没有设置自定义价格的模型" };
  $("prices-empty").textContent = !scope.length ? "暂无模型记录" : (!words.length && EMPTY[status]) || "没有符合条件的模型";
  $("prices-note").textContent = rows.length === scope.length ? `共 ${int(scope.length)} 个模型` : `显示 ${int(rows.length)} / ${int(scope.length)} 个模型`;
}

function setFilters(filters) {
  view.filters = filters;
  savePref("price-filters", filters);
  $("prices-wrap").scrollTop = 0;
  renderPrices();
}

async function resetPrice(row) {
  const confirmed = await confirmAction({ title: "重置价格", message: `删除「${row.model}」的自定义价格？`, action: "重置" });
  if (!confirmed) return;
  try {
    await api("DELETE", "/api/prices?model=" + encodeURIComponent(row.model));
  } catch (error) {
    showNote(error.message);
  }
}

function showNote(message) {
  $("prices-error").textContent = message;
  $("prices-error").classList.toggle("hidden", !message);
}

// A themed confirmation; resolves true when the action is chosen.
export function confirmAction({ title, message, action }) {
  const dialog = $("confirm-dialog");
  $("confirm-title").textContent = title;
  $("confirm-message").textContent = message;
  $("confirm-ok").textContent = action;
  dialog.returnValue = "";
  dialog.showModal();
  $("confirm-cancel").focus();
  return new Promise((resolve) => dialog.addEventListener("close", () => resolve(dialog.returnValue === "ok"), { once: true }));
}

function fillRates(rates) {
  const long = rates?.long_context;
  for (const [key] of RATES) {
    $("price-" + key).value = rates?.[key] ?? "";
    $("price-long-" + key).value = long?.[key] ?? "";
  }
  $("price-threshold").value = long?.threshold ?? "";
  requireLongRates();
}

// A long-context threshold needs its own input and output rates.
function requireLongRates() {
  const long = $("price-threshold").value !== "";
  $("price-long-input").required = $("price-long-output").required = long;
  $("price-dialog").classList.toggle("has-long", long);
}

function readRates() {
  const number = (id) => ($(id).value === "" ? null : Number($(id).value));
  const tier = (prefix) => Object.fromEntries(RATES.map(([key]) => [key, number(prefix + key)]));
  const rates = tier("price-");
  const threshold = number("price-threshold");
  if (threshold) rates.long_context = { threshold, ...tier("price-long-") };
  return rates;
}

// row is { model, price }; onSaved runs after the price is stored.
export function openPriceDialog(row, { onSaved = null } = {}) {
  hideTip();
  view.editing = row;
  view.onSaved = onSaved;
  $("price-model").textContent = row.model;
  $("price-current").textContent = row.price ? "当前：" + priceSource(row.price) : "当前未定价";
  $("price-error").classList.add("hidden");
  $("price-search").value = "";
  clearReferenceSearch();
  fillRates(row.price);
  $("price-dialog").showModal();
  $("price-search").focus();
}

function clearReferenceSearch() {
  clearTimeout(view.searchTimer);
  $("price-search-results").replaceChildren();
  return ++view.searchSequence;
}

async function searchReference(query, sequence) {
  let results;
  try {
    ({ results } = await api("GET", "/api/prices/search?limit=30&q=" + encodeURIComponent(query)));
  } catch (error) {
    if (sequence === view.searchSequence) $("price-search-results").replaceChildren(el("div", { class: "notice error", text: error.message }));
    return;
  }
  if (sequence !== view.searchSequence) return;
  $("price-search-results").replaceChildren(
    ...(results.length
      ? results.map((result) =>
          el(
            "button",
            {
              type: "button",
              onclick: () => {
                fillRates(result.rates);
                $("price-search").value = `${result.provider}/${result.model_id}`;
                clearReferenceSearch();
                $("price-input").focus();
              }
            },
            el("span", { class: "mono", text: `${result.provider}/${result.model_id}` }),
            el("span", { class: "muted small", text: `${rate(result.rates.input)} / ${rate(result.rates.output)}` })
          )
        )
      : [el("div", { class: "search-empty", text: "没有匹配的参考价" })])
  );
}

async function savePrice(event) {
  event.preventDefault();
  try {
    await api("PUT", "/api/prices", { model: view.editing.model, rates: readRates() });
    $("price-dialog").close();
    view.onSaved?.();
  } catch (error) {
    $("price-error").textContent = error.message;
    $("price-error").classList.remove("hidden");
  }
}

export function initPrices() {
  $("prices-status").append(statusSelect.element);
  $("prices-query").value = view.filters.query;
  $("prices-query").addEventListener("input", (event) => setFilters({ ...view.filters, query: event.target.value }));
  const refresh = $("catalog-refresh");
  refresh.replaceChildren(icon("rotate-cw"), "更新参考价");
  refresh.addEventListener("click", async () => {
    refresh.disabled = true;
    refresh.classList.add("busy");
    showNote("");
    try {
      await api("POST", "/api/prices/refresh");
    } catch (error) {
      showNote("更新参考价失败：" + error.message);
    } finally {
      refresh.disabled = false;
      refresh.classList.remove("busy");
    }
  });
  $("price-form").addEventListener("submit", savePrice);
  $("price-threshold").addEventListener("input", requireLongRates);
  $("price-cancel").addEventListener("click", () => $("price-dialog").close());
  $("price-dialog").addEventListener("close", clearReferenceSearch);
  $("price-search").addEventListener("input", (event) => {
    const sequence = clearReferenceSearch();
    const query = event.target.value.trim();
    if (query) view.searchTimer = setTimeout(() => searchReference(query, sequence), 250);
  });
  $("confirm-form").addEventListener("submit", (event) => {
    event.preventDefault();
    $("confirm-dialog").close(event.submitter?.value || "");
  });
}
