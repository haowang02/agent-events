// Columns of the events table and the cost breakdown they share with the
// detail dialog.
import { el } from "./dom.js";
import { tip } from "./tooltip.js";
import { copyButton } from "./controls.js";
import { int, usd, rate, latency, tps, percent, clockTime, dateText, shortPath, projectName, agentTag, priceSource, costNotes, promptTokens, cacheRate } from "./core.js";

const line = (...children) => el("div", { class: "cell-line" }, ...children);
const sub = (text) => el("div", { class: "cell-sub cell-line", text: text || "—" });
export const clip = (text, className = "") =>
  tip(el("span", { class: ["cell-detail", className].filter(Boolean).join(" "), text: text || "—" }), text, { overflowOnly: true });
export const SCOPES = { turn: "轮次汇总", session: "会话汇总" };

export const statusTag = (e) => el("span", { class: "tag " + (e.failed ? "bad" : "ok"), text: e.failed ? "失败" : "成功" });

// Rows of [label, tokens, rate per million] that make up an event's cost.
export function costRows(e) {
  const { cost } = e;
  const fiveMinute = e.cache_write_tokens - e.cache_write_1h_tokens;
  return [
    ["非缓存输入", e.input_tokens, cost?.input_rate],
    ["缓存读取", e.cache_read_tokens, cost?.cache_read_rate],
    (fiveMinute > 0 || !e.cache_write_1h_tokens) && ["缓存写入", fiveMinute, cost?.cache_write_rate],
    e.cache_write_1h_tokens > 0 && ["1h 缓存写入", e.cache_write_1h_tokens, cost?.cache_write_1h_rate],
    ["输出", e.output_tokens, cost?.output_rate, e.reasoning_tokens > 0 && `含推理 ${int(e.reasoning_tokens)}`]
  ].filter(Boolean);
}

export const costNote = (e) => [priceSource(e.price), ...costNotes(e.cost)].join(" · ");

function costTooltip(e) {
  return [
    el("div", { class: "tip-title", text: "费用明细" }),
    el(
      "div",
      { class: "tip-grid" },
      el("span", { class: "head", text: "项目" }),
      el("span", { class: "head num", text: "Token × 单价 / 1M" }),
      el("span", { class: "head num", text: "金额" }),
      costRows(e).map(([name, count, unit]) => [
        el("span", { text: name }),
        el("span", { class: "num", text: int(count) + " × " + rate(unit) }),
        el("span", { class: "num", text: usd((count * unit) / 1e6) })
      ])
    ),
    el("div", { class: "tip-total" }, el("span", { text: "合计" }), el("span", { text: usd(e.cost.total) })),
    el("div", { class: "tip-note", text: costNote(e) })
  ];
}

// Columns in their default order. Time anchors each row, so it cannot be
// hidden. sort names the server's sort key.
export const COLUMNS = [
  { id: "time", label: "时间", sort: "time", required: true, cell: (e) => [el("div", { class: "cell-line mono", text: clockTime(e.time) }), el("div", { class: "cell-sub", text: dateText(new Date(e.time)) })] },
  { id: "agent", label: "Agent", sort: "agent", cell: (e) => agentTag(e.agent) },
  { id: "model", label: "模型", sort: "model", cell: (e) => [line(clip(e.model || "未知模型", "mono")), e.provider && sub(e.provider)] },
  {
    id: "effort",
    label: "推理 / 档位",
    cell: (e) => [line(el("span", { class: "mono", text: e.effort || "—" })), e.tier && line(el("span", { class: "tag warn", text: e.tier }))]
  },
  {
    id: "status",
    label: "结果",
    sort: "status",
    cell: (e) => tip(statusTag(e), e.error && (() => [el("div", { class: "tip-title", text: "错误信息" }), el("pre", { class: "mono", text: e.error })]))
  },
  {
    id: "speed",
    label: "TPS / 延迟",
    sort: "speed",
    num: true,
    cell: (e) => [
      el("div", { text: tps(e.tps) }),
      tip(el("div", { class: "cell-sub", text: e.ttft_ms ? latency(e.ttft_ms) + " · " + latency(e.latency_ms) : latency(e.latency_ms) }), e.latency_ms > 0 && (e.ttft_ms ? "首字 · 总耗时" : "总耗时"))
    ]
  },
  { id: "input", label: "输入", sort: "input", num: true, cell: (e) => int(promptTokens(e)) },
  {
    id: "cache_read",
    label: "缓存读取",
    sort: "cache_read",
    num: true,
    cell: (e) => [el("div", { text: int(e.cache_read_tokens) }), el("div", { class: "cell-sub", text: percent(cacheRate(e)) })]
  },
  {
    id: "cache_write",
    label: "缓存写入",
    sort: "cache_write",
    num: true,
    cell: (e) => [el("div", { text: int(e.cache_write_tokens) }), e.cache_write_1h_tokens > 0 && el("div", { class: "cell-sub", text: "1h " + int(e.cache_write_1h_tokens) })]
  },
  {
    id: "output",
    label: "输出",
    sort: "output",
    num: true,
    cell: (e) => [el("div", { text: int(e.output_tokens) }), e.reasoning_tokens > 0 && el("div", { class: "cell-sub", text: "推理 " + int(e.reasoning_tokens) })]
  },
  { id: "total", label: "总 Token", sort: "total", num: true, hidden: true, cell: (e) => int(e.total_tokens) },
  {
    id: "cost",
    label: "费用",
    sort: "cost",
    num: true,
    cell: (e) =>
      e.cost
        ? [
            el("div", {}, tip(el("span", { class: "has-tip", text: usd(e.cost.total) }), () => costTooltip(e))),
            el("div", { class: "cell-sub", text: [e.price.source === "custom" ? "自定义" : "参考价", ...costNotes(e.cost)].join(" · ") })
          ]
        : el("span", { class: "tag warn", text: "未定价" })
  },
  {
    id: "project",
    label: "项目",
    sort: "project",
    cell: (e) => [line(el("span", { text: projectName(e.project) || "—" })), el("div", { class: "cell-sub" }, clip(shortPath(e.project)))]
  },
  {
    id: "session",
    label: "会话",
    sort: "session",
    cell: (e) => [line(clip(e.session, "mono"), e.session && copyButton("复制会话 ID", e.session)), sub([e.kind, SCOPES[e.scope]].filter(Boolean).join(" · "))]
  },
  { id: "client", label: "客户端", hidden: true, cell: (e) => [line(el("span", { text: e.client || "—" })), sub(e.version)] },
  { id: "stop", label: "结束原因", hidden: true, cell: (e) => el("span", { class: "mono", text: e.stop || "—" }) },
  { id: "error", label: "错误内容", hidden: true, cell: (e) => (e.error ? el("div", { class: "error-preview", text: e.error }) : "—") },
  { id: "request_id", label: "请求 ID", hidden: true, cell: (e) => line(clip(e.id, "mono"), copyButton("复制请求 ID", e.id)) },
  { id: "source", label: "日志文件", hidden: true, cell: (e) => clip(shortPath(e.source), "mono") }
];
