// Ask a headless Chromium what it actually laid out: viewport width, document
// scroll width, and every element sticking out past the right edge.
//
//   node website/scripts/probe.mjs <url> <width> <height>
//
// Node's global WebSocket talks CDP directly, so this needs no dependencies.

import { spawn } from "node:child_process";

const [url = "http://localhost:4321/", width = "390", height = "900"] = process.argv.slice(2);
const port = 9333;

const browser =
  process.env.BROWSER ??
  "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser";

const child = spawn(
  browser,
  [
    "--headless=new",
    "--disable-gpu",
    `--remote-debugging-port=${port}`,
    `--window-size=${width},${height}`,
    "--user-data-dir=/tmp/flowbench-probe",
    "about:blank",
  ],
  { stdio: "ignore" },
);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function target() {
  for (let i = 0; i < 40; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(url)}`, {
        method: "PUT",
      });
      if (res.ok) return await res.json();
    } catch {}
    await sleep(250);
  }
  throw new Error("browser never came up");
}

const page = await target();
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
  width: Number(width),
  height: Number(height),
  deviceScaleFactor: 1,
  mobile: Number(width) < 768,
});
await send("Page.navigate", { url });
await sleep(1500);

const { result } = await send("Runtime.evaluate", {
  returnByValue: true,
  expression: `(() => {
    const doc = document.documentElement;
    const limit = doc.clientWidth;
    const over = [];
    for (const el of document.querySelectorAll("body *")) {
      const box = el.getBoundingClientRect();
      if (box.right > limit + 1 && box.width > 0) {
        over.push({
          tag: el.tagName.toLowerCase(),
          cls: el.className?.toString().slice(0, 40),
          right: Math.round(box.right),
          width: Math.round(box.width),
        });
      }
    }
    return {
      innerWidth: window.innerWidth,
      clientWidth: limit,
      scrollWidth: doc.scrollWidth,
      matches420: window.matchMedia("(max-width: 420px)").matches,
      widest: over.sort((a, b) => b.right - a.right).slice(0, 12),
    };
  })()`,
});

console.log(JSON.stringify(result.value, null, 2));
ws.close();
child.kill();
