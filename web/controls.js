// Dropdowns, segmented switches, popovers and copy buttons.
import { el, icon } from "./dom.js";
import { tip } from "./tooltip.js";
import { joinText } from "./core.js";

export function copyButton(label, value) {
  const button = tip(el("button", { class: "copy-button", type: "button", "aria-label": label }, icon("copy")), label);
  button.addEventListener("click", async (event) => {
    event.stopPropagation();
    await navigator.clipboard.writeText(value);
    button.replaceChildren(icon("check"));
    button.classList.add("copied");
    setTimeout(() => {
      button.replaceChildren(icon("copy"));
      button.classList.remove("copied");
    }, 1200);
  });
  return button;
}

export function segmented({ label, value = "", options = [], onChange }) {
  const element = el("div", { class: "segmented", role: "group", "aria-label": label });
  function update(nextOptions, nextValue) {
    options = nextOptions;
    value = nextValue;
    const previous = new Map([...element.children].map((button) => [button.dataset.value, button]));
    options.forEach((option, index) => {
      const button =
        previous.get(option.value) ||
        el("button", {
          type: "button",
          "data-value": option.value,
          onclick: () => {
            if (button.dataset.value === value) return;
            update(options, button.dataset.value);
            onChange(button.dataset.value);
          }
        });
      previous.delete(option.value);
      button.classList.toggle("active", option.value === value);
      button.setAttribute("aria-pressed", String(option.value === value));
      button.replaceChildren(option.label, ...(option.detail == null ? [] : [el("span", { class: "segmented-count", text: option.detail })]));
      if (element.children[index] !== button) element.insertBefore(button, element.children[index] || null);
    });
    for (const button of previous.values()) button.remove();
  }
  update(options, value);
  return { element, update };
}

// Options are { value, label, shortLabel?, detail?, icon?() }. Search appears
// once a searchable list is longer than searchThreshold.
export function select({ label, value = "", options = [], onChange, searchable = false, searchThreshold = 6, showSelectedDetail = true, align = "start", className = "" }) {
  const current = el("span", { class: "select-value" });
  const button = el("button", { type: "button", class: "select-button", "aria-haspopup": "listbox" }, current, icon("chevron-down", { class: "select-chevron" }));
  const list = el("div", { class: "select-options", role: "listbox", "aria-label": label });
  const search = searchable && el("input", { type: "search", class: "select-search", placeholder: joinText("搜索", label), "aria-label": joinText("搜索", label), autocomplete: "off", oninput: () => renderOptions() });
  const panel = el("div", { class: "select-menu" }, list);
  const element = el("div", { class: ["select", className].filter(Boolean).join(" ") }, button, panel);
  const hasSearch = () => searchable && options.length > searchThreshold;
  // Labels cut short show in full on hover.
  const content = (option, showDetail = true, text = option.label) => [
    option.icon?.(),
    text && tip(el("span", { class: "select-label", text }), text, { overflowOnly: true }),
    showDetail && option.detail != null && el("span", { class: "select-detail", text: option.detail })
  ];
  const focusOption = (option) => {
    if (!option) return;
    option.focus({ preventScroll: true });
    if (option.offsetTop < list.scrollTop) list.scrollTop = option.offsetTop;
    else if (option.offsetTop + option.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = option.offsetTop + option.offsetHeight - list.clientHeight;
  };
  function choose(option) {
    panel.hidePopover();
    button.focus();
    if (option.value === value) return;
    update(options, option.value);
    onChange(option.value);
  }
  function renderOptions() {
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.value : null;
    const words = hasSearch() ? search.value.trim().toLowerCase().split(/\s+/).filter(Boolean) : [];
    const filtered = options.filter((option) => words.every((word) => option.label.toLowerCase().includes(word)));
    list.replaceChildren(
      ...filtered.map((option) =>
        el(
          "button",
          {
            type: "button",
            role: "option",
            class: "select-option",
            tabIndex: -1,
            "data-value": option.value,
            "aria-selected": String(option.value === value),
            "data-label": option.label.toLowerCase(),
            onclick: () => choose(option)
          },
          content(option),
          icon("check", { class: "select-check" })
        )
      )
    );
    if (!filtered.length) list.append(el("div", { class: "select-empty", text: "没有匹配项" }));
    if (focused !== null) {
      const items = [...list.querySelectorAll("[role=option]")];
      focusOption(items.find((option) => option.dataset.value === focused) || items.find((option) => option.dataset.value === value) || items[0]);
      if (!items.length) (hasSearch() ? search : button).focus({ preventScroll: true });
    }
  }
  function update(nextOptions, nextValue) {
    const searchFocused = search && document.activeElement === search;
    options = nextOptions;
    value = nextValue;
    if (hasSearch()) {
      if (search.parentElement !== panel) panel.prepend(search);
    } else if (search) {
      search.remove();
      search.value = "";
    }
    panel.classList.toggle("searchable", hasSearch());
    const selected = options.find((option) => option.value === value) || { label, value };
    current.replaceChildren(...content(selected, showSelectedDetail, selected.shortLabel ?? selected.label).filter(Boolean));
    button.setAttribute("aria-label", `${label}：${selected.label}`);
    renderOptions();
    if (searchFocused && !hasSearch()) focusOption(list.querySelector('[aria-selected="true"]') || list.firstElementChild);
  }
  attachPopover(button, panel, {
    align,
    onOpen: () => {
      if (hasSearch()) search.value = "";
      renderOptions();
      if (hasSearch()) search.focus({ preventScroll: true });
      else focusOption(list.querySelector('[aria-selected="true"]') || list.firstElementChild);
    }
  });
  button.addEventListener("keydown", (event) => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    panel.showPopover();
  });
  let typed = "", typedAt = 0;
  panel.addEventListener("keydown", (event) => {
    if (event.key === "Tab") {
      panel.hidePopover();
      button.focus();
      return;
    }
    const items = [...list.querySelectorAll("[role=option]")];
    if (!items.length) return;
    const index = items.indexOf(document.activeElement);
    if (event.key === "Enter" && document.activeElement === search) {
      event.preventDefault();
      items[0].click();
      return;
    }
    const next = { ArrowDown: (index + 1) % items.length, ArrowUp: index <= 0 ? items.length - 1 : index - 1, Home: 0, End: items.length - 1 }[event.key];
    if (next !== undefined && !(document.activeElement === search && ["Home", "End"].includes(event.key))) {
      event.preventDefault();
      focusOption(items[next]);
    } else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && document.activeElement !== search && event.key !== " ") {
      typed = (Date.now() - typedAt < 600 ? typed : "") + event.key.toLowerCase();
      typedAt = Date.now();
      focusOption(items.find((item) => item.dataset.label.startsWith(typed)));
    }
  });
  update(options, value);
  return { element, button, update };
}

let popoverID = 0;

export function attachPopover(button, panel, { align = "start", onOpen = () => {} } = {}) {
  panel.id ||= `popover-${++popoverID}`;
  panel.setAttribute("popover", "auto");
  panel.classList.add("control-popover");
  button.popoverTargetElement = panel;
  button.setAttribute("aria-controls", panel.id);
  button.setAttribute("aria-expanded", "false");
  const position = () => {
    const box = button.getBoundingClientRect();
    const left = align === "end" ? box.right - panel.offsetWidth : box.left;
    const below = innerHeight - box.bottom - 20;
    const above = box.top - 20;
    panel.style.maxHeight = Math.max(80, Math.max(below, above)) + "px";
    panel.style.minWidth = Math.max(box.width, 0) + "px";
    panel.style.left = Math.max(12, Math.min(left, innerWidth - panel.offsetWidth - 12)) + "px";
    const down = panel.offsetHeight <= below || below >= above;
    panel.dataset.side = down ? "bottom" : "top";
    panel.style.top = (down ? box.bottom + 6 : box.top - panel.offsetHeight - 6) + "px";
  };
  let listeners;
  panel.addEventListener("toggle", () => {
    const open = panel.matches(":popover-open");
    button.setAttribute("aria-expanded", String(open));
    listeners?.abort();
    if (!open) return;
    onOpen();
    position();
    listeners = new AbortController();
    addEventListener("resize", position, { signal: listeners.signal });
    addEventListener("scroll", (event) => {
      if (!panel.contains(event.target)) panel.hidePopover();
    }, { capture: true, signal: listeners.signal });
  });
  panel.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    panel.hidePopover();
    button.focus();
  });
}
