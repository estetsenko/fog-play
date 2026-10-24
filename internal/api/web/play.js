// Fog Play player: receives the host's stream over WebRTC and sends mouse
// and keyboard input back on the "input" data channel.
"use strict";

const $ = (id) => document.getElementById(id);
const video = $("video");

// The session and token arrive in the URL fragment; keep them for reloads
// and take them out of the address bar.
const frag = new URLSearchParams(location.hash.slice(1));
if (frag.get("s")) {
  try { sessionStorage.setItem("fogplay", JSON.stringify({ s: frag.get("s"), t: frag.get("t") })); } catch {}
  history.replaceState(null, "", location.pathname);
}
let saved = {};
try { saved = JSON.parse(sessionStorage.getItem("fogplay") || "{}"); } catch {}
const sid = frag.get("s") || saved.s;
const token = frag.get("t") || saved.t;

let ws, pc, dc, over = false, active = false;

function setStatus(text) { $("status").textContent = text; }

function api(method, path, body) {
  return fetch(path, {
    method,
    headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
    body: body && JSON.stringify(body),
  });
}

function finish(text) {
  over = true;
  setStatus(text);
  $("stats").textContent = "";
  if (pc) pc.close();
  if (ws) ws.close();
  if (document.fullscreenElement) document.exitFullscreen();
  video.srcObject = null;
}

async function checkSession() {
  const r = await api("GET", "/v1/sessions/" + sid);
  if (!r.ok) return finish("Session not found");
  const s = await r.json();
  if (s.state === "ended") finish("Session ended");
  else if (s.state === "failed") finish("Session failed: " + (s.failure_reason || "unknown error"));
  return s;
}

function connect() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  ws = new WebSocket(`${proto}://${location.host}/v1/sessions/${sid}/signal?access_token=${encodeURIComponent(token)}`);
  ws.onopen = () => ws.send(JSON.stringify({ type: "hello" }));
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.type === "offer") answer(m.data.sdp).catch((err) => setStatus("Connection failed: " + err));
  };
  ws.onclose = async () => {
    if (over) return;
    await checkSession().catch(() => {});
    if (!over) setTimeout(connect, 2000);
  };
}

function iceGathered(pc) {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const done = () => { if (pc.iceGatheringState === "complete") resolve(); };
    pc.addEventListener("icegatheringstatechange", done);
    setTimeout(resolve, 3000);
  });
}

async function answer(sdp) {
  if (pc) pc.close();
  setStatus("Connecting to host…");
  pc = new RTCPeerConnection();
  pc.ontrack = (e) => {
    // Show frames as soon as they arrive rather than buffering for smoothness.
    try { e.receiver.jitterBufferTarget = 0; } catch {}
    try { e.receiver.playoutDelayHint = 0; } catch {}
    video.srcObject = new MediaStream([e.track]);
    video.play().catch(() => {});
  };
  pc.ondatachannel = (e) => { dc = e.channel; };
  pc.onconnectionstatechange = () => {
    if (over) return;
    const st = pc.connectionState;
    if (st === "connected") {
      setStatus("Playing");
      if (!active) {
        active = true;
        api("PUT", "/v1/sessions/" + sid + "/state", { state: "active" });
      }
    } else if (st === "failed" || st === "disconnected") {
      setStatus("Connection lost, retrying…");
    }
  };
  await pc.setRemoteDescription({ type: "offer", sdp });
  await pc.setLocalDescription(await pc.createAnswer());
  await iceGathered(pc);
  ws.send(JSON.stringify({ type: "answer", data: { sdp: pc.localDescription.sdp } }));
}

// Stats line: frame rate, bitrate, round trip and frame delay.
let lastBytes = 0, lastTime = 0;
setInterval(async () => {
  if (!pc || over || pc.connectionState !== "connected") return;
  const report = await pc.getStats();
  let fps = 0, bytes = 0, rtt, delay, res;
  report.forEach((s) => {
    if (s.type === "inbound-rtp" && s.kind === "video") {
      fps = s.framesPerSecond || 0;
      bytes = s.bytesReceived;
      if (s.frameWidth) res = s.frameWidth + "×" + s.frameHeight;
      if (s.jitterBufferEmittedCount) delay = (1000 * s.jitterBufferDelay) / s.jitterBufferEmittedCount;
    }
    if (s.type === "candidate-pair" && s.nominated && s.currentRoundTripTime !== undefined) rtt = s.currentRoundTripTime * 1000;
  });
  const now = performance.now();
  const mbps = lastTime ? ((bytes - lastBytes) * 8) / ((now - lastTime) * 1000) : 0;
  lastBytes = bytes; lastTime = now;
  const parts = [res, Math.round(fps) + " fps", mbps.toFixed(1) + " Mbps"];
  if (rtt !== undefined) parts.push("rtt " + Math.round(rtt) + " ms");
  if (delay !== undefined) parts.push("buffer " + Math.round(delay) + " ms");
  $("stats").textContent = parts.filter(Boolean).join(" · ");
}, 1000);

// Input. Without pointer lock the mouse is absolute (desktop use); in game
// mode it is locked and relative, which games expect.
function send(e) { if (dc && dc.readyState === "open") dc.send(JSON.stringify(e)); }
const locked = () => document.pointerLockElement === video;

function toScreen(e) {
  const r = video.getBoundingClientRect();
  const vw = video.videoWidth, vh = video.videoHeight;
  if (!vw || !vh) return null;
  const scale = Math.min(r.width / vw, r.height / vh);
  const w = vw * scale, h = vh * scale;
  const x = (e.clientX - r.left - (r.width - w) / 2) / w;
  const y = (e.clientY - r.top - (r.height - h) / 2) / h;
  return x < 0 || x > 1 || y < 0 || y > 1 ? null : { x, y };
}

video.addEventListener("mousemove", (e) => {
  if (locked()) {
    send({ t: "mm", x: e.movementX, y: e.movementY });
    return;
  }
  const p = toScreen(e);
  if (p) send({ t: "ma", x: p.x, y: p.y });
});
video.addEventListener("mousedown", (e) => {
  e.preventDefault();
  video.focus();
  if (!locked()) { const p = toScreen(e); if (!p) return; send({ t: "ma", x: p.x, y: p.y }); }
  send({ t: "md", b: e.button });
});
video.addEventListener("mouseup", (e) => { e.preventDefault(); send({ t: "mu", b: e.button }); });
video.addEventListener("contextmenu", (e) => e.preventDefault());
video.addEventListener("wheel", (e) => {
  e.preventDefault();
  send({ t: "wh", y: -Math.sign(e.deltaY), x: Math.sign(e.deltaX) });
}, { passive: false });

const down = new Set();
function key(e, type) {
  if (over || !dc || e.target.tagName === "BUTTON") return;
  e.preventDefault();
  if (type === "kd") down.add(e.code); else down.delete(e.code);
  send({ t: type, c: e.code });
}
window.addEventListener("keydown", (e) => key(e, "kd"));
window.addEventListener("keyup", (e) => key(e, "ku"));
// Release held keys when the tab loses focus, so none stay stuck on the host.
window.addEventListener("blur", () => { down.forEach((c) => send({ t: "ku", c })); down.clear(); });

$("game").onclick = async () => {
  try {
    await document.documentElement.requestFullscreen();
    if (navigator.keyboard && navigator.keyboard.lock) await navigator.keyboard.lock();
  } catch {}
  video.requestPointerLock();
  video.focus();
};
$("end").onclick = async () => {
  await api("DELETE", "/v1/sessions/" + sid).catch(() => {});
  finish("Session ended");
};

if (!sid || !token) {
  finish("Start a session with: fogplay play GAME");
} else {
  checkSession().then(() => { if (!over) connect(); }).catch(() => finish("Cannot reach the server"));
}
