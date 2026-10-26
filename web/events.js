import { $, el, icon, tip, hideTip, copyButton, makeSortable } from "./dom.js";
import {
  api, pref, savePref, state, rangeParams, int, usd, rate, latency, tps, percent, dateTimeLines, shortPath, projectName, agentTag,
  priceSource
} from "./core.js";

const line = (...children) => el("div", { class: "cell-line" }, ...children);
const sub = (text) => el("div", { class: "cell-sub cell-line", text: text || "—" });
const detail = (text, className = "") => tip(el("span", { class: "cell-detail " + className, text: text || "—" }), text, { overflowOnly: true });
const promptTokens = (e) => e.input_tokens + e.cache_read_tokens + e.cache_write_tokens;

function costTooltip(entry) {
  const { cost } = entry;
  const fiveMinute = entry.cache_write_tokens - entry.cache_write_1h_tokens;
  const rows = [
    ["非缓存输入", entry.input_tokens, cost.input_rate],
    ["缓存读取", entry.cache_read_tokens, cost.cache_read_rate],
    fiveMinute && ["缓存写入", fiveMinute, cost.cache_write_rate],
    entry.cache_write_1h_tokens && ["1h 缓存写入", entry.cache_write_1h_tokens, cost.cache_write_1h_rate],
    ["输出", entry.output_tokens, cost.output_rate]
  ].filter(Boolean);
  return [
    el("div", { class: "tip-title", text: "费用明细" }),
    el(
      "div",
      { class: "tip-grid" },
      el("span", { class: "head", text: "项目" }),
      el("span", { class: "head num", text: "Token × 单价 / 1M" }),
      el("span", { class: "head num", text: "金额" }),
      rows.map(([name, count, unit]) => [
        el("span", { text: name }),
        el("span", { class: "num", text: int(count) + " × " + rate(unit) }),
        el("span", { class: "num", text: usd((count * unit) / 1e6) })
      ])
    ),
    el("div", { class: "tip-total" }, el("span", { text: "合计" }), el("span", { text: usd(cost.total) })),
    el("div", { class: "tip-note", text: priceSource(entry.price) + (cost.long_context ? " · 长上下文价格" : "") })
  ];
}

// Columns in their default order. Time anchors each row, so it cannot be hidden.
const COLUMNS = [
  { id: "time", label: "时间", sort: "time", required: true, cell: (e) => dateTimeLines(e.time) },
  { id: "agent", label: "Agent", sort: "agent", cell: (e) => agentTag(e.agent) },
  {
    id: "model",
    label: "模型",
    sort: "model",
    cell: (e) => [line(el("span", { class: "mono", text: e.model || "未知模型" })), e.provider && sub(e.provider)]
  },
  {
    id: "effort",
    label: "推理 / 档位",
    cell: (e) => [line(el("span", { class: "mono", text: e.effort || "—" })), e.tier && line(el("span", { class: "tag warn", text: e.tier }))]
  },
  {
    id: "status",
    label: "结果",
    sort: "status",
    cell: (e) =>
      el("span", {
        class: "tag " + (e.failed ? "bad" : "ok"),
        text: e.failed ? "失败" : "成功",
        tip: e.error && (() => [el("div", { class: "tip-title", text: "错误信息" }), el("pre", { class: "mono", text: e.error })])
      })
  },
  {
    id: "speed",
    label: "TPS / 延迟",
    sort: "speed",
    num: true,
    cell: (e) => [
      el("div", { text: tps(e.tps) }),
      el("div", {
        class: "cell-sub",
        text: e.ttft_ms ? latency(e.ttft_ms) + " · " + latency(e.latency_ms) : latency(e.latency_ms),
        tip: e.ttft_ms ? "首字时间 · 总延迟" : "总延迟"
      })
    ]
  },
  { id: "input", label: "输入", sort: "input", num: true, cell: (e) => int(promptTokens(e)) },
  {
    id: "cache_read",
    label: "缓存读取",
    sort: "cache_read",
    num: true,
    cell: (e) => [
      el("div", { text: int(e.cache_read_tokens) }),
      el("div", { class: "cell-sub", text: promptTokens(e) ? percent((e.cache_read_tokens * 100) / promptTokens(e)) : "—" })
    ]
  },
  {
    id: "cache_write",
    label: "缓存写入",
    sort: "cache_write",
    num: true,
    cell: (e) => [
      el("div", { text: int(e.cache_write_tokens) }),
      e.cache_write_1h_tokens > 0 && el("div", { class: "cell-sub", text: "1h " + int(e.cache_write_1h_tokens) })
    ]
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
            el("div", {}, el("span", { class: "has-tip", text: usd(e.cost.total), tip: () => costTooltip(e) })),
            el("div", { class: "cell-sub", text: (e.price.source === "custom" ? "自定义" : "参考价") + (e.cost.long_context ? " · 长上下文" : "") })
          ]
        : el("span", { class: "muted", text: "未定价" })
  },
  {
    id: "project",
    label: "项目",
    sort: "project",
    cell: (e) => [line(el("span", { text: projectName(e.project) || "—" })), el("div", { class: "cell-sub" }, detail(shortPath(e.project)))]
  },
  {
    id: "session",
    label: "会话",
    sort: "session",
    cell: (e) => [line(detail(e.session, "mono"), e.session && copyButton("复制会话 ID", e.session)), sub(e.kind)]
  },
  { id: "client", label: "客户端", hidden: true, cell: (e) => [line(el("span", { text: e.client || "—" })), sub(e.version)] },
  { id: "stop", label: "结束原因", hidden: true, cell: (e) => el("span", { class: "mono", text: e.stop || "—" }) },
  { id: "request_id", label: "请求 ID", hidden: true, cell: (e) => line(detail(e.id, "mono"), copyButton("复制请求 ID", e.id)) },
  { id: "source", label: "日志文件", hidden: true, cell: (e) => detail(shortPath(e.source), "mono") }
];

// The saved order, with columns added later inserted before their next default neighbor.
function columnLayout(preference = pref("columns", null)) {
  const order = preference?.order || [];
  const hidden = preference?.hidden || COLUMNS.filter((column) => column.hidden).map((column) => column.id);
  const ordered = [...new Set(order)].map((id) => COLUMNS.find((column) => column.id === id)).filter(Boolean);
  for (const [index, column] of COLUMNS.entries()) {
    if (ordered.includes(column)) continue;
    const next = COLUMNS.slice(index + 1).find((candidate) => ordered.includes(candidate));
    ordered.splice(next ? ordered.indexOf(next) : ordered.length, 0, column);
  }
  return ordered.map((column) => ({ column, visible: column.required || !hidden.includes(column.id) }));
}

const NO_FILTERS = { q: "", model: "", project: "", status: "" };

const view = {
  filters: pref("event-filters", NO_FILTERS),
  sort: pref("event-sort", { key: "time", order: "desc" }),
  limit: pref("page-size", 50),
  offset: 0,
  data: null,
  sequence: 0
};

export function resetEventPage() {
  view.offset = 0;
}

export async function loadEvents() {
  const params = rangeParams();
  for (const [key, value] of Object.entries(view.filters)) if (value) params.set(key, value);
  params.set("sort", view.sort.key);
  params.set("order", view.sort.order);
  params.set("offset", String(view.offset));
  params.set("limit", String(view.limit));
  const sequence = ++view.sequence;
  const data = await api("GET", "/api/events?" + params);
  if (sequence !== view.sequence) return;
  view.data = data;
  renderEvents();
}

// Keeps the current value selectable even when the new range has no matches for it.
function fillSelect(select, allLabel, options, label = (value) => value) {
  const current = select.value;
  const rows = current && !options.some((option) => option.value === current) ? [{ value: current, count: 0 }, ...options] : options;
  select.replaceChildren(
    el("option", { value: "", text: allLabel }),
    ...rows.map((option) => el("option", { value: option.value, text: (label(option.value) || "（空）") + " · " + int(option.count) }))
  );
  select.value = current;
}

function header(columns) {
  return el(
    "tr",
    {},
    columns.map((column) => {
      if (!column.sort) return el("th", { class: column.num ? "num" : "" }, column.label);
      const active = view.sort.key === column.sort;
      const ascending = active && view.sort.order === "asc";
      return el(
        "th",
        {
          class: (column.num ? "num " : "") + "sortable",
          "aria-sort": active ? (ascending ? "ascending" : "descending") : "none",
          tabindex: "0",
          onclick: () => setSort(column.sort),
          onkeydown: (event) => {
            if (event.key !== "Enter" && event.key !== " ") return;
            event.preventDefault();
            setSort(column.sort);
          }
        },
        column.label,
        el("span", { class: "sort-mark", text: ascending ? "↑" : "↓" })
      );
    })
  );
}

function renderEvents() {
  hideTip();
  const { data, filters } = view;
  fillSelect($("filter-model"), "全部模型", data.models);
  fillSelect($("filter-project"), "全部项目", data.projects, shortPath);
  const counts = data.status_counts;
  $("filter-status").replaceChildren(
    ...[
      ["", "全部结果", counts.all],
      ["ok", "成功", counts.ok],
      ["failed", "失败", counts.failed]
    ].map(([value, label, count]) => el("option", { value, text: label + " · " + int(count) }))
  );
  $("filter-status").value = filters.status;
  $("filter-clear").disabled = !Object.values(filters).some(Boolean);

  // A single agent makes the agent column redundant.
  const columns = columnLayout()
    .filter(({ column, visible }) => visible && !(column.id === "agent" && state.agent))
    .map(({ column }) => column);
  $("events-table").querySelector("thead").replaceChildren(header(columns));
  $("events-table").querySelector("tbody").replaceChildren(
    ...data.entries.map((entry) => el("tr", {}, columns.map((column) => el("td", { class: column.num ? "num" : "" }, column.cell(entry)))))
  );
  $("events-empty").classList.toggle("hidden", data.entries.length > 0);
  $("events-empty").textContent = state.server?.ready ? "所选范围内没有请求事件" : "正在扫描日志…";

  const pages = Math.max(1, Math.ceil(data.total / view.limit));
  const page = Math.floor(view.offset / view.limit) + 1;
  $("page-info").textContent = `${page} / ${pages}`;
  $("page-prev").disabled = page <= 1;
  $("page-next").disabled = page >= pages;
  const shown = data.total ? `，显示第 ${int(view.offset + 1)}–${int(view.offset + data.entries.length)} 条` : "";
  $("events-note").textContent = `共 ${int(data.total)} 条${shown}`;
}

// A column sorts descending first, then ascending, then back to newest first.
function setSort(key) {
  const { sort } = view;
  if (sort.key !== key) view.sort = { key, order: "desc" };
  else if (sort.order === "desc") view.sort = { key, order: "asc" };
  else view.sort = { key: "time", order: "desc" };
  savePref("event-sort", view.sort);
  view.offset = 0;
  loadEvents();
}

function setFilters(filters) {
  view.filters = filters;
  savePref("event-filters", filters);
  view.offset = 0;
  loadEvents();
}

function renderColumnSettings(layout) {
  $("columns-list").replaceChildren(
    ...layout.map(({ column, visible }) =>
      el(
        "div",
        { class: "column-setting", "data-column": column.id },
        el("button", { class: "column-handle", type: "button", "aria-label": `移动「${column.label}」列` }, icon("grip")),
        el(
          "label",
          {},
          el("span", { text: column.label }),
          el("span", { class: "switch" }, el("input", { type: "checkbox", role: "switch", checked: visible, disabled: !!column.required }), el("i"))
        )
      )
    )
  );
}

export function initEvents() {
  $("columns-button").replaceChildren(icon("columns"), "列设置");
  $("columns-button").addEventListener("click", () => {
    renderColumnSettings(columnLayout());
    $("columns-dialog").showModal();
  });
  $("columns-reset").addEventListener("click", () => renderColumnSettings(columnLayout({})));
  $("columns-cancel").addEventListener("click", () => $("columns-dialog").close());
  $("columns-save").addEventListener("click", () => {
    const rows = [...$("columns-list").children];
    savePref("columns", {
      order: rows.map((row) => row.dataset.column),
      hidden: rows.filter((row) => !row.querySelector("input").checked).map((row) => row.dataset.column)
    });
    $("columns-dialog").close();
    renderEvents();
  });
  makeSortable($("columns-list"), ".column-handle", (row) => {
    const rows = [...row.parentElement.children];
    $("columns-status").textContent = `「${row.querySelector("label > span").textContent}」移到第 ${rows.indexOf(row) + 1} / ${rows.length} 位`;
  });

  let searchTimer;
  $("filter-q").value = view.filters.q;
  $("filter-q").addEventListener("input", (event) => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => setFilters({ ...view.filters, q: event.target.value.trim() }), 300);
  });
  for (const key of ["model", "project", "status"]) {
    $("filter-" + key).addEventListener("change", (event) => setFilters({ ...view.filters, [key]: event.target.value }));
  }
  $("filter-clear").addEventListener("click", () => {
    $("filter-q").value = "";
    $("filter-model").value = $("filter-project").value = "";
    setFilters(NO_FILTERS);
  });
  $("page-size").value = String(view.limit);
  $("page-size").addEventListener("change", (event) => {
    view.limit = Number(event.target.value);
    savePref("page-size", view.limit);
    view.offset = 0;
    loadEvents();
  });
  $("page-prev").addEventListener("click", () => {
    view.offset = Math.max(0, view.offset - view.limit);
    loadEvents();
  });
  $("page-next").addEventListener("click", () => {
    view.offset += view.limit;
    loadEvents();
  });
}
