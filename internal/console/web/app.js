"use strict";
// Mind console: reads the agent's store through /api, and follows new
// decisions live through /api/events, firing the brain's parts as they flow.

const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const api = (path) => fetch(path, { credentials: "same-origin" }).then((r) => {
  if (!r.ok) throw new Error(path + ": " + r.status);
  return r.json();
});
const money = (n) => "$" + (n || 0).toFixed(n >= 0.01 ? 3 : 5);

let info = { timezone: "UTC", name: "" };
const fmtTime = (iso, withDate) => {
  try {
    return new Intl.DateTimeFormat("en-GB", {
      timeZone: info.timezone, hour: "2-digit", minute: "2-digit",
      ...(withDate ? { day: "numeric", month: "short" } : {}),
    }).format(new Date(iso));
  } catch { return ""; }
};
const ago = (ms) => {
  const s = Math.floor(ms / 1000), d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
};

/* ---------- what a decision means ---------- */

// family groups a decision for colour: fast, slow, reflex, or its own life.
function family(e) {
  if (["plan_day", "activity", "task", "weekly_review", "score_skill", "plan_learning"].includes(e.action) || e.source === "life") return "life";
  return e.tier || "reflex";
}
const setName = (set) => (set || "").split(",")[0].split("@")[0];

const ACTIONS = {
  share: "shared something", ask_about_reader: "asked about him", ask_reader_abilities: "asked what he can do",
  ask_about_writer: "asked about themselves", ask_for_help: "asked for help", remind: "reminder",
  small_talk: "small talk", other: "other", continue: "continued a thread", ask: "fairly sure → System 2",
  escalate: "unsure → System 2", reply: "replied", recall: "recalled facts", recall_all: "recalled everything",
  recall_open: "looked for open threads", check_in: "check-in", no_check_in: "nothing to check in on",
  plan_day: "planned the day", activity: "did something", task: "made what it promised", deliver: "sent what it promised", decide_public: "chose what's public", decide_public_works: "chose public works", approved: "you said yes", denied: "you said no", expired: "no answer: not done",
  send: "sent what you approved", follow_up: "followed up", score_skill: "scored its work", lesson: "learned a lesson", ask_review: "asked for your review", weekly_review: "reviewed its week", plan_learning: "planned how to learn",
  skill_feedback: "took your feedback", permission: "permission changed", correct_fact: "corrected a fact", forget_fact: "forgot a fact",
  label: "labelled a misread",
  write_profile: "wrote the profile page", error: "error",
};
const actionText = (a) => ACTIONS[a] || a;

// primary is the main answer of a System 1 decision, as "choice 92%".
function primary(e) {
  const answers = e.answers || {};
  for (const k of ["intent", "answers_previous", "circle"]) {
    const a = answers[k];
    if (a && a.choice) return `${a.choice} ${Math.round(a.confidence * 100)}%`;
  }
  return "";
}

// path is the parts of the mind a decision flows through, in order.
function pathFor(e) {
  const set = setName(e.set), a = e.action;
  if (a === "plan_day") return ["clock", "life", "system2", "intentions"];
  if (a === "activity" || a === "task") return ["intentions", "life", "system2", "facts"];
  if (a === "deliver") return ["clock", "intentions", "system2", "senses"];
  if (a === "remind") return e.tier === "system2" ? ["clock", "intentions", "system2", "senses"] : ["senses", "system1", "router"];
  if (a === "check_in" || a === "no_check_in") return e.tier === "system2" ? ["clock", "system2", "senses"] : ["clock", "system1", "facts"];
  if (a === "decide_public") return ["facts", "system1", "profile"];
  if (a === "decide_public_works") return ["life", "system1", "profile"];
  if (a === "score_skill") return ["life", "system1", "facts"];
  if (a === "ask_review") return ["life", "system2", "senses"];
  if (a === "lesson") return ["senses", "router", "life"];
  if (a === "weekly_review") return ["clock", "life", "system2", "intentions"];
  if (a === "plan_learning") return ["life", "system2", "intentions"];
  if (a === "skill_feedback") return ["senses", "router", "life"];
  if (["approved", "denied", "expired"].includes(a)) return ["senses", "router", "intentions"];
  if (a === "send") return ["intentions", "router", "senses"];
  if (a === "follow_up") return ["intentions", "system2", "senses"];
  if (a === "write_profile") return ["facts", "system2", "profile"];
  if (["recall", "recall_open", "recall_all"].includes(a)) return ["router", "system1", "facts"];
  if (e.tier === "reflex") return ["senses", "router", "senses"];
  if (e.tier === "system2") return ["router", "system2", "working", "people", "senses"];
  if (["incoming_message", "follow_up", "life_area"].includes(set) || e.tier === "system1") return ["senses", "system1", "router"];
  return ["router"];
}

/* ---------- the brain ---------- */

const COLORS = { system1: "#5be7ff", system2: "#b48cff", reflex: "#8a94a6", life: "#ffc35c", accent: "#7cff6b" };

// The parts of the mind, placed where a person's would roughly be (side
// view, facing left): reasoning in the prefrontal cortex, the senses at the
// back, fast judgement in the limbic system, the clock in the brainstem.
const NODES = {
  system2:    { x: -0.70, y: -0.20, z: 0.30, lobe: "frontal", label: "System 2", about: "Slow reasoning: writes every reply, plans the day, does the work." },
  working:    { x: -0.42, y: -0.56, z: 0.25, lobe: "frontal", label: "Working memory", about: "The conversation so far: the last exchanges in this thread." },
  intentions: { x: -0.10, y: -0.70, z: 0.20, lobe: "frontal", label: "Intentions", about: "What it means to do: reminders, promises, its own activities." },
  life:       { x: 0.36, y: -0.58, z: 0.25, lobe: "parietal", label: "Daily life", about: "Its own day: the plan, projects, what it makes." },
  senses:     { x: 0.76, y: -0.20, z: 0.20, lobe: "occipital", label: "Senses", about: "Telegram in and out: what it hears and says." },
  router:     { x: 0.10, y: -0.04, z: 0.00, lobe: "deep", label: "Router", about: "How sure is System 1? Act, ask, or escalate." },
  system1:    { x: -0.16, y: 0.24, z: 0.10, lobe: "temporal", label: "System 1", about: "Fast typed judgements: intent, life area, relevance." },
  facts:      { x: 0.20, y: 0.26, z: 0.20, lobe: "temporal", label: "Facts", about: "Long-term memory: about itself and the people it knows." },
  people:     { x: -0.44, y: 0.34, z: 0.25, lobe: "temporal", label: "People", about: "Who it knows, by circle: family, friends, strangers." },
  profile:    { x: -0.88, y: -0.02, z: 0.25, lobe: "frontal", label: "Profile", about: "Its public page, written from what it chose to share." },
  clock:      { x: 0.30, y: 0.72, z: 0.05, lobe: "stem", label: "Clock", about: "Ticks every minute: reminders, check-ins, its day." },
};
const EDGES = [
  ["senses", "system1"], ["system1", "router"], ["router", "system2"], ["system2", "senses"], ["router", "senses"],
  ["system1", "facts"], ["system2", "working"], ["system2", "facts"], ["system2", "people"], ["clock", "intentions"],
  ["intentions", "system2"], ["clock", "life"], ["life", "system2"], ["life", "facts"], ["facts", "profile"],
  ["system2", "profile"], ["clock", "system1"], ["intentions", "life"], ["clock", "system2"],
];
// Lobes: a soft tint each, so the brain reads as regions with jobs.
const LOBES = {
  frontal:   { x: -0.55, y: -0.28, r: 0.55, c: [180, 140, 255] },
  parietal:  { x: 0.33, y: -0.52, r: 0.42, c: [255, 195, 92] },
  occipital: { x: 0.74, y: -0.12, r: 0.32, c: [124, 255, 107] },
  temporal:  { x: -0.06, y: 0.30, r: 0.42, c: [91, 231, 255] },
  cerebellum:{ x: 0.60, y: 0.44, r: 0.30, c: [91, 231, 255] },
  deep:      { x: 0.10, y: -0.04, r: 0.22, c: [124, 255, 107] },
  stem:      { x: 0.30, y: 0.70, r: 0.22, c: [255, 195, 92] },
};

const brain = (() => {
  const canvas = $("#brain"), ctx = canvas.getContext("2d"), tip = $("#tip");
  let W = 0, H = 0, dpr = 1, scale = 1, cx = 0, cy = 0;
  const pts = [], dust = [], pulses = [], waves = [], heat = {}, fires = {};
  Object.keys(NODES).forEach((k) => (heat[k] = 0));

  /* --- anatomy: outlines as paths, rasterised once into masks --- */
  const M = 400, mask = { cortex: new Uint8Array(M * M), cerebellum: new Uint8Array(M * M), stem: new Uint8Array(M * M) };
  const toM = (v) => Math.round((v + 1.1) / 2.2 * (M - 1));
  function rasterise() {
    const off = document.createElement("canvas"); off.width = off.height = M;
    const o = off.getContext("2d");
    const P = (x) => (x + 1.1) / 2.2 * M;
    const shapes = {
      cortex: (p) => {
        p.moveTo(P(-0.92), P(0.12));
        p.bezierCurveTo(P(-1.02), P(-0.02), P(-1.0), P(-0.30), P(-0.86), P(-0.42));
        p.bezierCurveTo(P(-0.76), P(-0.62), P(-0.55), P(-0.78), P(-0.28), P(-0.82));
        p.bezierCurveTo(P(0.0), P(-0.88), P(0.32), P(-0.84), P(0.55), P(-0.66));
        p.bezierCurveTo(P(0.78), P(-0.50), P(0.96), P(-0.30), P(0.94), P(-0.04));
        p.bezierCurveTo(P(0.93), P(0.12), P(0.86), P(0.24), P(0.74), P(0.27));
        p.bezierCurveTo(P(0.58), P(0.30), P(0.42), P(0.28), P(0.30), P(0.36));
        p.bezierCurveTo(P(0.14), P(0.48), P(-0.12), P(0.54), P(-0.30), P(0.50));
        p.bezierCurveTo(P(-0.46), P(0.47), P(-0.52), P(0.36), P(-0.50), P(0.26));
        p.bezierCurveTo(P(-0.60), P(0.24), P(-0.78), P(0.26), P(-0.92), P(0.12));
      },
      cerebellum: (p) => {
        p.moveTo(P(0.34), P(0.38));
        p.bezierCurveTo(P(0.44), P(0.26), P(0.80), P(0.24), P(0.88), P(0.36));
        p.bezierCurveTo(P(0.94), P(0.48), P(0.80), P(0.62), P(0.60), P(0.62));
        p.bezierCurveTo(P(0.44), P(0.62), P(0.34), P(0.54), P(0.34), P(0.38));
      },
      stem: (p) => {
        p.moveTo(P(0.18), P(0.30));
        p.bezierCurveTo(P(0.24), P(0.46), P(0.24), P(0.70), P(0.26), P(0.98));
        p.lineTo(P(0.38), P(0.98));
        p.bezierCurveTo(P(0.38), P(0.72), P(0.40), P(0.50), P(0.34), P(0.34));
        p.closePath();
      },
    };
    for (const [name, draw] of Object.entries(shapes)) {
      o.clearRect(0, 0, M, M); o.beginPath(); draw(o); o.closePath(); o.fillStyle = "#fff"; o.fill();
      const data = o.getImageData(0, 0, M, M).data;
      for (let i = 0; i < M * M; i++) mask[name][i] = data[i * 4 + 3] > 128 ? 1 : 0;
    }
  }
  const at = (m, x, y) => {
    const i = toM(x), j = toM(y);
    return i >= 0 && j >= 0 && i < M && j < M && mask[m][j * M + i] === 1;
  };
  const inBrain = (x, y) => at("cortex", x, y) || at("cerebellum", x, y) || at("stem", x, y);

  // Major fissures: the cortex is folded around them, so they show as dark seams.
  const SULCI = [
    [[-0.58, 0.20], [-0.30, 0.10], [0.0, 0.02], [0.30, -0.06], [0.48, -0.18]], // lateral (Sylvian)
    [[0.02, -0.86], [-0.02, -0.60], [-0.08, -0.36], [-0.12, -0.10], [-0.10, 0.04]], // central
    [[0.62, -0.62], [0.66, -0.44], [0.70, -0.26]], // parieto-occipital
    [[-0.44, 0.34], [-0.20, 0.30], [0.06, 0.24], [0.28, 0.16]], // superior temporal
    [[-0.80, -0.40], [-0.56, -0.44], [-0.30, -0.46]], // superior frontal
  ].map((line) => {
    const out = [];
    for (let k = 0; k + 1 < line.length; k++) {
      for (let t = 0; t < 1; t += 0.05) out.push([line[k][0] + (line[k + 1][0] - line[k][0]) * t, line[k][1] + (line[k + 1][1] - line[k][1]) * t]);
    }
    return out;
  }).flat();
  const nearSulcus = (x, y, d) => SULCI.some(([a, b]) => (a - x) ** 2 + (b - y) ** 2 < d * d);

  function thickness(x, y) { // how deep the brain is at (x, y), for the 3D turn
    return Math.sqrt(Math.max(0.05, 1 - (x / 1.0) ** 2 - ((y + 0.1) / 0.95) ** 2)) * 0.62;
  }
  function add(x, y, a, size, kind) {
    const t = thickness(x, y);
    pts.push({ x, y, z: (Math.random() * 2 - 1) * t, a, size, tw: Math.random() * 6.28, kind });
  }

  function build() {
    rasterise();
    pts.length = 0;
    // Gyri: short meandering squiggles, like the folds of the cortex. A
    // density cap keeps any one place from piling up into a bright streak.
    const G = 110, dens = new Uint8Array(G * G);
    const cell = (x, y) => Math.max(0, Math.min(G - 1, Math.floor((y + 1.1) / 2.2 * G))) * G + Math.max(0, Math.min(G - 1, Math.floor((x + 1.1) / 2.2 * G)));
    for (let f = 0; f < 1400; f++) {
      let x = Math.random() * 1.9 - 0.95, y = Math.random() * 1.5 - 0.9;
      if (!at("cortex", x, y)) continue;
      let a = Math.random() * Math.PI * 2, curl = (Math.random() - 0.5) * 0.6;
      const steps = 14 + Math.floor(Math.random() * 22);
      for (let k = 0; k < steps; k++) {
        curl = Math.max(-0.45, Math.min(0.45, curl + (Math.random() - 0.5) * 0.35));
        a += curl;
        x += Math.cos(a) * 0.011; y += Math.sin(a) * 0.011;
        if (!at("cortex", x, y) || nearSulcus(x, y, 0.026)) break;
        const c = cell(x, y);
        if (dens[c] > 3) continue;
        dens[c]++;
        add(x, y, 0.5 + Math.random() * 0.5, 1.2, "gyrus");
      }
    }
    // Cerebellum: fine parallel folia.
    for (let k = 0; k < 16; k++) {
      for (let t = 0; t < 1; t += 0.006) {
        const ang = Math.PI * (0.95 + t * 1.1), r = 0.08 + k * 0.016;
        const x = 0.61 + Math.cos(ang) * r * 1.9, y = 0.44 + Math.sin(ang) * r * 0.95 + (Math.random() - 0.5) * 0.006;
        if (at("cerebellum", x, y) && Math.random() < 0.8) add(x, y, 0.45 + Math.random() * 0.4, 1.1, "folia");
      }
    }
    // Brainstem: long fibres.
    for (let i = 0; i < 700; i++) {
      const y = 0.32 + Math.random() * 0.66, x = 0.22 + Math.random() * 0.17 + (y - 0.3) * 0.05;
      if (at("stem", x, y)) add(x, y, 0.35 + Math.random() * 0.35, 1, "stem");
    }
    // Fill, faint.
    for (let i = 0; i < 2600; i++) {
      const x = Math.random() * 2.1 - 1.05, y = Math.random() * 2 - 1;
      if (inBrain(x, y) && !nearSulcus(x, y, 0.02)) add(x, y, 0.18 + Math.random() * 0.3, 1, "fill");
    }
    // The outline, bright, so the shape reads at a glance.
    for (let i = 0; i < 26000; i++) {
      const x = Math.random() * 2.1 - 1.05, y = Math.random() * 2 - 1, d = 0.022;
      if (inBrain(x, y) && (!inBrain(x + d, y) || !inBrain(x - d, y) || !inBrain(x, y + d) || !inBrain(x, y - d))) {
        add(x, y, 0.8 + Math.random() * 0.2, 1.5, "edge");
      }
    }
    dust.length = 0;
    for (let i = 0; i < 320; i++) {
      const r = 1.0 + Math.random() * 0.6, t = Math.random() * Math.PI * 2;
      dust.push({ x: Math.cos(t) * r * 1.05, y: Math.sin(t) * r * 0.82, z: Math.random() - 0.5, a: Math.random() * 0.6, v: 0.0004 + Math.random() * 0.0012, t });
    }
  }

  function resize() {
    const r = canvas.getBoundingClientRect();
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = r.width; H = r.height;
    canvas.width = W * dpr; canvas.height = H * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    scale = Math.min(W / 2.5, H / 2.15); cx = W / 2 - W * 0.01; cy = H / 2 - H * 0.07;
  }
  let rot = 0;
  function project(p) {
    const c = Math.cos(rot), s = Math.sin(rot);
    const x = p.x * c + p.z * s, z = -p.x * s + p.z * c;
    const k = 2.8 / (2.8 + z);
    return { X: cx + x * scale * k, Y: cy + p.y * scale * k, k, z };
  }
  // A nerve fibre between two parts: a gentle arc, bowed away from the centre.
  function fibre(a, b) {
    const p = project(NODES[a]), q = project(NODES[b]);
    const mx = (p.X + q.X) / 2, my = (p.Y + q.Y) / 2;
    const dx = mx - cx, dy = my - cy, len = Math.hypot(dx, dy) || 1, bow = Math.hypot(q.X - p.X, q.Y - p.Y) * 0.18;
    return { p, q, c: { X: mx + (dx / len) * bow, Y: my + (dy / len) * bow } };
  }
  const bez = (f, t) => ({
    X: (1 - t) ** 2 * f.p.X + 2 * (1 - t) * t * f.c.X + t * t * f.q.X,
    Y: (1 - t) ** 2 * f.p.Y + 2 * (1 - t) * t * f.c.Y + t * t * f.q.Y,
  });

  function fire(e) {
    const path = pathFor(e), color = COLORS[family(e)] || COLORS.accent;
    path.forEach((n, i) => setTimeout(() => {
      heat[n] = 1;
      waves.push({ node: n, born: performance.now(), color: hexRGB(color) });
    }, i * 480));
    for (let i = 0; i + 1 < path.length; i++) pulses.push({ from: path[i], to: path[i + 1], t: -i * 1.05, color });
  }
  const hexRGB = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  // The clock really ticks every minute; show it.
  setInterval(() => {
    heat.clock = Math.max(heat.clock, 0.55);
    waves.push({ node: "clock", born: performance.now(), color: [255, 195, 92], soft: true });
  }, 60000);

  let hover = null;
  canvas.addEventListener("mousemove", (ev) => {
    const r = canvas.getBoundingClientRect(), mx = ev.clientX - r.left, my = ev.clientY - r.top;
    hover = null;
    for (const [k, n] of Object.entries(NODES)) {
      const p = project(n);
      if (Math.hypot(p.X - mx, p.Y - my) < 20) hover = k;
    }
    if (hover) {
      const n = NODES[hover];
      tip.innerHTML = `<b>${esc(n.label)}</b>${esc(n.about)}<br><span class="muted">fired ${fires[hover] || 0}× since you opened this</span>`;
      tip.style.left = Math.min(mx + 18, W - 250) + "px";
      tip.style.top = my + 24 + "px";
      tip.style.opacity = 1;
    } else tip.style.opacity = 0;
  });
  canvas.addEventListener("mouseleave", () => { hover = null; tip.style.opacity = 0; });

  function hud(now) {
    // A holographic base and a slow ring of ticks around the brain.
    const baseY = cy + scale * 1.04, rx = scale * 0.95, ry = scale * 0.16;
    ctx.save();
    for (let i = 0; i < 3; i++) {
      ctx.strokeStyle = `rgba(124,255,107,${0.18 - i * 0.05})`;
      ctx.setLineDash(i === 1 ? [3, 7] : []);
      ctx.lineDashOffset = -now / 60;
      ctx.beginPath(); ctx.ellipse(cx, baseY, rx * (1 - i * 0.22), ry * (1 - i * 0.22), 0, 0, Math.PI * 2); ctx.stroke();
    }
    ctx.setLineDash([]);
    const glow = ctx.createRadialGradient(cx, baseY, 0, cx, baseY, rx);
    glow.addColorStop(0, "rgba(124,255,107,0.10)"); glow.addColorStop(1, "transparent");
    ctx.fillStyle = glow; ctx.beginPath(); ctx.ellipse(cx, baseY, rx, ry, 0, 0, Math.PI * 2); ctx.fill();
    // ring of ticks
    const R = scale * 1.22, spin = now / 40000;
    for (let i = 0; i < 120; i++) {
      const a = spin + (i / 120) * Math.PI * 2, long = i % 10 === 0;
      const r1 = R, r2 = R + (long ? 10 : 4);
      ctx.strokeStyle = `rgba(124,255,107,${long ? 0.35 : 0.12})`;
      ctx.beginPath(); ctx.moveTo(cx + Math.cos(a) * r1, cy + Math.sin(a) * r1 * 0.86); ctx.lineTo(cx + Math.cos(a) * r2, cy + Math.sin(a) * r2 * 0.86); ctx.stroke();
    }
    // scan line
    const sy = cy - scale * 0.95 + ((now / 4200) % 1) * scale * 2.0;
    const sg = ctx.createLinearGradient(0, sy - 30, 0, sy + 2);
    sg.addColorStop(0, "transparent"); sg.addColorStop(1, "rgba(124,255,107,0.07)");
    ctx.fillStyle = sg; ctx.fillRect(cx - scale * 1.1, sy - 30, scale * 2.2, 32);
    ctx.restore();
  }

  let last = performance.now();
  function frame(now) {
    const dt = Math.min(64, now - last); last = now;
    rot = Math.sin(now / 11000) * 0.38;
    ctx.clearRect(0, 0, W, H);
    hud(now);

    // lobe tints, brighter where a part just fired
    ctx.globalCompositeOperation = "lighter";
    for (const [name, l] of Object.entries(LOBES)) {
      const p = project({ x: l.x, y: l.y, z: 0 });
      let h = 0;
      for (const [k, n] of Object.entries(NODES)) if (n.lobe === name) h = Math.max(h, heat[k]);
      const g = ctx.createRadialGradient(p.X, p.Y, 0, p.X, p.Y, l.r * scale);
      g.addColorStop(0, `rgba(${l.c.join(",")},${0.07 + h * 0.22})`); g.addColorStop(1, "transparent");
      ctx.fillStyle = g; ctx.beginPath(); ctx.arc(p.X, p.Y, l.r * scale, 0, Math.PI * 2); ctx.fill();
    }
    ctx.globalCompositeOperation = "source-over";

    // dust
    for (const d of dust) {
      d.t += d.v * dt * 0.06;
      const p = project({ x: d.x + Math.cos(d.t) * 0.03, y: d.y + Math.sin(d.t * 1.3) * 0.03, z: d.z });
      ctx.fillStyle = `rgba(200,255,210,${0.12 + d.a * 0.45 * (0.6 + 0.4 * Math.sin(now / 900 + d.t * 9))})`;
      ctx.fillRect(p.X, p.Y, 1.3, 1.3);
    }

    // waves of activation spreading through the cortex
    for (let i = waves.length - 1; i >= 0; i--) if (now - waves[i].born > 1600) waves.splice(i, 1);
    const live = waves.map((w) => ({ n: NODES[w.node], r: ((now - w.born) / 1600) * 0.75, c: w.color, s: w.soft ? 0.5 : 1 }));

    for (const pt of pts) {
      const p = project(pt);
      const tw = 0.78 + 0.22 * Math.sin(now / 650 + pt.tw);
      const depth = 0.4 + 0.6 * (1 - (p.z + 0.65) / 1.3);
      let a = Math.min(1, pt.a * tw * depth), r = 225, g = 245, b = 236, size = pt.size * p.k;
      for (const w of live) {
        const d = Math.hypot(pt.x - w.n.x, pt.y - w.n.y), band = Math.abs(d - w.r);
        if (band < 0.07) {
          const k = (1 - band / 0.07) * (1 - w.r / 0.75) * w.s;
          r += (w.c[0] - r) * k; g += (w.c[1] - g) * k; b += (w.c[2] - b) * k;
          a = Math.min(1, a + k * 0.8); size += k * 1.4;
        }
      }
      ctx.fillStyle = `rgba(${r | 0},${g | 0},${b | 0},${a})`;
      ctx.fillRect(p.X, p.Y, size, size);
    }

    // fibres
    for (const [a, b] of EDGES) {
      const f = fibre(a, b), h = Math.max(heat[a], heat[b]);
      ctx.strokeStyle = `rgba(124,255,107,${0.12 + h * 0.5})`;
      ctx.lineWidth = 1 + h;
      ctx.beginPath(); ctx.moveTo(f.p.X, f.p.Y); ctx.quadraticCurveTo(f.c.X, f.c.Y, f.q.X, f.q.Y); ctx.stroke();
    }
    ctx.lineWidth = 1;

    // impulses travelling along the fibres
    ctx.globalCompositeOperation = "lighter";
    for (let i = pulses.length - 1; i >= 0; i--) {
      const u = pulses[i];
      u.t += dt / 480;
      if (u.t < 0) continue;
      if (u.t >= 1) { pulses.splice(i, 1); continue; }
      const f = fibre(u.from, u.to);
      for (let k = 0; k < 7; k++) {
        const t = u.t - k * 0.035;
        if (t < 0) break;
        const pnt = bez(f, t), rad = 9 - k;
        const g = ctx.createRadialGradient(pnt.X, pnt.Y, 0, pnt.X, pnt.Y, rad * 1.8);
        g.addColorStop(0, u.color); g.addColorStop(1, "transparent");
        ctx.globalAlpha = 1 - k / 7;
        ctx.fillStyle = g; ctx.beginPath(); ctx.arc(pnt.X, pnt.Y, rad * 1.8, 0, Math.PI * 2); ctx.fill();
      }
      ctx.globalAlpha = 1;
    }
    ctx.globalCompositeOperation = "source-over";

    // the parts: glowing neurons with HUD callouts
    for (const [k, n] of Object.entries(NODES)) {
      const p = project(n), h = heat[k];
      if (h > 0.02) heat[k] *= Math.pow(0.9986, dt);
      const pulse = 0.5 + 0.5 * Math.sin(now / 900 + n.x * 5);
      const R = 18 + h * 30;
      const glow = ctx.createRadialGradient(p.X, p.Y, 0, p.X, p.Y, R);
      glow.addColorStop(0, `rgba(124,255,107,${0.35 + h * 0.5})`); glow.addColorStop(1, "transparent");
      ctx.fillStyle = glow; ctx.beginPath(); ctx.arc(p.X, p.Y, R, 0, Math.PI * 2); ctx.fill();
      ctx.fillStyle = h > 0.25 ? "#eaffe6" : "#9dff8f";
      ctx.beginPath(); ctx.arc(p.X, p.Y, 3 + h * 2, 0, Math.PI * 2); ctx.fill();
      ctx.strokeStyle = `rgba(124,255,107,${0.35 + pulse * 0.25 + h * 0.4})`;
      ctx.beginPath(); ctx.arc(p.X, p.Y, 7 + pulse * 1.5 + h * 4 + (hover === k ? 3 : 0), 0, Math.PI * 2); ctx.stroke();

      const left = n.x < -0.05, sx = left ? -1 : 1;
      const ex = p.X + sx * 16, ey = p.Y - 12, tx = ex + sx * 16;
      ctx.strokeStyle = `rgba(124,255,107,${0.35 + h * 0.5})`;
      ctx.beginPath(); ctx.moveTo(p.X + sx * 6, p.Y - 4); ctx.lineTo(ex, ey); ctx.lineTo(tx, ey); ctx.stroke();
      ctx.font = "600 10px 'JetBrains Mono', monospace";
      const label = n.label.toUpperCase(), wLabel = ctx.measureText(label).width;
      const lx = left ? tx - wLabel - 4 : tx + 4;
      ctx.fillStyle = "rgba(5,8,10,0.72)"; ctx.fillRect(lx - 4, ey - 8, wLabel + 8, 14);
      ctx.fillStyle = h > 0.25 ? "#7cff6b" : "rgba(214,236,222,0.9)";
      ctx.fillText(label, lx, ey + 3.5);
    }
    requestAnimationFrame(frame);
  }

  build(); resize();
  new ResizeObserver(resize).observe(canvas);
  requestAnimationFrame(frame);
  return {
    fire: (e) => { fire(e); pathFor(e).forEach((n) => (fires[n] = (fires[n] || 0) + 1)); },
    replay: fire, // a past decision: fired again, not counted
  };
})();

/* ---------- EEG: its real activity over the last two hours ---------- */

const eeg = (() => {
  const canvas = $("#eeg"), ctx = canvas.getContext("2d");
  let times = [], W = 0, H = 0;
  function resize() {
    const r = canvas.getBoundingClientRect(), dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = r.width; H = r.height; canvas.width = W * dpr; canvas.height = H * dpr; ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  new ResizeObserver(resize).observe(canvas); resize();
  const span = 2 * 3600 * 1000;
  function draw(now) {
    ctx.clearRect(0, 0, W, H);
    const t = Date.now(), mid = H * 0.55;
    ctx.strokeStyle = "rgba(124,255,107,0.12)";
    for (let m = 0; m <= 8; m++) { const x = (m / 8) * W; ctx.beginPath(); ctx.moveTo(x, 4); ctx.lineTo(x, H - 4); ctx.stroke(); }
    ctx.lineWidth = 1.4; ctx.strokeStyle = "#7cff6b"; ctx.shadowColor = "#7cff6b"; ctx.shadowBlur = 6;
    ctx.beginPath();
    for (let x = 0; x <= W; x += 1.5) {
      const at = t - span + (x / W) * span;
      let y = Math.sin(x * 0.35 + now / 260) * 1.2 + Math.sin(x * 0.11 - now / 700) * 1.5; // resting rhythm
      for (const d of times) {
        const dx = (at - d) / 45000; // a decision is a spike, ~45s wide
        if (Math.abs(dx) < 3) y -= Math.exp(-dx * dx * 2) * Math.sin(dx * 9) * H * 0.34;
      }
      x === 0 ? ctx.moveTo(x, mid + y) : ctx.lineTo(x, mid + y);
    }
    ctx.stroke(); ctx.shadowBlur = 0; ctx.lineWidth = 1;
    requestAnimationFrame(draw);
  }
  requestAnimationFrame(draw);
  return {
    set: (log) => { times = log.map((e) => new Date(e.at).getTime()).filter((x) => Date.now() - x < span + 60000); },
    add: (e) => times.push(new Date(e.at).getTime()),
  };
})();

/* ---------- background: a calm field of drifting neurons ---------- */

(() => {
  const canvas = $("#bg"), ctx = canvas.getContext("2d"), nodes = [];
  let W = 0, H = 0;
  function resize() {
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = innerWidth; H = innerHeight; canvas.width = W * dpr; canvas.height = H * dpr; ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  addEventListener("resize", resize); resize();
  for (let i = 0; i < 70; i++) nodes.push({ x: Math.random() * W, y: Math.random() * H, vx: (Math.random() - 0.5) * 0.12, vy: (Math.random() - 0.5) * 0.12 });
  function frame() {
    if (!document.hidden) {
      ctx.clearRect(0, 0, W, H);
      for (const n of nodes) {
        n.x += n.vx; n.y += n.vy;
        if (n.x < -20) n.x = W + 20; if (n.x > W + 20) n.x = -20; if (n.y < -20) n.y = H + 20; if (n.y > H + 20) n.y = -20;
      }
      for (let i = 0; i < nodes.length; i++) {
        for (let j = i + 1; j < nodes.length; j++) {
          const a = nodes[i], b = nodes[j], d = Math.hypot(a.x - b.x, a.y - b.y);
          if (d < 150) { ctx.strokeStyle = `rgba(124,255,107,${0.07 * (1 - d / 150)})`; ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke(); }
        }
        ctx.fillStyle = "rgba(160,255,170,0.22)"; ctx.fillRect(nodes[i].x - 1, nodes[i].y - 1, 2, 2);
      }
    }
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);
})();

/* ---------- shared bits ---------- */

const FAMILY_COLOR = { system1: "var(--s1)", system2: "var(--s2)", reflex: "var(--rx)", life: "var(--lf)" };
// Colours for kinds of work and projects: the built-in ones fixed, the ones an
// agent makes (from its life.yaml) picked from the palette by name.
const PALETTE = ["#7cff6b", "#ffc35c", "#ff9de2", "#5be7ff", "#8affd6", "#ff8a6b", "#c9ff5c", "#6b9dff"];
const hashColor = (s) => PALETTE[[...String(s)].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7) % PALETTE.length];
const KIND_FIXED = { journal: "#b48cff", notes: "#5be7ff", reading_notes: "#8affd6", writing: "#ff9de2" };
const kindColor = (k) => KIND_FIXED[k] || hashColor(k);
const AREA_COLORS = ["#5be7ff", "#7cff6b", "#ffc35c", "#b48cff", "#ff9de2", "#8affd6", "#ff8a6b", "#c7ff6b"];
const areaColor = (a) => {
  if (!a) return "#8a94a6";
  let h = 0; for (const c of a) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return AREA_COLORS[h % AREA_COLORS.length];
};
const dayKey = (iso) => new Intl.DateTimeFormat("en-CA", { timeZone: info.timezone }).format(new Date(iso));
const dayName = (iso) => new Intl.DateTimeFormat("en-GB", { timeZone: info.timezone, weekday: "long", day: "numeric", month: "long" }).format(new Date(iso));
const minutesOfDay = (iso) => {
  const p = new Intl.DateTimeFormat("en-GB", { timeZone: info.timezone, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).formatToParts(new Date(iso));
  return +p.find((x) => x.type === "hour").value * 60 + +p.find((x) => x.type === "minute").value;
};

/* ---------- mind ---------- */

function sigItem(e, isNew) {
  const fam = family(e), text = e.reply || e.text, conf = primary(e);
  const b = document.createElement("button");
  b.className = "sig t-" + fam + (isNew ? " new" : "");
  b.innerHTML = `<span class="n"></span>
    <div><div class="sig-top"><b>${esc(actionText(e.action))}</b><time>${fmtTime(e.at)}</time></div>
    <div class="sig-text" style="color:var(--soft)">${esc(text || setName(e.set))}</div>
    <div class="sig-meta">${esc(setName(e.set))}${conf ? " · " + esc(conf) : ""} · ${money(e.cost_usd)} · ${e.latency_ms}ms</div></div>
    <span class="rp" role="button" tabindex="0" title="replay it on the brain">▶ REPLAY</span>`;
  b.onclick = (ev) => (ev.target.closest(".rp") ? replay(e.id) : openChain(e.id));
  return b;
}

async function loadFeed() {
  const log = await api("/api/log?limit=200");
  eeg.set(log);
  const feed = $("#feed");
  feed.innerHTML = log.length ? "" : `<div class="empty">no signals yet</div>`;
  log.slice(0, 40).forEach((e) => feed.appendChild(sigItem(e)));
}

function drawDial(n) {
  const svg = $("#dialSvg"), ticks = 60, full = Math.max(40, Math.ceil(n / 20) * 20), lit = Math.round((Math.min(n, full) / full) * ticks);
  let html = `<circle class="orbit" cx="100" cy="100" r="93"/><circle class="orbit2" cx="100" cy="100" r="62"/>`;
  for (let i = 0; i < ticks; i++) {
    const a = (i / ticks) * Math.PI * 2 - Math.PI / 2, r1 = 72, r2 = i % 5 === 0 ? 86 : 82;
    html += `<line class="tick${i < lit ? " lit" : ""}" x1="${100 + Math.cos(a) * r1}" y1="${100 + Math.sin(a) * r1}" x2="${100 + Math.cos(a) * r2}" y2="${100 + Math.sin(a) * r2}"/>`;
  }
  svg.innerHTML = html;
}

async function loadOverview() {
  const o = await api("/api/overview");
  info = o;
  $("#agentName").textContent = o.name;
  $("#crumbName").textContent = o.name.toUpperCase();
  $("#zone").textContent = o.timezone;
  $("#s1").textContent = o.system1; $("#s1").title = o.system1;
  $("#s2").textContent = o.system2; $("#s2").title = o.system2;
  document.title = `${o.name} · Mind Console`;
  started = new Date(o.started_at);
  const t = o.today;
  $("#decToday").textContent = t.decisions;
  drawDial(t.decisions);
  const max = Math.max(1, ...Object.values(t.by_tier));
  $("#tiers").innerHTML = [["system1", "System 1"], ["system2", "System 2"], ["reflex", "Reflex"]].map(([k, l]) => {
    const n = t.by_tier[k] || 0, lit = Math.round((n / max) * 20);
    return `<div class="tier"><span>${l}</span><div class="seg" style="color:${COLORS[k]}">${Array.from({ length: 20 }, (_, i) => `<i class="${i < lit ? "lit" : ""}"></i>`).join("")}</div><b>${n}</b></div>`;
  }).join("");
  const s1 = t.by_tier.system1 || 0;
  $("#escRate").textContent = s1 ? Math.round((t.escalations / s1) * 100) + "%" : "–";
  $("#avgLat").textContent = t.avg_latency_ms ? (t.avg_latency_ms / 1000).toFixed(1) + "s" : "–";
  $("#costToday").textContent = money(t.cost_usd);
  $("#costPill").textContent = money(t.cost_usd) + " today";
  const c = o.counts;
  $("#counts").innerHTML = [["facts", c.facts, "#5be7ff"], ["about self", c.self_facts, "#7cff6b"], ["people", c.people, "#ff9de2"],
    ["intentions", c.intentions, "#ffc35c"], ["projects", c.projects, "#b48cff"], ["works", c.works, "#8affd6"],
    ["percepts", c.percepts, "#8a94a6"], ["decisions", c.decisions, "#7cff6b"]]
    .map(([k, v, col]) => `<div class="cell" style="--c:${col}"><span>${k}</span><b>${v}</b></div>`).join("");
  const days = o.cost_by_day || [], maxC = Math.max(0.0001, ...days.map((d) => d.cost_usd));
  $("#bars").innerHTML = days.length ? days.map((d) =>
    `<div style="height:${Math.max(2, (d.cost_usd / maxC) * 100)}%" data-t="${esc(d.day)} · ${money(d.cost_usd)} · ${d.decisions} decisions"></div>`).join("")
    : `<div class="empty">no cost yet</div>`;
  $("#barsAxis").innerHTML = days.length ? `<span>${esc(days[0].day.slice(5))}</span><span>max ${money(maxC)}</span><span>${esc(days[days.length - 1].day.slice(5))}</span>` : "";
  if (o.next) {
    const mins = Math.round((new Date(o.next.due_at) - Date.now()) / 60000);
    $("#next").innerHTML = `<time>${fmtTime(o.next.due_at)}</time><span class="in">${mins > 0 ? "in " + ago(mins * 60000) : "due now"} · ${esc(o.next.kind)}</span><p>${esc(o.next.text)}</p>`;
  } else $("#next").innerHTML = "nothing pending";
}

let planDay = null;
// approvals: what it asked its owner (the autonomy gate), waiting first.
const CLASS_TEXT = { reach_out: "message first", commit: "promise", publish: "publish", spend: "spend", external_message: "message outside" };
async function loadApprovals() {
  const as = await api("/api/approvals?limit=3"), el = $("#approvals");
  if (!as.length) { el.innerHTML = `<div class="empty">nothing asked yet</div>`; return; }
  el.innerHTML = as.map((a) => {
    const who = a.name || (a.person ? a.person : "");
    const body = a.class === "reach_out" ? a.text : a.what;
    const state = a.status === "pending" ? (a.sent ? "waiting · since " + fmtTime(a.asked_at) : "not sent yet") : a.status;
    return `<div class="appr ${a.status === "pending" ? "" : "done"}"><div class="appr-top"><b>${esc(CLASS_TEXT[a.class] || a.class)}${who ? " · " + esc(who) : ""}</b><span>${esc(state)}</span></div>
      <p>${esc(body)}</p></div>`;
  }).join("");
}

async function loadPlan() {
  planDay = await api("/api/day");
  renderPlan();
}
let planWidth = 0; // its height follows the layout, so only a new width re-lays it out
new ResizeObserver(([e]) => { if (planDay && e.contentRect.width !== planWidth) renderPlan(); }).observe($("#plan"));
function renderPlan() {
  const d = planDay, el = $("#plan");
  if (!d.intentions.length) { el.innerHTML = `<div class="empty">nothing planned today</div>`; el.style.height = ""; return; }
  const ms = d.intentions.map((i) => minutesOfDay(i.due_at)), nowM = minutesOfDay(new Date().toISOString());
  const lo = Math.min(...ms, nowM) - 30, hi = Math.max(...ms, nowM) + 30, pos = (m) => ((m - lo) / (hi - lo)) * 100;

  // Give each label a lane (below or above the axon, then further out) where
  // it overlaps no other; a label that fits nowhere is left to its node's tooltip.
  const W = (planWidth = el.clientWidth) || 300, LW = 132, LH = 44, GAP = 6, TIERS = 2;
  const lanes = []; // lanes[side*TIERS+tier] = occupied [from, to] in px
  for (let k = 0; k < 2 * TIERS; k++) lanes.push([]);
  const nowX = (pos(nowM) / 100) * W;
  lanes[TIERS].push([nowX - 18, nowX + 18]); // the NOW tag sits just above the axon
  const free = (lane, a, b) => lanes[lane].every(([x, y]) => b + GAP <= x || a >= y + GAP);
  const order = ms.map((m, k) => k).sort((a, b) => ms[a] - ms[b]);
  const place = [];
  let above = 1, below = 0, hidden = 0;
  for (const k of order) {
    // a label may lean inwards at the ends, into the axon's margin, but not past it
    const x = Math.min(Math.max((pos(ms[k]) / 100) * W, LW / 2 - 60), W - LW / 2 + 60), a = x - LW / 2, b = x + LW / 2;
    let lane = -1;
    for (let tier = 0; tier < TIERS && lane < 0; tier++)
      for (const side of [0, 1]) if (free(side * TIERS + tier, a, b)) { lane = side * TIERS + tier; break; }
    if (lane < 0) { hidden++; continue; }
    lanes[lane].push([a, b]);
    const side = Math.floor(lane / TIERS), tier = lane % TIERS;
    place[k] = { side, tier, x };
    if (side) above = Math.max(above, tier + 1); else below = Math.max(below, tier + 1);
  }
  const L = 22 + above * (LH + GAP) + 6, H = L + 22 + below * (LH + GAP);
  el.style.height = H + "px";

  let html = `<div class="line" style="top:${L}px"></div><div class="now" style="left:${pos(nowM)}%;top:${L - 22}px"></div>`;
  for (let h = Math.ceil(lo / 60); h * 60 <= hi; h++) html += `<span class="hour" style="left:${pos(h * 60)}%;top:${L + 6}px">${String(h).padStart(2, "0")}</span>`;
  d.intentions.forEach((i, k) => {
    const col = i.status === "missed" ? "var(--bad)" : i.status === "done" ? "var(--accent)" : i.kind === "remind" ? "var(--s1)" : "var(--lf)";
    const tip = `${fmtTime(i.due_at)} · ${i.status} · ${i.result || i.text}`;
    html += `<span class="pnode ${i.status === "done" ? "done" : ""}" style="left:${pos(ms[k])}%;top:${L - 6}px;color:${col}" title="${esc(tip)}"></span>`;
    const p = place[k];
    if (!p) return;
    const top = p.side ? L - 20 - LH - p.tier * (LH + GAP) : L + 20 + p.tier * (LH + GAP);
    const stem = p.side ? [top + LH, L - 7] : [L + 7, top];
    html += `<span class="pstem" style="left:${pos(ms[k])}%;top:${stem[0]}px;height:${stem[1] - stem[0]}px;color:${col}"></span>
      <div class="plabel" style="left:${p.x}px;top:${top}px;width:${LW}px"><time style="color:${col}">${fmtTime(i.due_at)}</time><span>${esc(i.result || i.text)}</span></div>`;
  });
  if (hidden) html += `<span class="pmore">+${hidden} more · hover the dots</span>`;
  el.innerHTML = html;
}

/* ---------- activity: decision chains ---------- */

let logFilter = "all", logCache = [];
async function loadLog() {
  logCache = await api("/api/log?limit=300");
  renderLog();
}
function renderLog() {
  const groups = new Map();
  for (const e of logCache) {
    const key = e.percept_id || e.parent_id || e.id;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(e);
  }
  const fams = ["all", "system1", "system2", "life", "reflex"];
  $("#logChips").innerHTML = fams.map((f) => `<button class="chip ${f === logFilter ? "on" : ""}" data-f="${f}" style="${f !== "all" ? "color:" + FAMILY_COLOR[f] : ""}">${f === "all" ? "" : "<i></i>"}${f === "all" ? "all" : f}</button>`).join("");
  $("#logChips").querySelectorAll(".chip").forEach((c) => (c.onclick = () => { logFilter = c.dataset.f; renderLog(); }));
  const el = $("#log");
  el.innerHTML = "";
  for (const g of groups.values()) {
    g.sort((a, b) => new Date(a.at) - new Date(b.at));
    if (logFilter !== "all" && !g.some((e) => family(e) === logFilter)) continue;
    const head = g[0], last = g[g.length - 1], cost = g.reduce((s, e) => s + e.cost_usd, 0);
    const reply = [...g].reverse().find((e) => e.reply);
    const what = head.text ? esc(head.text) : ["reminder", "check_in", "delivery", "review_request"].includes(head.kind) ? `<span class="muted">spoke first</span>` : `<span class="muted">${esc(actionText(head.action))}</span>`;
    const b = document.createElement("button");
    b.className = "chain";
    b.innerHTML = `<div class="when">${fmtTime(head.at)}<small>${fmtTime(head.at, true).split(",")[0] || ""}</small></div>
      <div><div class="what"><span class="src">${esc(head.source || "self")}${head.kind ? " · " + esc(head.kind) : ""}</span>${what}</div>
      <div class="links">${g.map((e) => `<span class="link" style="color:${FAMILY_COLOR[family(e)]}"><i></i>${esc(actionText(e.action))}${primary(e) ? " " + esc(primary(e).split(" ").pop()) : ""}</span>`).join("")}</div>
      ${reply ? `<div class="reply">${esc(reply.reply.split("\n")[0])}</div>` : ""}</div>
      <div class="cost"><b>${money(cost)}</b>${g.length} step${g.length > 1 ? "s" : ""} · ${((new Date(last.at) - new Date(head.at)) / 1000 + last.latency_ms / 1000).toFixed(1)}s</div>`;
    b.onclick = () => openChain(last.id);
    el.appendChild(b);
  }
  if (!el.children.length) el.innerHTML = `<div class="card empty">no decisions here yet</div>`;
}

/* ---------- memory network ---------- */

let memory = null, factFilter = "all";
const net = (() => {
  const canvas = $("#net"), ctx = canvas.getContext("2d"), tip = $("#netTip");
  let W = 0, H = 0, nodes = [], links = [], hover = null;
  function resize() {
    const r = canvas.getBoundingClientRect(), dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = r.width; H = r.height; canvas.width = W * dpr; canvas.height = H * dpr; ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    if (memory) build(memory);
  }
  new ResizeObserver(resize).observe(canvas);
  function build(m) {
    nodes = []; links = [];
    const people = m.people.map((p) => p.key), hubs = {};
    const cx = W / 2, cy = H / 2;
    const n = people.length, single = n === 1;
    hubs.self = { x: single ? W * 0.32 : cx, y: cy, r: 20, label: info.name || "self", hub: true, color: "#7cff6b" };
    people.forEach((k, i) => {
      const a = single ? 0 : -Math.PI / 2 + (i / n) * Math.PI * 2 + 0.6, d = single ? W * 0.36 / 1.25 : Math.min(W, H) * 0.32;
      const p = m.people[i];
      hubs[k] = { x: hubs.self.x + Math.cos(a) * d * 1.25, y: cy + Math.sin(a) * d, r: 16, label: p.name || k, sub: p.circle, hub: true, color: "#ff9de2" };
      links.push([hubs.self, hubs[k], 0.35]);
    });
    nodes.push(...Object.values(hubs));
    const bySubject = {};
    for (const f of m.facts) (bySubject[f.subject] ||= []).push(f);
    for (const [subj, fs] of Object.entries(bySubject)) {
      const hub = hubs[subj] || hubs.self;
      fs.forEach((f, i) => {
        const per = 9, ring = 62 + Math.floor(i / per) * 28, count = Math.min(per, fs.length - Math.floor(i / per) * per);
        const a = (i % per) / count * Math.PI * 2 + ring * 0.05 - Math.PI / 2;
        const node = { x: hub.x + Math.cos(a) * ring, y: hub.y + Math.sin(a) * ring, r: 6, fact: f, color: areaColor(f.areas[0]), ph: Math.random() * 6.28 };
        nodes.push(node); links.push([hub, node, 0.22]);
      });
    }
  }
  canvas.addEventListener("mousemove", (ev) => {
    const r = canvas.getBoundingClientRect(), mx = ev.clientX - r.left, my = ev.clientY - r.top;
    hover = null;
    for (const n of nodes) if (Math.hypot(n.dx - mx, n.dy - my) < n.r + 6) hover = n;
    if (hover) {
      tip.innerHTML = hover.fact
        ? `<b>${esc(hover.fact.about || hover.fact.subject)}</b>${esc(hover.fact.text)}<br><span class="muted">${esc(hover.fact.areas.join(", ") || "no area")} · ${fmtTime(hover.fact.at, true)}</span>`
        : `<b>${esc(hover.label)}</b>${esc(hover.sub || "")}`;
      tip.style.left = Math.min(mx + 16, W - 270) + "px"; tip.style.top = my + 18 + "px"; tip.style.opacity = 1;
    } else tip.style.opacity = 0;
  });
  canvas.addEventListener("mouseleave", () => { hover = null; tip.style.opacity = 0; });
  function frame(now) {
    if (view === "memory") {
      ctx.clearRect(0, 0, W, H);
      for (const n of nodes) {
        const wob = n.hub ? 0 : 2.2;
        n.dx = n.x + Math.sin(now / 1400 + (n.ph || 0)) * wob; n.dy = n.y + Math.cos(now / 1700 + (n.ph || 0)) * wob;
      }
      for (const [a, b, al] of links) {
        ctx.strokeStyle = `rgba(124,255,107,${al})`; ctx.beginPath(); ctx.moveTo(a.dx, a.dy); ctx.lineTo(b.dx, b.dy); ctx.stroke();
      }
      for (const n of nodes) {
        const h = hover === n ? 1 : 0, g = ctx.createRadialGradient(n.dx, n.dy, 0, n.dx, n.dy, n.r * 3.2);
        g.addColorStop(0, n.color + (h ? "cc" : "66")); g.addColorStop(1, "transparent");
        ctx.fillStyle = g; ctx.beginPath(); ctx.arc(n.dx, n.dy, n.r * 3.2, 0, Math.PI * 2); ctx.fill();
        ctx.fillStyle = n.hub ? "#06110a" : n.color; ctx.strokeStyle = n.color; ctx.lineWidth = n.hub ? 2 : 1;
        ctx.beginPath(); ctx.arc(n.dx, n.dy, n.r + h * 2, 0, Math.PI * 2); ctx.fill(); if (n.hub) ctx.stroke();
        if (n.hub) {
          ctx.font = "600 11px 'JetBrains Mono', monospace"; ctx.fillStyle = "#e6f0ea"; ctx.textAlign = "center";
          ctx.fillText(n.label.toUpperCase(), n.dx, n.dy + n.r + 18);
          if (n.sub) { ctx.font = "10px 'JetBrains Mono', monospace"; ctx.fillStyle = "#7a8884"; ctx.fillText(n.sub, n.dx, n.dy + n.r + 31); }
          ctx.textAlign = "start";
        }
      }
      ctx.lineWidth = 1;
    }
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);
  return { set: (m) => { memory = m; resize(); } };
})();

async function loadMemory() {
  const m = await api("/api/memory");
  net.set(m);
  const subjects = [...new Set(m.facts.map((f) => f.about || f.subject))];
  $("#factChips").innerHTML = ["all", ...subjects].map((s) => `<button class="chip ${s === factFilter ? "on" : ""}" data-s="${esc(s)}">${esc(s)}</button>`).join("");
  $("#factChips").querySelectorAll(".chip").forEach((c) => (c.onclick = () => { factFilter = c.dataset.s; loadMemory(); }));
  renderFacts();
  $("#people").innerHTML = m.people.length ? m.people.map((p) =>
    `<div class="person"><div class="avatar">${esc((p.name || "?")[0])}</div><div><h4>${esc(p.name || p.key)}</h4>
     <p>${esc(p.relation || "relation not known yet")}</p><div class="fact-meta"><span class="tag">${esc(p.circle || "no circle")}</span>
     <span class="tag">${esc(p.source)}</span></div></div></div>`).join("") : `<div class="empty">nobody yet</div>`;
}
function renderFacts() {
  if (!memory) return;
  const q = $("#factSearch").value.toLowerCase();
  const fs = memory.facts.filter((f) => (factFilter === "all" || (f.about || f.subject) === factFilter) && f.text.toLowerCase().includes(q));
  $("#facts").innerHTML = fs.length ? fs.map((f) => {
    const pub = f.subject === "self" ? (f.public === true ? `<span class="tag pub">public</span>` : f.public === false ? `<span class="tag priv">private</span>` : `<span class="tag">undecided</span>`) : "";
    return `<div class="fact" style="--c:${areaColor(f.areas[0])}"><p>${esc(f.text)}</p><div class="fact-meta"><span>${esc(f.about || f.subject)}</span><span>${fmtTime(f.at, true)}</span>
      ${f.areas.map((a) => `<span class="tag" style="color:${areaColor(a)}">${esc(a)}</span>`).join("")}${pub}${f.asked_at ? `<span class="tag">asked ${fmtTime(f.asked_at, true)}</span>` : ""}</div></div>`;
  }).join("") : `<div class="empty">nothing matches</div>`;
}
$("#factSearch").addEventListener("input", renderFacts);

/* ---------- day ---------- */

let dayOffset = 0;
const PROJECT_FIXED = { writing: "#ff9de2", learning: "#5be7ff", reading: "#ffc35c" };
const projectColor = (k) => PROJECT_FIXED[k] || hashColor(k);
async function loadDay() {
  const d = new Date(Date.now() + dayOffset * 86400000);
  const date = new Intl.DateTimeFormat("en-CA", { timeZone: info.timezone }).format(d);
  const day = await api("/api/day?date=" + date);
  $("#dayTitle").textContent = dayOffset === 0 ? "Today" : dayName(d.toISOString());
  $("#dayLabel").textContent = date;
  const now = Date.now();
  let html = "", marked = dayOffset !== 0;
  for (const i of day.intentions) {
    if (!marked && new Date(i.due_at) > now) { html += `<div class="now-mark"></div>`; marked = true; }
    html += `<div class="tl st-${esc(i.status)}"><div class="tl-top"><time>${fmtTime(i.due_at)}</time><span class="st st-${esc(i.status)}">${esc(i.status)}</span>
      <span class="tag">${esc(i.kind)}${i.subject !== "self" ? " · for " + esc(i.subject) : ""}</span>${i.ability ? `<span class="tag" title="${esc(i.query)}">${esc(i.ability)}${i.query ? ": " + esc(i.query) : ""}</span>` : ""}</div>
      <p>${esc(i.text)}</p>${i.result ? `<div class="res">${esc(i.result)}</div>` : ""}</div>`;
  }
  if (!marked && day.intentions.length) html += `<div class="now-mark"></div>`;
  $("#dayTimeline").innerHTML = html || `<div class="empty">nothing planned</div>`;
  $("#dayWorks").innerHTML = day.works.length ? "" : `<div class="empty">nothing made</div>`;
  day.works.forEach((w) => $("#dayWorks").appendChild(workButton(w, "wk")));
  $("#projects").innerHTML = day.projects.length ? day.projects.map((p) =>
    `<div class="pj" style="--c:${projectColor(p.kind)}"><b>${esc(p.title)}</b><span>${esc(p.kind)} · ${esc(p.status)} · since ${fmtTime(p.created_at, true)}</span>
     ${p.reading ? `<div class="progress"><div class="progress-h"><span>${esc(p.reading.title)}${p.reading.author ? " · " + esc(p.reading.author) : ""}</span><b>${p.reading.percent}%</b></div><div class="progress-bar"><i style="width:${p.reading.percent}%"></i></div></div>` : ""}
     <p>${esc(p.notes || "no notes yet")}</p></div>`).join("") : `<div class="empty">no projects yet</div>`;
}
$("#dayPrev").onclick = () => { dayOffset--; loadDay(); };
$("#dayNext").onclick = () => { dayOffset = Math.min(0, dayOffset + 1); loadDay(); };

/* ---------- works ---------- */

let workFilter = "all", worksCache = [];
// publicTag says whether a work is on its profile page, and who decided.
const publicTag = (w) => w.public === true ? `<span class="tag pub" title="by ${esc(w.public_by)}">public</span>`
  : w.public === false ? `<span class="tag priv" title="by ${esc(w.public_by)}">private</span>` : `<span class="tag">undecided</span>`;
function workButton(w, cls) {
  const b = document.createElement("button"), col = kindColor(w.kind);
  b.className = cls;
  b.style.setProperty("--c", col);
  b.innerHTML = cls === "wk" ? `<b>${esc(w.title)}</b><span>${esc(w.kind)} · ${fmtTime(w.created_at)} · ${w.words} words</span>`
    : `<span class="kind">${esc(w.kind.replace("_", " "))}</span><h3>${esc(w.title)}</h3><p>${esc(w.excerpt || "")}</p>
       <span class="when"><span>${fmtTime(w.created_at, true)}</span><span>${publicTag(w)} ${w.words} words</span></span>`;
  b.onclick = () => openWork(w.id);
  return b;
}
async function loadWorks() {
  worksCache = await api("/api/works");
  renderWorks();
}
function renderWorks() {
  const kinds = ["all", ...new Set(worksCache.map((w) => w.kind))];
  $("#workChips").innerHTML = kinds.map((k) => `<button class="chip ${k === workFilter ? "on" : ""}" data-k="${esc(k)}" style="${k !== "all" ? "color:" + kindColor(k) : ""}">${esc(k.replace("_", " "))}</button>`).join("");
  $("#workChips").querySelectorAll(".chip").forEach((c) => (c.onclick = () => { workFilter = c.dataset.k; renderWorks(); }));
  const grid = $("#works"), ws = worksCache.filter((w) => workFilter === "all" || w.kind === workFilter);
  grid.innerHTML = ws.length ? "" : `<div class="card empty">nothing made yet: its first activity will show up here</div>`;
  ws.forEach((w) => grid.appendChild(workButton(w, "card work-card")));
}
async function openWork(id) {
  const w = await api("/api/works/" + id), col = kindColor(w.kind);
  openDrawer(w.kind.replace("_", " ").toUpperCase(), `<h2 style="margin:0 0 4px;font:600 24px var(--sans)">${esc(w.title)}</h2>
    <div style="font:11px var(--mono);color:var(--muted);margin-bottom:18px"><span style="color:${col}">●</span> ${fmtTime(w.created_at, true)} · ${w.words} words · ${publicTag(w)}</div>
    <div class="reader">${esc(w.text)}</div>`);
}

/* ---------- growth ---------- */

// planHTML is a goal's learning plan: its stages, the current one open with
// what it practises and studies.
function planHTML(p) {
  return `<div class="lplan"><div class="lplan-h">LEARNING PLAN · ${esc(p.skill)} · ${p.status === "active" ? "stage " + (p.stage + 1) + " of " + p.stages.length : esc(p.status)}</div>
    <ol>${p.stages.map((s, i) => `<li class="${s.done ? "done" : i === p.stage && p.status === "active" ? "now" : ""}"><b>${esc(s.learn)}</b>
      ${i === p.stage && p.status === "active" ? `<div class="lp-d">practise: ${esc(s.practice)}<br>done when: ${esc(s.done_when || "–")}</div>
        ${s.resources.length ? `<ul>${s.resources.map((r) => `<li class="${r.status}">${r.status === "done" ? "✓" : "○"} ${esc(r.title || r.query)} <em>${esc(r.ability.replace(/_/g, " "))}</em></li>`).join("")}</ul>` : ""}` : ""}
    </li>`).join("")}</ol></div>`;
}

async function loadGrowth() {
  const g = await api("/api/growth");
  const dreams = [...g.dreams].sort((a, b) => (a.status === "active" ? 0 : 1) - (b.status === "active" ? 0 : 1));
  $("#dreams").innerHTML = dreams.length ? dreams.map((d) => `<div class="dream ${d.status === "active" ? "" : "dropped"}">
      <h3>${esc(d.text)}</h3>
      <div class="src">${d.source === "self" ? "its own" : "from its life story"} · since ${fmtTime(d.since, true)}${d.status !== "active" ? " · " + esc(d.status) : ""}</div>
      ${d.goals.length ? d.goals.map((x) => `<div class="goal ${esc(x.status)}"><div class="goal-top"><b>${x.status === "active" ? "goal" : esc(x.status)}</b>
          <span class="when">due ${fmtTime(x.due, true)}</span></div>
        <p>${esc(x.text)}</p><p class="when">done when: ${esc(x.done_when || "–")}</p>
        ${x.progress ? `<p>${esc(x.progress)}</p>` : ""}
        ${x.projects.length ? `<div class="pj-chips">${x.projects.map((p) => `<span>${esc(p.title)}</span>`).join("")}</div>` : ""}
        ${x.plan ? planHTML(x.plan) : ""}</div>`).join("")
        : `<div class="goal"><p class="when">no goals yet: it sets them in its Sunday review</p></div>`}
    </div>`).join("") : `<div class="empty">no dreams yet: add some to self/life.yaml</div>`;
  $("#skills").innerHTML = g.skills.length ? g.skills.map((s) => {
    const ev = [...s.evidence].reverse(); // oldest first, left to right
    return `<div class="skill"><div class="skill-top"><span>${esc(s.name)}</span><b>${s.level.toFixed(1)}</b></div>
      <div class="skill-bar"><i style="width:${s.level * 10}%"></i></div>
      ${ev.length ? `<div class="skill-ev">${ev.map((e) => `<i class="${e.source === "owner" ? "owner" : ""}" style="height:${e.score * 10}%"
        title="${e.score.toFixed(1)} · ${esc(e.source === "owner" ? "your feedback" : e.source === "life" ? "where its life story starts it" : "scored work")}${e.note ? ": " + esc(e.note) : ""}"></i>`).join("")}</div>` : ""}
      <div class="skill-meta">${ev.length ? ev.length + " recent score" + (ev.length > 1 ? "s" : "") + " · gold is your feedback" : "no scores yet"} · shown by ${esc(s.shown_by.join(", ").replace(/_/g, " ") || "nothing yet")}</div>
      ${s.lessons.length ? `<ul class="lessons">${s.lessons.map((l) => `<li class="src-${esc(l.source)}"><span>${esc({ owner: "you", self: "himself", others: "others" }[l.source] || l.source)}</span>${esc(l.text)}</li>`).join("")}</ul>` : ""}</div>`;
  }).join("") : `<div class="empty">no skills yet: add some to self/life.yaml</div>`;
  $("#review").innerHTML = g.review ? `<h4>${esc(g.review.title)}</h4><div class="skill-meta">${fmtTime(g.review.at, true)}</div><p>${esc(g.review.text)}</p>`
    : `<div class="empty">no review yet: it reviews its week on Sundays</div>`;
}

/* ---------- judgment: how the owner decides, for them to review ---------- */

let judgment = null, jFilter = "proposed";
const J_STATUS = { proposed: "to review", approved: "approved", rejected: "rejected", all: "all" };

async function loadJudgment() {
  judgment = await api("/api/judgment");
  renderJudgment();
}

function jShown(x) { return jFilter === "all" || x.status === jFilter; }

function jButtons(kind, x) {
  const b = (status, label) => `<button data-kind="${kind}" data-id="${esc(x.id)}" data-status="${status}" class="jb jb-${status}">${label}</button>`;
  const edit = `<button data-kind="${kind}" data-id="${esc(x.id)}" data-edit="1" class="jb">${kind === "principle" ? "edit" : x.reason ? "edit reason" : "add reason"}</button>`;
  return `<div class="jbtns">${x.status !== "approved" ? b("approved", "approve") : ""}${edit}${x.status !== "rejected" ? b("rejected", "reject") : ""}${x.status !== "proposed" ? b("proposed", "undo") : ""}</div>`;
}

function caseHTML(c, compact) {
  return `<div class="jcase st-${esc(c.status)}" data-key="${esc(c.key)}">
    <div class="jtop"><b>${esc(c.type)}</b><span>${esc(J_STATUS[c.status] || c.status)}</span></div>
    <p class="jsit">${esc(c.situation)}</p>
    ${c.options.length ? `<div class="jopts">${c.options.map((o) => `<span class="${o === c.choice ? "picked" : ""}">${esc(o)}</span>`).join("")}</div>` : ""}
    <p class="jchoice">→ ${esc(c.choice)}</p>
    ${c.reason ? `<p class="jreason">“${esc(c.reason)}”</p>` : `<p class="jreason none">no reason given</p>`}
    ${compact ? "" : jButtons("case", c)}</div>`;
}

function renderJudgment() {
  if (!judgment) return;
  const all = [...judgment.principles, ...judgment.cases];
  const count = (s) => all.filter((x) => s === "all" || x.status === s).length;
  $("#jFilter").innerHTML = Object.keys(J_STATUS).map((s) => `<button class="chip ${s === jFilter ? "on" : ""}" data-f="${s}">${J_STATUS[s]} · ${count(s)}</button>`).join("");
  const byKey = Object.fromEntries(judgment.cases.map((c) => [c.key, c]));
  const ps = judgment.principles.filter(jShown), cs = judgment.cases.filter(jShown);
  $("#principles").innerHTML = ps.length ? ps.map((p) => `<div class="jp st-${esc(p.status)}">
      <div class="jtop"><b>${p.types.map(esc).join(" · ") || "any decision"}</b><span>${esc(J_STATUS[p.status] || p.status)}${p.edited ? " · edited" : ""}</span></div>
      <h3>${esc(p.text)}</h3>
      ${jButtons("principle", p)}
      ${p.cases.length ? `<details><summary>${p.cases.length} decision${p.cases.length > 1 ? "s" : ""} behind it</summary>
        ${p.cases.map((k) => byKey[k] ? caseHTML(byKey[k], true) : "").join("")}</details>` : ""}
    </div>`).join("") : `<div class="empty">${judgment.principles.length ? "nothing " + J_STATUS[jFilter] : "no principles yet: add some to self/judgment.yaml"}</div>`;
  $("#cases").innerHTML = cs.length ? cs.map((c) => caseHTML(c, false)).join("")
    : `<div class="empty">${judgment.cases.length ? "nothing " + J_STATUS[jFilter] : "no decisions yet: add some to self/judgment.yaml"}</div>`;
}

$("#jFilter").onclick = (e) => {
  const b = e.target.closest("[data-f]");
  if (b) { jFilter = b.dataset.f; renderJudgment(); }
};
$("#view-judgment").addEventListener("click", async (e) => {
  const b = e.target.closest(".jb");
  if (!b) return;
  const { kind, id } = b.dataset, item = (kind === "principle" ? judgment.principles : judgment.cases).find((x) => x.id === id);
  if (b.dataset.edit) {
    const box = b.closest(".jp, .jcase"), was = kind === "principle" ? item.text : item.reason;
    box.querySelector(".jbtns").outerHTML = `<div class="jedit"><textarea rows="3" maxlength="1000" placeholder="${kind === "principle" ? "the rule, in your words" : "why you chose it"}">${esc(was)}</textarea>
      <div class="jbtns"><button class="jb jb-approved" data-save="1">save &amp; approve</button><button class="jb" data-cancel="1">cancel</button></div></div>`;
    const ta = box.querySelector("textarea");
    ta.focus();
    box.querySelector("[data-cancel]").onclick = (ev) => { ev.stopPropagation(); renderJudgment(); };
    box.querySelector("[data-save]").onclick = (ev) => { ev.stopPropagation(); reviewJudgment(kind, id, "approved", ta.value); };
    return;
  }
  if (b.dataset.status) reviewJudgment(kind, id, b.dataset.status, "");
});

async function reviewJudgment(kind, id, status, text) {
  const r = await fetch("/api/judgment", { method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind, id, status, text }) });
  if (r.ok) loadJudgment();
}

/* ---------- conversations ---------- */

async function loadTalk() {
  const [convs, m] = await Promise.all([api("/api/conversation"), memory ? Promise.resolve(memory) : api("/api/memory")]);
  const people = Object.fromEntries(m.people.map((p) => [p.key, p.name]));
  const el = $("#talk");
  el.innerHTML = convs.length ? "" : `<div class="empty">no conversations yet</div>`;
  for (const c of convs) {
    el.insertAdjacentHTML("beforeend", `<div class="who"><span class="dot on"></span>${esc(people[c.person] || c.person)} · ${esc(c.source)}</div>`);
    let lastDay = "";
    for (const x of c.exchanges) {
      const dk = dayKey(x.at);
      if (dk !== lastDay) { el.insertAdjacentHTML("beforeend", `<div class="dayline">${esc(dayName(x.at))}</div>`); lastDay = dk; }
      if (x.message) el.insertAdjacentHTML("beforeend", `<div class="bub them">${esc(x.message)}<small>${fmtTime(x.at)}</small></div>`);
      if (x.reply) el.insertAdjacentHTML("beforeend", `<div class="bub me">${esc(x.reply)}<small>${x.message ? "" : `<span class="first">spoke first · </span>`}${esc(info.name)}</small></div>`);
    }
  }
}

/* ---------- chain drawer ---------- */

function answersHTML(e) {
  const entries = Object.entries(e.answers || {});
  if (!entries.length) return "";
  // A fact check asks one yes/no question per fact, keyed by its ID: summarise.
  if (entries.length > 4 && entries.every(([k, a]) => a.type === "predicate" && /^[0-9A-Z]{26}$/.test(k))) {
    const kept = entries.filter(([, a]) => a.probability >= 0.5).length;
    return `<div class="ans"><span>facts checked</span><div class="bar"><i style="width:${(kept / entries.length) * 100}%"></i></div><b>${kept}/${entries.length}</b></div>`;
  }
  // System 1's answers can be labelled right or wrong, for replay.
  const label = (k, said, choices) => {
    if (e.tier !== "system1") return "";
    const have = (e.labels || {})[k];
    if (have) return `<div class="lbl ${have === said ? "right" : "wrong"}">${have === said ? "✓ right" : "✎ should be " + esc(have.replace(/_/g, " "))}</div>`;
    return `<div class="lbl" data-d="${esc(e.id)}" data-q="${esc(k)}" data-said="${esc(said)}" data-choices="${esc(choices.join(","))}">
      <button class="lb" data-v="right" title="System 1 got this right">✓</button><button class="lb" data-v="wrong" title="System 1 got this wrong">✗</button></div>`;
  };
  return entries.map(([k, a]) => {
    if (a.refused) return `<div class="ans"><span>${esc(k)}</span><div>refused</div><b></b></div>`;
    if (a.type === "choice") {
      const top = Object.entries(a.probabilities || {}).sort((x, y) => y[1] - x[1]).slice(0, 3).map(([c, p]) => `${c} ${Math.round(p * 100)}%`).join(" · ");
      return `<div class="ans"><span title="${esc(top)}">${esc(k)}: ${esc(a.choice)}</span><div class="bar"><i style="width:${a.confidence * 100}%"></i></div><b>${Math.round(a.confidence * 100)}%</b>
        ${label(k, a.choice, Object.keys(a.probabilities || {}))}</div>`;
    }
    const p = a.probability ?? a.confidence ?? 0;
    return `<div class="ans"><span>${esc(k)}</span><div class="bar"><i style="width:${p * 100}%"></i></div><b>${Math.round(p * 100)}%</b>
      ${label(k, String(p >= 0.5), ["true", "false"])}</div>`;
  }).join("");
}

async function openChain(id) {
  const chain = await api("/api/chain/" + id);
  if (!chain.length) return;
  const first = chain[0];
  let html = first.text || first.source
    ? `<div class="percept"><span>${esc(first.source)} · ${esc(first.kind)} · ${fmtTime(first.at, true)}</span><p>${esc(first.text || "(spoke first)")}</p></div>` : "";
  for (const e of chain) {
    const fam = family(e);
    html += `<div class="step t-${fam}"><h4>${esc(actionText(e.action))}</h4>
      <div class="meta">${esc(e.tier)} · ${esc(e.set)} · ${esc(e.provider || "no model")} · ${e.latency_ms}ms · ${money(e.cost_usd)}${e.gate && e.gate !== "auto" ? " · gate: " + esc(e.gate) : ""}${e.outcome ? " · " + esc(e.outcome.status) + (e.outcome.detail ? ": " + esc(e.outcome.detail) : "") : ""}</div>
      ${answersHTML(e)}${e.reply ? `<div class="quote">${esc(e.reply)}</div>` : ""}</div>`;
  }
  openDrawer("DECISION CHAIN", `<button class="drawer-replay" id="drawerReplay">▶ REPLAY ON THE BRAIN</button>` + html);
  $("#drawerReplay").onclick = () => replay(id);
  $("#drawerBody").querySelectorAll(".lbl[data-d]").forEach((el) => (el.onclick = (ev) => labelClick(el, ev, id)));
}

// labelClick: ✓ labels System 1's answer right; ✗ asks what it should have
// been (a yes/no flips), then saves the label and reopens the chain.
async function labelClick(el, ev, chainID) {
  const b = ev.target.closest("button, select");
  if (!b) return;
  const { d, q, said } = el.dataset, choices = el.dataset.choices.split(",").filter(Boolean);
  let value = null;
  if (b.dataset.v === "right") value = said;
  else if (b.dataset.v === "wrong") {
    if (choices.length === 2 && choices.includes("true")) value = said === "true" ? "false" : "true";
    else {
      el.innerHTML = `<select class="lb-pick"><option value="">should be…</option>${choices.filter((c) => c !== said)
        .map((c) => `<option value="${esc(c)}">${esc(c.replace(/_/g, " "))}</option>`).join("")}</select>`;
      el.querySelector("select").onchange = (e2) => e2.target.value && saveLabel(d, q, e2.target.value, chainID);
      return;
    }
  }
  if (value !== null) saveLabel(d, q, value, chainID);
}
async function saveLabel(decision, question, value, chainID) {
  const r = await fetch("/api/labels", { method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ decision_id: decision, question, value }) });
  if (r.ok) openChain(chainID);
}

/* ---------- replay: a past decision chain, fired again on the brain ---------- */

// Each step fires when it happened, relative to the first: real timing, but
// a long wait is shortened and a short one stretched so every impulse shows.
const REPLAY_MIN_MS = 1700, REPLAY_MAX_MS = 4000;
let replayRun = 0;
async function replay(id) {
  const chain = await api("/api/chain/" + id);
  if (!chain.length) return;
  closeDrawer();
  if (view !== "mind") show("mind");
  $(".brain-card").scrollIntoView({ behavior: "smooth", block: "center" });
  const run = ++replayRun, first = chain[0], panel = $("#replay");
  panel.hidden = false;
  panel.classList.remove("done");
  $("#replayAgain").hidden = true;
  $("#replayWhen").textContent = fmtTime(first.at, true);
  $("#replayWhat").textContent = first.text || (["reminder", "check_in", "delivery"].includes(first.kind) ? "It spoke first" : actionText(first.action));
  $("#replaySteps").innerHTML = chain.map((e) => `<li style="--c:${FAMILY_COLOR[family(e)]}"><span>${esc(actionText(e.action))}</span>
    <em>${e.latency_ms >= 1000 ? (e.latency_ms / 1000).toFixed(1) + "s" : e.latency_ms + "ms"}</em>${e.reply ? `<span class="say">${esc(e.reply)}</span>` : ""}</li>`).join("");
  $("#replayBar").style.width = "0";
  const steps = [...$("#replaySteps").children];
  await new Promise((r) => setTimeout(r, 700)); // let the view settle first
  for (let i = 0; i < chain.length; i++) {
    if (run !== replayRun) return; // stopped, or another replay started
    const e = chain[i];
    brain.replay(e);
    steps.forEach((s, k) => { s.classList.toggle("on", k <= i); s.classList.toggle("now", k === i); });
    steps[i].scrollIntoView({ block: "nearest" });
    $("#replayBar").style.width = ((i + 1) / chain.length) * 100 + "%";
    const next = chain[i + 1];
    const gap = next ? new Date(next.at) - new Date(e.at) : e.latency_ms;
    await new Promise((r) => setTimeout(r, Math.min(REPLAY_MAX_MS, Math.max(REPLAY_MIN_MS, gap))));
  }
  if (run !== replayRun) return;
  steps.forEach((s) => s.classList.remove("now"));
  panel.classList.add("done");
  $("#replayAgain").hidden = false;
  $("#replayAgain").onclick = () => replay(id);
  if (location.hash !== "#replay/" + id) history.replaceState(null, "", "#replay/" + id);
}
$("#replayStop").onclick = () => { replayRun++; $("#replay").hidden = true; history.replaceState(null, "", "#" + view); };

function openDrawer(title, html) {
  $("#drawerTitle").textContent = title;
  $("#drawerBody").innerHTML = html;
  $("#drawer").classList.add("on"); $("#scrim").classList.add("on");
}
const closeDrawer = () => { $("#drawer").classList.remove("on"); $("#scrim").classList.remove("on"); };
$("#drawerClose").onclick = closeDrawer;
$("#scrim").onclick = closeDrawer;
document.addEventListener("keydown", (e) => e.key === "Escape" && closeDrawer());

/* ---------- navigation, clock, live stream ---------- */

const LOADERS = { mind: () => Promise.all([loadOverview(), loadFeed(), loadPlan(), loadApprovals()]), activity: loadLog, memory: loadMemory,
  day: loadDay, works: loadWorks, growth: loadGrowth, judgment: loadJudgment, talk: loadTalk };
let view = "mind";
function show(v) {
  view = v;
  document.querySelectorAll(".nav button").forEach((b) => b.classList.toggle("on", b.dataset.view === v));
  document.querySelectorAll(".view").forEach((s) => s.classList.toggle("on", s.id === "view-" + v));
  $("#viewTitle").textContent = { mind: "MIND", activity: "ACTIVITY", memory: "MEMORY", day: "DAY", works: "WORKS", growth: "GROWTH", judgment: "JUDGMENT", talk: "CONVERSATIONS" }[v];
  history.replaceState(null, "", "#" + v);
  LOADERS[v]().catch(console.error);
}
document.querySelectorAll(".nav button").forEach((b) => (b.onclick = () => show(b.dataset.view)));

let started = null;
setInterval(() => {
  try {
    $("#clock").textContent = new Intl.DateTimeFormat("en-GB", { timeZone: info.timezone, hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date());
  } catch {}
  if (started) $("#uptime").textContent = ago(Date.now() - started);
}, 1000);

let refresh = null;
function connect() {
  const es = new EventSource("/api/events");
  es.onopen = () => { $("#liveDot").classList.add("on"); $("#agentState").textContent = "awake · live"; };
  es.onerror = () => { $("#liveDot").classList.remove("on"); $("#agentState").textContent = "reconnecting"; };
  es.addEventListener("decision", (m) => {
    const e = JSON.parse(m.data);
    brain.fire(e);
    eeg.add(e);
    const feed = $("#feed");
    if (feed.querySelector(".empty")) feed.innerHTML = "";
    feed.prepend(sigItem(e, true));
    while (feed.children.length > 60) feed.lastChild.remove();
    clearTimeout(refresh);
    refresh = setTimeout(() => { loadOverview(); loadPlan(); loadApprovals(); if (view !== "mind") LOADERS[view](); }, 1200);
  });
}

// #replay/<decision id> opens the mind and replays that decision's chain: a link to share or record.
const start = (location.hash || "#mind").slice(1), replayID = start.startsWith("replay/") ? start.slice(7) : "";
loadOverview().then(() => {
  show(start in LOADERS ? start : "mind");
  if (replayID) replay(replayID).catch(console.error);
}).catch(console.error);
connect();
setInterval(() => { loadOverview().catch(() => {}); if (view === "mind") loadApprovals().catch(() => {}); }, 30000);
