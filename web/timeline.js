// The event time distribution under the events table.
import { el } from "./dom.js";
import { int, monthDay, hourMinute, timeSpan } from "./core.js";

const DAY = 86400000;
const TICK_MINUTES = [5, 10, 15, 30, 60, 120, 180, 360, 720, 1440, 2880, 10080, 20160, 43200, 131040, 525600];

// Ticks fall on local hours or midnights, at most six; midnights show the date.
function axisTicks(start, end) {
  const minutes = TICK_MINUTES.find((m) => (end - start) / 60000 / m <= 6) ?? TICK_MINUTES.at(-1);
  const at = new Date(start);
  at.setHours(0, 0, 0, 0);
  const advance = () => (minutes < 1440 ? at.setMinutes(at.getMinutes() + minutes) : at.setDate(at.getDate() + minutes / 1440));
  while (at < start) advance();
  const ticks = [];
  for (; at < end; advance()) ticks.push({ time: at.getTime(), label: at.getHours() || at.getMinutes() ? hourMinute(at) : monthDay(at) });
  return ticks;
}

function bucketLabel(start, step) {
  if (step < DAY) return timeSpan(start, start + step, hourMinute);
  const first = monthDay(new Date(start)), last = monthDay(new Date(start + step - 1));
  return first === last ? first : `${first} – ${last}`;
}

// onPick(index) runs when a span with events is clicked, or chosen with the
// keyboard and Enter.
export function createTimeline(root, onPick) {
  const bars = el("div", { class: "timeline-bars" });
  const band = el("div", { class: "timeline-band" });
  const cursor = el("div", { class: "timeline-cursor hidden" });
  const card = el("div", { class: "timeline-tip hidden", "aria-hidden": "true" });
  const plot = el("div", { class: "timeline-plot" }, band, cursor, bars, card);
  const axis = el("div", { class: "timeline-axis", "aria-hidden": "true" });
  root.append(plot, axis);
  let model = null, shown = -1, focused = -1;

  const count = (i) => model.ok[i] + model.failed[i];
  const clamp = (i) => Math.min(Math.max(i, 0), model.ok.length - 1);
  function show(i) {
    shown = i;
    const n = model.ok.length, total = count(i), label = bucketLabel(model.start + i * model.step, model.step);
    cursor.style.left = `${(i / n) * 100}%`;
    cursor.style.width = `${100 / n}%`;
    cursor.classList.remove("hidden");
    card.replaceChildren(
      ...[
      el("div", { class: "timeline-tip-head" }, el("span", { text: label }), el("b", { class: total ? null : "muted", text: total ? int(total) + " 条" : "没有事件" })),
      total > 0 &&
        el(
          "div",
          { class: "timeline-tip-rows" },
          [["ok", "成功"], ["failed", "失败"]].map(([kind, text]) => [el("i", { class: "timeline-" + kind }), el("span", { text }), el("b", { text: int(model[kind][i]) })])
        ),
      ].filter(Boolean)
    );
    card.classList.remove("hidden");
    const width = plot.clientWidth, half = card.offsetWidth / 2;
    card.style.left = `${Math.min(Math.max(((i + 0.5) / n) * width, half), width - half)}px`;
    plot.classList.toggle("pickable", total > 0);
    root.setAttribute("aria-valuenow", i);
    root.setAttribute("aria-valuetext", `${label}，` + (total ? `${int(total)} 条，失败 ${int(model.failed[i])} 条` : "没有事件"));
  }
  function hide() {
    shown = -1;
    cursor.classList.add("hidden");
    card.classList.add("hidden");
  }
  const pick = (i) => count(i) && onPick(i, model);
  const indexAt = (x) => {
    const box = plot.getBoundingClientRect();
    return clamp(Math.floor(((x - box.left) / box.width) * model.ok.length));
  };

  plot.onpointermove = (event) => model && show(indexAt(event.clientX));
  plot.onpointerleave = () => (focused >= 0 ? show(focused) : hide());
  plot.onclick = (event) => model && pick(indexAt(event.clientX));
  root.onfocus = () => {
    if (model && root.matches(":focus-visible")) show((focused = model.current));
  };
  root.onblur = () => {
    focused = -1;
    hide();
  };
  root.onkeydown = (event) => {
    if (!model) return;
    const at = focused >= 0 ? focused : model.current;
    const next = { ArrowLeft: at - 1, ArrowRight: at + 1, Home: 0, End: model.ok.length - 1 }[event.key];
    if (next !== undefined) show((focused = clamp(next)));
    else if (event.key === "Enter" || event.key === " ") pick(at);
    else return;
    event.preventDefault();
  };

  // input is { timeline, page, openEnd }: the server's span counts, the
  // current page's oldest and newest times when it is in time order, and
  // whether the range runs to now.
  function render(input) {
    root.classList.toggle("hidden", !input);
    if (!input) {
      model = null;
      return hide();
    }
    const { timeline, page, openEnd } = input, n = timeline.ok.length;
    model = { start: timeline.start, step: timeline.step, ok: timeline.ok, failed: timeline.failed };
    const end = model.start + n * model.step, x = (ms) => ((ms - model.start) / (end - model.start)) * 100;
    const [first, last] = page ? page.map((ms) => clamp(Math.floor((ms - model.start) / model.step))).sort((a, b) => a - b) : [-1, -1];
    model.current = page ? last : n - 1;
    const max = Math.max(1, ...timeline.ok.map((_, i) => count(i)));
    const bar = (kind, value) => value > 0 && el("i", { class: "timeline-" + kind, style: { height: `max(2px, ${(value / max) * 100}%)` } });
    bars.replaceChildren(
      ...timeline.ok.map((ok, i) =>
        el("div", { class: "timeline-bucket" + (i >= first && i <= last ? " current" : "") }, bar("failed", timeline.failed[i]), bar("ok", ok))
      )
    );
    band.classList.toggle("hidden", !page);
    band.style.left = `${(first / n) * 100}%`;
    band.style.width = `${((last - first + 1) / n) * 100}%`;
    axis.replaceChildren(
      ...axisTicks(model.start, end)
        .filter(({ time }) => x(time) > 3 && x(time) < (openEnd ? 88 : 97))
        .map(({ time, label }) => el("span", { style: { left: x(time) + "%" }, text: label })),
      ...(openEnd ? [el("span", { class: "timeline-now", style: { left: Math.min(100, x(Date.now())) + "%" }, text: "现在" })] : [])
    );
    root.setAttribute("aria-valuemax", n - 1);
    focused = Math.min(focused, n - 1);
    if (shown >= 0) show(clamp(shown));
  }
  return { render };
}
