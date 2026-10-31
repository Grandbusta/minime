// Records a web page frame by frame, on a virtual clock: time only moves when
// a frame is taken, so every frame is exact however long the screenshot takes.
// It drives Chrome over its DevTools pipe, with nothing to install.
//
//   node capture.mjs <url> <out dir> <seconds> [--fps 30] [--size 1920x1080]
//     [--scale 1] [--clip x,y,w,h | --clip-selector css] [--wait 3000] [--seek] [--format jpeg]
//
// Without --seek, the page runs on the virtual clock (timers, animation
// frames, CSS animations, Date). With --seek, the page draws itself: before
// each frame it calls window.seek(t) and awaits it.
import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const args = process.argv.slice(2);
const opt = (name, def) => { const i = args.indexOf("--" + name); return i < 0 ? def : args[i + 1]; };
const [url, out, seconds] = args;
const fps = +opt("fps", 30), [W, H] = opt("size", "1920x1080").split("x").map(Number), scale = +opt("scale", 1);
let clip = opt("clip", "") ? opt("clip").split(",").map(Number) : null;
const wait = +opt("wait", 3000), clipSelector = opt("clip-selector", "");
const seek = args.includes("--seek"), format = opt("format", "jpeg");
const chrome = process.env.CHROME || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
mkdirSync(out, { recursive: true });

// The virtual clock, in the page before anything else runs.
const clock = `(() => {
  let now = 0, seq = 0;
  const base = Date.now(), RealDate = Date, timers = new Map(), rafs = new Map(), born = new WeakMap();
  window.Date = class extends RealDate {
    constructor(...a) { a.length ? super(...a) : super(base + now); }
    static now() { return base + now; }
  };
  performance.now = () => now;
  window.setTimeout = (fn, ms = 0, ...a) => { const id = ++seq; timers.set(id, { at: now + Math.max(0, ms), fn, a }); return id; };
  window.setInterval = (fn, ms = 0, ...a) => { const id = ++seq; timers.set(id, { at: now + Math.max(1, ms), fn, a, every: Math.max(1, ms) }); return id; };
  window.clearTimeout = window.clearInterval = (id) => timers.delete(id);
  window.requestAnimationFrame = (fn) => { const id = ++seq; rafs.set(id, fn); return id; };
  window.cancelAnimationFrame = (id) => rafs.delete(id);
  const settle = () => new Promise((r) => queueMicrotask(r));
  window.__advance = async (dt) => {
    const end = now + dt;
    for (;;) { // timers due before the frame, in order
      let next = null;
      for (const [id, t] of timers) if (t.at <= end && (!next || t.at < next[1].at)) next = [id, t];
      if (!next) break;
      const [id, t] = next;
      now = Math.max(now, t.at);
      if (t.every) t.at += t.every; else timers.delete(id);
      try { typeof t.fn === "function" ? t.fn(...t.a) : 0; } catch (e) { console.error(e); }
      await settle();
    }
    now = end;
    const due = [...rafs.values()]; rafs.clear();
    for (const fn of due) { try { fn(now); } catch (e) { console.error(e); } }
    for (const a of document.getAnimations()) { // CSS animations and transitions
      if (!born.has(a)) born.set(a, now - 0);
      a.pause(); a.currentTime = now - born.get(a);
    }
    await settle();
  };
})();`;

const proc = spawn(chrome, ["--headless=new", "--remote-debugging-pipe", "--hide-scrollbars", "--mute-audio",
  "--no-first-run", "--no-default-browser-check", "--user-data-dir=" + join(out, ".chrome"), "about:blank"],
  { stdio: ["ignore", "ignore", "ignore", "pipe", "pipe"] });
const [toChrome, fromChrome] = [proc.stdio[3], proc.stdio[4]];
let id = 0, buf = "";
const waiting = new Map(), listeners = [];
fromChrome.on("data", (d) => {
  buf += d;
  let i;
  while ((i = buf.indexOf("\0")) >= 0) {
    const msg = JSON.parse(buf.slice(0, i)); buf = buf.slice(i + 1);
    if (msg.id && waiting.has(msg.id)) {
      const { ok, no } = waiting.get(msg.id); waiting.delete(msg.id);
      msg.error ? no(new Error(JSON.stringify(msg.error))) : ok(msg.result);
    } else listeners.forEach((l) => l(msg));
  }
});
const send = (method, params = {}, sessionId) => new Promise((ok, no) => {
  const n = ++id; waiting.set(n, { ok, no });
  toChrome.write(JSON.stringify({ id: n, method, params, ...(sessionId ? { sessionId } : {}) }) + "\0");
});
const once = (method) => new Promise((r) => listeners.push(function l(m) { if (m.method === method) { listeners.splice(listeners.indexOf(l), 1); r(m.params); } }));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const { targetId } = await send("Target.createTarget", { url: "about:blank" });
const { sessionId: s } = await send("Target.attachToTarget", { targetId, flatten: true });
const page = (m, p) => send(m, p, s);
await page("Page.enable");
await page("Runtime.enable");
listeners.push((m) => { if (m.method === "Runtime.exceptionThrown") console.error("page:", m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text); });
await page("Emulation.setDeviceMetricsOverride", { width: W, height: H, deviceScaleFactor: scale, mobile: false });
await page("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "dark" }] });
if (!seek) await page("Page.addScriptToEvaluateOnNewDocument", { source: clock });
const loaded = once("Page.loadEventFired");
await page("Page.navigate", { url });
await loaded;
await sleep(wait); // fonts, data; the virtual clock stands still meanwhile

if (clipSelector) {
  const { result } = await page("Runtime.evaluate", { returnByValue: true,
    expression: `(() => { const r = document.querySelector(${JSON.stringify(clipSelector)}).getBoundingClientRect(); return [r.x, r.y, r.width, r.height]; })()` });
  clip = result.value.map(Math.round);
  console.log("clip", clip.join(","));
}
const frames = Math.round(+seconds * fps), step = 1000 / fps;
const shot = { format, ...(format === "jpeg" ? { quality: 94 } : {}), ...(clip ? { clip: { x: clip[0], y: clip[1], width: clip[2], height: clip[3], scale: 1 } } : {}) };
const t0 = Date.now();
for (let f = 0; f < frames; f++) {
  const expr = seek ? `window.seek(${(f / fps).toFixed(5)})` : `window.__advance(${step})`;
  const r = await page("Runtime.evaluate", { expression: expr, awaitPromise: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
  const { data } = await page("Page.captureScreenshot", shot);
  writeFileSync(join(out, `f${String(f).padStart(5, "0")}.${format === "jpeg" ? "jpg" : "png"}`), Buffer.from(data, "base64"));
  if (f % 60 === 0) process.stdout.write(`frame ${f}/${frames}, ${((Date.now() - t0) / 1000).toFixed(0)}s\n`);
}
proc.kill();
console.log(`done: ${frames} frames in ${out}`);
process.exit(0);
