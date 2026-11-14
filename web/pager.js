// The events table footer: time distribution, live status and paging.
import { $, el, icon } from "./dom.js";
import { tip } from "./tooltip.js";
import { select } from "./controls.js";
import { pref, savePref, state, selectedRange, int, timeSpan } from "./core.js";
import { createTimeline } from "./timeline.js";

const PAGE_SIZES = [20, 50, 100, 200];

export function pageSize() {
  const size = pref("page-size", 50);
  return PAGE_SIZES.includes(size) ? size : 50;
}

// go jumps to an offset, latest returns to the newest events, resize changes
// the page size and pick(index, timeline) jumps to a timeline span.
export function createPager({ go, latest, resize, pick }) {
  let view = null;
  const pages = () => Math.max(1, Math.ceil((view?.data?.total || 0) / view.limit));
  const page = () => Math.floor(view.offset / view.limit) + 1;
  const input = $("page-input");
  const goPage = (target) => {
    target = Math.min(Math.max(target, 1), pages());
    input.value = target;
    if (target !== page()) go((target - 1) * view.limit);
  };
  const timeline = createTimeline($("events-timeline"), (index, model) => pick(index, model));

  $("page-size").append(
    select({
      label: "每页条数",
      value: String(pageSize()),
      align: "end",
      options: PAGE_SIZES.map((n) => ({ value: String(n), label: n + " 条/页" })),
      onChange: (value) => {
        savePref("page-size", Number(value));
        resize(Number(value));
      }
    }).element
  );
  const shortcut = (label, key) => () => [el("div", { class: "tip-shortcut" }, label, el("kbd", { text: key }))];
  $("page-prev").replaceChildren(icon("chevron-left"));
  $("page-next").replaceChildren(icon("chevron-right"));
  $("page-latest").replaceChildren(icon("arrow-up-to-line"), "最新");
  tip($("page-prev"), shortcut("上一页", "["));
  tip($("page-next"), shortcut("下一页", "]"));
  tip($("events-span"), "本页时间范围");
  $("page-prev").onclick = () => goPage(page() - 1);
  $("page-next").onclick = () => goPage(page() + 1);
  $("page-latest").onclick = $("events-newer").onclick = () => latest();
  input.onfocus = () => input.select();
  input.oninput = () => (input.value = input.value.replace(/\D/g, ""));
  input.onblur = () => goPage(Number(input.value) || page());
  input.onkeydown = (event) => {
    if (event.key === "Enter") input.blur();
    if (event.key === "Escape") {
      input.value = page();
      input.blur();
    }
  };
  // ← → stay with the wide table's horizontal scrolling.
  document.addEventListener("keydown", (event) => {
    if (
      !["[", "]"].includes(event.key) ||
      !view?.data ||
      state.tab !== "events" ||
      event.isComposing ||
      event.metaKey ||
      event.ctrlKey ||
      event.altKey ||
      event.target.closest?.("input, textarea, select, [contenteditable]") ||
      document.querySelector("dialog[open], .control-popover:popover-open")
    )
      return;
    event.preventDefault();
    goPage(page() + (event.key === "]" ? 1 : -1));
  });

  // live: on the first page, scrolled to the top, with no dialog open, so new
  // events join the table directly.
  function status(current, live) {
    view = current;
    const newer = view.data?.newer || 0, chip = $("events-live"), mode = view.data && (live ? "live" : "fixed");
    chip.classList.toggle("hidden", !mode);
    if (mode && chip.dataset.mode !== mode) {
      chip.dataset.mode = mode;
      chip.classList.toggle("live", live);
      chip.replaceChildren(...(live ? [el("i", { class: "live-dot" }), "实时"] : [icon("pause"), "已固定"]));
      tip(chip, live ? "新事件会直接加入表格" : "离开第 1 页顶部时表格保持不变，新事件在顶部提示");
    }
    $("page-latest").disabled = !view.data || (live && !newer);
    const pill = $("events-newer");
    pill.classList.toggle("hidden", !newer);
    if (newer) {
      pill.replaceChildren(icon("arrow-up"), `${int(newer)} 条新事件`);
      pill.setAttribute("aria-label", `${int(newer)} 条新事件，回到最新`);
    }
  }

  function render(current, { live, announce }) {
    view = current;
    const entries = view.data?.entries || [], total = view.data?.total || 0;
    const timeOrder = view.sort.key === "time";
    const times = entries.map((entry) => entry.time);
    const span = entries.length ? [Math.min(...times), Math.max(...times)] : null;
    $("events-note").textContent = total ? `第 ${int(view.offset + 1)}–${int(view.offset + entries.length)} 条，共 ${int(total)} 条` : "";
    $("events-span").classList.toggle("hidden", !span || !timeOrder);
    if (span) $("events-span").replaceChildren(icon("clock"), timeSpan(...span));
    $("page-controls").classList.toggle("hidden", pages() <= 1);
    $("page-prev").disabled = page() <= 1;
    $("page-next").disabled = page() >= pages();
    if (document.activeElement !== input) input.value = page();
    input.style.setProperty("--digits", String(pages()).length);
    $("page-count").textContent = "/ " + int(pages());
    if (announce) $("page-status").textContent = `第 ${page()} 页，共 ${pages()} 页`;
    timeline.render(total && view.data.timeline && { timeline: view.data.timeline, page: timeOrder ? span : null, openEnd: !selectedRange().to });
    status(view, live);
  }
  return { render, status };
}
