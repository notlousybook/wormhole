/* Wormhole — shared helpers */
"use strict";

const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error?.message || `request failed (${res.status})`);
  }
  return res.json();
}

/* formatters */
const fmtMoney = (v) => v === 0 ? "Free" : "$" + v.toFixed(v < 0.01 ? 4 : 2);
const fmtNum = (v) => new Intl.NumberFormat().format(v ?? 0);
const fmtTimeAgo = (iso) => {
  if (!iso || iso.startsWith("0001")) return "never";
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
};
const escapeHtml = (s) =>
  String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/* toast */
let toastTimer;
function toast(msg) {
  let el = $("#toast");
  if (!el) {
    el = document.createElement("div");
    el.id = "toast";
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 2600);
}

/* copy */
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("Copied to clipboard");
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
    toast("Copied to clipboard");
  }
}

/* staggered reveal for .reveal elements */
function initReveals() {
  const els = $$(".reveal");
  els.forEach((el, i) => (el.style.transitionDelay = `${Math.min(i * 60, 500)}ms`));
  const io = new IntersectionObserver(
    (entries) => entries.forEach((e) => {
      if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); }
    }),
    { threshold: 0.06 }
  );
  els.forEach((el) => io.observe(el));
}

/* copy buttons inside .code blocks */
function initCopyButtons() {
  $$(".code").forEach((block) => {
    const btn = document.createElement("button");
    btn.className = "copy-btn";
    btn.textContent = "Copy";
    btn.addEventListener("click", () => {
      copyText(block.textContent.replace(/Copy$/, ""));
    });
    block.appendChild(btn);
  });
}

/* status dot for provider key state */
function providerDot(hasKey) {
  return hasKey ? '<span class="dot"></span>' : '<span class="dot warn"></span>';
}
function tagHtml(tag) {
  const cls = ["free", "paid", "local", "demo", "fast"].includes(tag) ? tag : "";
  return `<span class="tag ${cls}">${escapeHtml(tag)}</span>`;
}

document.addEventListener("DOMContentLoaded", () => {
  // mark active nav link
  const path = location.pathname;
  $$("nav a.link").forEach((a) => {
    const href = a.getAttribute("href");
    if (href === "/" ? path === "/" : path.startsWith(href)) {
      a.setAttribute("aria-current", "page");
    }
  });
  initReveals();
  initCopyButtons();
});
