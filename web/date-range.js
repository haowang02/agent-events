import { $, el, icon } from "./dom.js";
import { attachPopover } from "./controls.js";
import { state, savePref, selectedRange } from "./core.js";

const PRESETS = { today: "今天", yesterday: "昨天", "24h": "最近 24 小时", "7d": "最近 7 天", "30d": "最近 30 天", "90d": "最近 90 天", all: "全部时间" };
const pad = (value) => String(value).padStart(2, "0");
const dateText = (at) => `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
const timeText = (at) => `${pad(at.getHours())}:${pad(at.getMinutes())}`;
const monthStart = (at, offset = 0) => new Date(at.getFullYear(), at.getMonth() + offset, 1);
const parseDate = (date, time) => {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date) || !/^\d{2}:\d{2}$/.test(time)) return null;
  const at = new Date(`${date}T${time}`);
  return Number.isFinite(at.getTime()) && dateText(at) === date && timeText(at) === time ? at : null;
};

export function initRange(onChange) {
  let month, choosingEnd = false, hoverDay = "";
  const fields = {};
  const buttonLabel = el("span");
  const button = el("button", { type: "button", class: "range-button", "aria-haspopup": "dialog" }, icon("calendar"), buttonLabel, el("i", { class: "select-chevron", "aria-hidden": "true" }));
  const calendars = el("div", { class: "range-calendars" });
  const message = el("span", { class: "range-message", "aria-live": "polite" });
  const apply = el("button", { type: "button", class: "button primary", text: "应用范围", onclick: () => {
    const { from, to } = read();
    if (from && to && from < to) commit({ mode: "custom", from: from.toISOString(), to: to.toISOString() });
  } });
  const shortcuts = el("div", { class: "range-shortcuts", "aria-label": "快捷时间范围" },
    el("span", { class: "range-section-label", text: "快捷范围" }),
    Object.entries(PRESETS).map(([mode, label]) => el("button", { type: "button", text: label, "data-mode": mode, onclick: () => commit({ mode }) }))
  );
  const inputs = el("div", { class: "range-inputs" }, ...[["from", "开始时间"], ["to", "结束时间"]].map(([key, label]) => {
    const day = el("input", { type: "text", class: "range-date-input", placeholder: "YYYY-MM-DD", "aria-label": label + "日期", autocomplete: "off", maxLength: 10 });
    const time = el("input", { type: "text", class: "range-time-input", placeholder: "HH:mm", "aria-label": label + "时分", autocomplete: "off", maxLength: 5 });
    fields[key] = { day, time };
    for (const input of [day, time]) input.addEventListener("input", () => {
      choosingEnd = false;
      hoverDay = "";
      highlight();
      validate();
    });
    day.addEventListener("change", () => {
      const at = parseDate(day.value, "00:00");
      if (at) { month = monthStart(at, key === "to" ? -1 : 0); renderCalendars(); }
    });
    return el("div", { class: "range-endpoint" }, el("span", { class: "range-section-label", text: label }), el("div", { class: "range-input-group" }, day, time));
  }));
  const panel = el("div", { class: "range-panel", role: "dialog", "aria-label": "选择时间范围" }, shortcuts,
    el("div", { class: "range-editor" },
      el("div", { class: "range-editor-heading" }, el("strong", { text: "自定义时间范围" }), el("span", { class: "muted small", text: Intl.DateTimeFormat().resolvedOptions().timeZone })),
      calendars, inputs,
      el("div", { class: "range-footer" }, message, el("button", { type: "button", class: "button", text: "取消", onclick: close }), apply)
    )
  );
  function close() {
    panel.hidePopover();
    button.focus();
  }
  function commit(range) {
    state.range = range;
    savePref("range", range);
    renderLabel();
    close();
    onChange();
  }
  function renderLabel() {
    const { mode, from, to } = state.range;
    buttonLabel.textContent = PRESETS[mode] || `${dateText(new Date(from))} ${timeText(new Date(from))} — ${dateText(new Date(to))} ${timeText(new Date(to))}`;
    button.setAttribute("aria-label", `时间范围：${buttonLabel.textContent}`);
  }
  function read() {
    return Object.fromEntries(Object.entries(fields).map(([key, { day, time }]) => [key, parseDate(day.value, time.value)]));
  }
  function validate() {
    const { from, to } = read();
    apply.disabled = !from || !to || from >= to;
    message.classList.toggle("invalid", !choosingEnd && apply.disabled);
    message.textContent = choosingEnd ? "" : !from || !to ? "请输入有效日期和时间" : from >= to ? "结束时间须晚于开始时间" : "";
    for (const { day, time } of Object.values(fields)) {
      day.setAttribute("aria-invalid", String(!(choosingEnd && !day.value) && !parseDate(day.value, "00:00")));
      time.setAttribute("aria-invalid", String(!parseDate("2000-01-01", time.value)));
    }
  }
  function highlight() {
    let start = fields.from.day.value;
    let end = choosingEnd ? hoverDay : fields.to.day.value;
    if (!end) end = start;
    if (end && end < start) [start, end] = [end, start];
    for (const day of calendars.querySelectorAll("[data-day]")) {
      const key = day.dataset.day;
      const endpoint = key === start || key === end;
      day.classList.toggle("range-start", key === start);
      day.classList.toggle("range-end", key === end);
      day.classList.toggle("in-range", !!end && key > start && key < end);
      day.setAttribute("aria-pressed", String(endpoint));
    }
  }
  function choose(day) {
    if (!choosingEnd) {
      fields.from.day.value = day;
      fields.from.time.value = "00:00";
      fields.to.day.value = "";
      fields.to.time.value = "23:59";
      choosingEnd = true;
    } else {
      const first = fields.from.day.value;
      fields.from.day.value = day < first ? day : first;
      fields.to.day.value = day < first ? first : day;
      choosingEnd = false;
    }
    hoverDay = "";
    highlight();
    validate();
  }
  function shiftMonth(offset) {
    month = monthStart(month, offset);
    renderCalendars();
    calendars.querySelector(`[aria-label="${offset < 0 ? "上个月" : "下个月"}"]`).focus({ preventScroll: true });
  }
  function renderCalendars() {
    const today = dateText(new Date());
    calendars.replaceChildren(...[0, 1].map((offset) => {
      const at = monthStart(month, offset);
      const title = `${at.getFullYear()} 年 ${at.getMonth() + 1} 月`;
      const days = [];
      for (let index = 0; index < 42; index++) {
        const day = new Date(at.getFullYear(), at.getMonth(), index - at.getDay() + 1);
        const key = dateText(day);
        days.push(day.getMonth() !== at.getMonth() ? el("span", { class: "calendar-outside", text: day.getDate(), "aria-hidden": "true" }) : el("button", {
          type: "button", class: "calendar-day", text: day.getDate(), "data-day": key, "aria-label": key,
          "aria-current": key === today ? "date" : null,
          tabIndex: key === fields.from.day.value || day.getDate() === 1 ? 0 : -1,
          onclick: () => choose(key),
          onpointerenter: () => { if (choosingEnd) { hoverDay = key; highlight(); } }
        }));
      }
      return el("section", { class: "calendar-month", "aria-label": title },
        el("div", { class: "calendar-heading" },
          offset === 0 ? el("button", { type: "button", class: "icon-button", "aria-label": "上个月", onclick: () => shiftMonth(-1) }, icon("chevronLeft")) : el("span"),
          el("strong", { text: title }),
          offset === 1 ? el("button", { type: "button", class: "icon-button", "aria-label": "下个月", onclick: () => shiftMonth(1) }, icon("chevronRight")) : el("span")
        ),
        el("div", { class: "calendar-weekdays", "aria-hidden": "true" }, ...["日", "一", "二", "三", "四", "五", "六"].map((text) => el("span", { text }))),
        el("div", { class: "calendar-grid" }, days)
      );
    }));
    highlight();
  }
  inputs.addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); apply.click(); }
  });
  calendars.addEventListener("pointerleave", () => { hoverDay = ""; highlight(); });
  calendars.addEventListener("keydown", (event) => {
    const key = event.target.dataset.day;
    const offset = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }[event.key];
    if (!key || offset === undefined) return;
    event.preventDefault();
    const day = parseDate(key, "00:00");
    day.setDate(day.getDate() + offset);
    const next = dateText(day);
    if (!calendars.querySelector(`[data-day="${next}"]`)) {
      month = monthStart(day, offset > 0 ? -1 : 0);
      renderCalendars();
    }
    calendars.querySelector(`[data-day="${next}"]`)?.focus({ preventScroll: true });
  });
  attachPopover(button, panel, { align: "end", onOpen: () => {
    const range = selectedRange();
    const to = new Date(range.to || Date.now());
    const from = new Date(range.from || to.getTime() - 7 * 86400000);
    for (const [key, at] of [["from", from], ["to", to]]) {
      fields[key].day.value = dateText(at);
      fields[key].time.value = timeText(at);
    }
    choosingEnd = false;
    hoverDay = "";
    month = monthStart(from);
    for (const shortcut of shortcuts.querySelectorAll("button")) shortcut.setAttribute("aria-pressed", String(shortcut.dataset.mode === state.range.mode));
    renderCalendars();
    validate();
    (shortcuts.querySelector('[aria-pressed="true"]') || fields.from.day).focus({ preventScroll: true });
  } });
  $("range-control").replaceChildren(button, panel);
  renderLabel();
}
