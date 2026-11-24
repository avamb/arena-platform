// Live view of a running staging-hype load test (Node 22, no npm deps).
//   node ops/loadtest/live-view.mjs   then open http://localhost:4319/
// Env: SESSION_ID (required), RUN (jsonl path or run name, default newest results/hype-*.jsonl),
//      PORT (4319), API_BASE (default staging; only "staging"/localhost hosts accepted).
// Uses only public endpoints (schema, seat-status) and the local JSONL. No tokens.
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const SESSION_ID = process.env.SESSION_ID || "";
if (!SESSION_ID) {
  console.error("live-view: set SESSION_ID (the session id of the staging-hype run)");
  process.exit(2);
}
const PORT = Number(process.env.PORT || 4319);
const API_BASE = (process.env.API_BASE || "https://api-staging.arenasoldout.com").replace(/\/+$/, "");
const API_HOST = (() => { try { return new URL(API_BASE).hostname; } catch { return ""; } })();
if (!(API_HOST.includes("staging") || API_HOST === "localhost" || API_HOST === "127.0.0.1")) {
  console.error("refusing API_BASE without staging/localhost: " + API_BASE);
  process.exit(2);
}
const dir = path.join(path.dirname(fileURLToPath(import.meta.url)), "results");
function pickRun() {
  const r = process.env.RUN;
  if (r) {
    for (const c of [r, path.join(dir, r), path.join(dir, r + ".jsonl"), path.join(dir, "hype-" + r + ".jsonl")]) {
      if (fs.existsSync(c) && fs.statSync(c).isFile()) return c;
    }
    console.error("RUN not found: " + r);
    process.exit(2);
  }
  const files = fs.existsSync(dir) ? fs.readdirSync(dir).filter((f) => /^hype-.*\.jsonl$/.test(f)) : [];
  if (!files.length) { console.error("no hype-*.jsonl in " + dir); process.exit(2); }
  files.sort((a, b) => fs.statSync(path.join(dir, b)).mtimeMs - fs.statSync(path.join(dir, a)).mtimeMs);
  return path.join(dir, files[0]);
}
const RUN_FILE = pickRun();

// ---- upstream: schema (once) and seat-status (every 2 s)
let schemaBody = null, schemaPromise = null;
async function getSchema() {
  if (schemaBody) return schemaBody;
  schemaPromise ||= fetch(`${API_BASE}/v1/event-sessions/${SESSION_ID}/schema`)
    .then(async (r) => { if (!r.ok) throw new Error("schema " + r.status); schemaBody = await r.text(); return schemaBody; })
    .finally(() => { schemaPromise = null; });
  return schemaPromise;
}
let status = { status_version: 0, seats: {}, error: "no data yet" };
async function pollStatus() {
  try {
    const r = await fetch(`${API_BASE}/v1/event-sessions/${SESSION_ID}/seat-status`);
    if (!r.ok) throw new Error("seat-status " + r.status);
    const j = await r.json();
    status = { status_version: j.status_version, seats: j.seats || {}, at: Date.now() };
  } catch (e) {
    status = { ...status, error: String(e.message || e) };
  }
}
setInterval(pollStatus, 2000);
pollStatus();

// ---- run file, read incrementally
let offset = 0, carry = "";
const agg = { reqCount: 0, c409: 0, c429: 0, c5xx: 0, transport: 0, maxT: 0, buyersDone: 0, outcomes: {} };
const recent = []; // {t, name, ms}, last ~60 s of run time
function ingest(line) {
  let o; try { o = JSON.parse(line); } catch { return; }
  if (o.type === "req") {
    agg.reqCount++;
    if (o.status === 409) agg.c409++;
    else if (o.status === 429) agg.c429++;
    else if (o.status >= 500) agg.c5xx++;
    else if (o.status === 0) agg.transport++;
    if (typeof o.t === "number") { if (o.t > agg.maxT) agg.maxT = o.t; recent.push({ t: o.t, name: o.name, ms: o.ms }); }
  } else if (o.type === "buyer") {
    agg.buyersDone++;
    const k = o.outcome || "?";
    agg.outcomes[k] = (agg.outcomes[k] || 0) + 1;
    if (typeof o.tEnd === "number" && o.tEnd > agg.maxT) agg.maxT = o.tEnd;
  }
}
function readRun() {
  let size; try { size = fs.statSync(RUN_FILE).size; } catch { return; }
  if (size < offset) { offset = 0; carry = ""; }
  if (size === offset) return;
  const fd = fs.openSync(RUN_FILE, "r");
  try {
    const buf = Buffer.alloc(size - offset);
    fs.readSync(fd, buf, 0, buf.length, offset);
    offset = size;
    const parts = (carry + buf.toString("utf8")).split("\n");
    carry = parts.pop();
    for (const p of parts) if (p.trim()) ingest(p);
  } finally { fs.closeSync(fd); }
  const cut = agg.maxT - 60000;
  let i = 0; while (i < recent.length && recent[i].t < cut) i++;
  if (i) recent.splice(0, i);
}
function runJson() {
  readRun();
  const t = agg.maxT;
  const rps = recent.filter((r) => r.t > t - 10000).length / 10;
  const cs = recent.filter((r) => r.name === "checkout_start" && r.t > t - 30000).map((r) => r.ms).sort((a, b) => a - b);
  const p95 = cs.length ? cs[Math.min(cs.length - 1, Math.ceil(cs.length * 0.95) - 1)] : null;
  return { file: path.basename(RUN_FILE), elapsedMs: t, rps, checkoutStartP95: p95, checkoutStartN: cs.length,
    requests: agg.reqCount, c409: agg.c409, c429: agg.c429, c5xx: agg.c5xx, transport: agg.transport,
    buyersDone: agg.buyersDone, outcomes: agg.outcomes };
}

const PAGE = String.raw`<!doctype html><html lang="ru"><meta charset="utf-8"><title>Нагрузочный тест: живой вид</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
*{box-sizing:border-box}body{margin:0;background:#12151c;color:#e6e9ef;font:15px system-ui,sans-serif;display:flex;height:100vh}
#hall{flex:1 1 58%;min-width:0;padding:8px}#hall svg{width:100%;height:100%;background:#0b0d12;border-radius:8px}
#side{flex:0 0 42%;max-width:560px;padding:12px 16px;overflow:auto}
h1{font-size:16px;margin:0 0 8px;color:#9aa4b8;font-weight:600}
.big{display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-bottom:10px}
.card{background:#1b2030;border-radius:8px;padding:8px 10px}.card b{display:block;font-size:30px;line-height:1.1;font-variant-numeric:tabular-nums}
.card span{color:#9aa4b8;font-size:12px}
.free b{color:#7ee08a}.held b{color:#ffa43a}.sold b{color:#ff5b5b}
.grid{display:grid;grid-template-columns:repeat(2,1fr);gap:8px;margin-bottom:10px}.grid .card b{font-size:22px}
.out{font-size:13px;color:#c5cbd8;margin:6px 0 10px;line-height:1.7}.out i{font-style:normal;background:#252c40;border-radius:10px;padding:2px 8px;margin-right:4px;white-space:nowrap}
canvas{width:100%;height:170px;background:#0b0d12;border-radius:8px}
#msg{color:#ff8a8a;font-size:12px;min-height:16px}
@keyframes pulse{0%{stroke:#fff;stroke-width:6}100%{stroke:none;stroke-width:0}}
.pulse{animation:pulse 1.2s ease-out}
</style>
<div id="hall"><svg id="svg" preserveAspectRatio="xMidYMid meet"></svg></div>
<div id="side"><h1 id="title">Загрузка схемы…</h1>
<div class="big"><div class="card free"><b id="free">-</b><span>свободно</span></div><div class="card held"><b id="held">-</b><span>в брони</span></div><div class="card sold"><b id="sold">-</b><span>продано</span></div></div>
<div class="grid">
<div class="card"><b id="elapsed">-</b><span>прошло времени</span></div>
<div class="card"><b id="done">-</b><span>покупателей завершили</span></div>
<div class="card"><b id="rps">-</b><span>запросов/с (10 с)</span></div>
<div class="card"><b id="p95">-</b><span>checkout_start p95 (30 с)</span></div>
<div class="card"><b id="e409">-</b><span>409 за весь тест</span></div>
<div class="card"><b id="e429">-</b><span>429 за весь тест</span></div>
<div class="card"><b id="e5xx">-</b><span>5xx за весь тест</span></div>
<div class="card"><b id="e0">-</b><span>сетевые ошибки (0)</span></div></div>
<div class="out" id="out"></div>
<canvas id="chart" width="520" height="170"></canvas>
<div style="font-size:12px;color:#9aa4b8;margin-top:4px"><span style="color:#ff5b5b">■</span> продано &nbsp;<span style="color:#ffa43a">■</span> в брони</div>
<div id="msg"></div></div>
<script>
const NS="http://www.w3.org/2000/svg",$=id=>document.getElementById(id);
const OUTRU={paid:"оплатили",sold_out:"не хватило мест",abandoned:"отказались",silent:"молчат",failed:"ошибка"};
let circles={},prev={},cats={},total=0,hist=[];
function fmtT(ms){const s=Math.floor(ms/1000);return Math.floor(s/60)+":"+String(s%60).padStart(2,"0")}
function colorFor(st){
  if(st==="held")return"#ffa43a";if(st==="sold")return"#c0262d";if(st==="available")return null;return"#5a5f6b"}
function catFree(i){return cats[i]||"#7ee08a"}
async function init(){
  const r=await fetch("/api/schema");const s=await r.json();const g=s.geometry;
  (g.categories||[]).forEach(c=>cats[c.index]=c.color);
  const svg=$("svg");let minX=1e9,minY=1e9,maxX=-1e9,maxY=-1e9;const seats=[];
  for(const sec of g.sections||[])for(const row of sec.rows||[])for(const st of row.seats||[]){
    seats.push(st);const rr=st.radius||10;minX=Math.min(minX,st.x-rr);maxX=Math.max(maxX,st.x+rr);minY=Math.min(minY,st.y-rr);maxY=Math.max(maxY,st.y+rr)}
  let vb;const cv=g.canvas||{};
  if(cv.width&&cv.height)vb=[0,0,cv.width,cv.height];else vb=[minX-20,minY-20,maxX-minX+40,maxY-minY+40];
  svg.setAttribute("viewBox",vb.join(" "));
  if(g.decor_svg){const d=document.createElementNS(NS,"g");d.setAttribute("opacity","0.35");
    const t=String(g.decor_svg).replace(/&#xA;/g,"\n").replace(/<(\/?)svg:/g,"<$1");
    try{const doc=new DOMParser().parseFromString('<svg xmlns="'+NS+'">'+t+'</svg>',"image/svg+xml");
      if(!doc.querySelector("parsererror"))for(const n of [...doc.documentElement.childNodes])d.appendChild(document.importNode(n,true))}catch(e){}
    svg.appendChild(d)}
  const grp=document.createElementNS(NS,"g");svg.appendChild(grp);
  for(const st of seats){const c=document.createElementNS(NS,"circle");
    c.setAttribute("cx",st.x);c.setAttribute("cy",st.y);c.setAttribute("r",st.radius||10);
    c.dataset.cat=st.category_index;c.setAttribute("fill",catFree(st.category_index));c.setAttribute("fill-opacity","0.55");
    grp.appendChild(c);circles[st.key]=c}
  total=seats.length;$("title").textContent="Зал: "+total+" мест, сеанс "+(s.session_id||"").slice(0,8);
}
function applyStatus(j){
  if(!j.at)return;const seats=j.seats||{};let held=0,sold=0,unav=0;
  for(const k in seats){const v=seats[k];if(v==="held")held++;else if(v==="sold")sold++;else if(v!=="available")unav++}
  for(const k in circles){const c=circles[k];const v=seats[k]||"available";const col=colorFor(v);
    if(prev[k]!==v){c.setAttribute("fill",col||catFree(c.dataset.cat));c.setAttribute("fill-opacity",col?1:0.55);
      if(prev[k]!==undefined){c.classList.remove("pulse");void c.getBoundingClientRect();c.classList.add("pulse")}
      prev[k]=v}}
  $("held").textContent=held;$("sold").textContent=sold;$("free").textContent=Math.max(0,total-held-sold-unav);
  hist.push({held,sold});if(hist.length>1500)hist.shift();drawChart();
}
function drawChart(){const cv=$("chart"),x=cv.getContext("2d"),W=cv.width,H=cv.height;x.clearRect(0,0,W,H);
  const mx=Math.max(total,1),n=hist.length;if(n<2)return;
  for(const [k,col] of [["sold","#ff5b5b"],["held","#ffa43a"]]){x.strokeStyle=col;x.lineWidth=2;x.beginPath();
    hist.forEach((p,i)=>{const px=i/(n-1)*(W-4)+2,py=H-4-p[k]/mx*(H-8);i?x.lineTo(px,py):x.moveTo(px,py)});x.stroke()}}
function applyRun(r){
  $("elapsed").textContent=fmtT(r.elapsedMs);$("done").textContent=r.buyersDone;$("rps").textContent=r.rps.toFixed(1);
  $("p95").textContent=r.checkoutStartP95==null?"-":r.checkoutStartP95+" мс";
  $("e409").textContent=r.c409;$("e429").textContent=r.c429;$("e5xx").textContent=r.c5xx;$("e0").textContent=r.transport;
  $("out").innerHTML=Object.entries(r.outcomes).sort((a,b)=>b[1]-a[1]).map(([k,v])=>"<i>"+(OUTRU[k]||k)+": <b>"+v+"</b></i>").join("")}
async function tick(){
  try{const [a,b]=await Promise.all([fetch("/api/status").then(r=>r.json()),fetch("/api/run").then(r=>r.json())]);
    applyStatus(a);applyRun(b);$("msg").textContent=a.error||""}catch(e){$("msg").textContent=String(e)}}
init().then(()=>{tick();setInterval(tick,2000)}).catch(e=>$("msg").textContent="схема: "+e);
</script></html>`;

http.createServer(async (req, res) => {
  const url = req.url.split("?")[0];
  const send = (code, type, body) => { res.writeHead(code, { "content-type": type, "cache-control": "no-store" }); res.end(body); };
  try {
    if (url === "/") return send(200, "text/html; charset=utf-8", PAGE);
    if (url === "/api/schema") return send(200, "application/json", await getSchema());
    if (url === "/api/status") return send(200, "application/json", JSON.stringify(status));
    if (url === "/api/run") return send(200, "application/json", JSON.stringify(runJson()));
    send(404, "text/plain", "not found");
  } catch (e) { send(502, "application/json", JSON.stringify({ error: String(e.message || e) })); }
}).listen(PORT, "127.0.0.1", () => console.log(`live view: http://localhost:${PORT}/  run=${path.basename(RUN_FILE)} api=${API_BASE}`));
