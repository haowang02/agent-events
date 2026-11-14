// Chart.js helpers and legends for the analysis cards.
import { $, el, cssVar } from "./dom.js";

const charts = {};

Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
Chart.defaults.font.size = 12;
if (matchMedia("(prefers-reduced-motion: reduce)").matches) Chart.defaults.animation = false;

// Round the whole stack, including when its top segment is only a pixel tall.
export const roundedStacks = {
  id: "roundedStacks",
  beforeDatasetsDraw(chart) {
    const stacks = new Map();
    for (const meta of chart.getSortedVisibleDatasetMetas()) {
      if (meta.type !== "bar") continue;
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
    for (const { x, width, top, bottom } of stacks.values()) ctx.roundRect(x, top, width, bottom - top, [Math.min(5, width / 2), Math.min(5, width / 2), 0, 0]);
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
  } else if (chart && chart.config.type === config.type) {
    chart.data = config.data;
    chart.options = config.options;
    chart.update("none");
  } else {
    chart?.destroy();
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
    cornerRadius: 10,
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
      x: { stacked, grid: { display: false }, border: { display: false }, ticks: { color: text, padding: 8, maxRotation: 0, autoSkip: true, maxTicksLimit: 12 } },
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
        el("i", { class: "legend-key", style: { "--key": dataset.hoverBackgroundColor || dataset.backgroundColor } }),
        dataset.label
      )
    )
  );
}
