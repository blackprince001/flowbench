// The strip of runs a reader is holding open, across the top of every run page.
//
// Which runs someone is comparing is *their* state, not the store's: the server
// is read-only and stateless, and two people looking at the same run store are
// not looking at the same three runs. So the list lives in localStorage and the
// server renders only the page it is actually serving — with JavaScript off,
// the strip is a correct one-tab strip rather than a broken many-tab one.
//
// The tab that is open is the page you are on. Closing one is not a navigation
// unless you close the one you are reading, in which case the neighbour it
// leaves behind is where you land, the way a browser does it.
(() => {
  const KEY = "fb-tabs";
  const LIMIT = 12;

  const strip = document.querySelector(".tabstrip");
  if (!strip) return;

  const current = {
    id: strip.dataset.tabId,
    label: strip.dataset.tabLabel,
    sub: strip.dataset.tabSub,
    href: strip.dataset.tabHref,
    badge: strip.dataset.tabBadge,
    tone: strip.dataset.tabTone,
  };

  const read = () => {
    try {
      const raw = JSON.parse(localStorage.getItem(KEY));
      return Array.isArray(raw) ? raw.filter((t) => t && t.id && t.href) : [];
    } catch (_) {
      return [];
    }
  };

  const write = (tabs) => {
    try {
      localStorage.setItem(KEY, JSON.stringify(tabs));
    } catch (_) {
      // Storage can be denied outright; the strip still works for this page.
    }
  };

  // Visiting a run opens it. Re-visiting one moves nothing — a tab that jumped
  // to the front every time you looked at it would never be where you left it.
  let tabs = read();
  if (!tabs.some((t) => t.id === current.id)) {
    tabs.push(current);
    if (tabs.length > LIMIT) tabs = tabs.slice(tabs.length - LIMIT);
    write(tabs);
  }

  const nav = strip.querySelector(".tabstrip-tabs");

  const render = () => {
    nav.replaceChildren();
    for (const tab of tabs) {
      const active = tab.id === current.id;
      const el = document.createElement("a");
      el.className = "runtab" + (active ? " is-active" : "");
      el.href = tab.href;
      el.style.setProperty("--tone", `var(--${tab.tone})`);
      if (active) el.setAttribute("aria-current", "page");

      const badge = document.createElement("span");
      badge.className = "runtab-badge";
      badge.setAttribute("aria-hidden", "true");
      badge.textContent = tab.badge;

      const label = document.createElement("span");
      label.className = "runtab-label";
      label.textContent = tab.label;

      const sub = document.createElement("span");
      sub.className = "runtab-sub";
      sub.textContent = tab.sub;

      const close = document.createElement("button");
      close.type = "button";
      close.className = "runtab-close";
      close.dataset.close = tab.id;
      close.title = "Close this run";
      close.setAttribute("aria-label", `Close ${tab.label}`);
      close.textContent = "×";

      el.append(badge, label, sub, close);
      nav.append(el);
    }
  };

  nav.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-close]");
    if (!btn) return;
    // The close button lives inside the tab's own link, so the click has to be
    // stopped from following it.
    e.preventDefault();
    e.stopPropagation();

    const id = btn.dataset.close;
    const at = tabs.findIndex((t) => t.id === id);
    if (at < 0) return;
    tabs.splice(at, 1);
    write(tabs);

    if (id !== current.id) {
      render();
      return;
    }
    // Closing the run you are reading has to go somewhere: the neighbour it
    // left behind, or the run list when it was the last one.
    const next = tabs[at] || tabs[at - 1];
    window.location.href = next ? next.href : strip.querySelector(".runtab-add").href;
  });

  render();
})();
