// Hover and focus tooltips. tip(node, content) attaches one: content is text,
// a function returning nodes, or a function returning a Promise of nodes.
// The pointer can move into an open tooltip, so its content can scroll.
import { $, el } from "./dom.js";

const tips = new WeakMap();
const OPEN_DELAY = 400, CLOSE_DELAY = 150, WARM_DELAY = 300;
let anchor = null, pending = null, openTimer, closeTimer, warmUntil = 0, switching = false, version = 0;

// With overflowOnly, a text tip shows only while the node's text is truncated.
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
  for (let node = target; node instanceof Element; node = node.parentElement) if (tips.has(node)) return node;
  return null;
}

function usable(node) {
  const spec = node && tips.get(node);
  return spec && node.isConnected && node.getClientRects().length && (!spec.overflowOnly || node.scrollWidth > node.clientWidth + 1);
}

function cancelOpen() {
  clearTimeout(openTimer);
  pending = null;
}

export function hideTip() {
  version++;
  cancelOpen();
  clearTimeout(closeTimer);
  if (anchor) {
    const ids = (anchor.getAttribute("aria-describedby") || "").split(/\s+/).filter((id) => id && id !== "tooltip");
    if (ids.length) anchor.setAttribute("aria-describedby", ids.join(" "));
    else anchor.removeAttribute("aria-describedby");
    warmUntil = performance.now() + WARM_DELAY;
  }
  anchor = null;
  const tooltip = $("tooltip");
  switching = true;
  try {
    if (tooltip.matches(":popover-open")) tooltip.hidePopover();
  } finally {
    switching = false;
  }
}

function position(target) {
  const tooltip = $("tooltip");
  const box = target.getBoundingClientRect();
  const { offsetWidth: width, offsetHeight: height } = tooltip;
  const top = box.top - height - 8 >= 8 ? box.top - height - 8 : box.bottom + 8;
  tooltip.style.top = Math.max(8, Math.min(top, innerHeight - height - 8)) + "px";
  tooltip.style.left = Math.min(Math.max(8, box.left + box.width / 2 - width / 2), innerWidth - width - 8) + "px";
}

// Content with its own scroll area takes focus there, so arrow keys scroll it.
function focusTip() {
  const tooltip = $("tooltip");
  (tooltip.querySelector(".tip-scroll") || tooltip).focus({ preventScroll: true });
}

function show(target) {
  if (switching || !usable(target)) return;
  const { content } = tips.get(target);
  hideTip();
  const tooltip = $("tooltip");
  const shown = version;
  const result = typeof content === "function" ? content() : [el("pre", { text: content })];
  const replace = (nodes) => {
    const focused = tooltip.contains(document.activeElement);
    tooltip.replaceChildren(...nodes.filter((node) => node != null && node !== false));
    if (focused) focusTip();
  };
  if (result instanceof Promise) {
    replace([el("div", { class: "tip-loading", text: "正在加载…" })]);
    result.then(
      (nodes) => {
        if (anchor !== target || shown !== version) return;
        replace(nodes);
        position(target);
      },
      (error) => {
        if (anchor !== target || shown !== version) return;
        replace([el("div", { class: "tip-title", text: error.message })]);
        position(target);
      }
    );
  } else replace(result);
  (target.closest("dialog") || document.body).append(tooltip);
  anchor = target;
  const ids = (target.getAttribute("aria-describedby") || "").split(/\s+/).filter(Boolean);
  target.setAttribute("aria-describedby", [...new Set([...ids, "tooltip"])].join(" "));
  switching = true;
  try {
    tooltip.showPopover();
  } finally {
    switching = false;
  }
  tooltip.scrollTop = 0;
  position(target);
}

function scheduleOpen(target, point) {
  clearTimeout(closeTimer);
  cancelOpen();
  if (!usable(target)) return scheduleClose(null);
  if (anchor || performance.now() < warmUntil) return show(target);
  pending = target;
  openTimer = setTimeout(() => {
    pending = null;
    if (tipTarget(document.elementFromPoint(point.x, point.y)) === target) show(target);
  }, OPEN_DELAY);
}

function scheduleClose(next) {
  if (pending && !pending.contains(next)) cancelOpen();
  if (next && ($("tooltip").contains(next) || anchor?.contains(next))) return clearTimeout(closeTimer);
  if (anchor) {
    clearTimeout(closeTimer);
    closeTimer = setTimeout(hideTip, CLOSE_DELAY);
  }
}

document.addEventListener("pointerover", (event) => {
  if (event.pointerType === "touch") return;
  if ($("tooltip").contains(event.target)) return clearTimeout(closeTimer);
  const target = tipTarget(event.target);
  if (!target) scheduleClose(event.target);
  else if (target === anchor) clearTimeout(closeTimer);
  else if (target !== pending) scheduleOpen(target, { x: event.clientX, y: event.clientY });
});
document.addEventListener("pointerout", (event) => {
  if (event.pointerType !== "touch") scheduleClose(event.relatedTarget);
});
document.addEventListener("focusin", (event) => {
  if (switching) return;
  if ($("tooltip").contains(event.target)) return clearTimeout(closeTimer);
  const target = tipTarget(event.target);
  if (target)
    queueMicrotask(() => {
      const focused = document.activeElement;
      if (target.contains(focused) && focused.matches(":focus-visible")) show(target);
    });
  else scheduleClose(event.target);
});
document.addEventListener("focusout", (event) => scheduleClose(event.relatedTarget));
document.addEventListener("pointerdown", (event) => {
  if (!$("tooltip").contains(event.target) && !anchor?.contains(event.target)) hideTip();
});
addEventListener("scroll", (event) => {
  if (!$("tooltip").contains(event.target)) hideTip();
}, true);
addEventListener("resize", hideTip);
document.addEventListener("keydown", (event) => {
  if (event.key === "Tab") hideTip();
  if (event.key === "Escape" && (anchor || pending)) {
    event.preventDefault();
    hideTip();
  }
  if (event.key === "ArrowDown" && typeof tips.get(event.target)?.content === "function") {
    event.preventDefault();
    show(event.target);
    focusTip();
  }
});
