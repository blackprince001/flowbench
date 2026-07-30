// Screenshot a page at a real emulated viewport.
//
//   node website/scripts/shot.mjs <url> <width>x<height> <out.png> [--full] [--tab=N] [--scroll=N] [--selector=CSS]
//
// --selector clips to one element's border box — how the landing page gets
// card-sized captures of a single results-server card instead of a whole view.
//
// Chromium's --window-size does NOT set the layout viewport in headless: it
// crops a desktop-width render, which reads as a broken mobile layout that is
// not actually broken. Only CDP's Emulation.setDeviceMetricsOverride does the
// real thing, so responsive checks go through here.

import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

const args = process.argv.slice(2);
const flags = args.filter((a) => a.startsWith("--"));
const [url, size = "1440x900", out = "shot.png"] = args.filter((a) => !a.startsWith("--"));
const [width, height] = size.split("x").map(Number);
const full = flags.includes("--full");
const tab = Number(flags.find((f) => f.startsWith("--tab="))?.split("=")[1] ?? -1);
const scroll = Number(flags.find((f) => f.startsWith("--scroll="))?.split("=")[1] ?? 0);
const selector = flags.find((f) => f.startsWith("--selector="))?.slice("--selector=".length);

const port = 9334;
const browser =
  process.env.BROWSER ?? "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser";

const child = spawn(
  browser,
  [
    "--headless=new",
    "--disable-gpu",
    "--hide-scrollbars",
    `--remote-debugging-port=${port}`,
    "--user-data-dir=/tmp/flowbench-shot",
    "about:blank",
  ],
  { stdio: "ignore" },
);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let page;
for (let i = 0; i < 40 && !page; i++) {
  try {
    const res = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" });
    if (res.ok) page = await res.json();
  } catch {}
  if (!page) await sleep(250);
}
if (!page) throw new Error("browser never came up");

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));

let id = 0;
const pending = new Map();
ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  const resolve = pending.get(msg.id);
  if (resolve) {
    pending.delete(msg.id);
    resolve(msg.result);
  }
};
const send = (method, params = {}) =>
  new Promise((resolve) => {
    const n = ++id;
    pending.set(n, resolve);
    ws.send(JSON.stringify({ id: n, method, params }));
  });

await send("Page.enable");
await send("Emulation.setDeviceMetricsOverride", {
  width,
  height,
  deviceScaleFactor: 2,
  mobile: width < 768,
});
await send("Page.navigate", { url });
await sleep(1600);

// Drive the product switcher when asked, so every scene can be reviewed.
if (tab >= 0) {
  await send("Runtime.evaluate", {
    expression: `document.querySelectorAll('[role="tab"]')[${tab}]?.click()`,
  });
  await sleep(600);
}

if (scroll) {
  await send("Runtime.evaluate", { expression: `window.scrollTo(0, ${scroll})` });
  await sleep(400);
}

let clip = full ? undefined : { x: 0, y: scroll, width, height, scale: 1 };
if (selector) {
  const { result } = await send("Runtime.evaluate", {
    returnByValue: true,
    expression: `(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { x: b.x + scrollX, y: b.y + scrollY, width: b.width, height: b.height };
    })()`,
  });
  if (!result.value) throw new Error(`selector matched nothing: ${selector}`);
  clip = { ...result.value, scale: 2 };
}

// The clip is in page coordinates. Without captureBeyondViewport, anything the
// viewport does not currently hold comes back blank — which reads as a section
// that failed to render rather than a screenshot that failed to take.
const shot = await send("Page.captureScreenshot", {
  format: "png",
  captureBeyondViewport: full || scroll > 0 || Boolean(selector),
  ...(clip ? { clip } : {}),
});

writeFileSync(out, Buffer.from(shot.data, "base64"));
console.log(`${out} — ${width}x${height}${full ? " (full page)" : ""}`);

ws.close();
child.kill();
