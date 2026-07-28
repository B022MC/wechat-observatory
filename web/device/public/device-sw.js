const CACHE_PREFIX = "observatory-device-static-";
const CACHE = `${CACHE_PREFIX}v1`;
const scriptPath = new URL(self.location.href).pathname;
const mountPath = scriptPath.endsWith("/device-sw.js")
  ? scriptPath.slice(0, -"/device-sw.js".length)
  : "";
const appPath = `${mountPath}/device` || "/device";
const offlinePath = `${mountPath}/device-offline.html` || "/device-offline.html";
const manifestPath = `${mountPath}/device-manifest.webmanifest` || "/device-manifest.webmanifest";
const icon180Path = `${mountPath}/device-icons/icon-180.png` || "/device-icons/icon-180.png";
const icon192Path = `${mountPath}/device-icons/icon-192.png` || "/device-icons/icon-192.png";
const icon512Path = `${mountPath}/device-icons/icon-512.png` || "/device-icons/icon-512.png";
const assetPrefix = `${mountPath}/device-assets/` || "/device-assets/";
const apiPrefix = `${mountPath}/api/` || "/api/";
const SHELL = [offlinePath, manifestPath, icon180Path, icon192Path, icon512Path];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE)
      .then((cache) => cache.addAll(SHELL))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys
        .filter((key) => key.startsWith(CACHE_PREFIX) && key !== CACHE)
        .map((key) => caches.delete(key))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== "GET" || url.origin !== self.location.origin || url.pathname.startsWith(apiPrefix)) return;

  if (request.mode === "navigate") {
    if (url.pathname !== appPath && url.pathname !== `${appPath}/`) return;
    event.respondWith(fetch(request).catch(() => caches.match(offlinePath)));
    return;
  }

  const versionedAsset = url.pathname.startsWith(assetPrefix)
    && /[-.][A-Za-z0-9_-]{8,}\.(?:js|css|woff2?|png|webp)$/.test(url.pathname);
  if (!versionedAsset && !SHELL.includes(url.pathname)) return;

  event.respondWith(caches.open(CACHE).then(async (cache) => {
    const cached = await cache.match(request);
    if (cached) return cached;
    const response = await fetch(request);
    if (response.ok && response.type === "basic") await cache.put(request, response.clone());
    return response;
  }));
});
