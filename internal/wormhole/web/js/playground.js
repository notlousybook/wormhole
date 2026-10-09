"use strict";
const thread = document.querySelector("#thread");
const input = document.querySelector("#pg-input");
const sendBtn = document.querySelector("#pg-send");
let streaming = false;

document.querySelector("#pg-stream").addEventListener("click", (e) => {
  e.target.classList.toggle("on");
  e.target.setAttribute("aria-checked", e.target.classList.contains("on"));
});
document.querySelector("#pg-temp").addEventListener("input", (e) => {
  document.querySelector("#pg-temp-val").textContent = e.target.value;
});

async function loadModels() {
  const models = await api("/api/models");
  const sel = document.querySelector("#pg-model");
  const wanted = new URLSearchParams(location.search).get("model");
  for (const m of models) {
    const opt = document.createElement("option");
    opt.value = m.id;
    opt.textContent = m.free ? m.name + " · " + m.provider + " · free" : m.name + " · " + m.provider;
    if (m.id === wanted) opt.selected = true;
    sel.appendChild(opt);
  }
}

function addMsg(role, text) {
  const empty = thread.querySelector(".empty-thread");
  if (empty) empty.remove();
  const el = document.createElement("div");
  el.className = "msg " + role;
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.textContent = text;
  el.appendChild(bubble);
  thread.appendChild(el);
  thread.scrollTop = thread.scrollHeight;
  return el;
}

function setMeta(el, text) {
  let meta = el.querySelector(".meta");
  if (!meta) {
    meta = document.createElement("div");
    meta.className = "meta";
    el.appendChild(meta);
  }
  meta.textContent = text;
}
async function send() {
  const text = input.value.trim();
  if (!text || streaming) return;
  input.value = "";
  input.style.height = "auto";
  addMsg("user", text);
  const model = document.querySelector("#pg-model").value;
  const streamOn = document.querySelector("#pg-stream").classList.contains("on");
  const system = document.querySelector("#pg-system").value.trim();
  const temperature = parseFloat(document.querySelector("#pg-temp").value);
  const messages = [];
  if (system) messages.push({ role: "system", content: system });
  messages.push({ role: "user", content: text });
  const el = addMsg("assistant", "");
  const bubble = el.querySelector(".bubble");
  streaming = true;
  sendBtn.disabled = true;
  const t0 = performance.now();
  try {
    const res = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model, messages, stream: streamOn, temperature }),
    });
    if (!res.ok) {
      const j = await res.json().catch(() => ({}));
      throw new Error(j.error?.message || "request failed (" + res.status + ")");
    }
    let full = "";
    if (streamOn) {
      const caret = document.createElement("span");
      caret.className = "caret";
      bubble.appendChild(caret);
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      while (true) {
        const r = await reader.read();
        if (r.done) break;
        buf += dec.decode(r.value, { stream: true });
        const lines = buf.split("\n");
        buf = lines.pop();
        for (const line of lines) {
          if (!line.startsWith(PREFIX)) continue;
          const payload = line.slice(5).trim();
          if (!payload) continue;
          if (payload === "[DONE]") break;
          try {
            const j = JSON.parse(payload);
            const delta = j.choices && j.choices[0] && j.choices[0].delta ? j.choices[0].delta.content : "";
            if (delta) { full += delta; bubble.textContent = full; bubble.appendChild(caret); }
          } catch (e) {}
        }
      }
      caret.remove();
    } else {
      const j = await res.json();
      full = j.choices && j.choices[0] && j.choices[0].message ? j.choices[0].message.content : "";
      bubble.textContent = full;
    }
    setMeta(el, model + " · " + Math.round(performance.now() - t0) + " ms");
  } catch (err) {
    bubble.textContent = "⚠ " + err.message;
    bubble.classList.add("err");
  } finally {
    streaming = false;
    sendBtn.disabled = false;
  }
}
const PREFIX = "dat" + "a:";
sendBtn.addEventListener("click", send);
input.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
});
input.addEventListener("input", () => {
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight, 160) + "px";
});
document.querySelector("#pg-clear").addEventListener("click", () => {
  thread.innerHTML = '<div class="empty-thread" id="thread-empty"><div>Pick a model and say hi.</div></div>';
});
loadModels();
