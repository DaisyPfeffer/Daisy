// GoDM browser integration: hands new browser downloads to GoDM through the
// native messaging host "com.godm.host". If GoDM is not reachable the browser
// keeps the download, so nothing is ever lost.
const HOST = "com.godm.host";
const MIN_SIZE = 0; // bytes; set e.g. 1024*1024 to only capture files >= 1 MB
const api = globalThis.browser ?? globalThis.chrome;

function send(msg) {
  return new Promise((resolve) => {
    try {
      const p = api.runtime.sendNativeMessage(HOST, msg, (reply) => {
        resolve(api.runtime.lastError ? { ok: false, error: api.runtime.lastError.message } : reply);
      });
      if (p && p.then) p.then(resolve, (e) => resolve({ ok: false, error: String(e) }));
    } catch (e) {
      resolve({ ok: false, error: String(e) });
    }
  });
}

async function enabled() {
  const { enabled = true } = await api.storage.local.get("enabled");
  return enabled;
}

async function cookieHeader(url) {
  try {
    const cookies = await api.cookies.getAll({ url });
    return cookies.map((c) => `${c.name}=${c.value}`).join("; ");
  } catch {
    return "";
  }
}

async function hand(url, referer, fileName) {
  return send({
    cmd: "download",
    url,
    referer: referer || "",
    cookie: await cookieHeader(url),
    userAgent: navigator.userAgent,
    fileName: fileName || "",
  });
}

api.downloads.onCreated.addListener(async (item) => {
  if (!(await enabled())) return;
  if (!/^https?:/i.test(item.finalUrl || item.url)) return; // blob:, data:, file: stay in the browser
  if (item.state !== "in_progress") return;
  if (MIN_SIZE && item.totalBytes > 0 && item.totalBytes < MIN_SIZE) return;
  const url = item.finalUrl || item.url;
  const name = (item.filename || "").split(/[\\/]/).pop();
  const reply = await hand(url, item.referrer, name);
  if (reply && reply.ok) {
    try { await api.downloads.cancel(item.id); } catch {}
    try { await api.downloads.erase({ id: item.id }); } catch {}
  } else {
    console.warn("GoDM did not take the download:", reply && reply.error);
  }
});

api.runtime.onInstalled.addListener(() => {
  api.contextMenus.create({ id: "godm-link", title: "Download with GoDM", contexts: ["link", "video", "audio", "image"] });
});

api.contextMenus.onClicked.addListener(async (info, tab) => {
  const url = info.linkUrl || info.srcUrl;
  if (url) await hand(url, tab && tab.url, "");
});

api.action.onClicked.addListener(async () => {
  const on = !(await enabled());
  await api.storage.local.set({ enabled: on });
  api.action.setBadgeText({ text: on ? "" : "off" });
});
