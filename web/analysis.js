// The analysis tab: summary cards, usage trend and distribution, per-entity
// statistics and the speed trend of one model.
import { select, segmented } from "./controls.js";
import { $, el, icon, seriesColor, cssVar } from "./dom.js";
import { tip, hideTip } from "./tooltip.js";
import {
  api, pref, savePref, state, rangeParams, selectedRange, int, tokens, usd, latency, tps, percent, promptTokens, cacheRate, shortPath, agentColor, agentTag,
  monthDay, hourMinute, joinText, DIMENSIONS, dimensionLabel
} from "./core.js";
import { drawChart, tooltipStyle, timeSeriesOptions, legend, toggleLegend, roundedStacks } from "./charts.js";
import { summaryCards } from "./summary.js";
import { dataTable, numberColumn } from "./data-table.js";

const METRICS = {
  total_tokens: { label: "Token", option: "Token 占比", total: "总 Token", format: tokens },
  requests: { label: "请求", option: "请求占比", total: "总请求", format: int },
  cost: { label: "费用", option: "费用占比", total: "总费用", format: usd }
};
const metricKey = (value) => (value === "tokens" ? "total_tokens" : METRICS[value] ? value : "total_tokens");
const MONTH = 31 * 86400000;

const view = {
  data: null,
  params: null,
  sequence: 0,
  breakdowns: new Map(),
  trendMetric: metricKey(pref("trend-metric", "total_tokens")),
  trendGroup: pref("trend-group", "category"),
  bucket: pref("trend-bucket", ""), // "" lets the server choose by range.
  dimension: pref("distribution-dimension", "models"),
  metric: metricKey(pref("distribution-metric", "total_tokens")),
  tableDimension: pref("stats-dimension", "models"),
  speedModel: "",
  hidden: {}
};

// A single agent makes the agent dimension redundant.
function dimensions() {
  const result = { ...DIMENSIONS };
  if (state.agent) delete result.agents;
  return result;
}
const validDimension = (value) => (dimensions()[value] ? value : "models");
const trendGroup = () => (view.trendGroup === "category" || dimensions()[view.trendGroup] ? view.trendGroup : "category");
// What an entity's usage splits into by default.
const breakdownBy = (dimension) => (dimension !== "models" ? "models" : state.agent ? "projects" : "agents");

// interactive fades the current charts while the user's change loads.
export async function loadAnalysis({ interactive = false } = {}) {
  const params = rangeParams();
  if (view.bucket) params.set("bucket", view.bucket);
  if (trendGroup() !== "category") params.set("trend_by", trendGroup());
  if (view.speedModel) params.set("speed_model", view.speedModel);
  const sequence = ++view.sequence;
  if (interactive) $("analysis-content").classList.add("loading");
  let data;
  try {
    data = await api("GET", "/api/analysis?" + params);
  } finally {
    if (sequence === view.sequence) $("analysis-content").classList.remove("loading");
  }
  if (sequence !== view.sequence) return;
  view.data = data;
  view.params = params;
  view.breakdowns.clear();
  renderAnalysis();
}

export function renderAnalysis() {
  const { data } = view;
  if (!data) return;
  hideTip();
  $("analysis-loading").classList.add("hidden");
  $("analysis-content").classList.remove("hidden");
  $("summary").replaceChildren(...summaryCards(data, { onUnpriced: () => document.dispatchEvent(new CustomEvent("navigate", { detail: { tab: "prices", priceStatus: "unpriced" } })) }));
  renderTrend(data);
  renderDistribution(data);
  renderStats(data);
  renderSpeed(data);
}

function bucketLabel(ms, bucket) {
  const at = new Date(ms);
  return bucket === "hour" ? `${monthDay(at)} ${hourMinute(at)}` : monthDay(at);
}

// Agents keep their own color; other entities take colors by total tokens,
// and past seven of them, those after the sixth share the "other" color.
function entityColor(dimension, key, rows) {
  if (dimension === "agents") return agentColor(key);
  const index = rows.findIndex((row) => row.key === key);
  return index >= 0 && index < (rows.length <= 7 ? 7 : 6) ? seriesColor(index + 1) : cssVar("--series-other");
}

const CATEGORY_SERIES = {
  total_tokens: [["非缓存输入", (p) => p.input_tokens, 3], ["缓存读取", (p) => p.cache_read_tokens, 1], ["缓存写入", (p) => p.cache_write_tokens, 7], ["输出", (p) => p.output_tokens, 4]],
  requests: [["成功", (p) => p.requests - p.failed, 3], ["失败", (p) => p.failed, "bad"]],
  cost: [["非缓存输入", (p) => p.cost_input, 3], ["缓存读取", (p) => p.cost_cache_read, 1], ["缓存写入", (p) => p.cost_cache_write, 7], ["输出", (p) => p.cost_output, 4]]
};

function trendDatasets(data, metric, group) {
  if (group === "category" || data.trend_by !== group) {
    return CATEGORY_SERIES[metric]
      .filter(([, pick]) => data.trend.some((point) => pick(point) > 0))
      .map(([label, pick, slot]) => ({ label, data: data.trend.map(pick), color: slot === "bad" ? cssVar("--bad-text") : seriesColor(slot) }));
  }
  const rows = data.distribution[group];
  return data.trend_series
    .filter((series) => series[metric].some((value) => value > 0))
    .map((series) => ({
      label: series.others ? `其他 ${series.others} 项` : dimensionLabel(group, series.key),
      data: series[metric],
      color: series.others ? cssVar("--series-other") : entityColor(group, series.key, rows)
    }));
}

const trendMetricSelect = segmented({
  label: "趋势指标",
  onChange: (value) => {
    view.trendMetric = value;
    savePref("trend-metric", value);
    renderTrend(view.data);
  }
});
const trendGroupSelect = select({
  label: "趋势分组",
  align: "end",
  onChange: (value) => {
    view.trendGroup = value;
    savePref("trend-group", value);
    loadAnalysis({ interactive: true }).catch(console.error);
  }
});
const bucketSelect = select({
  label: "统计粒度",
  align: "end",
  options: [{ value: "hour", label: "按小时" }, { value: "day", label: "按天" }],
  onChange: (value) => {
    view.bucket = value;
    savePref("trend-bucket", value);
    loadAnalysis({ interactive: true }).catch(console.error);
  }
});

function renderTrend(data) {
  const metric = view.trendMetric, group = trendGroup(), { format } = METRICS[metric];
  const hidden = (view.hidden[metric + group] ||= new Set());
  const surface = cssVar("--surface-strong");
  const datasets = trendDatasets(data, metric, group).map(({ label, data: values, color }) => ({
    label,
    data: values,
    hidden: hidden.has(label),
    backgroundColor: `color-mix(in srgb, ${color} 85%, ${surface})`,
    hoverBackgroundColor: color,
    borderWidth: 0,
    maxBarThickness: 44,
    categoryPercentage: 0.74,
    barPercentage: 0.92
  }));
  trendMetricSelect.update(Object.entries(METRICS).map(([value, item]) => ({ value, label: item.label })), metric);
  trendGroupSelect.update([{ value: "category", label: "按类别" }, ...Object.entries(dimensions()).map(([value, label]) => ({ value, label: joinText("按", label) }))], group);
  const { from, to } = selectedRange();
  const span = (to || Date.now()) - (from || data.trend[0]?.time || Date.now());
  bucketSelect.element.classList.toggle("hidden", span > MONTH);
  bucketSelect.update([{ value: "hour", label: "按小时" }, { value: "day", label: "按天" }], data.bucket);
  $("trend-legend").replaceChildren(datasets.length > 1 ? toggleLegend("trend-chart", datasets, hidden) : "");
  const sum = (items) => items.reduce((total, item) => total + Number(item.raw || 0), 0);
  drawChart(
    "trend-chart",
    {
      type: "bar",
      plugins: [roundedStacks],
      data: { labels: data.trend.map((point) => bucketLabel(point.time, data.bucket)), datasets },
      options: timeSeriesOptions({
        stacked: true,
        tooltip: {
          filter: (item) => item.raw > 0,
          itemSort: (a, b) => b.raw - a.raw,
          callbacks: {
            label: (item) => `${item.dataset.label}：${format(item.raw)}`,
            footer: (items) => (items.length > 1 ? "合计：" + format(sum(items)) : "")
          }
        },
        y: { beginAtZero: true, grace: "8%", ticks: { precision: metric === "requests" ? 0 : undefined, callback: (value) => format(value) } }
      })
    },
    datasets.length ? "" : "所选范围内没有数据"
  );
}

// Breakdowns load once per analysis load, when first shown.
function breakdown(dimension, key, by) {
  const cacheKey = [dimension, key, by].join("\0");
  if (!view.breakdowns.has(cacheKey)) {
    const params = new URLSearchParams(view.params);
    for (const name of ["bucket", "trend_by", "speed_model"]) params.delete(name);
    params.set("dimension", dimension);
    params.set("key", key);
    params.set("by", by);
    const pending = api("GET", "/api/breakdown?" + params).then((result) => result.rows);
    pending.catch(() => view.breakdowns.delete(cacheKey));
    view.breakdowns.set(cacheKey, pending);
  }
  return view.breakdowns.get(cacheKey);
}

function nameCell(dimension, row, { path = true } = {}) {
  if (row.total) return el("strong", { text: "合计" });
  if (dimension === "agents") return agentTag(row.key);
  const label = dimensionLabel(dimension, row.key);
  if (dimension === "projects" && path && row.key) return [el("div", { class: "cell-line", text: label }), el("div", { class: "cell-sub", text: shortPath(row.key) })];
  return el("span", { class: dimension === "models" ? "mono" : null, text: label });
}

function totals(rows, extra = {}) {
  const total = { total: true, key: "", speed: {}, ...extra };
  for (const field of ["requests", "failed", "input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "total_tokens", "cost"])
    total[field] = rows.reduce((sum, row) => sum + row[field], 0);
  return total;
}

function breakdownTable(rows, by, compact = false) {
  return dataTable({
    class: "breakdown-table" + (compact ? " compact" : ""),
    columns: [
      { id: "name", label: DIMENSIONS[by], sort: (row) => (row.total ? null : dimensionLabel(by, row.key)), cell: (row) => nameCell(by, row, { path: !compact }) },
      numberColumn("requests", "请求", int),
      numberColumn("total_tokens", "Token", tokens),
      numberColumn("cache_rate", "缓存命中率", (value) => percent(value, 1), cacheRate),
      !compact && numberColumn("tps", "TPS", tps, (row) => row.speed.tps || null),
      numberColumn("cost", "费用", usd)
    ].filter(Boolean),
    rows,
    footer: rows.length > 1 && totals(rows),
    sortPref: "breakdown-sort",
    initialSort: { key: "cost", order: "desc" }
  });
}

const breakdownTitle = (label, by) => [el("span", { text: label }), el("span", { class: "tip-caption", text: joinText("按", DIMENSIONS[by]) })];

// The tip content of an entity: its usage split by another dimension.
const breakdownTip = (dimension, row, label) => () =>
  breakdown(dimension, row.key, breakdownBy(dimension)).then((rows) => [
    el("div", { class: "tip-title" }, breakdownTitle(label, breakdownBy(dimension))),
    el("div", { class: "tip-scroll", tabindex: "-1" }, breakdownTable(rows, breakdownBy(dimension), true))
  ]);

const dimensionSelect = segmented({
  label: "分布维度",
  onChange: (value) => {
    view.dimension = value;
    savePref("distribution-dimension", value);
    renderDistribution(view.data);
  }
});
const distributionSelect = select({
  label: "分布指标",
  align: "end",
  onChange: (value) => {
    view.metric = value;
    savePref("distribution-metric", value);
    renderDistribution(view.data);
  }
});

// The six largest entities and the rest as one entry; agents are listed in
// their color order.
function distributionRows(rows, dimension, metric) {
  let colored;
  if (dimension === "agents") {
    colored = rows.map((row) => ({ ...row, color: agentColor(row.key) }));
  } else {
    const shown = rows.length <= 7 ? rows : rows.slice(0, 6);
    colored = shown.map((row) => ({ ...row, color: entityColor(dimension, row.key, rows) }));
    const rest = rows.slice(shown.length);
    if (rest.length) colored.push({ ...totals(rest, { total: false }), others: rest.length, label: `其他 ${rest.length} 项`, color: cssVar("--series-other") });
  }
  const total = colored.reduce((sum, row) => sum + row[metric], 0);
  return colored.map((row) => ({ ...row, label: row.label || dimensionLabel(dimension, row.key), percent: total > 0 ? (row[metric] * 100) / total : 0 }));
}

function renderDistribution(data) {
  const dimension = validDimension(view.dimension);
  const metric = view.metric;
  const rows = distributionRows(data.distribution[dimension], dimension, metric);
  dimensionSelect.update(Object.entries(dimensions()).map(([value, label]) => ({ value, label })), dimension);
  distributionSelect.update(Object.entries(METRICS).map(([value, item]) => ({ value, label: item.option })), metric);
  $("distribution-list").replaceChildren(
    ...rows.map((row) =>
      el(
        "div",
        { class: "distribution-row", style: { "--key": row.color } },
        el(
          "div",
          { class: "distribution-heading" },
          el(
            "span",
            { class: "name" },
            el("i", { class: "distribution-key" }),
            row.others ? el("span", { text: row.label }) : tip(el("span", { class: "has-tip", text: row.label }), breakdownTip(dimension, row, row.label))
          ),
          el("strong", { class: "distribution-percent", text: percent(row.percent, 2) })
        ),
        el("div", { class: "distribution-track" }, el("i", { style: { width: row.percent + "%" } })),
        el(
          "div",
          { class: "distribution-metrics" },
          Object.entries(METRICS).map(([key, item]) => el("span", { class: "distribution-tag" + (key === metric ? " active" : ""), text: item.label + " " + item.format(row[key]) }))
        )
      )
    )
  );
  const total = rows.reduce((sum, row) => sum + row[metric], 0);
  $("distribution-center").replaceChildren(el("strong", { text: METRICS[metric].format(total) }), el("span", { text: METRICS[metric].total }));
  let empty = "";
  if (!rows.length) empty = "所选范围内没有请求事件";
  else if (!(total > 0)) empty = joinText("所选范围内", joinText(METRICS[metric].label, "为 0"));
  $("distribution-center").classList.toggle("hidden", !!empty);
  drawChart(
    "distribution-chart",
    {
      type: "doughnut",
      data: {
        labels: rows.map((row) => row.label),
        datasets: [{ data: rows.map((row) => row.percent), backgroundColor: rows.map((row) => row.color), borderColor: cssVar("--surface-strong"), borderWidth: 2, borderRadius: 4, hoverOffset: 6 }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        cutout: "68%",
        layout: { padding: 8 },
        plugins: {
          legend: { display: false },
          tooltip: tooltipStyle({
            callbacks: {
              title: (items) => `${DIMENSIONS[dimension]}：${items[0].label}`,
              label: (item) => {
                const row = rows[item.dataIndex];
                return [`${METRICS[metric].option} ${percent(row.percent, 2)}`, ...Object.entries(METRICS).map(([key, entry]) => `${entry.label} ${entry.format(row[key])}`)];
              }
            }
          })
        }
      }
    },
    empty
  );
}

const statsSelect = segmented({
  label: "明细维度",
  onChange: (value) => {
    view.tableDimension = value;
    savePref("stats-dimension", value);
    renderStats(view.data);
  }
});

const failureRate = (row) => (row.requests ? (row.failed * 100) / row.requests : null);
const speedValue = (field) => (row) => row.speed[field] || null;

function renderStats(data) {
  const dimension = validDimension(view.tableDimension);
  statsSelect.update(Object.entries(dimensions()).map(([value, label]) => ({ value, label })), dimension);
  const rows = data.distribution[dimension];
  $("stats-table").replaceChildren(
    rows.length
      ? dataTable({
          class: "stats-table",
          columns: [
            { id: "name", label: DIMENSIONS[dimension], sort: (row) => (row.total ? null : dimensionLabel(dimension, row.key)), cell: (row) => nameCell(dimension, row) },
            numberColumn("requests", "请求", int),
            numberColumn("failure", "失败率", (value) => (value == null ? "—" : percent(value, 1)), failureRate),
            numberColumn("input", "输入", tokens, promptTokens),
            numberColumn("output_tokens", "输出", tokens),
            numberColumn("cache_rate", "缓存命中率", (value) => percent(value, 1), cacheRate),
            numberColumn("cost", "费用", usd),
            numberColumn("tps", "TPS", tps, speedValue("tps")),
            numberColumn("ttft", "首字", latency, speedValue("ttft_ms")),
            numberColumn("latency", "耗时", latency, speedValue("latency_ms")),
            numberColumn("latency_p95", "P95 耗时", latency, speedValue("latency_p95_ms")),
            {
              id: "detail",
              label: "",
              className: "detail-cell",
              cell: (row) => !row.total && el("button", { class: "link", type: "button", text: "详情", "aria-label": "用量拆分：" + dimensionLabel(dimension, row.key), onclick: () => openBreakdown(dimension, row) })
            }
          ],
          rows,
          footer: rows.length > 1 && totals(rows),
          sortPref: "stats-sort",
          initialSort: { key: "cost", order: "desc" },
          onRow: (row) => openBreakdown(dimension, row)
        })
      : el("div", { class: "empty", text: "所选范围内没有请求事件" })
  );
}

// The dialog splitting one entity's usage by another dimension.
function openBreakdown(dimension, row) {
  hideTip();
  const label = dimensionLabel(dimension, row.key);
  const dialog = $("breakdown-dialog"), body = $("breakdown-body");
  const others = Object.entries(dimensions()).filter(([value]) => value !== dimension);
  let by = breakdownBy(dimension);
  const bySelect = segmented({
    label: "拆分维度",
    value: by,
    options: others.map(([value, name]) => ({ value, label: joinText("按", name) })),
    onChange: (value) => show((by = value))
  });
  $("breakdown-title").replaceChildren(el("span", { text: label }), el("span", { class: "tip-caption", text: joinText(DIMENSIONS[dimension], "用量拆分") }));
  $("breakdown-controls").replaceChildren(bySelect.element);
  async function show(target) {
    const loading = el("div", { class: "empty", text: "正在加载…" });
    body.replaceChildren(loading);
    try {
      const rows = await breakdown(dimension, row.key, target);
      if (dialog.open && loading.isConnected) body.replaceChildren(breakdownTable(rows, target));
    } catch (error) {
      if (dialog.open && loading.isConnected) loading.textContent = error.message;
    }
  }
  if (!dialog.open) dialog.showModal();
  show(by);
}

const tpsLabel = (value) => Number(value || 0).toFixed(1) + " t/s";

// Medians and boxes encode position, not length, so the axis fits the data on
// round steps. Explicit bounds also stop Chart.js counting the null gaps as 0.
function speedAxis(points) {
  const ticks = { callback: (value) => Number(value).toFixed(0) + " t/s" };
  const sampled = points.filter((point) => point.samples);
  if (!sampled.length) return { ticks };
  const low = Math.min(...sampled.map((point) => point.p10));
  const high = Math.max(...sampled.map((point) => point.p90));
  const rough = (high - low || high || 1) / 5;
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  const step = [1, 2, 5, 10].map((factor) => factor * magnitude).find((candidate) => candidate >= rough);
  return { min: Math.max(0, Math.floor(low / step) * step), max: Math.ceil(high / step) * step, ticks: { ...ticks, stepSize: step } };
}

const speedSelect = select({
  label: "模型",
  searchable: true,
  align: "end",
  onChange: (value) => {
    view.speedModel = value;
    loadAnalysis({ interactive: true }).catch(console.error);
  }
});

function renderSpeed({ speed_models: models, speed_model: model, speed: stats, speed_trend: points, bucket }) {
  $("speed-controls").classList.toggle("hidden", !models.length);
  speedSelect.update(models.map((option) => ({ value: option.value, label: option.value || "未知模型", detail: int(option.count) })), model);
  const colors = { median: seriesColor(2), box: seriesColor(1), whisker: cssVar("--text-tertiary") };
  const sampled = stats.samples > 0;
  $("speed-legend").replaceChildren(
    sampled
      ? legend([
          [colors.median, `中位数 ${tpsLabel(stats.p50)}`, "line"],
          [colors.box, `P25–P75 ${tpsLabel(stats.p25)} – ${tpsLabel(stats.p75)}`, "box"],
          [colors.whisker, `P10–P90 ${tpsLabel(stats.p10)} – ${tpsLabel(stats.p90)}`, "line"]
        ])
      : ""
  );
  const surface = cssVar("--surface-strong");
  const range = (from, to) => points.map((point) => (point.samples ? [point[from], point[to]] : null));
  const boxFill = `color-mix(in srgb, ${colors.box} 22%, ${surface})`;
  drawChart(
    "speed-chart",
    {
      type: "bar",
      data: {
        labels: points.map((point) => bucketLabel(point.time, bucket)),
        datasets: [
          {
            type: "line",
            label: "中位数",
            data: points.map((point) => (point.samples ? point.p50 : null)),
            borderColor: colors.median,
            backgroundColor: colors.median,
            borderWidth: 2,
            pointRadius: points.length > 48 ? 0 : 3.5,
            pointHoverRadius: 5,
            pointBorderColor: surface,
            pointBorderWidth: 2,
            spanGaps: true,
            cubicInterpolationMode: "monotone"
          },
          {
            label: "P25–P75",
            data: range("p25", "p75"),
            grouped: false,
            backgroundColor: boxFill,
            hoverBackgroundColor: boxFill,
            borderColor: colors.box,
            borderWidth: 1.5,
            borderRadius: 4,
            borderSkipped: false,
            maxBarThickness: 18,
            barPercentage: 0.6
          },
          ...[range("p10", "p25"), range("p75", "p90")].map((data) => ({ label: "P10–P90", data, grouped: false, backgroundColor: colors.whisker, barThickness: 2 }))
        ]
      },
      options: timeSeriesOptions({
        tooltip: {
          filter: (item) => item.datasetIndex === 0,
          callbacks: {
            label: (item) => {
              const point = points[item.dataIndex];
              return [`中位数 ${tpsLabel(point.p50)}`, `P25–P75 ${tpsLabel(point.p25)} – ${tpsLabel(point.p75)}`, `P10–P90 ${tpsLabel(point.p10)} – ${tpsLabel(point.p90)}`];
            },
            footer: (items) => (items.length ? `${int(points[items[0].dataIndex].samples)} 次成功请求` : "")
          }
        },
        y: speedAxis(points)
      })
    },
    !models.length ? "所选范围内没有带耗时的成功请求" : sampled ? "" : "该模型没有可用样本"
  );
}

export function initAnalysis() {
  $("trend-controls").append(trendMetricSelect.element, trendGroupSelect.element, bucketSelect.element);
  $("distribution-controls").append(dimensionSelect.element, distributionSelect.element);
  $("stats-controls").append(statsSelect.element);
  $("speed-controls").append(speedSelect.element);
  $("close-breakdown").replaceChildren(icon("x"));
  $("close-breakdown").addEventListener("click", () => $("breakdown-dialog").close());
}
