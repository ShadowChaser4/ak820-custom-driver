"use strict";
const $ = (id) => document.getElementById(id);
const token = new URLSearchParams(location.hash.slice(1)).get("token") || "";
let dev = { config: {}, interfaces: [], ready: false, features: {} };
let current = null;          // current preview {id, frames, bytes}
let source = null;           // {kind:"gallery", id, name} | {kind:"file", file}
let anim = null, pvTimer = null, pvSeq = 0, busy = false;

// ---- helpers -----------------------------------------------------------------
async function api(path, opts = {}) {
  opts.headers = Object.assign({ "X-Token": token }, opts.headers || {});
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch (_) { /* not JSON */ }
    const e = new Error(msg); e.status = r.status; throw e;
  }
  return r;
}
const post = (path, body) => api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).then((r) => r.json());
const getJSON = (path) => api(path).then((r) => r.json());
const blobURL = (path) => api(path).then((r) => r.blob()).then((b) => URL.createObjectURL(b));

function toast(msg, ms = 4000) {
  const t = $("toast"); t.textContent = msg; t.hidden = false;
  clearTimeout(toast.t); toast.t = setTimeout(() => (t.hidden = true), ms);
}
function show(id, text) { const e = $(id); e.textContent = text; e.hidden = !text; }
function setMsg(id, text, cls) { const e = $(id); e.textContent = text; e.className = "msg" + (cls ? " " + cls : ""); }
function el(tag, cls, text) { const e = document.createElement(tag); if (cls) e.className = cls; if (text) e.textContent = text; return e; }

// Turn server/technical errors into plain language.
function nice(err) {
  const m = (err && err.message) || String(err);
  if ((err && err.status === 409) || /not connected|no confirmed target/i.test(m)) return "Connect your keyboard first (step 1).";
  if (/no reply|no usable reply|timed? ?out|timeout/i.test(m)) return "The keyboard didn't answer. Check the cable and USB mode, then try again.";
  if (/permission denied|EACCES/i.test(m)) return "Your computer isn't allowing access to the keyboard yet. This is a permission setting on the computer.";
  if (/no such device|not found|no keyboard|no device/i.test(m)) return "The keyboard isn't connected. Plug it in with the USB cable and set the switch to USB.";
  if (/unauthorized|bad token/i.test(m)) return "This page lost its access key. Please re-open it from the link the program printed.";
  return "Something went wrong: " + m;
}

// Every write goes through here. Plain text first, technical bits collapsed.
function confirmDlg({ title, text, details, ok = "Yes, continue" }) {
  return new Promise((resolve) => {
    $("dlg-title").textContent = title; $("dlg-text").textContent = text;
    $("dlg-body").textContent = details || ""; $("dlg-det").hidden = !details; $("dlg-det").open = false;
    $("dlg-ok").textContent = ok;
    const d = $("dlg");
    d.addEventListener("close", () => resolve(d.returnValue === "yes"), { once: true });
    d.showModal();
  });
}
const techOf = (d) => `${d.node} (${d.len} byte reports)\n${d.packets} packets\nfirst packet:\n${d.first}`;

// ---- step 1: device ----------------------------------------------------------
async function loadDevices() {
  let d;
  try { d = await getJSON("/api/devices"); }
  catch (e) { d = { config: {}, interfaces: [], ready: false, friendly: { title: "Can't reach the program", message: nice(e), level: "bad" } }; }
  dev = d; dev.features = d.features || {}; dev.interfaces = d.interfaces || []; dev.config = d.config || {};
  const f = d.friendly || (d.ready
    ? { title: "Connected", message: "Your keyboard is ready.", level: "ok" }
    : { title: d.error ? "Keyboard not found" : "Not connected yet", message: d.error ? nice({ message: d.error }) : "Press the button below to connect.", level: d.error ? "bad" : "warn" });
  const title = d.ready && f.level === "ok" && !/connected/i.test(f.title) ? "Connected" : f.title;
  $("conn").dataset.level = f.level; $("conn-title").textContent = title; $("conn-msg").textContent = f.message;
  if (d.version) $("ver").textContent = "ak820 " + d.version;
  // The "check the cable / switch" list only makes sense when the keyboard was NOT found.
  $("checklist").hidden = !!d.ready || f.level !== "bad";
  $("perm-tip").hidden = !/permission|access|denied/i.test((f.title + " " + f.message + " " + (d.error || "")));
  $("btn-connect").textContent = d.ready ? "Set the keyboard clock again" : "Connect & set the keyboard clock";
  $("btn-connect").classList.toggle("primary", !d.ready);
  $("builtin-box").hidden = dev.features.builtin === false || !dev.features.builtin;
  renderIfaces(); updateLock();
}

$("btn-recheck").onclick = async () => {
  await loadDevices();
  const found = !dev.ready && dev.friendly && dev.friendly.level === "warn";
  setMsg("connect-msg",
    dev.ready ? "Connected." : found ? "Your keyboard is found. Press the blue button to connect." : "Still not connected.",
    dev.ready || found ? "ok" : "bad");
};
$("btn-connect").onclick = async () => {
  setMsg("connect-msg", "");
  try {
    await post("/api/time", { dry: true });
    const ok = await confirmDlg({
      title: "Connect to your keyboard?",
      text: "The program will talk to your keyboard to check the connection, and set the clock on its screen to your computer's time.",
      ok: "Connect",
    });
    if (!ok) return;
    setMsg("connect-msg", "Connecting...");
    await post("/api/time", { dry: false });
    await loadDevices();
    setMsg("connect-msg", dev.ready ? "Connected! The keyboard clock was set." : "The clock was sent, but the keyboard didn't confirm yet. Try again.", dev.ready ? "ok" : "bad");
  } catch (e) { setMsg("connect-msg", nice(e), "bad"); }
};

// ---- step 2: pick ------------------------------------------------------------
async function loadGallery() {
  let list = [];
  try { list = await getJSON("/api/gallery"); } catch (_) { /* older server */ }
  const box = $("gallery"); box.textContent = "";
  $("gallery-empty").hidden = list.length > 0;
  for (const g of list) {
    const b = el("button", "card"); b.type = "button"; b.setAttribute("aria-pressed", "false");
    b.dataset.id = g.id;
    const im = el("img"); im.alt = ""; im.width = 128; im.height = 128;
    blobURL("/api/gallery/thumb?id=" + encodeURIComponent(g.id)).then((u) => (im.src = u)).catch(() => {});
    b.append(im, el("span", "nm", g.name), el("span", "ds", g.description || ""));
    b.onclick = () => { source = { kind: "gallery", id: g.id, name: g.name }; markSelected(g.id); refreshPreview(); };
    box.append(b);
  }
}
function markSelected(id) {
  document.querySelectorAll("#gallery .card").forEach((c) => {
    const on = c.dataset.id === id; c.setAttribute("aria-checked", on); c.setAttribute("aria-pressed", on);
  });
}

const drop = $("drop");
drop.onclick = () => $("file").click();
drop.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); $("file").click(); } };
drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("over"); };
drop.ondragleave = () => drop.classList.remove("over");
drop.ondrop = (e) => { e.preventDefault(); drop.classList.remove("over"); if (e.dataTransfer.files[0]) setFile(e.dataTransfer.files[0]); };
$("file").onchange = (e) => { if (e.target.files[0]) setFile(e.target.files[0]); };
function setFile(f) { source = { kind: "file", file: f, name: f.name }; markSelected(""); refreshPreview(); }
["fit", "bg", "speed", "maxf"].forEach((id) => $(id).addEventListener("input", () => { clearTimeout(pvTimer); pvTimer = setTimeout(refreshPreview, 250); }));

async function refreshPreview() {
  if (!source) return;
  const seq = ++pvSeq;
  const o = { fit: $("fit").value, bg: $("bg").value.slice(1), delay: parseInt($("speed").value, 10) || 0, max_frames: parseInt($("maxf").value, 10) || 255 };
  setMsg("send-msg", ""); $("pv-meta").textContent = "Preparing preview...";
  try {
    let r;
    if (source.kind === "gallery") r = await post("/api/gallery/preview", Object.assign({ id: source.id }, o));
    else {
      const fd = new FormData(); fd.append("file", source.file);
      for (const k in o) fd.append(k, o[k]);
      r = await (await api("/api/preview", { method: "POST", body: fd })).json();
    }
    if (seq !== pvSeq) return;
    current = r; playFrames(r.frames);
    const n = r.frames.length;
    $("pv-meta").textContent = `${source.name} - ${n > 1 ? n + " frames, animated" : "single picture"}. This is exactly how it will look on the keyboard.`;
  } catch (e) {
    if (seq !== pvSeq) return;
    current = null; clearTimeout(anim); $("pv").hidden = true; $("pv-empty").hidden = false;
    $("pv-meta").textContent = ""; setMsg("send-msg", /decode|image|format/i.test(e.message) ? "That file couldn't be read as a picture. Try a PNG, JPEG or GIF." : nice(e), "bad");
  }
  updateLock();
}
function playFrames(frames) {
  clearTimeout(anim);
  const im = $("pv"); im.hidden = false; $("pv-empty").hidden = true; let i = 0;
  const step = () => { im.src = frames[i].url; const d = Math.max(60, frames[i].delay); i = (i + 1) % frames.length; if (frames.length > 1) anim = setTimeout(step, d); };
  step();
}

// ---- step 3: send ------------------------------------------------------------
function updateLock() {
  const can = dev.ready && current && !busy;
  $("btn-send").disabled = !can;
  document.querySelectorAll("#hist-list button, #hist-adv button.restore").forEach((b) => (b.disabled = !dev.ready || busy));
  $("lock-note").textContent = !dev.ready ? "Locked: connect your keyboard first (step 1)." : !current ? "Pick a picture first (step 2)." : "";
}

async function sendFlow(endpoint, body, what) {
  try {
    const dry = await post(endpoint, Object.assign({}, body, { dry: true }));
    const text = "This will replace the picture on your keyboard screen." +
      ((dry.flash ?? dev.features.flash) ? " The picture is saved in the keyboard and stays after unplugging." : "");
    const ok = await confirmDlg({ title: `Send "${what}" to your keyboard?`, text, details: techOf(dry), ok: "Send it" });
    if (!ok) return;
    busy = true; updateLock();
    setMsg("send-msg", "Sending... please don't unplug the keyboard.");
    await sse(endpoint, Object.assign({}, body, { dry: false }));
    setMsg("send-msg", "Done! Look at your keyboard screen.", "ok");
    loadHistory();
  } catch (e) {
    $("prog-box").hidden = true; setMsg("send-msg", nice(e), "bad");
  } finally { busy = false; updateLock(); }
}
$("btn-send").onclick = () => current && sendFlow("/api/upload", { previewId: current.id }, source ? source.name : "picture");

async function sse(path, body) {
  const box = $("prog-box"), p = $("prog"); box.hidden = false; p.value = 0; $("prog-pct").textContent = "0%";
  const r = await api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const rd = r.body.getReader(), dec = new TextDecoder(); let buf = "", finished = false;
  for (;;) {
    const { value, done } = await rd.read(); if (done) break;
    buf += dec.decode(value, { stream: true });
    let k; while ((k = buf.indexOf("\n\n")) >= 0) {
      const line = buf.slice(0, k).replace(/^data: /, ""); buf = buf.slice(k + 2);
      if (!line.trim()) continue;
      const ev = JSON.parse(line);
      if (ev.total) { p.value = (100 * ev.done) / ev.total; $("prog-pct").textContent = Math.round(p.value) + "%"; }
      if (ev.event === "error") throw new Error(ev.error);
      if (ev.event === "done") { finished = true; p.value = 100; $("prog-pct").textContent = "100%"; if (ev.warning) toast(ev.warning); }
    }
  }
  if (!finished) throw new Error("no reply: the connection ended early");
}

// ---- history -----------------------------------------------------------------
async function loadHistory() {
  let list = [];
  try { list = await getJSON("/api/history"); } catch (_) { /* ignore */ }
  const box = $("hist-list"), adv = $("hist-adv"); box.textContent = ""; adv.textContent = "";
  $("recent").hidden = !list.length;
  if (!list.length) adv.textContent = "Nothing sent yet.";
  for (const e of list.slice(0, 12)) {
    const when = new Date(e.when).toLocaleString();
    const b = el("button", "card restore"); b.type = "button"; b.title = `${e.name} - ${when}`;
    const im = el("img"); im.alt = ""; im.width = 128; im.height = 128;
    blobURL("/api/history/thumb?id=" + encodeURIComponent(e.id)).then((u) => (im.src = u)).catch(() => {});
    b.append(im, el("span", "nm", e.name), el("span", "ds", when), el("span", "again", "Use again"));
    b.onclick = () => sendFlow("/api/history/restore", { id: e.id }, e.name);
    box.append(b);
    const a = el("button", "restore", `Use again: ${e.name}`); a.type = "button";
    a.onclick = b.onclick; adv.append(a);
    const g = el("button", null, "GIF"); g.type = "button"; g.title = "Download " + e.name + " as GIF";
    g.onclick = async () => { const u = await blobURL("/api/history/gif?id=" + encodeURIComponent(e.id)); const l = el("a"); l.href = u; l.download = e.name + ".gif"; l.click(); };
    adv.append(g);
  }
  updateLock();
}

// ---- advanced ----------------------------------------------------------------
function renderIfaces() {
  const tb = document.querySelector("#ifaces tbody"); tb.textContent = "";
  for (const i of dev.interfaces) {
    const tr = el("tr", i.vendor ? "vendor" : "");
    [i.node, i.iface, i.page, i.in, i.out, i.feature, i.rw ? "yes" : "NO"].forEach((v) => tr.append(el("td", null, String(v))));
    tb.append(tr);
  }
  const v = dev.interfaces.filter((i) => i.vendor && i.out > 0);
  const cs = $("cmd-node"), ds = $("data-node"), keepC = cs.value, keepD = ds.value;
  cs.textContent = ""; ds.textContent = ""; ds.append(new Option("same as command", ""));
  for (const i of v) { const t = `${i.node} (${i.page}, out ${i.out})`; cs.append(new Option(t, i.node)); ds.append(new Option(t, i.node)); }
  cs.value = keepC || (dev.config.cmd && dev.config.cmd.node) || (v[0] && v[0].node) || "";
  ds.value = keepD || (dev.config.data && dev.config.data.node) || "";
  syncLen();
}
function syncLen() { const i = dev.interfaces.find((x) => x.node === $("cmd-node").value); if (i && !$("cmd-len").value) $("cmd-len").value = i.out; }
$("cmd-node").addEventListener("change", () => { $("cmd-len").value = ""; syncLen(); });

const advConfirm = (title, details) => confirmDlg({ title, text: "This sends data to the keyboard.", details, ok: "Send" });

$("btn-probe-dry").onclick = async () => { try { const r = await post("/api/probe", { dry: true, feature: true }); show("probe-out", r.text); $("btn-copylog").hidden = true; } catch (e) { toast(nice(e)); } };
$("btn-probe").onclick = async () => {
  try {
    const dry = await post("/api/probe", { dry: true, feature: true });
    if (!(await advConfirm("Send probe", dry.text))) return;
    const r = await post("/api/probe", { dry: false, feature: true });
    show("probe-out", r.text); $("btn-copylog").hidden = false; $("btn-copylog").dataset.text = r.text;
  } catch (e) { toast(nice(e)); }
};
$("btn-copylog").onclick = () => { navigator.clipboard.writeText($("btn-copylog").dataset.text || ""); toast("Copied"); };
$("btn-report").onclick = async () => { try { const r = await getJSON("/api/report"); await navigator.clipboard.writeText(r.text); toast("Report copied"); } catch (e) { toast(nice(e)); } };

const infoBody = () => ({ node: $("cmd-node").value, len: parseInt($("cmd-len").value || "0", 10) });
$("btn-info-dry").onclick = async () => {
  try { const r = await post("/api/info", Object.assign(infoBody(), { dry: true })); show("info-out", `would write ${r.node} (${r.len} B):\n` + (r.frames || []).join("\n")); } catch (e) { toast(nice(e)); }
};
$("btn-info").onclick = async () => {
  try {
    const b = infoBody();
    const dry = await post("/api/info", Object.assign({}, b, { dry: true }));
    if (!(await advConfirm("Get device info", `${dry.node} (${dry.len} B report)\n` + (dry.frames || []).join("\n")))) return;
    const r = await post("/api/info", Object.assign({}, b, { dry: false, save: true }));
    show("info-out", JSON.stringify(r.info, null, 2)); toast("Target confirmed"); loadDevices();
  } catch (e) { show("info-out", "no usable reply: " + e.message); }
};
$("btn-data").onclick = async () => {
  try { await post("/api/target", { node: $("data-node").value, len: parseInt($("data-len").value || "0", 10) }); toast("Saved"); loadDevices(); } catch (e) { toast(nice(e)); }
};

$("btn-bi-dry").onclick = async () => { try { const r = await post("/api/builtin", { index: +$("bi").value, dry: true }); show("extras-out", `would write ${r.node} (${r.len} B):\n${r.first}`); } catch (e) { toast(nice(e)); } };
$("btn-bi").onclick = async () => {
  try {
    const d = await post("/api/builtin", { index: +$("bi").value, dry: true });
    if (!(await advConfirm("Select built-in animation", `${d.node} (${d.len} B)\n${d.first}`))) return;
    await post("/api/builtin", { index: +$("bi").value, dry: false }); toast("Sent");
  } catch (e) { toast(nice(e)); }
};

loadDevices(); loadGallery(); loadHistory();
