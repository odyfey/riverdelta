// Instance switcher, injected by riverdelta into every riverui page. It reads
// /instances.json and shows a fixed badge linking to the same page in the
// other instances. Any failure leaves the page as stock riverui.
(() => {
  const DEFAULT_COLOR = "#64748b";
  const CSS = `
    :host { all: initial; }
    .sw { position: fixed; right: 16px; bottom: 16px; z-index: 2147483000;
      font: 600 13px/20px ui-sans-serif, system-ui, sans-serif; color: #0f172a; }
    .sw.dark { color: #f8fafc; }
    button { display: flex; align-items: center; gap: 8px; max-width: 240px; padding: 6px 12px 6px 6px;
      border: 1px solid #cbd5e1; border-radius: 9999px; background: #fff; color: inherit; font: inherit;
      cursor: pointer; box-shadow: 0 1px 3px rgb(0 0 0 / 0.15); }
    .dark button { border-color: #334155; background: #1e293b; }
    .menu { position: absolute; right: 0; bottom: calc(100% + 8px); min-width: 200px; padding: 4px;
      border: 1px solid #cbd5e1; border-radius: 10px; background: #fff; box-shadow: 0 8px 24px rgb(0 0 0 / 0.2); }
    .dark .menu { border-color: #334155; background: #1e293b; }
    .menu[hidden] { display: none; }
    a { display: flex; align-items: center; gap: 10px; padding: 6px 10px; border-radius: 6px;
      color: inherit; text-decoration: none; font-weight: 400; }
    a:hover, a:focus-visible { background: #3b82f6; color: #fff; outline: none; }
    .mark { display: inline-flex; flex: none; align-items: center; justify-content: center; width: 24px;
      height: 24px; border-radius: 6px; color: #fff; font-size: 12px; font-weight: 700; text-transform: uppercase; }
    .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .name { flex: 1; }
    .note { margin-left: 8px; font-size: 12px; opacity: 0.7; }
  `;

  // Same source as riverui's own basepath: the server-rendered page config.
  const currentBase = () => {
    try {
      return JSON.parse(document.getElementById("config__json")?.textContent || "{}").base || "";
    } catch {
      return "";
    }
  };

  // Keeps the current page and filters. It's a full page load because every
  // instance has its own router basepath and API URL.
  const switchHref = (fromBase, target) => {
    const { pathname, search } = window.location;
    const rest = pathname.startsWith(fromBase) ? pathname.slice(fromBase.length) : "";
    return `${target.base}${rest || "/"}${search}`;
  };

  const el = (tag, props, ...children) => {
    const node = Object.assign(document.createElement(tag), props);
    node.append(...children);
    return node;
  };

  const mark = (inst) => {
    const node = el("span", { className: "mark", textContent: inst.label.slice(0, 1) });
    node.style.backgroundColor = inst.color || DEFAULT_COLOR;
    return node;
  };

  const render = (instances, current, base) => {
    const host = el("div", { id: "riverdelta-switcher" });
    const wrap = el("div", { className: "sw" });
    const button = el("button", { type: "button", title: `Instance: ${current.label}` },
      mark(current), el("span", { className: "label", textContent: current.label }));
    button.setAttribute("aria-haspopup", "menu");
    button.setAttribute("aria-expanded", "false");
    const items = instances.map((inst) => {
      const name = el("span", { className: "name", textContent: inst.label });
      if (!inst.healthy) name.append(el("span", { className: "note", textContent: "unreachable" }));
      const link = el("a", { role: "menuitem" }, mark(inst), name,
        el("span", { textContent: inst.name === current.name ? "✓" : "" }));
      return { inst, link };
    });
    const menu = el("div", { className: "menu", role: "menu", hidden: true }, ...items.map((item) => item.link));

    const setOpen = (open) => {
      if (open) {
        wrap.classList.toggle("dark", document.documentElement.classList.contains("dark"));
        for (const { inst, link } of items) link.href = switchHref(base, inst);
      }
      menu.hidden = !open;
      button.setAttribute("aria-expanded", String(open));
    };
    button.addEventListener("click", () => setOpen(menu.hidden));
    document.addEventListener("click", (e) => { if (!e.composedPath().includes(host)) setOpen(false); });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") setOpen(false); });

    wrap.classList.toggle("dark", document.documentElement.classList.contains("dark"));
    wrap.append(menu, button);
    host.attachShadow({ mode: "open" }).append(el("style", { textContent: CSS }), wrap);
    document.body.append(host);
  };

  const load = async () => {
    const res = await fetch("/instances.json", {
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    });
    if (!res.ok || !res.headers.get("Content-Type")?.includes("application/json")) return;
    const instances = await res.json();
    if (!Array.isArray(instances)) return;
    const base = currentBase();
    const current = instances.find((inst) => inst.base === base);
    if (current) render(instances, current, base);
  };

  load().catch(() => {});
})();
