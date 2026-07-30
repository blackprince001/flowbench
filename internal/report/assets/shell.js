// Collapsing the run list, the detail rail, or the project tabs. The state
// lives on <html> so the stylesheet owns the layout, and in localStorage so a
// collapsed panel stays collapsed as you move between runs. Loaded in <head>
// and delegated from document, so the state is applied before first paint — a
// rail that flashes open and then closes is worse than one that never
// collapsed.
(() => {
  const root = document.documentElement;
  const sides = { side: "fb-side", rail: "fb-rail", tabs: "fb-tabs" };

  for (const [name, key] of Object.entries(sides)) {
    try {
      if (localStorage.getItem(key) === "off") root.dataset[name] = "off";
    } catch (_) {
      // Storage can be denied outright; the shell just opens expanded.
    }
  }

  const sync = () => {
    for (const btn of document.querySelectorAll("[data-toggle]")) {
      btn.setAttribute("aria-pressed", String(root.dataset[btn.dataset.toggle] !== "off"));
    }
  };

  document.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-toggle]");
    if (!btn) return;
    const name = btn.dataset.toggle;
    const wasOff = root.dataset[name] === "off";
    if (wasOff) delete root.dataset[name];
    else root.dataset[name] = "off";
    try {
      localStorage.setItem(sides[name], wasOff ? "on" : "off");
    } catch (_) {
      // Not persisting is fine; the toggle still works for this page.
    }
    sync();
  });

  document.addEventListener("DOMContentLoaded", sync);
})();
