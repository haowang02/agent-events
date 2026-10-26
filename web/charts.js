// Chart.js plumbing shared by the analysis cards.
import { $, el } from "./dom.js";
import { cssVar } from "./core.js";

const charts = {};

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
      x: { stacked, grid: { display: false }, border: { color: grid }, ticks: { color: text, maxRotation: 0, autoSkip: true, maxTicksLimit: 10 } },
      y: { stacked, grid: { color: grid }, border: { display: false }, ...y, ticks: { color: text, maxTicksLimit: 6, ...y.ticks } }
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

// Counts start from zero; with fit, rates span their own range instead.
export function sparkline(values, color, { fit = false } = {}) {
  const canvas = el("canvas", { class: "summary-spark", "aria-hidden": "true" });
  requestAnimationFrame(() => {
    const { clientWidth: width, clientHeight: height } = canvas;
    if (!width || !height || !values.length) return;
    const ratio = devicePixelRatio || 1;
    canvas.width = width * ratio;
    canvas.height = height * ratio;
    const context = canvas.getContext("2d");
    context.scale(ratio, ratio);
    const points = values.length > 1 ? values : [values[0], values[0]];
    const maximum = Math.max(...points);
    const minimum = fit ? Math.min(...points) : Math.min(0, ...points);
    const span = maximum - minimum || 1;
    const pad = 4;
    context.beginPath();
    points.forEach((value, index) => {
      const x = (index * width) / (points.length - 1);
      const y = pad + ((maximum - value) * (height - pad * 2)) / span;
      if (index) context.lineTo(x, y);
      else context.moveTo(x, y);
    });
    context.lineWidth = 2;
    context.lineJoin = "round";
    context.lineCap = "round";
    context.strokeStyle = color;
    context.stroke();
    context.lineTo(width, height);
    context.lineTo(0, height);
    context.closePath();
    context.globalAlpha = 0.1;
    context.fillStyle = color;
    context.fill();
  });
  return canvas;
}
