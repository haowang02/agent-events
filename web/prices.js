import { $, el, tip, hideTip } from "./dom.js";
import { segmented } from "./controls.js";
import { api, state, pref, savePref, int, rate, tokens, priceSource } from "./core.js";

const RATES = [
  ["input", "输入"],
  ["output", "输出"],
  ["cache_read", "缓存读"],
  ["cache_write", "缓存写"]
];

const STATUSES = { all: "全部", unpriced: "未定价", custom: "自定义", reference: "参考价" };
const view = {
  data: null,
  sequence: 0,
  filters: pref("price-filters", { query: "", status: "all" }),
  editing: null,
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

const ownCustom = (price) => price?.source === "custom" && !price.as;
const priceStatus = (price) => !price ? "unpriced" : price.source === "custom" ? "custom" : "reference";

function sourceTag(price) {
  if (!price) return el("span", { class: "tag warn", text: "未定价" });
  return el("span", { class: "tag " + (price.source === "custom" ? "info" : "ok"), text: price.source === "custom" ? "自定义" : "models.dev", tip: priceSource(price) });
}

function renderPrices() {
  if (!view.data) return;
  hideTip();
  const { models, catalog } = view.data;
  const fetched = catalog.fetched_at ? new Date(catalog.fetched_at).toLocaleString("zh-CN", { hour12: false }) : "未获取";
  tip($("catalog-refresh"), `models.dev 参考价：${fetched}` + (catalog.error ? `\n上次更新失败：${catalog.error}` : ""));
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
  $("prices-table").querySelector("tbody").replaceChildren(...rows.map(priceRow));
  $("prices-empty").classList.toggle("hidden", rows.length > 0);
  $("prices-empty").textContent = !scope.length ? "暂无模型记录" : !words.length && status === "unpriced" ? "没有未定价模型" : "没有符合条件的模型";
  $("prices-note").textContent = rows.length === scope.length ? `共 ${int(scope.length)} 个模型` : `显示 ${int(rows.length)} / ${int(scope.length)} 个模型`;
}

function priceRow(row) {
  const { model, price } = row;
  const long = price?.long_context;
  const tiers = [price, long].filter(Boolean);
  return el("tr", {},
    el("td", { class: "mono", text: model }),
    el("td", { class: "muted" }, long && [el("div", { text: "≤ " + tokens(long.threshold) }), el("div", { text: "> " + tokens(long.threshold) })]),
    RATES.map(([key]) => el("td", { class: "num" }, tiers.length ? tiers.map((tier) => el("div", { text: tier[key] == null ? "—" : rate(tier[key]) })) : "—")),
    el("td", {}, sourceTag(price), price?.as && el("div", { class: "cell-sub", text: `按 ${price.as} 计价` })),
    el("td", {}, el("div", { class: "price-actions" },
      el("button", { class: "link", type: "button", text: price ? "编辑" : "设置价格", onclick: () => openPriceDialog(row) }),
      ownCustom(price) && el("button", { class: "link danger", type: "button", text: "重置", onclick: () => resetPrice(row) })
    ))
  );
}

function setFilters(filters) {
  view.filters = filters;
  savePref("price-filters", filters);
  $("prices-table").parentElement.scrollTop = 0;
  renderPrices();
}

async function resetPrice(row) {
  if (confirm(`删除「${row.model}」的自定义价格？`)) await api("DELETE", "/api/prices?model=" + encodeURIComponent(row.model));
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
  $("price-long-input").required = $("price-long-output").required = $("price-threshold").value !== "";
}

function readRates() {
  const number = (id) => ($(id).value === "" ? null : Number($(id).value));
  const tier = (prefix) => Object.fromEntries(RATES.map(([key]) => [key, number(prefix + key)]));
  const rates = tier("price-");
  const threshold = number("price-threshold");
  if (threshold) rates.long_context = { threshold, ...tier("price-long-") };
  return rates;
}

function openPriceDialog(row) {
  view.editing = row;
  $("price-model").textContent = row.model;
  $("price-error").classList.add("hidden");
  $("price-search").value = "";
  clearReferenceSearch();
  fillRates(row.price);
  $("price-dialog").showModal();
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
    ...results.map((result) =>
      el(
        "button",
        {
          type: "button",
          onclick: () => {
            fillRates(result.rates);
            $("price-search").value = `${result.provider}/${result.model_id}`;
            clearReferenceSearch();
          }
        },
        el("span", { class: "mono", text: `${result.provider}/${result.model_id}` }),
        el("span", { class: "muted small", text: `${rate(result.rates.input)} / ${rate(result.rates.output)}` })
      )
    )
  );
}

async function savePrice(event) {
  event.preventDefault();
  try {
    await api("PUT", "/api/prices", { model: view.editing.model, rates: readRates() });
    $("price-dialog").close();
  } catch (error) {
    $("price-error").textContent = error.message;
    $("price-error").classList.remove("hidden");
  }
}

export function initPrices() {
  $("prices-table").querySelector("thead").append(el("tr", {},
    el("th", { text: "模型" }), el("th", { text: "上下文" }),
    RATES.map(([, label]) => el("th", { class: "num", text: label + " $/M" })),
    el("th", { text: "来源" }), el("th", { "aria-label": "操作" })
  ));
  $("prices-status").append(statusSelect.element);
  $("prices-query").value = view.filters.query;
  $("prices-query").addEventListener("input", (event) => setFilters({ ...view.filters, query: event.target.value }));
  const refresh = $("catalog-refresh");
  refresh.addEventListener("click", async () => {
    refresh.disabled = true;
    try {
      await api("POST", "/api/prices/refresh");
    } catch (error) {
      alert("更新失败：" + error.message);
    } finally {
      refresh.disabled = false;
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
}
