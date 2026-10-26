import { $, el, tip, hideTip } from "./dom.js";
import { api, state, rate, tokens, priceSource } from "./core.js";

const RATES = [
  ["input", "输入"],
  ["output", "输出"],
  ["cache_read", "缓存读"],
  ["cache_write", "缓存写"]
];

const view = { data: null, editing: null, searchTimer: 0, searchSequence: 0 };

export async function loadPrices() {
  view.data = await api("GET", "/api/prices");
  renderPrices();
}

const ownCustom = (price) => price?.source === "custom" && !price.as;

function sourceTag(price) {
  if (!price) return el("span", { class: "tag plain", text: "未定价" });
  return el("span", { class: "tag " + (price.source === "custom" ? "info" : "ok"), text: price.source === "custom" ? "自定义" : "models.dev", tip: priceSource(price) });
}

// One line per tier: the standard rates, then the long-context ones.
function tiers(price) {
  return price ? [price, price.long_context].filter(Boolean) : [];
}

function renderPrices() {
  hideTip();
  const { models, catalog } = view.data;
  const fetched = catalog.fetched_at ? new Date(catalog.fetched_at).toLocaleString("zh-CN", { hour12: false }) : "未获取";
  tip($("catalog-refresh"), `models.dev 参考价：${fetched}` + (catalog.error ? `\n上次更新失败：${catalog.error}` : ""));
  const rows = models.filter((row) => !state.agent || row.agents.includes(state.agent));
  $("prices-table").replaceChildren(
    el(
      "thead",
      {},
      el(
        "tr",
        {},
        el("th", { text: "模型" }),
        el("th", { text: "上下文" }),
        RATES.map(([, label]) => el("th", { class: "num", text: label + " $/M" })),
        el("th", { text: "来源" }),
        el("th")
      )
    ),
    el(
      "tbody",
      {},
      rows.map((row) => {
        const long = row.price?.long_context;
        return el(
          "tr",
          {},
          el("td", { class: "mono", text: row.model }),
          el("td", { class: "muted" }, long && [el("div", { text: "≤ " + tokens(long.threshold) }), el("div", { text: "> " + tokens(long.threshold) })]),
          RATES.map(([key]) =>
            el("td", { class: "num" }, row.price ? tiers(row.price).map((tier) => el("div", { text: tier[key] == null ? "—" : rate(tier[key]) })) : "—")
          ),
          el("td", {}, sourceTag(row.price)),
          el(
            "td",
            {},
            el(
              "div",
              { class: "price-actions" },
              el("button", { class: "link", type: "button", text: "编辑", onclick: () => openPriceDialog(row) }),
              ownCustom(row.price) && el("button", { class: "link danger", type: "button", text: "重置", onclick: () => resetPrice(row) })
            )
          )
        );
      })
    )
  );
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
  $("price-search-results").replaceChildren();
  fillRates(row.price);
  $("price-dialog").showModal();
}

async function searchReference(query) {
  const sequence = ++view.searchSequence;
  const { results } = query ? await api("GET", "/api/prices/search?limit=30&q=" + encodeURIComponent(query)) : { results: [] };
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
            $("price-search-results").replaceChildren();
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
  $("price-search").addEventListener("input", (event) => {
    clearTimeout(view.searchTimer);
    view.searchTimer = setTimeout(() => searchReference(event.target.value.trim()), 250);
  });
}
