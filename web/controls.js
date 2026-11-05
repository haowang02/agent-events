import { el, icon } from "./dom.js";

export function segmented({ label, value = "", options = [], onChange }) {
  const element = el("div", { class: "segmented", role: "group", "aria-label": label });
  function update(nextOptions, nextValue) {
    options = nextOptions;
    value = nextValue;
    const previous = new Map([...element.children].map((button) => [button.dataset.value, button]));
    options.forEach((option, index) => {
      const button = previous.get(option.value) || el("button", {
        type: "button", "data-value": option.value,
        onclick: () => {
          if (option.value === value) return;
          update(options, option.value);
          onChange(option.value);
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

export function select({ label, value = "", options = [], onChange, searchable = false, className = "" }) {
  const current = el("span", { class: "select-value" });
  const button = el("button", { type: "button", class: "select-button", "aria-haspopup": "listbox" }, current, el("i", { class: "select-chevron", "aria-hidden": "true" }));
  const list = el("div", { class: "select-options", role: "listbox", "aria-label": label });
  const search = searchable && el("input", { type: "search", class: "select-search", placeholder: `搜索${label}`, "aria-label": `搜索${label}`, autocomplete: "off", oninput: () => renderOptions() });
  const panel = el("div", { class: "select-menu" }, search, list);
  const element = el("div", { class: "select " + className }, button, panel);
  const content = (option) => [
    option.icon?.(),
    el("span", { class: "select-label", text: option.label }),
    option.detail != null && el("span", { class: "select-detail", text: option.detail })
  ];
  const focusOption = (option) => {
    if (!option) return;
    option.focus({ preventScroll: true });
    if (option.offsetTop < list.scrollTop) list.scrollTop = option.offsetTop;
    else if (option.offsetTop + option.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = option.offsetTop + option.offsetHeight - list.clientHeight;
  };
  function renderOptions() {
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.value : null;
    const query = search ? search.value.trim().toLowerCase() : "";
    const filtered = options.filter((option) => option.label.toLowerCase().includes(query));
    list.replaceChildren(...filtered.map((option) => el("button", {
      type: "button", role: "option", class: "select-option", tabIndex: -1,
      "data-value": option.value, "aria-selected": String(option.value === value), title: option.label,
      onclick: () => {
        panel.hidePopover();
        button.focus();
        if (option.value === value) return;
        update(options, option.value);
        onChange(option.value);
      }
    }, content(option), icon("check", { class: "select-check" }))));
    if (!filtered.length) list.append(el("div", { class: "select-empty", text: "没有匹配项" }));
    if (focused !== null) {
      const items = [...list.querySelectorAll("[role=option]")];
      focusOption(items.find((option) => option.dataset.value === focused) || items.find((option) => option.dataset.value === value) || items[0]);
      if (!items.length) (search || button).focus({ preventScroll: true });
    }
  }
  function update(nextOptions, nextValue) {
    options = nextOptions;
    value = nextValue;
    const selected = options.find((option) => option.value === value) || { label, value };
    current.replaceChildren(...content(selected).filter(Boolean));
    button.setAttribute("aria-label", `${label}：${selected.label}`);
    button.title = selected.label;
    renderOptions();
  }
  attachPopover(button, panel, { onOpen: () => {
    if (search) search.value = "";
    renderOptions();
    panel.style.minWidth = Math.max(180, button.offsetWidth) + "px";
    if (search) search.focus({ preventScroll: true });
    else focusOption(list.querySelector('[aria-selected="true"]') || list.firstElementChild);
  } });
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
    const next = { ArrowDown: (index + 1) % items.length, ArrowUp: index <= 0 ? items.length - 1 : index - 1, Home: 0, End: items.length - 1 }[event.key];
    if (next !== undefined && !(document.activeElement === search && ["Home", "End"].includes(event.key))) {
      event.preventDefault();
      focusOption(items[next]);
    } else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && document.activeElement !== search && event.key !== " ") {
      typed = (Date.now() - typedAt < 600 ? typed : "") + event.key.toLowerCase();
      typedAt = Date.now();
      focusOption(items.find((item) => item.title.toLowerCase().startsWith(typed)));
    }
  });
  update(options, value);
  return { element, update };
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
    panel.style.left = Math.max(12, Math.min(left, innerWidth - panel.offsetWidth - 12)) + "px";
    panel.style.top = (panel.offsetHeight <= below || below >= above ? box.bottom + 8 : box.top - panel.offsetHeight - 8) + "px";
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
