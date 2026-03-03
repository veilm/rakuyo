const state = {
  roots: [],
  rootId: null,
  path: "",
};

const loginPanel = document.getElementById("loginPanel");
const browserPanel = document.getElementById("browserPanel");
const loginForm = document.getElementById("loginForm");
const loginError = document.getElementById("loginError");
const passwordInput = document.getElementById("passwordInput");
const rootSelect = document.getElementById("rootSelect");
const entriesEl = document.getElementById("entries");
const locationEl = document.getElementById("location");
const upBtn = document.getElementById("upBtn");
const logoutBtn = document.getElementById("logoutBtn");
const rowTemplate = document.getElementById("rowTemplate");
const viewerModal = document.getElementById("viewerModal");
const viewerBody = document.getElementById("viewerBody");
const viewerTitle = document.getElementById("viewerTitle");
const viewerClose = document.getElementById("viewerClose");

async function api(path, options = {}) {
  const res = await fetch(path, {
    credentials: "include",
    ...options,
  });
  return res;
}

function fmtSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

function fmtTime(ts) {
  if (!ts) return "";
  const d = new Date(ts);
  return d.toLocaleString();
}

function parentPath(p) {
  if (!p) return "";
  const parts = p.split("/").filter(Boolean);
  parts.pop();
  return parts.join("/");
}

function setVisible(isBrowser) {
  browserPanel.classList.toggle("hidden", !isBrowser);
  loginPanel.classList.toggle("hidden", isBrowser);
}

function setURLFromState(replace) {
  const qs = new URLSearchParams();
  if (state.rootId !== null) qs.set("root", String(state.rootId));
  if (state.path) qs.set("path", state.path);
  const query = qs.toString();
  const nextURL = query ? `${window.location.pathname}?${query}` : window.location.pathname;
  if (replace) {
    window.history.replaceState({ rootId: state.rootId, path: state.path }, "", nextURL);
  } else {
    window.history.pushState({ rootId: state.rootId, path: state.path }, "", nextURL);
  }
}

function applyStateFromURL() {
  const qs = new URLSearchParams(window.location.search);
  const root = qs.get("root");
  const p = qs.get("path");
  state.rootId = root === null ? null : Number(root);
  state.path = p || "";
}

function fileURL(entryPath) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath });
  return `/api/file?${q.toString()}`;
}

function clearViewer() {
  viewerBody.innerHTML = "";
}

function closeViewer() {
  clearViewer();
  viewerModal.classList.add("hidden");
}

function openViewer(entry) {
  const src = fileURL(entry.path);
  const mime = entry.mime || "";
  viewerTitle.textContent = entry.name;
  clearViewer();
  if (mime.startsWith("image/")) {
    const img = document.createElement("img");
    img.src = src;
    img.alt = entry.name;
    viewerBody.appendChild(img);
  } else if (mime.startsWith("video/")) {
    const video = document.createElement("video");
    video.src = src;
    video.controls = true;
    video.autoplay = true;
    viewerBody.appendChild(video);
  } else if (mime.startsWith("audio/")) {
    const audio = document.createElement("audio");
    audio.src = src;
    audio.controls = true;
    audio.autoplay = true;
    viewerBody.appendChild(audio);
  } else {
    const link = document.createElement("a");
    link.href = src;
    link.textContent = "Open file";
    link.target = "_blank";
    link.rel = "noopener";
    viewerBody.appendChild(link);
  }
  viewerModal.classList.remove("hidden");
}

async function loadRoots() {
  const res = await api("/api/roots");
  if (res.status === 401) {
    setVisible(false);
    return false;
  }
  if (!res.ok) {
    throw new Error(`roots failed: ${res.status}`);
  }
  const data = await res.json();
  state.roots = data.roots || [];

  rootSelect.innerHTML = "";
  state.roots.forEach((r) => {
    const opt = document.createElement("option");
    opt.value = String(r.id);
    opt.textContent = `${r.name} (${r.path})`;
    rootSelect.appendChild(opt);
  });

  if (state.roots.length === 0) {
    locationEl.textContent = "No roots configured";
    entriesEl.innerHTML = "";
    setVisible(true);
    return true;
  }

  if (state.rootId === null || Number.isNaN(state.rootId)) {
    state.rootId = state.roots[0].id;
  }
  if (!state.roots.some((r) => r.id === state.rootId)) {
    state.rootId = state.roots[0].id;
  }
  rootSelect.value = String(state.rootId);
  setVisible(true);
  return true;
}

async function loadList(options = {}) {
  if (state.rootId === null) return;
  const replaceURL = options.replaceURL ?? false;
  const pushURL = options.pushURL ?? false;
  const q = new URLSearchParams({ root: String(state.rootId), path: state.path });
  const res = await api(`/api/list?${q.toString()}`);
  if (res.status === 401) {
    setVisible(false);
    return;
  }
  if (!res.ok) {
    throw new Error(`list failed: ${res.status}`);
  }
  const data = await res.json();

  state.path = data.path || "";
  if (replaceURL || pushURL) {
    setURLFromState(replaceURL);
  }
  locationEl.textContent = `${data.rootPath}/${state.path}`.replace(/\/+/g, "/");
  upBtn.disabled = state.path === "";

  entriesEl.innerHTML = "";
  if (state.path !== "") {
    const tr = document.createElement("tr");
    tr.innerHTML = '<td colspan="4"><a href="#" id="upLink">..</a></td>';
    tr.querySelector("#upLink").addEventListener("click", (e) => {
      e.preventDefault();
      state.path = parentPath(state.path);
      loadList({ pushURL: true });
    });
    entriesEl.appendChild(tr);
  }

  for (const entry of data.entries) {
    const node = rowTemplate.content.firstElementChild.cloneNode(true);
    const link = node.querySelector(".entryLink");
    const thumb = node.querySelector(".thumb");

    link.textContent = entry.name;
    link.href = "#";
    link.addEventListener("click", (e) => {
      e.preventDefault();
      if (entry.isDir) {
        state.path = entry.path;
        loadList({ pushURL: true });
      } else {
        openViewer(entry);
      }
    });

    node.querySelector(".typeCell").textContent = entry.isDir ? "dir" : entry.mime || "file";
    node.querySelector(".sizeCell").textContent = entry.isDir ? "-" : fmtSize(entry.size);
    node.querySelector(".timeCell").textContent = fmtTime(entry.modTime);

    if (entry.thumb && !entry.isDir) {
      const tq = new URLSearchParams({ root: String(state.rootId), path: entry.path, size: "256" });
      thumb.src = `/api/thumb?${tq.toString()}`;
      thumb.onerror = () => thumb.classList.add("hidden");
    } else {
      thumb.classList.add("hidden");
    }

    entriesEl.appendChild(node);
  }
}

loginForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  loginError.textContent = "";
  const password = passwordInput.value;
  const res = await api("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });
  if (!res.ok) {
    loginError.textContent = "Invalid password";
    return;
  }
  passwordInput.value = "";
  const ok = await loadRoots();
  if (ok) {
    await loadList({ replaceURL: true });
  }
});

rootSelect.addEventListener("change", async () => {
  state.rootId = Number(rootSelect.value);
  state.path = "";
  await loadList({ pushURL: true });
});

upBtn.addEventListener("click", async () => {
  state.path = parentPath(state.path);
  await loadList({ pushURL: true });
});

logoutBtn.addEventListener("click", async () => {
  await api("/api/logout", { method: "POST" });
  setVisible(false);
});

viewerClose.addEventListener("click", () => closeViewer());
viewerModal.querySelector(".viewerBackdrop").addEventListener("click", () => closeViewer());
window.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !viewerModal.classList.contains("hidden")) {
    closeViewer();
  }
});
window.addEventListener("popstate", async () => {
  applyStateFromURL();
  await loadRoots();
  await loadList();
});

(async function init() {
  try {
    applyStateFromURL();
    const ok = await loadRoots();
    if (ok) {
      await loadList({ replaceURL: true });
    }
  } catch (err) {
    console.error(err);
    locationEl.textContent = "Failed to load";
  }
})();
