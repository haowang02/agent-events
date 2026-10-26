// DOM building, icons, tooltips and drag sorting.

export const $ = (id) => document.getElementById(id);

// el("div", { class: "x", text: "…", onclick, tip }, ...children). Null and
// false attributes and children are skipped; child arrays are flattened.
export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value == null || value === false) continue;
    if (key === "text") node.textContent = value;
    else if (key === "tip") tip(node, value);
    else if (key === "style") for (const [name, v] of Object.entries(value)) node.style.setProperty(name, v);
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else if (value === true) node.setAttribute(key, "");
    else if (key in node && !key.includes("-")) node[key] = value;
    else node.setAttribute(key, value);
  }
  node.append(...children.flat(Infinity).filter((child) => child != null && child !== false));
  return node;
}

const ICONS = {
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
  columns: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16M15 4v16"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
  check: '<path d="M5 12l5 5 9-10"/>',
  grip: '<circle cx="9" cy="6" r="1.6"/><circle cx="15" cy="6" r="1.6"/><circle cx="9" cy="12" r="1.6"/><circle cx="15" cy="12" r="1.6"/><circle cx="9" cy="18" r="1.6"/><circle cx="15" cy="18" r="1.6"/>'
};

export function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  svg.innerHTML = ICONS[name];
  return svg;
}

export function copyButton(label, value) {
  const button = el("button", { class: "copy-button", type: "button", "aria-label": label, tip: label }, icon("copy"));
  button.addEventListener("click", async () => {
    await navigator.clipboard.writeText(value);
    button.replaceChildren(icon("check"));
    setTimeout(() => button.replaceChildren(icon("copy")), 1200);
  });
  return button;
}

// tip(node, "text") or tip(node, () => [nodes]) shows a tooltip on hover and
// keyboard focus; empty content removes it. With overflowOnly, a text tip
// shows only while the node's text is truncated.
const tips = new WeakMap();
let tipAnchor = null;

export function tip(node, content, { overflowOnly = false } = {}) {
  if (!content) {
    tips.delete(node);
    return node;
  }
  tips.set(node, { content, overflowOnly });
  if (typeof content === "function" && node.tabIndex < 0) node.tabIndex = 0;
  return node;
}

function tipTarget(target) {
  for (let node = target; node instanceof Element; node = node.parentElement) {
    if (tips.has(node)) return node;
  }
  return null;
}

function showTip(anchor) {
  const tooltip = $("tooltip");
  const { content, overflowOnly } = tips.get(anchor);
  if (overflowOnly && anchor.scrollWidth <= anchor.clientWidth + 1) return;
  tipAnchor = anchor;
  tooltip.replaceChildren(...(typeof content === "function" ? content() : [el("pre", { text: content })]));
  (anchor.closest("dialog") || document.body).append(tooltip);
  if (!tooltip.matches(":popover-open")) tooltip.showPopover();
  const box = anchor.getBoundingClientRect();
  const { offsetWidth: width, offsetHeight: height } = tooltip;
  const top = box.top - height - 8 >= 8 ? box.top - height - 8 : box.bottom + 8;
  tooltip.style.top = Math.max(8, Math.min(top, innerHeight - height - 8)) + "px";
  tooltip.style.left = Math.min(Math.max(8, box.left + box.width / 2 - width / 2), innerWidth - width - 8) + "px";
}

export function hideTip() {
  tipAnchor = null;
  const tooltip = $("tooltip");
  if (tooltip.matches(":popover-open")) tooltip.hidePopover();
}

document.addEventListener("pointerover", (event) => {
  const anchor = tipTarget(event.target);
  if (anchor === tipAnchor || event.target.closest?.("#tooltip")) return;
  if (anchor) showTip(anchor);
  else hideTip();
});
document.addEventListener("focusin", (event) => {
  const anchor = tipTarget(event.target);
  if (anchor) showTip(anchor);
  else hideTip();
});
document.addEventListener("focusout", hideTip);
addEventListener("scroll", hideTip, true);
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && tipAnchor) hideTip();
});

// Lets the children of a vertical list be reordered by dragging their handle,
// or with the arrow keys on a focused handle. moved(item) runs after each move.
export function makeSortable(list, handleSelector, moved) {
  let drag = null;
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
    const items = [...list.children];
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
  // Capture Escape before the surrounding dialog closes on it.
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
    const item = [...list.children].find((child) => child.contains(handle));
    const neighbor = { ArrowUp: item.previousElementSibling, ArrowDown: item.nextElementSibling }[event.key];
    if (!neighbor) return;
    event.preventDefault();
    if (event.key === "ArrowUp") item.after(neighbor);
    else item.before(neighbor);
    handle.focus();
    item.scrollIntoView({ block: "nearest" });
    moved(item);
  });
}
