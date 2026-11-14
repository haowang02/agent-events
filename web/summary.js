// The summary cards at the top of the analysis tab.
import { el, svgEl, icon } from "./dom.js";
import { tip } from "./tooltip.js";
import { state, int, tokens, usd, percent, latency, promptTokens, cacheRate, dimensionLabel } from "./core.js";

const WIDTH = 320, HEIGHT = 40, TOP = 5, BOTTOM = HEIGHT - 1;
let sparklineID = 0;

// The SVG stretches with the card (preserveAspectRatio="none") while strokes
// keep their width; dots are HTML placed by percentage so they stay round.
// Null values leave gaps.
function sparkline(values, label) {
  const line = (cls, x1, x2, y1, y2) => `<line class="${cls}" x1="${x1}" x2="${x2}" y1="${y1}" y2="${y2}"/>`;
  const dot = (cls, [x, y]) => el("i", { class: "spark-dot " + cls, style: { left: (x / WIDTH) * 100 + "%", top: (y / HEIGHT) * 100 + "%" } });
  const markup = [...[TOP, (2 * TOP + BOTTOM) / 3, (TOP + 2 * BOTTOM) / 3].map((y) => line("spark-grid", 0, WIDTH, y, y)), line("spark-base", 0, WIDTH, HEIGHT - 0.5, HEIGHT - 0.5)];
  const dots = [];
  const measured = values.filter((value) => value != null);
  if (measured.length) {
    const series = measured.length === 1 && values.length === 1 ? [measured[0], measured[0]] : values;
    const maximum = Math.max(...measured), minimum = Math.min(...measured), span = maximum - minimum;
    const step = (WIDTH - 2) / (series.length - 1);
    const toY = (value) => (span ? BOTTOM - ((value - minimum) * (BOTTOM - TOP)) / span : maximum ? (TOP + BOTTOM) / 2 : BOTTOM);
    const segments = [[]];
    series.forEach((value, i) => {
      if (value == null) segments.push([]);
      else segments.at(-1).push([1 + i * step, toY(value)]);
    });
    const gradient = `spark-${++sparklineID}`;
    markup.push(`<defs><linearGradient id="${gradient}" x1="0" y1="0" x2="0" y2="1"><stop class="spark-fill" offset="0"/><stop offset="1" stop-color="currentColor" stop-opacity="0"/></linearGradient></defs>`);
    for (const points of segments) {
      if (points.length === 1) dots.push(dot("spark-point", points[0]));
      if (points.length < 2) continue;
      const deltas = points.slice(1).map((p, i) => p[1] - points[i][1]);
      // Monotone tangents keep the curve between neighboring values.
      const slopes = points.map((_, i) => {
        const before = deltas[Math.max(0, i - 1)], after = deltas[Math.min(i, deltas.length - 1)];
        return before * after > 0 ? (2 * before * after) / (before + after) : 0;
      });
      let path = `M${points[0][0]},${points[0][1]}`;
      for (let i = 1; i < points.length; i++) {
        const [x, y] = points[i];
        path += ` C${x - (step * 2) / 3},${points[i - 1][1] + slopes[i - 1] / 3} ${x - step / 3},${y - slopes[i] / 3} ${x},${y}`;
      }
      markup.push(`<path d="${path} L${points.at(-1)[0]},${HEIGHT} L${points[0][0]},${HEIGHT} Z" fill="url(#${gradient})"/>`, `<path class="spark-curve" d="${path}"/>`);
    }
    if (span) {
      const x = 1 + series.indexOf(maximum) * step;
      markup.push(line("spark-peak", x, x, TOP + 4, BOTTOM));
      dots.push(dot("spark-peak-dot", [x, TOP]));
    }
  }
  const svg = svgEl("svg", { class: "summary-spark", viewBox: `0 0 ${WIDTH} ${HEIGHT}`, preserveAspectRatio: "none", role: "img", "aria-label": label });
  svg.innerHTML = markup.join("");
  return [svg, ...dots];
}

const card = ({ key, title, value, lines, points, symbol }) =>
  el(
    "section",
    { class: "summary-card summary-" + key, "aria-label": title },
    el(
      "div",
      { class: "summary-content" },
      el("h3", { class: "summary-title", text: title }),
      el("span", { class: "summary-icon" }, icon(symbol)),
      el("div", { class: "summary-value", text: value }),
      el(
        "div",
        { class: "summary-lines" },
        lines.map(([label, content]) => el("div", { class: "summary-line" }, el("span", { text: label }), el("strong", {}, content)))
      ),
      el("div", { class: "summary-chart" }, sparkline(points, title + "趋势"))
    )
  );

// The largest cost in a distribution, named.
function leader(data, dimension) {
  const [top] = [...(data.distribution[dimension] || [])].sort((a, b) => b.cost - a.cost);
  if (!top?.cost) return "—";
  const name = dimensionLabel(dimension, top.key);
  return el("span", { class: "summary-leader" }, tip(el("span", { class: "summary-leader-name", text: name }), name, { overflowOnly: true }), usd(top.cost));
}

// onUnpriced opens the unpriced models when requests lack a price.
export function summaryCards(data, { onUnpriced }) {
  const s = data.summary, trend = data.trend, points = (pick) => trend.map(pick);
  const second = state.agent ? "projects" : "agents";
  return [
    card({
      key: "requests",
      title: "请求总数",
      value: int(s.requests),
      lines: [
        ["成功 / 失败", int(s.requests - s.failed) + " / " + int(s.failed)],
        ["成功率", s.requests ? percent(s.success_rate, 2) : "—"],
        ["平均耗时", latency(s.avg_latency_ms)]
      ],
      points: points((p) => p.requests),
      symbol: "activity"
    }),
    card({
      key: "tokens",
      title: "Token 用量",
      value: tokens(s.total_tokens),
      lines: [
        ["输入 / 输出", tokens(promptTokens(s)) + " / " + tokens(s.output_tokens)],
        ["其中推理", tokens(s.reasoning_tokens)],
        ["平均每次请求", s.requests ? tokens(s.total_tokens / s.requests) : "—"]
      ],
      points: points((p) => p.total_tokens),
      symbol: "binary"
    }),
    card({
      key: "cache",
      title: "缓存命中率",
      value: percent(cacheRate(s), 2),
      lines: [
        ["缓存读取 / 写入", tokens(s.cache_read_tokens) + " / " + tokens(s.cache_write_tokens)],
        ["未缓存输入", tokens(s.input_tokens)],
        ["缓存读取费用", usd(s.cost_cache_read)]
      ],
      points: points((p) => cacheRate(p)),
      symbol: "database-zap"
    }),
    card({
      key: "cost",
      title: "总费用",
      value: usd(s.cost),
      lines: [
        ["模型第一", leader(data, "models")],
        [state.agent ? "项目第一" : "Agent 第一", leader(data, second)],
        [
          "未定价请求",
          s.unpriced > 0 ? el("button", { class: "summary-warn", type: "button", text: int(s.unpriced) + " 次", onclick: onUnpriced }) : "0"
        ]
      ],
      points: points((p) => p.cost),
      symbol: "coins"
    })
  ];
}
