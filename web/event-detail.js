// The event detail dialog.
import { $, el, icon } from "./dom.js";
import { hideTip } from "./tooltip.js";
import { copyButton } from "./controls.js";
import { int, usd, rate, latency, tps, percent, dateTime, shortPath, agentTag, promptTokens, cacheRate } from "./core.js";
import { SCOPES, statusTag, costRows, costNote } from "./event-fields.js";
import { openPriceDialog } from "./prices.js";

const datum = (label, content, mono = false) =>
  el(
    "span",
    { class: "detail-item" },
    label && el("span", { class: "detail-item-label", text: label }),
    el("span", { class: "detail-item-value" + (mono ? " mono" : "") }, content == null || content === "" ? "—" : content)
  );
const inline = (...items) => el("div", { class: "detail-inline" }, items);
const copyable = (value, label, display = value) => el("div", { class: "detail-copy" }, el("span", { class: "mono", text: display }), copyButton(label, value));

function fields(rows) {
  return el(
    "dl",
    { class: "detail-grid" },
    rows.filter(Boolean).map(([label, value]) => [el("dt", { text: label }), el("dd", {}, value)])
  );
}

function usageTable(e) {
  const priced = Boolean(e.cost);
  return el(
    "table",
    { class: "detail-usage" },
    el("thead", {}, el("tr", {}, el("th", { text: "项目" }), el("th", { class: "num", text: "Token" }), priced && [el("th", { class: "num", text: "单价 $/M" }), el("th", { class: "num", text: "金额" })])),
    el(
      "tbody",
      {},
      costRows(e).map(([name, count, unit, note]) =>
        el(
          "tr",
          {},
          el("td", {}, name, note && el("span", { class: "detail-usage-note", text: note })),
          el("td", { class: "num", text: int(count) }),
          priced && [el("td", { class: "num", text: rate(unit) }), el("td", { class: "num", text: usd((count * unit) / 1e6) })]
        )
      )
    ),
    el(
      "tfoot",
      {},
      el("tr", {}, el("td", { text: "合计" }), el("td", { class: "num", text: int(e.total_tokens) }), priced && [el("td"), el("td", { class: "num", text: usd(e.cost.total) })])
    )
  );
}

function pricing(e) {
  if (e.cost) return el("p", { class: "detail-note", text: costNote(e) });
  return el(
    "div",
    { class: "detail-unpriced" },
    icon("circle-alert"),
    el("span", { text: `${e.model || "该模型"} 未定价，费用未计入统计` }),
    e.model &&
      el("button", {
        class: "button small",
        type: "button",
        text: "设置价格",
        onclick: () => openPriceDialog({ model: e.model, price: e.price }, { onSaved: () => $("event-dialog").close() })
      })
  );
}

export function openDetail(e) {
  hideTip();
  const json = JSON.stringify(e, null, 2);
  const copyJSON = copyButton("复制事件 JSON", json);
  copyJSON.addEventListener("click", (event) => event.preventDefault());
  $("event-title").textContent = e.scope ? SCOPES[e.scope] + "详情" : "请求详情";
  $("event-status").replaceChildren(statusTag(e));
  $("event-detail").replaceChildren(
    el(
      "section",
      {},
      el("h3", { text: "请求" }),
      fields([
        ["时间", el("span", { class: "mono", text: dateTime(e.time) })],
        ["Agent", inline(agentTag(e.agent), (e.client || e.version) && datum("客户端", [e.client, e.version].filter(Boolean).join(" ")))],
        ["模型", inline(datum(null, e.model || "未知模型", true), e.provider && datum("提供商", e.provider), e.effort && datum("推理", e.effort, true), e.tier && datum("档位", e.tier))],
        ["结果", inline(statusTag(e), e.stop && datum("结束原因", e.stop, true))],
        e.error && ["错误信息", el("div", { class: "detail-error" }, el("pre", { text: e.error }), copyButton("复制错误信息", e.error))],
        ["速度", inline(datum("TPS", tps(e.tps)), datum("首字", latency(e.ttft_ms)), datum("总耗时", latency(e.latency_ms)))]
      ])
    ),
    el(
      "section",
      {},
      el("h3", { text: "用量与费用" }),
      inline(datum("总输入", int(promptTokens(e))), datum("缓存命中率", percent(cacheRate(e), 2)), datum("费用", e.cost ? usd(e.cost.total) : "未定价")),
      usageTable(e),
      pricing(e)
    ),
    el(
      "section",
      {},
      el("h3", { text: "来源" }),
      fields([
        ["项目", e.project ? copyable(e.project, "复制项目路径", shortPath(e.project)) : "—"],
        ["会话", e.session ? inline(copyable(e.session, "复制会话 ID"), e.kind && datum("类型", e.kind), e.scope && datum("范围", SCOPES[e.scope])) : "—"],
        ["请求 ID", copyable(e.id, "复制请求 ID")],
        ["日志文件", e.source ? copyable(e.source, "复制日志路径", shortPath(e.source)) : "—"]
      ])
    ),
    el(
      "details",
      { class: "detail-raw" },
      el("summary", {}, icon("chevron-right", { class: "detail-raw-mark" }), el("span", { text: "事件 JSON" }), copyJSON),
      el("pre", { class: "raw-json", text: json })
    )
  );
  const dialog = $("event-dialog");
  if (!dialog.open) dialog.showModal();
  $("event-detail").scrollTop = 0;
}

export function initEventDetail() {
  $("close-event").replaceChildren(icon("x"));
  $("close-event").addEventListener("click", () => $("event-dialog").close());
}
