// Client-side viewport for the flame graph: wheel to zoom, drag to pan, in the
// speedscope idiom.
//
// The server already rendered every frame at its true position, so this is pure
// progressive enhancement — with JavaScript off the graph is still correct, just
// fixed at the full extent. There is no build step and no framework (ADR 0014).
//
// Zooming re-computes each frame's left/width from the viewport rather than
// applying a CSS transform: a scaleX would stretch the labels horizontally,
// which is exactly what makes a stretched flame graph unreadable.
(function () {
  "use strict";

  var root = document.querySelector("[data-flame]");
  if (!root) return;

  var inner = root.querySelector(".flame-inner");
  var frames = Array.prototype.slice.call(inner.querySelectorAll(".frame"));
  if (!frames.length) return;

  var readout = document.querySelector("[data-flame-zoom]");
  var resetBtn = document.querySelector("[data-flame-reset]");
  var zoomInBtn = document.querySelector("[data-flame-zoom-in]");
  var zoomOutBtn = document.querySelector("[data-flame-zoom-out]");
  var fitSelectedBtn = document.querySelector("[data-flame-fit-selected]");
  var search = document.querySelector("[data-flame-search]");
  var searchCount = document.querySelector("[data-flame-search-count]");
  var tip = document.querySelector("[data-flame-tip]");
  var overview = document.querySelector("[data-flame-overview]");
  var overviewWindow = document.querySelector("[data-flame-overview-window]");

  // The viewport is a window onto the 0..100 coordinate space the server used.
  var MIN_SPAN = 0.0005; // ~200,000x, past the point of diminishing returns
  var x0 = 0;
  var x1 = 100;

  var geom = frames.map(function (el) {
    return {
      el: el,
      l: parseFloat(el.dataset.l),
      w: parseFloat(el.dataset.w),
    };
  });

  // basePad is the frame's resting left padding (CSS `padding: 0 6px`); sticky
  // labels are computed relative to it.
  var basePad = 6;

  function render() {
    var span = x1 - x0;
    var scale = 100 / span;
    var vw = root.clientWidth || 1;
    for (var i = 0; i < geom.length; i++) {
      var g = geom[i];
      var left = (g.l - x0) * scale;
      var width = g.w * scale;
      // Cull what has left the viewport: at deep zoom most frames have, and
      // leaving them positioned makes the browser lay out thousands of
      // off-screen boxes on every wheel tick.
      if (left + width < -20 || left > 120) {
        g.el.style.display = "none";
        continue;
      }
      g.el.style.display = "";
      g.el.style.left = left.toFixed(4) + "%";
      g.el.style.width = width.toFixed(4) + "%";

      // Sticky label: when a frame extends off the left edge, slide its label
      // right so it sits at the viewport edge and the frame stays named — the
      // difference between reading a zoomed graph and staring at blank bars.
      // Capped so the label never leaves the frame's own right edge.
      var leftPx = (left / 100) * vw;
      var widthPx = (width / 100) * vw;
      var shift = 0;
      if (leftPx < 0) {
        shift = Math.min(-leftPx, Math.max(0, widthPx - 34));
      }
      g.el.style.paddingLeft = (basePad + shift).toFixed(1) + "px";
    }
    if (readout) {
      readout.textContent = fmtScale(scale);
    }
    var isZoomed = scale > 1.005;
    root.classList.toggle("is-zoomed", isZoomed);
    if (resetBtn) resetBtn.disabled = !isZoomed;
    if (zoomOutBtn) zoomOutBtn.disabled = !isZoomed;
    if (overviewWindow) {
      overviewWindow.style.left = x0.toFixed(4) + "%";
      overviewWindow.style.width = span.toFixed(4) + "%";
    }
  }

  function fmtScale(s) {
    if (s < 1.005) return "100%";
    return s < 10 ? s.toFixed(1) + "×" : Math.round(s) + "×";
  }

  function clampViewport() {
    var span = Math.min(Math.max(x1 - x0, MIN_SPAN), 100);
    // Keep the window inside the graph so panning cannot lose it entirely.
    if (x0 < 0) x0 = 0;
    if (x0 + span > 100) x0 = 100 - span;
    x1 = x0 + span;
  }

  function zoomAt(fraction, factor) {
    var span = x1 - x0;
    var focus = x0 + fraction * span;
    var next = Math.min(Math.max(span * factor, MIN_SPAN), 100);
    // Hold the point under the cursor still — the thing that makes wheel-zoom
    // feel like a map rather than a slider.
    x0 = focus - fraction * next;
    x1 = x0 + next;
    clampViewport();
    render();
  }

  function reset() {
    x0 = 0;
    x1 = 100;
    render();
  }

  // Fit one frame to the viewport, with a little air so its edges stay visible.
  function fitTo(g) {
    var pad = g.w * 0.02;
    x0 = g.l - pad;
    x1 = g.l + g.w + pad;
    clampViewport();
    render();
  }

  function fractionOf(evt) {
    var box = root.getBoundingClientRect();
    if (box.width <= 0) return 0.5;
    return Math.min(Math.max((evt.clientX - box.left) / box.width, 0), 1);
  }

  root.addEventListener(
    "wheel",
    function (evt) {
      // Horizontal intent (trackpad two-finger, or shift+wheel) pans; vertical
      // zooms. Ctrl+wheel is the browser's own page zoom — leave it alone.
      if (evt.ctrlKey) return;
      var dx = evt.deltaX;
      var dy = evt.deltaY;
      if (Math.abs(dx) > Math.abs(dy)) {
        evt.preventDefault();
        var span = x1 - x0;
        x0 += (dx / root.clientWidth) * span;
        clampViewport();
        render();
        return;
      }
      if (!dy) return;
      evt.preventDefault();
      zoomAt(fractionOf(evt), dy > 0 ? 1.15 : 1 / 1.15);
    },
    { passive: false }
  );

  // Drag to pan. A drag must not also fire the frame's click, or panning would
  // navigate away mid-gesture.
  var dragging = false;
  var moved = false;
  var lastX = 0;

  root.addEventListener("pointerdown", function (evt) {
    if (evt.button !== 0) return;
    // A frame is a real link. Never capture its press for panning: doing so
    // makes selection unreliable on touchpads and touchscreens. Pan from the
    // graph background or use the overview strip instead.
    if (evt.target.closest(".frame")) return;
    if (x1 - x0 >= 99.999) return;
    dragging = true;
    moved = false;
    lastX = evt.clientX;
    root.classList.add("is-panning");
    root.setPointerCapture(evt.pointerId);
  });

  root.addEventListener("pointermove", function (evt) {
    if (!dragging) return;
    var dx = evt.clientX - lastX;
    if (Math.abs(dx) < 1) return;
    if (Math.abs(dx) > 2) moved = true;
    lastX = evt.clientX;
    var span = x1 - x0;
    x0 -= (dx / root.clientWidth) * span;
    clampViewport();
    render();
  });

  function endDrag(evt) {
    if (!dragging) return;
    dragging = false;
    if (root.hasPointerCapture && root.hasPointerCapture(evt.pointerId)) {
      root.releasePointerCapture(evt.pointerId);
    }
    root.classList.remove("is-panning");
  }
  root.addEventListener("pointerup", endDrag);
  root.addEventListener("pointercancel", endDrag);

  root.addEventListener(
    "click",
    function (evt) {
      if (moved) {
        evt.preventDefault();
        evt.stopPropagation();
        moved = false;
      }
    },
    true
  );

  // Double-click fits a frame without navigating, so the viewport can be driven
  // without losing the currently inspected frame.
  root.addEventListener("dblclick", function (evt) {
    var el = evt.target.closest(".frame");
    if (!el) return;
    evt.preventDefault();
    for (var i = 0; i < geom.length; i++) {
      if (geom[i].el === el) {
        fitTo(geom[i]);
        return;
      }
    }
  });

  if (resetBtn) {
    resetBtn.addEventListener("click", function (evt) {
      evt.preventDefault();
      reset();
    });
  }
  if (zoomInBtn) {
    zoomInBtn.addEventListener("click", function () {
      zoomAt(0.5, 1 / 1.6);
    });
  }
  if (zoomOutBtn) {
    zoomOutBtn.addEventListener("click", function () {
      zoomAt(0.5, 1.6);
    });
  }
  if (fitSelectedBtn) {
    fitSelectedBtn.addEventListener("click", function () {
      var selected = inner.querySelector(".frame.is-selected");
      if (!selected) return;
      for (var i = 0; i < geom.length; i++) {
        if (geom[i].el === selected) {
          fitTo(geom[i]);
          selected.focus({ preventScroll: true });
          return;
        }
      }
    });
  }

  // The overview is both a minimap and a direct navigation surface. Clicking
  // recentres the current window; dragging continuously pans at a 1:1 ratio.
  if (overview && overviewWindow) {
    var overviewDragging = false;

    function panOverview(evt) {
      var box = overview.getBoundingClientRect();
      if (!box.width) return;
      var centre = Math.min(Math.max((evt.clientX - box.left) / box.width, 0), 1) * 100;
      var span = x1 - x0;
      x0 = centre - span / 2;
      x1 = x0 + span;
      clampViewport();
      render();
    }

    overview.addEventListener("pointerdown", function (evt) {
      if (evt.button !== 0) return;
      overviewDragging = true;
      overview.setPointerCapture(evt.pointerId);
      panOverview(evt);
    });
    overview.addEventListener("pointermove", function (evt) {
      if (overviewDragging) panOverview(evt);
    });
    overview.addEventListener("pointerup", function (evt) {
      overviewDragging = false;
      if (overview.hasPointerCapture(evt.pointerId)) {
        overview.releasePointerCapture(evt.pointerId);
      }
    });
    overview.addEventListener("pointercancel", function () {
      overviewDragging = false;
    });
  }

  document.addEventListener("keydown", function (evt) {
    if (evt.target.matches("input, textarea")) {
      if (evt.key === "Escape") evt.target.blur();
      return;
    }
    var span = x1 - x0;
    switch (evt.key) {
      case "Escape":
      case "0":
        reset();
        break;
      case "+":
      case "=":
        zoomAt(0.5, 1 / 1.3);
        break;
      case "-":
        zoomAt(0.5, 1.3);
        break;
      case "ArrowLeft":
        x0 -= span * 0.1;
        clampViewport();
        render();
        break;
      case "ArrowRight":
        x0 += span * 0.1;
        clampViewport();
        render();
        break;
      default:
        return;
    }
    evt.preventDefault();
  });

  // Tooltip. The frames carry a title attribute for the no-JS case; once this
  // runs, that native tooltip is replaced by one that appears immediately.
  if (tip) {
    frames.forEach(function (el) {
      el.dataset.tip = el.getAttribute("title") || "";
      el.removeAttribute("title");
    });

    root.addEventListener("pointermove", function (evt) {
      if (dragging) return;
      var el = evt.target.closest(".frame");
      if (!el || !el.dataset.tip) {
        tip.hidden = true;
        return;
      }
      tip.textContent = el.dataset.tip;
      tip.hidden = false;
      var box = root.getBoundingClientRect();
      var x = evt.clientX - box.left;
      // Flip before the tooltip runs off the right edge.
      tip.style.left = Math.min(x + 14, box.width - tip.offsetWidth - 8) + "px";
      tip.style.top = evt.clientY - box.top + 18 + "px";
    });
    root.addEventListener("pointerleave", function () {
      tip.hidden = true;
    });
  }

  // Search dims every frame whose span path does not match, so one step's cost
  // can be picked out of a wide graph.
  if (search) {
    search.addEventListener("input", function () {
      var q = search.value.trim().toLowerCase();
      var matches = 0;
      inner.classList.toggle("is-filtered", q !== "");
      frames.forEach(function (el) {
        var hit = q !== "" && (el.dataset.path || "").toLowerCase().indexOf(q) >= 0;
        el.classList.toggle("is-match", hit);
        if (hit) matches++;
      });
      if (searchCount) searchCount.textContent = q === "" ? "" : matches + " found";
    });
  }

  render();
})();
