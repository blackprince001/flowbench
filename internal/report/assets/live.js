// Progressive enhancement for the live view: subscribe to the run's server-sent
// event stream and update the figures in place, so watching a run costs one long
// GET rather than a page reload a second. With JavaScript off, the page's own
// <noscript> meta-refresh keeps the server-rendered snapshot current, and the
// Abort button falls back to a plain form POST. No framework, no build (ADR 0014).
(function () {
  "use strict";

  function set(id, v) {
    var el = document.getElementById(id);
    if (el && v !== undefined && v !== null) el.textContent = v;
  }

  var src = new EventSource("/live/stream");

  src.onmessage = function (e) {
    var d;
    try {
      d = JSON.parse(e.data);
    } catch (err) {
      return;
    }
    set("live-elapsed", d.elapsed);
    set("live-vus", d.vus);
    set("live-completed", d.completed);
    set("live-rps", d.rps);
    set("live-error", d.error);
    set("live-throttle", d.throttle);
  };

  // The run finished (or was aborted). Follow the server to the stored run so the
  // full dashboard, flame graph and traces are one hop away.
  src.addEventListener("done", function (e) {
    src.close();
    if (e.data) window.location.assign(e.data);
    else window.location.reload();
  });

  // Abort without leaving the page; if the fetch cannot be made, let the form
  // submit normally so the run still stops.
  var form = document.querySelector("form[data-live-abort]");
  if (form) {
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      set("live-status", "aborting");
      fetch(form.action, { method: "POST" }).catch(function () {
        form.submit();
      });
    });
  }
})();
