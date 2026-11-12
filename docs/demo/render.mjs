// renders index.html frame by frame through headless chrome (devtools protocol).
// usage:
//   node render.mjs frames <out.mp4> [fps]      silent video, piped into ffmpeg
//   node render.mjs stills <dir> t1 t2 ...      png stills at those times
// needs node 22+ (built-in WebSocket), google chrome, ffmpeg.
import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const CHROME = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const PORT = 9400 + Math.floor(Math.random() * 400);
const [mode, out, ...rest] = process.argv.slice(2);
const page = pathToFileURL(resolve(import.meta.dirname, 'index.html')).href;

const profile = mkdtempSync(join(tmpdir(), 'yonder-render-'));
const chrome = spawn(CHROME, [
  '--headless=new', `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`,
  '--window-size=1920,1080', '--force-device-scale-factor=1', '--hide-scrollbars',
  '--no-first-run', '--no-default-browser-check', '--disable-extensions', 'about:blank',
], { stdio: 'ignore' });

const sleep = ms => new Promise(r => setTimeout(r, ms));
let target;
for (let i = 0; i < 100 && !target; i++) {
  try {
    const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
    target = list.find(t => t.type === 'page');
  } catch { await sleep(100); }
}
if (!target) throw new Error('chrome did not start');

const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((ok, bad) => { ws.onopen = ok; ws.onerror = bad; });
let seq = 0;
const pending = new Map();
ws.onmessage = ev => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) {
    const { ok, bad } = pending.get(m.id);
    pending.delete(m.id);
    m.error ? bad(new Error(m.error.message)) : ok(m.result);
  }
};
const send = (method, params = {}) => new Promise((ok, bad) => {
  const id = ++seq;
  pending.set(id, { ok, bad });
  ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = async expr => {
  const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.text);
  return r.result.value;
};

await send('Page.enable');
await send('Emulation.setDeviceMetricsOverride', { width: 1920, height: 1080, deviceScaleFactor: 1, mobile: false });
await send('Page.navigate', { url: page });
for (let i = 0; i < 100; i++) {
  if (await evaluate('document.readyState === "complete" && typeof window.render === "function"').catch(() => false)) break;
  await sleep(100);
}
// load every bundled face up front; fonts.ready alone skips ones not on screen yet
await evaluate('Promise.all([...document.fonts].map(f => f.load())).then(() => document.fonts.ready).then(() => [...document.fonts].every(f => f.status === "loaded"))');

const shot = async t => {
  await evaluate(`render(${t}); new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => r(true))))`);
  const { data } = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
  return Buffer.from(data, 'base64');
};

try {
  if (mode === 'stills') {
    for (const t of rest) writeFileSync(join(out, `t${String(t).padStart(5, '0')}.png`), await shot(Number(t)));
  } else if (mode === 'frames') {
    const fps = Number(rest[0] || 30);
    const dur = await evaluate('window.DURATION');
    const n = Math.round(dur * fps);
    const ff = spawn('ffmpeg', ['-y', '-loglevel', 'error', '-f', 'image2pipe', '-framerate', String(fps), '-i', '-',
      '-c:v', 'libx264', '-preset', 'slow', '-crf', '20', '-pix_fmt', 'yuv420p', '-movflags', '+faststart', out],
      { stdio: ['pipe', 'inherit', 'inherit'] });
    for (let i = 0; i < n; i++) {
      const png = await shot(i / fps);
      if (!ff.stdin.write(png)) await new Promise(r => ff.stdin.once('drain', r));
      if (i % 150 === 0) process.stderr.write(`frame ${i}/${n}\n`);
    }
    ff.stdin.end();
    await new Promise(r => ff.on('close', r));
  } else {
    throw new Error('mode must be frames or stills');
  }
} finally {
  ws.close();
  chrome.kill();
  await sleep(300);
  rmSync(profile, { recursive: true, force: true });
}
