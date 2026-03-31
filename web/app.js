const state = {
  roots: [],
  rootId: null,
  path: "",
  entries: [],
  rootPath: "",
};

const settings = {
  viewMode: "list",
  theme: "light",
  gridSize: 200,
};

const loginPanel = document.getElementById("loginPanel");
const browserPanel = document.getElementById("browserPanel");
const loginForm = document.getElementById("loginForm");
const loginError = document.getElementById("loginError");
const passwordInput = document.getElementById("passwordInput");
const rootSelect = document.getElementById("rootSelect");
const entriesEl = document.getElementById("entries");
const galleryEntriesEl = document.getElementById("galleryEntries");
const listWrap = document.getElementById("listWrap");
const galleryWrap = document.getElementById("galleryWrap");
const locationEl = document.getElementById("location");
const upBtn = document.getElementById("upBtn");
const logoutBtn = document.getElementById("logoutBtn");
const rowTemplate = document.getElementById("rowTemplate");
const viewModeSelect = document.getElementById("viewModeSelect");
const themeSelect = document.getElementById("themeSelect");
const gridSizeInput = document.getElementById("gridSizeInput");
const gridSizeValue = document.getElementById("gridSizeValue");
const gridSizeCtl = document.querySelector(".gridSizeCtl");
const viewerModal = document.getElementById("viewerModal");
const viewerBody = document.getElementById("viewerBody");
const viewerTitle = document.getElementById("viewerTitle");
const viewerClose = document.getElementById("viewerClose");
const viewerNoteForm = document.getElementById("viewerNoteForm");
const viewerNoteInput = document.getElementById("viewerNoteInput");
const viewerNoteStatus = document.getElementById("viewerNoteStatus");
let thumbObserver = null;
const thumbLoader = {
  pauseUntil: 0,
};
let viewerEntry = null;

const folderSVG =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>';

async function api(path, options = {}) {
  const res = await fetch(path, {
    credentials: "include",
    ...options,
  });
  return res;
}

function getCookie(name) {
  const wanted = `${name}=`;
  for (const part of document.cookie.split(";")) {
    const v = part.trim();
    if (v.startsWith(wanted)) {
      return decodeURIComponent(v.slice(wanted.length));
    }
  }
  return "";
}

function setCookie(name, value) {
  document.cookie = `${name}=${encodeURIComponent(value)}; Path=/; Max-Age=31536000; SameSite=Lax`;
}

function clamp(n, min, max) {
  return Math.min(max, Math.max(min, n));
}

function loadSettingsFromCookies() {
  const viewMode = getCookie("rakuyo_view_mode");
  const theme = getCookie("rakuyo_theme");
  const gridSizeRaw = parseInt(getCookie("rakuyo_grid_size"), 10);

  if (viewMode === "gallery" || viewMode === "list") {
    settings.viewMode = viewMode;
  }
  if (theme === "dark" || theme === "light") {
    settings.theme = theme;
  }
  if (Number.isFinite(gridSizeRaw)) {
    settings.gridSize = clamp(gridSizeRaw, 60, 320);
  }
}

function applyTheme() {
  document.body.dataset.theme = settings.theme;
  themeSelect.value = settings.theme;
}

function applyViewMode() {
  const gallery = settings.viewMode === "gallery";
  viewModeSelect.value = settings.viewMode;
  listWrap.classList.toggle("hidden", gallery);
  galleryWrap.classList.toggle("hidden", !gallery);
  gridSizeCtl.classList.toggle("hidden", !gallery);
}

function applyGridSize() {
  galleryEntriesEl.style.setProperty("--grid-size", `${settings.gridSize}px`);
  gridSizeInput.value = String(settings.gridSize);
  gridSizeValue.textContent = `${settings.gridSize}`;
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

function mkvProbeURL(entryPath) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath });
  return `/api/mkv/probe?${q.toString()}`;
}

function videoProbeURL(entryPath) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath });
  return `/api/video/probe?${q.toString()}`;
}

function videoPlayURL(entryPath, audioIndex) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath });
  if (audioIndex !== "" && audioIndex !== null && audioIndex !== undefined) {
    q.set("audio", String(audioIndex));
  }
  return `/api/video/play?${q.toString()}`;
}

function mkvPlayURL(entryPath, audioIndex) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath, audio: String(audioIndex) });
  return `/api/mkv/play?${q.toString()}`;
}

function mkvSubURL(entryPath, subIndex) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath, sub: String(subIndex) });
  return `/api/mkv/sub?${q.toString()}`;
}

function thumbURL(entryPath, size) {
  const q = new URLSearchParams({ root: String(state.rootId), path: entryPath, size: String(size) });
  return `/api/thumb?${q.toString()}`;
}

function browseURL(rootId, entryPath) {
  const q = new URLSearchParams({ root: String(rootId) });
  if (entryPath) q.set("path", entryPath);
  const query = q.toString();
  return query ? `${window.location.pathname}?${query}` : window.location.pathname;
}

function clearViewer() {
  viewerBody.innerHTML = "";
}

function isViewerNoteEntry(entry) {
  if (!entry || entry.isDir) {
    return false;
  }
  const mime = (entry.mime || "").toLowerCase();
  if (mime.startsWith("image/") || mime.startsWith("video/")) {
    return true;
  }
  const name = (entry.name || "").toLowerCase();
  return (
    name.endsWith(".jpg") ||
    name.endsWith(".jpeg") ||
    name.endsWith(".png") ||
    name.endsWith(".gif") ||
    name.endsWith(".webp") ||
    name.endsWith(".bmp") ||
    name.endsWith(".mp4") ||
    name.endsWith(".mkv") ||
    name.endsWith(".webm") ||
    name.endsWith(".mov") ||
    name.endsWith(".avi") ||
    name.endsWith(".m4v") ||
    name.endsWith(".ts")
  );
}

function resetViewerNoteBar() {
  viewerEntry = null;
  viewerNoteForm.classList.add("hidden");
  viewerNoteInput.value = "";
  viewerNoteInput.disabled = false;
  viewerNoteInput.placeholder = "Add note...";
  viewerNoteStatus.textContent = "";
}

function setupViewerNoteBar(entry) {
  if (!isViewerNoteEntry(entry)) {
    resetViewerNoteBar();
    return;
  }
  viewerEntry = entry;
  viewerNoteForm.classList.remove("hidden");
  viewerNoteInput.value = "";
  viewerNoteInput.disabled = false;
  viewerNoteInput.placeholder = "Add note...";
  viewerNoteStatus.textContent = "";
}

function closeViewer() {
  clearViewer();
  resetViewerNoteBar();
  viewerModal.classList.add("hidden");
}

function openViewer(entry) {
  setupViewerNoteBar(entry);
  if (entry.name.toLowerCase().endsWith(".mkv")) {
    openMKVViewer(entry);
    return;
  }

  if ((entry.mime || "").startsWith("video/")) {
    openVideoViewer(entry);
    return;
  }

  const src = fileURL(entry.path);
  const mime = entry.mime || "";
  viewerTitle.textContent = entry.name;
  clearViewer();
  if (mime.startsWith("image/")) {
    const img = document.createElement("img");
    img.src = src;
    img.alt = entry.name;
    viewerBody.appendChild(img);
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

function pauseThumbLoading(ms) {
  thumbLoader.pauseUntil = Math.max(thumbLoader.pauseUntil, Date.now() + ms);
}

function trackLabel(track) {
  const parts = [];
  if (track.language) parts.push(track.language);
  if (track.title) parts.push(track.title);
  if (!track.language && !track.title) parts.push(`track ${track.index}`);
  if (track.default) parts.push("default");
  if (track.forced) parts.push("forced");
  return `${parts.join(" - ")} [${track.codec}]`;
}

function canPlayMP4Codec(videoCodec) {
  const codec = (videoCodec || "").toLowerCase();
  const v = document.createElement("video");
  if (codec === "h264") {
    return v.canPlayType('video/mp4; codecs="avc1.42E01E,mp4a.40.2"') !== "";
  }
  if (codec === "hevc" || codec === "h265") {
    return v.canPlayType('video/mp4; codecs="hvc1,mp4a.40.2"') !== "";
  }
  return false;
}

function fallbackVideoViewer(entry, message) {
  viewerTitle.textContent = entry.name;
  clearViewer();
  const video = document.createElement("video");
  video.src = fileURL(entry.path);
  video.controls = true;
  video.autoplay = true;
  video.preload = "metadata";
  viewerBody.appendChild(video);
  if (message) {
    const hint = document.createElement("div");
    hint.style.fontSize = "0.8rem";
    hint.style.opacity = "0.85";
    hint.textContent = message;
    viewerBody.appendChild(hint);
  }
  viewerModal.classList.remove("hidden");
}

async function openVideoViewer(entry) {
  viewerTitle.textContent = entry.name;
  clearViewer();
  viewerModal.classList.remove("hidden");
  viewerBody.textContent = "Inspecting video...";

  try {
    const probeRes = await api(videoProbeURL(entry.path));
    if (!probeRes.ok) {
      throw new Error(`probe failed: ${probeRes.status}`);
    }
    const probe = await probeRes.json();
    const videoTrack = probe.video || {};
    const audioTracks = probe.audio || [];
    const remuxSupported = Boolean(probe.remuxSupported);
    const remuxRecommended = Boolean(probe.remuxRecommended);
    const nativeLikely = probe.nativeLikely !== false;
    const formatLabel = probe.formatLongName || probe.formatName || "unknown container";
    const remuxLikelyPlayable = remuxSupported && canPlayMP4Codec(videoTrack.codec);

    viewerBody.innerHTML = "";
    const controls = document.createElement("div");
    controls.className = "mkvControls";

    let audioSelect = null;
    if (audioTracks.length > 0) {
      const audioLabel = document.createElement("label");
      audioLabel.textContent = "Audio";
      audioSelect = document.createElement("select");
      for (const a of audioTracks) {
        const opt = document.createElement("option");
        opt.value = String(a.index);
        opt.textContent = trackLabel(a);
        audioSelect.appendChild(opt);
        if (a.default) {
          audioSelect.value = String(a.index);
        }
      }
      if (!audioSelect.value && audioTracks[0]) {
        audioSelect.value = String(audioTracks[0].index);
      }
      audioLabel.appendChild(audioSelect);
      controls.appendChild(audioLabel);
    }

    const modeLabel = document.createElement("label");
    modeLabel.textContent = "Playback";
    const modeSelect = document.createElement("select");
    const nativeOpt = document.createElement("option");
    nativeOpt.value = "native";
    nativeOpt.textContent = "Native file";
    modeSelect.appendChild(nativeOpt);
    if (remuxSupported) {
      const remuxOpt = document.createElement("option");
      remuxOpt.value = "remux";
      remuxOpt.textContent = remuxLikelyPlayable ? "Repair for browser" : "Repair for browser (May Fail)";
      modeSelect.appendChild(remuxOpt);
      modeSelect.value = remuxRecommended ? "remux" : "native";
    } else {
      modeSelect.value = "native";
    }
    modeLabel.appendChild(modeSelect);
    controls.appendChild(modeLabel);

    const hint = document.createElement("div");
    hint.style.fontSize = "0.8rem";
    hint.style.opacity = "0.85";

    const video = document.createElement("video");
    video.controls = true;
    video.autoplay = true;
    video.preload = "metadata";
    video.className = "mkvVideo";

    const setPlayback = () => {
      const mode = modeSelect.value;
      const useNative = mode === "native" || !remuxSupported;
      const audioIndex = audioSelect ? audioSelect.value : "";
      video.src = useNative ? fileURL(entry.path) : videoPlayURL(entry.path, audioIndex);
      if (audioSelect) {
        audioSelect.disabled = useNative || !remuxSupported;
      }
      if (useNative) {
        if (!nativeLikely && probe.remuxReason) {
          hint.textContent = `${formatLabel}: native browser playback is likely unreliable. ${probe.remuxReason}.`;
        } else {
          hint.textContent = `${formatLabel}: using direct browser playback.`;
        }
      } else {
        hint.textContent = `${formatLabel}: server remuxes this file to a cached MP4 without re-encoding video.`;
      }
      video.load();
    };

    if (audioSelect) {
      audioSelect.addEventListener("change", () => {
        pauseThumbLoading(1500);
        setPlayback();
      });
    }
    modeSelect.addEventListener("change", () => {
      pauseThumbLoading(1500);
      setPlayback();
    });

    viewerBody.appendChild(controls);
    viewerBody.appendChild(hint);
    viewerBody.appendChild(video);
    pauseThumbLoading(2000);
    setPlayback();
  } catch (err) {
    fallbackVideoViewer(entry, `Video probe failed, falling back to direct playback: ${err.message || err}`);
  }
}

async function openMKVViewer(entry) {
  viewerTitle.textContent = `${entry.name} (mkv)`;
  clearViewer();
  viewerModal.classList.remove("hidden");
  viewerBody.textContent = "Loading tracks...";

  try {
    const probeRes = await api(mkvProbeURL(entry.path));
    if (!probeRes.ok) {
      throw new Error(`probe failed: ${probeRes.status}`);
    }
    const probe = await probeRes.json();
    const videoTrack = probe.video || {};
    const audioTracks = probe.audio || [];
    const subTracks = probe.subs || [];
    if (audioTracks.length === 0) {
      throw new Error("No audio tracks found");
    }
    const remuxLikelyPlayable = canPlayMP4Codec(videoTrack.codec);

    viewerBody.innerHTML = "";
    const controls = document.createElement("div");
    controls.className = "mkvControls";

    const audioLabel = document.createElement("label");
    audioLabel.textContent = "Audio";
    const audioSelect = document.createElement("select");
    for (const a of audioTracks) {
      const opt = document.createElement("option");
      opt.value = String(a.index);
      opt.textContent = trackLabel(a);
      audioSelect.appendChild(opt);
      if (a.default) {
        audioSelect.value = String(a.index);
      }
    }
    if (!audioSelect.value && audioTracks[0]) {
      audioSelect.value = String(audioTracks[0].index);
    }
    audioLabel.appendChild(audioSelect);

    const subLabel = document.createElement("label");
    subLabel.textContent = "Subtitles";
    const subSelect = document.createElement("select");
    const offOpt = document.createElement("option");
    offOpt.value = "";
    offOpt.textContent = "Off";
    subSelect.appendChild(offOpt);
    for (const s of subTracks) {
      const opt = document.createElement("option");
      opt.value = String(s.index);
      opt.textContent = trackLabel(s);
      subSelect.appendChild(opt);
      if ((s.default || s.forced) && !subSelect.value) {
        subSelect.value = String(s.index);
      }
    }
    subLabel.appendChild(subSelect);

    const modeLabel = document.createElement("label");
    modeLabel.textContent = "Playback";
    const modeSelect = document.createElement("select");
    const nativeOpt = document.createElement("option");
    nativeOpt.value = "native";
    nativeOpt.textContent = "Native MKV";
    const remuxOpt = document.createElement("option");
    remuxOpt.value = "remux";
    remuxOpt.textContent = remuxLikelyPlayable ? "Remux MP4" : "Remux MP4 (May Fail)";
    modeSelect.appendChild(nativeOpt);
    modeSelect.appendChild(remuxOpt);
    modeSelect.value = remuxLikelyPlayable ? "remux" : "native";
    modeLabel.appendChild(modeSelect);

    const hint = document.createElement("div");
    hint.style.fontSize = "0.8rem";
    hint.style.opacity = "0.85";

    const video = document.createElement("video");
    video.controls = true;
    video.autoplay = true;
    video.preload = "metadata";
    video.className = "mkvVideo";

    const setPlayback = () => {
      const audioIndex = audioSelect.value;
      const subIndex = subSelect.value;
      const mode = modeSelect.value;
      const useNative = mode === "native";
      video.src = useNative ? fileURL(entry.path) : mkvPlayURL(entry.path, audioIndex);
      audioSelect.disabled = useNative;
      hint.textContent = useNative
        ? "Native MKV mode: subtitle selection works, audio track selection depends on browser support."
        : "Remux MP4 mode: selected audio track is applied server-side.";
      for (const t of [...video.querySelectorAll("track")]) {
        t.remove();
      }
      if (subIndex !== "") {
        const selectedSub = subTracks.find((s) => String(s.index) === subIndex);
        const track = document.createElement("track");
        track.kind = "subtitles";
        track.label = selectedSub ? trackLabel(selectedSub) : "Selected subtitle";
        track.srclang = selectedSub && selectedSub.language ? selectedSub.language : "und";
        track.src = mkvSubURL(entry.path, subIndex);
        track.default = true;
        video.appendChild(track);
      }
      video.load();
    };

    audioSelect.addEventListener("change", () => {
      pauseThumbLoading(1500);
      setPlayback();
    });
    subSelect.addEventListener("change", () => {
      pauseThumbLoading(1500);
      setPlayback();
    });
    modeSelect.addEventListener("change", () => {
      pauseThumbLoading(1500);
      setPlayback();
    });

    controls.appendChild(audioLabel);
    controls.appendChild(subLabel);
    controls.appendChild(modeLabel);
    viewerBody.appendChild(controls);
    viewerBody.appendChild(hint);
    viewerBody.appendChild(video);
    pauseThumbLoading(2000);
    setPlayback();
  } catch (err) {
    viewerBody.textContent = `Failed to load MKV tracks: ${err.message || err}`;
  }
}

function setupEntryClick(link, entry) {
  link.href = entry.isDir ? browseURL(state.rootId, entry.path) : fileURL(entry.path);
  link.addEventListener("click", (e) => {
    if (e.ctrlKey || e.metaKey || e.shiftKey || e.button === 1) {
      return;
    }
    e.preventDefault();
    if (entry.isDir) {
      state.path = entry.path;
      loadList({ pushURL: true });
    } else {
      pauseThumbLoading(2000);
      openViewer(entry);
    }
  });
}

function ensureThumbObserver() {
  if (thumbObserver || !("IntersectionObserver" in window)) {
    return;
  }
  thumbObserver = new IntersectionObserver(
    (obsEntries) => {
      for (const ent of obsEntries) {
        const img = ent.target;
        if (!img || !img.isConnected) {
          thumbObserver.unobserve(img);
          continue;
        }
        if (img.dataset.thumbState === "done" || img.dataset.thumbState === "failed") {
          thumbObserver.unobserve(img);
          continue;
        }
        if (ent.isIntersecting && img.dataset.src && !img.src) {
          enqueueThumbRequest(img);
        }
      }
    },
    {
      root: null,
      rootMargin: "300px 0px",
      threshold: 0.01,
    },
  );
}

function isNearViewport(el, marginPx = 300) {
  if (!el || !el.isConnected) return false;
  const r = el.getBoundingClientRect();
  return r.bottom >= -marginPx && r.top <= window.innerHeight + marginPx;
}

function queueThumbLoad(img, src) {
  img.dataset.src = src;
  img.removeAttribute("src");
  img.loading = "lazy";
  img.decoding = "async";
  img.classList.remove("hidden");

  img.dataset.thumbState = "idle";
  if (!("IntersectionObserver" in window)) {
    enqueueThumbRequest(img);
    return;
  }
  ensureThumbObserver();
  thumbObserver.observe(img);
}

function enqueueThumbRequest(img) {
  if (!img || !img.isConnected) return;
  const stateName = img.dataset.thumbState || "idle";
  if (stateName === "loading" || stateName === "done" || stateName === "failed") {
    return;
  }
  if (Date.now() < thumbLoader.pauseUntil) {
    setTimeout(() => enqueueThumbRequest(img), 120);
    return;
  }
  if (!isNearViewport(img, 360)) {
    return;
  }

  img.dataset.thumbState = "loading";
  img.onload = () => {
    img.dataset.thumbState = "done";
  };
  img.onerror = () => {
    img.classList.add("hidden");
    img.dataset.thumbState = "failed";
  };
  img.src = img.dataset.src;
}

function renderList(entries) {
  entriesEl.innerHTML = "";
  if (state.path !== "") {
    const tr = document.createElement("tr");
    const up = parentPath(state.path);
    tr.innerHTML = '<td colspan="4"><a href="#" id="upLink">..</a></td>';
    const upLink = tr.querySelector("#upLink");
    upLink.href = browseURL(state.rootId, up);
    upLink.addEventListener("click", (e) => {
      if (e.ctrlKey || e.metaKey || e.shiftKey || e.button === 1) {
        return;
      }
      e.preventDefault();
      state.path = up;
      loadList({ pushURL: true });
    });
    entriesEl.appendChild(tr);
  }

  for (const entry of entries) {
    const node = rowTemplate.content.firstElementChild.cloneNode(true);
    const link = node.querySelector(".entryLink");
    const thumb = node.querySelector(".thumb");

    link.textContent = entry.name;
    setupEntryClick(link, entry);

    node.querySelector(".typeCell").textContent = entry.isDir ? "dir" : entry.mime || "file";
    node.querySelector(".sizeCell").textContent = entry.isDir ? "-" : fmtSize(entry.size);
    node.querySelector(".timeCell").textContent = fmtTime(entry.modTime);

    if (entry.thumb && !entry.isDir) {
      queueThumbLoad(thumb, thumbURL(entry.path, 256));
      thumb.onerror = () => thumb.classList.add("hidden");
    } else {
      thumb.classList.add("hidden");
    }

    entriesEl.appendChild(node);
  }
}

function renderGallery(entries) {
  galleryEntriesEl.innerHTML = "";
  if (state.path !== "") {
    const up = document.createElement("a");
    up.className = "gup";
    up.href = browseURL(state.rootId, parentPath(state.path));
    up.textContent = "..";
    up.addEventListener("click", (e) => {
      if (e.ctrlKey || e.metaKey || e.shiftKey || e.button === 1) {
        return;
      }
      e.preventDefault();
      state.path = parentPath(state.path);
      loadList({ pushURL: true });
    });
    galleryEntriesEl.appendChild(up);
  }

  const thumbSize = clamp(Math.round(settings.gridSize * 1.8), 200, 768);

  for (const entry of entries) {
    const link = document.createElement("a");
    link.className = "glink";
    setupEntryClick(link, entry);

    const card = document.createElement("div");
    card.className = "gcard";

    if (entry.isDir) {
      const folder = document.createElement("div");
      folder.className = "gfolder";
      folder.innerHTML = folderSVG;
      card.appendChild(folder);
    } else if (entry.thumb) {
      const img = document.createElement("img");
      img.className = "gthumb";
      img.alt = entry.name;
      queueThumbLoad(img, thumbURL(entry.path, thumbSize));
      img.onerror = () => {
        img.remove();
      };
      card.appendChild(img);
    }

    const label = document.createElement("div");
    label.className = `glabel ${entry.isDir ? "glabel-dir" : ""}`.trim();
    label.textContent = entry.name;
    card.appendChild(label);

    link.appendChild(card);
    galleryEntriesEl.appendChild(link);
  }
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
    galleryEntriesEl.innerHTML = "";
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
  state.entries = data.entries || [];
  state.rootPath = data.rootPath || "";

  if (replaceURL || pushURL) {
    setURLFromState(replaceURL);
  }
  locationEl.textContent = `${state.rootPath}/${state.path}`.replace(/\/+/g, "/");
  upBtn.disabled = state.path === "";

  if (thumbObserver) {
    thumbObserver.disconnect();
  }
  renderList(state.entries);
  renderGallery(state.entries);
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

viewModeSelect.addEventListener("change", () => {
  settings.viewMode = viewModeSelect.value === "gallery" ? "gallery" : "list";
  setCookie("rakuyo_view_mode", settings.viewMode);
  applyViewMode();
});

themeSelect.addEventListener("change", () => {
  settings.theme = themeSelect.value === "dark" ? "dark" : "light";
  setCookie("rakuyo_theme", settings.theme);
  applyTheme();
});

gridSizeInput.addEventListener("input", () => {
  settings.gridSize = clamp(parseInt(gridSizeInput.value, 10) || 200, 60, 320);
  setCookie("rakuyo_grid_size", String(settings.gridSize));
  applyGridSize();
  if (settings.viewMode === "gallery") {
    renderGallery(state.entries);
  }
});

viewerNoteForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  if (!viewerEntry || viewerNoteInput.disabled) {
    return;
  }

  const note = viewerNoteInput.value.trim();
  if (!note) {
    viewerNoteStatus.textContent = "Enter a note first.";
    return;
  }

  const entry = viewerEntry;
  viewerNoteInput.disabled = true;
  viewerNoteStatus.textContent = "Saving...";

  try {
    const res = await api("/api/media-note", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        root: state.rootId,
        path: entry.path,
        note,
      }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      throw new Error(data.error || `save failed: ${res.status}`);
    }
    closeViewer();
    await loadList();
  } catch (err) {
    viewerNoteInput.disabled = false;
    viewerNoteStatus.textContent = err.message || String(err);
  }
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

(function init() {
  loadSettingsFromCookies();
  applyTheme();
  applyGridSize();
  applyViewMode();
})();

(async function initData() {
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
