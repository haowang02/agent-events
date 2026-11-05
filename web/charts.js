// Charts, legends and sparklines for the analysis cards.
import { $, el, svgEl } from "./dom.js";
import { cssVar } from "./core.js";

const charts = {};

Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
Chart.defaults.font.size = 12;

// Round the whole stack, including when its top segment is only a pixel tall.
export const roundedStacks = {
  id: "roundedStacks",
  beforeDatasetsDraw(chart) {
    const stacks = new Map();
    for (const meta of chart.getSortedVisibleDatasetMetas()) {
      for (const bar of meta.data) {
        if (!(bar.height > 0)) continue;
        const stack = stacks.get(bar.x) || { x: bar.x - bar.width / 2, width: bar.width, top: Infinity, bottom: -Infinity };
        stack.top = Math.min(stack.top, bar.y, bar.base);
        stack.bottom = Math.max(stack.bottom, bar.y, bar.base);
        stacks.set(bar.x, stack);
      }
    }
    const { ctx } = chart;
    ctx.save();
    ctx.beginPath();
    for (const { x, width, top, bottom } of stacks.values()) ctx.roundRect(x, top, width, bottom - top, [6, 6, 0, 0]);
    ctx.clip();
  },
  afterDatasetsDraw(chart) {
    chart.ctx.restore();
  }
};

// drawChart updates the chart on a canvas in place, so data pushed while the
// page is open does not replay the entry animation. With empty, the canvas
// gives way to that message.
export function drawChart(id, config, empty = "") {
  const canvas = $(id);
  const message = canvas.parentElement.querySelector(".empty");
  canvas.classList.toggle("hidden", !!empty);
  message.classList.toggle("hidden", !empty);
  message.textContent = empty;
  const chart = charts[id];
  if (empty) {
    chart?.destroy();
    delete charts[id];
  } else if (chart) {
    chart.data = config.data;
    chart.options = config.options;
    chart.update("none");
  } else {
    charts[id] = new Chart(canvas, config);
  }
}

export function tooltipStyle(extra) {
  return {
    backgroundColor: cssVar("--surface-strong"),
    borderColor: cssVar("--border-strong"),
    borderWidth: 1,
    titleColor: cssVar("--text-primary"),
    bodyColor: cssVar("--text-secondary"),
    footerColor: cssVar("--text-primary"),
    padding: 10,
    usePointStyle: true,
    boxWidth: 8,
    boxHeight: 8,
    boxPadding: 6,
    ...extra
  };
}

export function timeSeriesOptions({ tooltip, y, stacked = false }) {
  const text = cssVar("--text-tertiary");
  const grid = cssVar("--grid");
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: "index", intersect: false },
    plugins: { legend: { display: false }, tooltip: tooltipStyle(tooltip) },
    scales: {
      x: { stacked, grid: { display: false }, border: { display: false }, ticks: { color: text, padding: 8, maxRotation: 0, autoSkip: true, maxTicksLimit: 10 } },
      y: { stacked, grid: { color: grid, drawTicks: false }, border: { display: false, dash: [3, 5] }, ...y, ticks: { color: text, padding: 10, maxTicksLimit: 5, ...y.ticks } }
    }
  };
}

export function legend(items) {
  return el(
    "div",
    { class: "legend" },
    items.map(([color, label, kind = ""]) => el("span", { class: "legend-item" }, el("i", { class: "legend-key " + kind, style: { "--key": color } }), label))
  );
}

// A legend whose entries show and hide the chart's datasets; hidden collects
// the labels of hidden datasets so the choice survives reloads.
export function toggleLegend(chartId, datasets, hidden) {
  return el(
    "div",
    { class: "legend" },
    datasets.map((dataset) =>
      el(
        "button",
        {
          type: "button",
          class: "legend-item" + (dataset.hidden ? " off" : ""),
          "aria-pressed": String(!dataset.hidden),
          onclick: (event) => {
            dataset.hidden = !dataset.hidden;
            if (dataset.hidden) hidden.add(dataset.label);
            else hidden.delete(dataset.label);
            event.currentTarget.classList.toggle("off", dataset.hidden);
            event.currentTarget.setAttribute("aria-pressed", String(!dataset.hidden));
            charts[chartId]?.update();
          }
        },
        el("i", { class: "legend-key", style: { "--key": dataset.backgroundColor } }),
        dataset.label
      )
    )
  );
}

let sparklineID = 0;

// Counts start from zero; with fit, rates span their own range instead.
export function sparkline(values, color, { fit = false } = {}) {
  const width = 320, height = 56, top = 6, bottom = 48;
  const svg = svgEl("svg", { class: "summary-spark", viewBox: `0 0 ${width} ${height}`, preserveAspectRatio: "none", "aria-hidden": "true", color });
  if (!values.length) return svg;

  const points = values.length > 1 ? values : [values[0], values[0]];
  const maximum = Math.max(...points);
  const minimum = fit ? Math.min(...points) : Math.min(0, ...points);
  const span = maximum - minimum;
  const step = width / (points.length - 1);
  const ys = points.map((value) => span ? bottom - ((value - minimum) * (bottom - top)) / span : fit ? height / 2 : bottom);
  const deltas = ys.slice(1).map((y, index) => y - ys[index]);
  // Monotone tangents keep the curve within each pair of measured values.
  const slopes = ys.map((_, index) => {
    const before = deltas[Math.max(0, index - 1)];
    const after = deltas[Math.min(index, deltas.length - 1)];
    return before * after > 0 ? (2 * before * after) / (before + after) : 0;
  });
  let path = `M0,${ys[0]}`;
  for (let i = 1; i < ys.length; i++) {
    const x = i * step;
    path += ` C${x - step * 2 / 3},${ys[i - 1] + slopes[i - 1] / 3} ${x - step / 3},${ys[i] - slopes[i] / 3} ${x},${ys[i]}`;
  }
  const gradient = `spark-${++sparklineID}`;
  svg.innerHTML = `
    <defs><linearGradient id="${gradient}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="currentColor" stop-opacity=".22"/>
      <stop offset="1" stop-color="currentColor" stop-opacity=".02"/>
    </linearGradient></defs>
    <path d="${path} L${width},${height} L0,${height} Z" fill="url(#${gradient})"/>
    <path d="${path}" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" vector-effect="non-scaling-stroke"/>`;
  return svg;
}
