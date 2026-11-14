// The column settings dialog of the events table.
import { $, el, icon } from "./dom.js";
import { pref, savePref } from "./core.js";
import { COLUMNS } from "./event-fields.js";

// The saved order, with columns added later inserted before their next
// default neighbor. preference {} gives the default layout.
export function columnLayout(preference = pref("columns", null)) {
  const order = preference?.order || [];
  const hidden = preference?.hidden || COLUMNS.filter((column) => column.hidden).map((column) => column.id);
  const ordered = [...new Set(order)].map((id) => COLUMNS.find((column) => column.id === id)).filter(Boolean);
  for (const [index, column] of COLUMNS.entries()) {
    if (ordered.includes(column)) continue;
    const next = COLUMNS.slice(index + 1).find((candidate) => ordered.includes(candidate));
    ordered.splice(next ? ordered.indexOf(next) : ordered.length, 0, column);
  }
  // Columns added after the order was saved start with their default visibility.
  const known = (column) => !preference?.order || order.includes(column.id);
  return ordered.map((column) => ({ column, visible: column.required || (!hidden.includes(column.id) && (known(column) || !column.hidden)) }));
}

const rows = () => [...$("columns-list").children];

function renderColumns(layout) {
  $("columns-list").replaceChildren(
    ...layout.map(({ column, visible }) =>
      el(
        "div",
        { class: "column-setting", "data-column": column.id },
        el("button", { class: "column-handle", type: "button", "aria-label": `移动「${column.label}」列` }, icon("grip")),
        el(
          "label",
          {},
          el("span", { text: column.label }),
          el("span", { class: "switch" }, el("input", { type: "checkbox", role: "switch", checked: visible, disabled: !!column.required }), el("i"))
        )
      )
    )
  );
  $("columns-search").value = "";
  updateDefaults();
}

function updateDefaults() {
  const defaults = columnLayout({});
  $("columns-reset").disabled = rows().every((row, index) => row.dataset.column === defaults[index].column.id && row.querySelector("input").checked === defaults[index].visible);
  $("columns-all").disabled = rows().every((row) => row.querySelector("input").checked);
  $("columns-count").textContent = `显示 ${rows().filter((row) => row.querySelector("input").checked).length} / ${rows().length} 列`;
}

export function initColumnSettings(onSave) {
  $("columns-button").replaceChildren(icon("columns-3-cog"), "列设置");
  $("columns-button").addEventListener("click", () => {
    renderColumns(columnLayout());
    $("columns-dialog").showModal();
  });
  $("columns-reset").addEventListener("click", () => renderColumns(columnLayout({})));
  $("columns-all").addEventListener("click", () => {
    for (const input of $("columns-list").querySelectorAll("input")) input.checked = true;
    updateDefaults();
  });
  $("columns-list").addEventListener("change", updateDefaults);
  $("columns-cancel").addEventListener("click", () => $("columns-dialog").close());
  $("columns-save").addEventListener("click", () => {
    savePref("columns", {
      order: rows().map((row) => row.dataset.column),
      hidden: rows().filter((row) => !row.querySelector("input").checked).map((row) => row.dataset.column)
    });
    $("columns-dialog").close();
    onSave();
  });
  $("columns-search").addEventListener("input", (event) => {
    const query = event.target.value.trim().toLowerCase();
    for (const row of rows()) row.classList.toggle("hidden", !row.textContent.toLowerCase().includes(query));
  });
  reorderable($("columns-list"), ".column-handle", (row) => {
    const visible = rows().filter((item) => !item.classList.contains("hidden"));
    $("columns-status").textContent = `「${row.querySelector("label > span").textContent}」移到第 ${visible.indexOf(row) + 1} / ${visible.length} 位`;
    updateDefaults();
  });
}

// Lets the visible children of a list be reordered by dragging their handle,
// or with the arrow keys on a focused handle. moved(item) runs after a move.
function reorderable(list, handleSelector, moved) {
  let drag = null;
  const visibleItems = () => [...list.children].filter((item) => item.getClientRects().length);
  const finish = (commit) => {
    const { items, item, from, to, active } = drag;
    drag = null;
    list.classList.remove("sorting");
    item.classList.remove("dragging");
    for (const other of items) other.style.transform = "";
    if (active && commit && to !== from) {
      list.insertBefore(item, to > from ? items[to].nextSibling : items[to]);
      moved(item);
    }
  };
  list.addEventListener("pointerdown", (event) => {
    const handle = event.target.closest(handleSelector);
    if (!handle || event.button !== 0 || drag) return;
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const items = visibleItems();
    const item = items.find((child) => child.contains(handle));
    const bounds = items.map((child) => child.getBoundingClientRect());
    const from = items.indexOf(item);
    const size = bounds[from].height + (parseFloat(getComputedStyle(list).rowGap) || 0);
    drag = { pointerId: event.pointerId, items, item, bounds, from, to: from, size, startY: event.clientY, active: false };
  });
  list.addEventListener("pointermove", (event) => {
    if (drag?.pointerId !== event.pointerId) return;
    const { items, item, bounds, from, size } = drag;
    const offset = event.clientY - drag.startY;
    if (!drag.active) {
      if (Math.abs(offset) < 4) return;
      drag.active = true;
      list.classList.add("sorting");
      item.classList.add("dragging");
    }
    const own = bounds[from];
    const clamped = Math.min(Math.max(offset, bounds[0].top - own.top), bounds[bounds.length - 1].bottom - own.bottom);
    const center = (own.top + own.bottom) / 2 + clamped;
    drag.to = bounds.filter((box, index) => index !== from && center > (box.top + box.bottom) / 2).length;
    item.style.transform = `translateY(${clamped}px)`;
    items.forEach((other, index) => {
      if (other === item) return;
      const shift = index > from && index <= drag.to ? -size : index < from && index >= drag.to ? size : 0;
      other.style.transform = shift ? `translateY(${shift}px)` : "";
    });
  });
  list.addEventListener("pointerup", (event) => {
    if (drag?.pointerId === event.pointerId) finish(true);
  });
  list.addEventListener("pointercancel", (event) => {
    if (drag?.pointerId === event.pointerId) finish(false);
  });
  // Escape during a drag cancels the move without closing the dialog.
  addEventListener(
    "keydown",
    (event) => {
      if (event.key !== "Escape" || !drag?.active) return;
      event.preventDefault();
      event.stopPropagation();
      finish(false);
    },
    true
  );
  list.addEventListener("keydown", (event) => {
    const handle = event.target.closest(handleSelector);
    if (!handle || drag) return;
    const items = visibleItems();
    const item = items.find((child) => child.contains(handle));
    const index = items.indexOf(item);
    const neighbor = { ArrowUp: items[index - 1], ArrowDown: items[index + 1] }[event.key];
    if (!neighbor) return;
    event.preventDefault();
    if (event.key === "ArrowUp") neighbor.before(item);
    else neighbor.after(item);
    handle.focus();
    item.scrollIntoView({ block: "nearest" });
    moved(item);
  });
}
