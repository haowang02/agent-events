import { $, el, hideTip } from "./dom.js";
import { api, pref, savePref, state, rangeParams, int, tokens, usd, latency, percent, shortPath, projectName, agentName, agentSlot, seriesColor, cssVar } from "./core.js";
import { drawChart, tooltipStyle, timeSeriesOptions, legend, toggleLegend, sparkline } from "./charts.js";

const STATUS_GOOD = "#0ca30c";
const STATUS_CRITICAL = "#d03b3b";

const view = {
  data: null,
  sequence: 0,
  trendMetric: pref("trend-metric", "tokens"),
  dimension: pref("distribution-dimension", "models"),
  metric: pref("distribution-metric", "total_tokens"),
  speedModel: "",
  hiddenSeries: {}
};

export async function loadAnalysis() {
  const params = rangeParams();
  params.set("tz", Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  if (view.speedModel) params.set("speed_model", view.speedModel);
  const sequence = ++view.sequence;
  const data = await api("GET", "/api/analysis?" + params);
  if (sequence !== view.sequence) return;
  view.data = data;
  renderAnalysis();
}

export function renderAnalysis() {
  const { data } = view;
  if (!data) return;
  hideTip();
  $("analysis-content").classList.remove("hidden");
  renderSummary(data);
  renderTrend(data);
  renderDistribution(data);
  renderSpeed(data);
}

function bucketLabel(ms, bucket) {
  const at = new Date(ms);
  const pad = (n) => String(n).padStart(2, "0");
  const day = `${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
  return bucket === "hour" ? `${day} ${pad(at.getHours())}:00` : day;
}

function segmented(label, options, active, onSelect) {
  return el(
    "div",
    { class: "segmented small", role: "group", "aria-label": label },
    options.map(([value, text]) =>
      el("button", { type: "button", class: value === active ? "active" : "", "aria-pressed": String(value === active), text, onclick: () => onSelect(value) })
    )
  );
}

function summaryLine(label, value) {
  return el("div", { class: "summary-line" }, el("span", { text: label }), el("strong", { text: value }));
}

function summaryCard(title, value, lines, spark) {
  return el(
    "section",
    { class: "card summary-card", "aria-label": title },
    el("div", { class: "summary-title", text: title }),
    el("div", { class: "summary-value", text: value }),
    el("div", { class: "summary-lines" }, lines),
    spark
  );
}

function renderSummary({ summary: s, trend, bucket }) {
  const accent = seriesColor(1);
  const series = (pick) => trend.map(pick);
  $("summary").replaceChildren(
    summaryCard(
      "总请求",
      int(s.requests),
      [
        summaryLine("成功", int(s.requests - s.failed)),
        summaryLine("失败", int(s.failed)),
        summaryLine("成功率", percent(s.success_rate, 2)),
        summaryLine("平均延迟", latency(s.avg_latency_ms))
      ],
      sparkline(series((p) => p.requests), accent)
    ),
    summaryCard(
      "总 Token",
      tokens(s.total_tokens),
      [
        summaryLine("非缓存输入", tokens(s.input_tokens)),
        summaryLine("缓存读取", tokens(s.cache_read_tokens)),
        summaryLine("缓存写入", tokens(s.cache_write_tokens)),
        summaryLine("输出（含推理）", tokens(s.output_tokens) + (s.reasoning_tokens ? ` (${tokens(s.reasoning_tokens)})` : ""))
      ],
      sparkline(series((p) => p.total_tokens), accent)
    ),
    summaryCard(
      "缓存命中率",
      percent(s.cache_rate, 2),
      [
        summaryLine("缓存读取", tokens(s.cache_read_tokens)),
        summaryLine("总输入", tokens(s.input_tokens + s.cache_read_tokens + s.cache_write_tokens)),
        summaryLine(bucket === "hour" ? "小时峰值" : "日峰值", percent(Math.max(0, ...series((p) => p.cache_rate)), 2))
      ],
      sparkline(series((p) => p.cache_rate), accent, { fit: true })
    ),
    summaryCard(
      "总费用",
      usd(s.cost),
      [
        summaryLine("输入", usd(s.cost_input)),
        summaryLine("缓存读取", usd(s.cost_cache_read)),
        summaryLine("缓存写入", usd(s.cost_cache_write)),
        summaryLine("输出", usd(s.cost_output)),
        s.unpriced > 0 && el("div", { class: "summary-line" }, el("span", { class: "tag warn", text: `${int(s.unpriced)} 次请求未定价` }))
      ],
      sparkline(series((p) => p.cost), accent)
    )
  );
}

const TREND_METRICS = {
  tokens: {
    label: "Token",
    format: tokens,
    series: () => [
      ["非缓存输入", (p) => p.input_tokens, seriesColor(1)],
      ["缓存读取", (p) => p.cache_read_tokens, seriesColor(2)],
      ["缓存写入", (p) => p.cache_write_tokens, seriesColor(3)],
      ["输出", (p) => p.output_tokens, seriesColor(4)]
    ]
  },
  requests: {
    label: "请求",
    format: int,
    series: () => [
      ["成功", (p) => p.requests - p.failed, STATUS_GOOD],
      ["失败", (p) => p.failed, STATUS_CRITICAL]
    ]
  },
  cost: {
    label: "费用",
    format: usd,
    series: () => [
      ["非缓存输入", (p) => p.cost_input, seriesColor(1)],
      ["缓存读取", (p) => p.cost_cache_read, seriesColor(2)],
      ["缓存写入", (p) => p.cost_cache_write, seriesColor(3)],
      ["输出", (p) => p.cost_output, seriesColor(4)]
    ]
  }
};

function renderTrend({ trend, bucket }) {
  const metricKey = TREND_METRICS[view.trendMetric] ? view.trendMetric : "tokens";
  const metric = TREND_METRICS[metricKey];
  const hidden = (view.hiddenSeries[metricKey] ||= new Set());
  const surface = cssVar("--surface-strong");
  // A 2px surface gap separates stacked segments; only the top one is rounded.
  const datasets = metric
    .series()
    .filter(([, pick]) => trend.some((point) => pick(point) > 0))
    .map(([label, pick, color], index, all) => ({
      label,
      data: trend.map(pick),
      hidden: hidden.has(label),
      backgroundColor: color,
      borderColor: surface,
      borderWidth: { top: index < all.length - 1 ? 2 : 0 },
      borderSkipped: "bottom",
      borderRadius: index === all.length - 1 ? { topLeft: 4, topRight: 4 } : 0,
      maxBarThickness: 24,
      categoryPercentage: 0.8,
      barPercentage: 0.9
    }));
  $("trend-controls").replaceChildren(
    segmented("趋势指标", Object.entries(TREND_METRICS).map(([key, item]) => [key, item.label]), metricKey, (value) => {
      view.trendMetric = value;
      savePref("trend-metric", value);
      renderTrend(view.data);
    })
  );
  $("trend-legend").replaceChildren(datasets.length > 1 ? toggleLegend("trend-chart", datasets, hidden) : "");
  const sum = (items) => items.reduce((total, item) => total + Number(item.raw || 0), 0);
  drawChart(
    "trend-chart",
    {
      type: "bar",
      data: { labels: trend.map((point) => bucketLabel(point.time, bucket)), datasets },
      options: timeSeriesOptions({
        stacked: true,
        tooltip: {
          callbacks: {
            label: (item) => `${item.dataset.label}：${metric.format(item.raw)}`,
            footer: (items) => (items.length > 1 ? "合计：" + metric.format(sum(items)) : "")
          }
        },
        y: { beginAtZero: true, ticks: { callback: (value) => metric.format(value) } }
      })
    },
    datasets.length ? "" : "所选范围内没有数据"
  );
}

const DIMENSIONS = { models: "模型", agents: "Agent", projects: "项目", providers: "提供商" };
const DISTRIBUTION_METRICS = {
  total_tokens: { label: "Token", option: "Token 占比", format: (row) => tokens(row.total_tokens) },
  requests: { label: "请求", option: "请求占比", format: (row) => int(row.requests) },
  cost: { label: "费用", option: "费用占比", format: (row) => usd(row.cost) }
};
const USAGE_FIELDS = ["requests", "failed", "input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "total_tokens", "cost"];

function dimensionLabel(dimension, key) {
  if (dimension === "agents") return agentName(key);
  if (dimension === "projects") return projectName(key) || "未知项目";
  return key || (dimension === "models" ? "未知模型" : "未知");
}

// Colors follow entities, not the selected metric: agents keep their own
// slot; models, projects and providers take slots by total tokens, and those
// past the sixth share one "other" entry.
function distributionRows(rows, dimension, metric) {
  let colored;
  if (dimension === "agents") {
    colored = rows
      .map((row) => ({ ...row, slot: agentSlot(row.key) }))
      .sort((a, b) => (a.slot || 9) - (b.slot || 9))
      .map((row) => ({ ...row, color: row.slot ? seriesColor(row.slot) : cssVar("--series-other") }));
  } else {
    colored = rows.slice(0, 6).map((row, index) => ({ ...row, color: seriesColor(index + 1) }));
    const rest = rows.slice(6);
    if (rest.length) {
      const other = { key: "", label: `其他 ${rest.length} 项`, color: cssVar("--series-other") };
      for (const field of USAGE_FIELDS) other[field] = rest.reduce((sum, row) => sum + row[field], 0);
      colored.push(other);
    }
  }
  const total = colored.reduce((sum, row) => sum + row[metric], 0);
  return colored.map((row) => ({
    ...row,
    label: row.label || dimensionLabel(dimension, row.key),
    percent: total > 0 ? (row[metric] * 100) / total : 0
  }));
}

function renderDistribution(data) {
  const dimensions = { ...DIMENSIONS };
  if (state.agent) delete dimensions.agents;
  const dimension = dimensions[view.dimension] ? view.dimension : "models";
  const metric = DISTRIBUTION_METRICS[view.metric] ? view.metric : "total_tokens";
  const rows = distributionRows(data.distribution[dimension], dimension, metric).filter((row) => row.requests > 0);
  $("distribution-controls").replaceChildren(
    segmented("分布维度", Object.entries(dimensions), dimension, (value) => {
      view.dimension = value;
      savePref("distribution-dimension", value);
      renderDistribution(view.data);
    }),
    el(
      "span",
      { class: "native-select" },
      el(
        "select",
        {
          "aria-label": "分布指标",
          onchange: (event) => {
            view.metric = event.target.value;
            savePref("distribution-metric", view.metric);
            renderDistribution(view.data);
          }
        },
        Object.entries(DISTRIBUTION_METRICS).map(([value, item]) => el("option", { value, text: item.option, selected: value === metric }))
      )
    )
  );
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
            el("span", { text: row.label, tip: dimension === "projects" && row.key ? shortPath(row.key) : null })
          ),
          el("strong", { class: "mono", text: percent(row.percent, 2) })
        ),
        el("div", { class: "distribution-track" }, el("i", { style: { width: row.percent + "%" } })),
        el(
          "div",
          { class: "distribution-metrics" },
          Object.entries(DISTRIBUTION_METRICS).map(([key, item]) => el("span", { class: key === metric ? "active" : "", text: item.label + " " + item.format(row) }))
        )
      )
    )
  );
  let empty = "";
  if (!rows.length) empty = "所选范围内没有请求事件";
  else if (!rows.some((row) => row.percent > 0)) empty = `所选范围内${DISTRIBUTION_METRICS[metric].label}为 0`;
  drawChart(
    "distribution-chart",
    {
      type: "doughnut",
      data: {
        labels: rows.map((row) => row.label),
        datasets: [
          { data: rows.map((row) => row.percent), backgroundColor: rows.map((row) => row.color), borderColor: cssVar("--surface-strong"), borderWidth: 2, hoverOffset: 6 }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        cutout: "64%",
        layout: { padding: 8 },
        plugins: {
          legend: { display: false },
          tooltip: tooltipStyle({
            callbacks: {
              title: (items) => `${dimensions[dimension]}：${items[0].label}`,
              label: (item) => {
                const row = rows[item.dataIndex];
                return [
                  `${DISTRIBUTION_METRICS[metric].option} ${percent(row.percent, 2)}`,
                  ...Object.values(DISTRIBUTION_METRICS).map((entry) => `${entry.label} ${entry.format(row)}`)
                ];
              }
            }
          })
        }
      }
    },
    empty
  );
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

function renderSpeed({ speed_models: models, speed_model: model, speed: stats, speed_trend: points, bucket }) {
  $("speed-controls").replaceChildren(
    models.length
      ? el(
          "span",
          { class: "native-select" },
          el(
            "select",
            {
              "aria-label": "速度趋势模型",
              onchange: (event) => {
                view.speedModel = event.target.value;
                loadAnalysis();
              }
            },
            models.map((option) => el("option", { value: option.value, text: `${option.value || "未知模型"} · ${int(option.count)}`, selected: option.value === model }))
          )
        )
      : ""
  );
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
            pointRadius: 4,
            pointHoverRadius: 6,
            pointBorderColor: surface,
            pointBorderWidth: 2,
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
