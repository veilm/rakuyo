const viewModeSelect = document.getElementById("viewModeSelect");
const themeSelect = document.getElementById("themeSelect");
const gridSizeInput = document.getElementById("gridSizeInput");
const gridSizeValue = document.getElementById("gridSizeValue");
const gridSizeRow = document.getElementById("gridSizeRow");
const autoMarkRow = document.getElementById("autoMarkRow");
const autoColorSelect = document.getElementById("autoColorSelect");
const logoutBtn = document.getElementById("logoutBtn");
const settingsStatus = document.getElementById("settingsStatus");

function getCookie(name) {
  const wanted = `${name}=`;
  for (const part of document.cookie.split(";")) {
    const value = part.trim();
    if (value.startsWith(wanted)) return decodeURIComponent(value.slice(wanted.length));
  }
  return "";
}

function setCookie(name, value) {
  document.cookie = `${name}=${encodeURIComponent(value)}; Path=/; Max-Age=31536000; SameSite=Lax`;
  localStorage.setItem("rakuyo_settings_changed", String(Date.now()));
}

function clamp(n, min, max) {
  return Math.min(max, Math.max(min, n));
}

function applyTheme(theme) {
  document.body.dataset.theme = theme;
}

function updateGallerySizeVisibility() {
  gridSizeRow.classList.toggle("hidden", viewModeSelect.value !== "gallery");
}

viewModeSelect.value = ["list", "gallery"].includes(getCookie("rakuyo_view_mode"))
  ? getCookie("rakuyo_view_mode") : "list";
themeSelect.value = ["dark", "light"].includes(getCookie("rakuyo_theme"))
  ? getCookie("rakuyo_theme") : "dark";
const gridSize = clamp(parseInt(getCookie("rakuyo_grid_size"), 10) || 200, 60, 320);
gridSizeInput.value = String(gridSize);
gridSizeValue.textContent = String(gridSize);
autoColorSelect.value = ["none", "red", "blue", "yellow", "green"].includes(getCookie("rakuyo_auto_color"))
  ? getCookie("rakuyo_auto_color") : "none";
applyTheme(themeSelect.value);
updateGallerySizeVisibility();

viewModeSelect.addEventListener("change", () => {
  setCookie("rakuyo_view_mode", viewModeSelect.value);
  updateGallerySizeVisibility();
});
themeSelect.addEventListener("change", () => {
  setCookie("rakuyo_theme", themeSelect.value);
  applyTheme(themeSelect.value);
});
gridSizeInput.addEventListener("input", () => {
  gridSizeValue.textContent = gridSizeInput.value;
  setCookie("rakuyo_grid_size", gridSizeInput.value);
});
autoColorSelect.addEventListener("change", () => {
  setCookie("rakuyo_auto_color", autoColorSelect.value);
});

logoutBtn.addEventListener("click", async () => {
  await fetch("/api/logout", { method: "POST", credentials: "include" });
  settingsStatus.textContent = "Logged out.";
  logoutBtn.disabled = true;
});

(async function loadConfig() {
  const response = await fetch("/api/config", { credentials: "include" });
  if (response.status === 401) {
    settingsStatus.textContent = "Log in to Rakuyo to view server settings.";
    return;
  }
  if (!response.ok) {
    settingsStatus.textContent = "Failed to load server settings.";
    return;
  }
  const config = await response.json();
  autoMarkRow.classList.toggle("hidden", config.marking !== true);
})();
