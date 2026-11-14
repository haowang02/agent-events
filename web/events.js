// The events tab. Pages come from a snapshot of the events saved up to an
// anchor, so paging and live updates never shift the rows; events saved
// later only count toward a "new events" pill until the view returns to the
// newest page.
import { $, el, icon } from "./dom.js";
import { hideTip } from "./tooltip.js";
import { select } from "./controls.js";
import { api, pref, savePref, state, rangeParams, int, shortPath } from "./core.js";
import { columnLayout, initColumnSettings } from "./column-settings.js";
import { openDetail, initEventDetail } from "./event-detail.js";
import { createPager, pageSize } from "./pager.js";

const DETAIL_COLUMN = {
  id: "detail",
  label: "",
  className: "detail-cell",
  cell: (e) => el("button", { class: "link", type: "button", text: "详情", "aria-label": "查看请求详情", onclick: () => openDetail(e) })
};
const NO_FILTERS = { q: "", model: "", project: "", status: "" };
const STATUSES = [{ value: "", label: "全部结果" }, { value: "ok", label: "成功" }, { value: "failed", label: "失败" }];
const DEFAULT_SORT = { key: "time", order: "desc" };
const saved = pref("event-filters", {});

const view = {
  filters: Object.fromEntries(Object.keys(NO_FILTERS).map((key) => [key, saved[key] || ""])),
  sort: pref("event-sort", DEFAULT_SORT),
  limit: pageSize(),
  offset: 0,
  anchor: 0, // The snapshot's largest seq; 0 takes a new one.
  data: null,
  rows: "", // The rendered rows, to skip redrawing identical pages.
  sequence: 0
};
let pager;
const selects = {};

const isLive = () => view.offset === 0 && $("events-wrap").scrollTop <= 4 && !document.querySelector("dialog[open]");

export function resetEvents() {
  view.offset = 0;
  view.anchor = 0;
  $("events-wrap").scrollTop = 0;
}

// Background reloads keep the snapshot unless the view is live.
export const loadEvents = ({ interactive = false } = {}) => fetchEvents({ rebase: isLive(), interactive });

// rebase drops the snapshot for the newest data; interactive marks a load
// the user asked for, which shows progress.
async function fetchEvents({ rebase = false, interactive = false } = {}) {
  const previous = view.data ? view.anchor : 0;
  if (rebase) view.anchor = 0;
  const params = rangeParams();
  for (const [key, value] of Object.entries(view.filters)) if (value) params.set(key, value);
  params.set("sort", view.sort.key);
  params.set("order", view.sort.order);
  params.set("offset", String(view.offset));
  params.set("limit", String(view.limit));
  if (view.anchor) params.set("anchor", String(view.anchor));
  const sequence = ++view.sequence;
  if (interactive) $("events-area").classList.add("loading");
  try {
    const data = await api("GET", "/api/events?" + params);
    if (sequence !== view.sequence) return;
    view.anchor = data.anchor;
    if (view.offset > 0 && view.offset >= data.total) {
      view.offset = Math.max(0, (Math.ceil(data.total / view.limit) - 1) * view.limit);
      return fetchEvents({ interactive });
    }
    const fresh = rebase && previous && data.anchor > previous ? previous : 0;
    view.data = data;
    renderEvents({ fresh, announce: interactive });
  } catch (error) {
    if (sequence === view.sequence && !view.data) $("events-empty").replaceChildren(emptyState("事件加载失败", error.message));
    throw error;
  } finally {
    if (sequence === view.sequence) $("events-area").classList.remove("loading");
  }
}

function showPage(options = {}) {
  $("events-wrap").scrollTop = 0;
  return fetchEvents({ ...options, interactive: true }).catch(console.error);
}

// Keep an active filter selectable when its count drops to zero.
function filterOptions(key, allLabel, options, label = (value) => value) {
  const current = view.filters[key];
  const rows = current && !options.some((option) => option.value === current) ? [{ value: current, count: 0 }, ...options] : options;
  const total = options.reduce((sum, option) => sum + option.count, 0);
  return [{ value: "", label: allLabel, detail: int(total) }, ...rows.map((option) => ({ value: option.value, label: label(option.value) || "（空）", detail: int(option.count) }))];
}

function updateFilters() {
  const { data, filters } = view;
  if (!data) return;
  selects.model.update(filterOptions("model", "全部模型", data.models), filters.model);
  selects.project.update(filterOptions("project", "全部项目", data.projects, shortPath), filters.project);
  selects.status.update(STATUSES, filters.status);
  $("filter-clear").disabled = !Object.values(filters).some(Boolean);
}

// Times and numbers sort newest or largest first, text A to Z; a third click
// returns to newest first.
const firstOrder = (column) => (column.num || column.sort === "time" ? "desc" : "asc");
function setSort(column) {
  const first = firstOrder(column);
  const { sort } = view;
  if (sort.key !== column.sort) view.sort = { key: column.sort, order: first };
  else if (sort.order === first) view.sort = { key: column.sort, order: first === "asc" ? "desc" : "asc" };
  else view.sort = DEFAULT_SORT;
  savePref("event-sort", view.sort);
  view.offset = 0;
  view.anchor = 0;
  showPage();
}

function header(columns) {
  return el(
    "tr",
    {},
    columns.map((column) => {
      const className = [column.num && "num", column.className].filter(Boolean).join(" ") || null;
      if (!column.sort) return el("th", { class: className, text: column.label });
      const active = view.sort.key === column.sort;
      return el(
        "th",
        { class: [className, firstOrder(column) === "desc" && "desc-first"].filter(Boolean).join(" "), "aria-sort": active ? (view.sort.order === "asc" ? "ascending" : "descending") : null },
        el("button", { class: "sort-button", type: "button", onclick: () => setSort(column) }, column.label, icon("arrow-up", { class: "sort-mark" }))
      );
    })
  );
}

function emptyState(title, detail, action) {
  return el("div", { class: "empty-state" }, el("strong", { text: title }), detail && el("span", { text: detail }), action);
}

// fresh is the previous snapshot's anchor; rows saved after it flash briefly.
function renderEvents({ fresh = 0, announce = false } = {}) {
  const { data } = view;
  updateFilters();
  // A single agent makes the agent column redundant.
  const columns = [
    ...columnLayout()
      .filter(({ column, visible }) => visible && !(column.id === "agent" && state.agent))
      .map(({ column }) => column),
    DETAIL_COLUMN
  ];
  const entries = data?.entries || [];
  const rows = JSON.stringify([columns.map((column) => column.id), view.sort, entries]);
  if (fresh || rows !== view.rows) {
    view.rows = rows;
    hideTip();
    $("events-table").querySelector("thead").replaceChildren(header(columns));
    $("events-table").querySelector("tbody").replaceChildren(
      ...entries.map((entry) =>
        el(
          "tr",
          { class: fresh && entry.seq > fresh ? "fresh" : null, onclick: (event) => openRow(event, entry) },
          columns.map((column) => el("td", { class: [column.num && "num", column.className].filter(Boolean).join(" ") || null }, column.cell(entry)))
        )
      )
    );
  }
  $("events-empty").classList.toggle("hidden", entries.length > 0);
  if (!entries.length) {
    const filtered = Object.values(view.filters).some(Boolean);
    $("events-empty").replaceChildren(
      !data
        ? emptyState("正在加载事件…")
        : !state.server?.ready
          ? emptyState("正在扫描日志…")
          : filtered
            ? emptyState("没有符合筛选条件的事件", null, el("button", { class: "button", type: "button", text: "清除筛选", onclick: clearFilters }))
            : emptyState("所选范围内没有请求事件")
    );
  }
  pager.render(view, { live: isLive(), announce });
}

function openRow(event, entry) {
  if (event.target.closest("button, a, input, .tag[tabindex], .has-tip") || getSelection()?.toString()) return;
  openDetail(entry);
}

export const renderEventStatus = () => view.data && pager.status(view, isLive());

function setFilters(filters) {
  view.filters = filters;
  savePref("event-filters", filters);
  view.offset = 0;
  view.anchor = 0;
  showPage();
}

function clearFilters() {
  $("filter-q").value = "";
  setFilters({ ...NO_FILTERS });
}

export function initEvents() {
  pager = createPager({
    go(offset) {
      if (offset === view.offset) return;
      view.offset = offset;
      showPage();
    },
    latest() {
      view.offset = 0;
      showPage({ rebase: true });
    },
    // Stay on the page that holds the current first row.
    resize(limit) {
      view.offset = Math.floor(view.offset / limit) * limit;
      view.limit = limit;
      showPage();
    },
    // Jump to the newest event of a span, or the oldest in ascending order;
    // other orders switch back to newest first.
    pick(index, { ok, failed }) {
      if (view.sort.key !== "time") {
        view.sort = DEFAULT_SORT;
        savePref("event-sort", view.sort);
      }
      const ascending = view.sort.order === "asc";
      let offset = 0;
      for (let i = 0; i < ok.length; i++) if (ascending ? i < index : i > index) offset += ok[i] + failed[i];
      view.offset = Math.floor(offset / view.limit) * view.limit;
      showPage();
    }
  });
  initColumnSettings(() => {
    view.rows = "";
    renderEvents();
  });
  initEventDetail();

  let searchTimer;
  $("filter-q").value = view.filters.q;
  $("filter-q").addEventListener("input", (event) => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => setFilters({ ...view.filters, q: event.target.value.trim() }), 300);
  });
  for (const [key, label] of [["model", "模型"], ["project", "项目"], ["status", "结果"]]) {
    selects[key] = select({
      label,
      value: view.filters[key],
      options: key === "status" ? STATUSES : [{ value: "", label: "全部" + label }],
      searchable: key !== "status",
      showSelectedDetail: false,
      onChange: (value) => setFilters({ ...view.filters, [key]: value })
    });
    $("filter-" + key).replaceChildren(selects[key].element);
  }
  $("filter-clear").addEventListener("click", clearFilters);
  let frame = 0;
  $("events-wrap").addEventListener(
    "scroll",
    () => {
      frame ||= requestAnimationFrame(() => {
        frame = 0;
        renderEventStatus();
      });
    },
    { passive: true }
  );
  $("event-dialog").addEventListener("close", renderEventStatus);
  renderEvents();
}
