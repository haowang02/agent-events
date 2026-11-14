// Tables sorted in the browser.
import { el, icon } from "./dom.js";
import { pref, savePref, compareValues } from "./core.js";

export const numberColumn = (id, label, format, value = (row) => row[id]) => ({
  id,
  label,
  num: true,
  sort: value,
  cell: (row) => format(value(row))
});

// Numbers sort descending first and text ascending, then the other way, and
// a third click restores the initial order.
function nextSort(sort, column, initial) {
  const first = column.num ? "desc" : "asc";
  if (sort.key !== column.id) return { key: column.id, order: first };
  if (sort.order === first) return { key: column.id, order: first === "asc" ? "desc" : "asc" };
  return initial;
}

// Columns are { id, label, cell, sort?, num?, className? }; a column with sort
// orders the rows when its header is clicked. The order is saved under
// sortPref. footer is a totals row drawn with the same columns; onRow makes
// rows open something on click.
export function dataTable({ columns, rows, footer, sortPref, initialSort, onRow, ...attrs }) {
  const sortable = columns.filter((column) => column.sort);
  const [tiebreak] = sortable;
  const saved = sortPref && pref(sortPref, null);
  let sort = sortable.some((column) => column.id === saved?.key) && ["asc", "desc"].includes(saved.order) ? saved : initialSort;
  const cellClass = (column) => [column.num && "num", column.className].filter(Boolean).join(" ") || null;
  const cells = (row) => columns.map((column) => el("td", { class: cellClass(column) }, column.cell(row)));
  const heads = columns.map((column) =>
    el(
      "th",
      { class: [cellClass(column), column.sort && column.num && "desc-first"].filter(Boolean).join(" ") || null },
      column.sort
        ? el(
            "button",
            {
              class: "sort-button",
              type: "button",
              onclick: () => {
                sort = nextSort(sort, column, initialSort);
                if (sortPref) savePref(sortPref, sort);
                render();
              }
            },
            column.label,
            icon("arrow-up", { class: "sort-mark" })
          )
        : column.label
    )
  );
  const items = rows.map((row) => {
    const node = el("tr", {}, cells(row));
    if (onRow) {
      node.classList.add("clickable");
      node.addEventListener("click", (event) => {
        if (!event.target.closest("button, a, input") && !getSelection()?.toString()) onRow(row);
      });
    }
    return { row, node };
  });
  const body = el("tbody");
  function render() {
    const active = sortable.find((column) => column.id === sort.key);
    columns.forEach((column, index) => {
      if (column === active) heads[index].setAttribute("aria-sort", sort.order === "asc" ? "ascending" : "descending");
      else heads[index].removeAttribute("aria-sort");
    });
    const direction = sort.order === "asc" ? 1 : -1;
    if (active)
      items.sort((a, b) => compareValues(active.sort(a.row), active.sort(b.row), direction) || compareValues(tiebreak.sort(a.row), tiebreak.sort(b.row)));
    body.replaceChildren(...items.map((item) => item.node));
  }
  render();
  return el("table", attrs, el("thead", {}, el("tr", {}, heads)), body, footer && el("tfoot", {}, el("tr", {}, cells(footer))));
}
