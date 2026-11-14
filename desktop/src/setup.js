const form = document.querySelector("#connect");
const field = document.querySelector("#hub");
const error = document.querySelector("#error");
const state = document.querySelector("#link-state");
const result = document.querySelector("#link-result");
let words = {};

function t(key, variables = {}) {
  return (words[key] || key).replace(/\{(\w+)\}/g, (_, name) => variables[name] ?? "");
}

function message(value) {
  error.textContent = value || t("generic_error");
  error.hidden = false;
}

async function refreshLink() {
  const status = await window.desktopSetup.linkStatus();
  if (!status.available) {
    state.textContent = t("link_absent");
    document.querySelector("#link-reconnect").disabled = true;
    document.querySelector("#pair-link").disabled = true;
    document.querySelector("#pair button").disabled = true;
    document.querySelector("#node-install").hidden = true;
    document.querySelector("#link-install-guide").hidden = false;
    document.querySelector("#link-install").hidden = false;
    return;
  }
  const link = status.link || {};
  state.textContent = t("link_state", { state: link.state || t("not_running") }) + (status.machine_name || status.machine ? ` · ${status.machine_name || status.machine}` : "");
  document.querySelector("#link-reconnect").disabled = false;
  document.querySelector("#pair-link").disabled = false;
  document.querySelector("#pair button").disabled = false;
  document.querySelector("#node-install").disabled = false;
  document.querySelector("#node-install").hidden = false;
  document.querySelector("#link-install-guide").hidden = true;
  document.querySelector("#link-install").hidden = true;
}

async function command(value) {
  result.textContent = t("working");
  const response = await window.desktopSetup.linkCommand(value);
  result.textContent = response.ok ? (response.stdout || t("done")) : (response.stderr || response.stdout || t("generic_error"));
  await refreshLink();
}

form.addEventListener("submit", async event => {
  event.preventDefault();
  error.hidden = true;
  const response = await window.desktopSetup.saveHub(field.value);
  if (!response.ok) message(response.error || t("hub_invalid"));
});

document.querySelector("#link-refresh").addEventListener("click", () => void refreshLink());
document.querySelector("#link-install").addEventListener("click", async () => {
  const { address } = await window.desktopSetup.getHub();
  if (address) window.desktopSetup.openExternal(`${address.replace(/\/$/, "")}/machines`);
});
document.querySelector("#link-reconnect").addEventListener("click", () => void command({ action: "reconnect" }));
document.querySelector("#node-install").addEventListener("click", () => void command({ action: "node-install" }));
document.querySelector("#pair").addEventListener("submit", event => {
  event.preventDefault();
  void command({ action: "pair-active", value: document.querySelector("#pair-link").value });
});

const loginToggle = document.querySelector("#launch-at-login");
loginToggle.addEventListener("change", async () => {
  const enabled = loginToggle.checked;
  loginToggle.disabled = true;
  try {
    const state = await window.desktopSetup.setLaunchAtLogin(enabled);
    loginToggle.checked = state.enabled;
    document.querySelector("#launch-at-login-status").textContent = state.requiresApproval ? t("launch_requires_approval") : "";
  } finally { loginToggle.disabled = false; }
});

void window.desktopSetup.getHub().then(async ({ address, locale, strings }) => {
  field.value = address;
  words = strings;
  document.documentElement.lang = locale;
  for (const element of document.querySelectorAll("[data-i18n]")) element.textContent = t(element.dataset.i18n);
  const login = await window.desktopSetup.getLaunchAtLogin();
  loginToggle.checked = login.enabled;
  document.querySelector("#launch-at-login-status").textContent = login.requiresApproval ? t("launch_requires_approval") : "";
  await refreshLink();
});
