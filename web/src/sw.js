/* Service worker — the closest thing to a CDN this deployment has.
 *
 * The origin is in one place and the people using this are not. A first visit
 * pays the full round trip; every visit after that is served from the local
 * cache and is effectively instant regardless of distance. It also means the
 * planner keeps working with no connection at all.
 *
 * Rendered from a template: VERSION changes whenever the HTML shell changes,
 * which is what retires the previous cache.
 */
var VERSION = '{{ .Version }}';
var CACHE = 'soiree-' + VERSION;

/* Content-addressed asset URLs, safe to cache forever. */
var PRECACHE = [
{{- range .Assets }}
  '{{ . }}',
{{- end }}
  '/'
];

self.addEventListener('install', function (event) {
  event.waitUntil(
    caches.open(CACHE)
      .then(function (cache) { return cache.addAll(PRECACHE); })
      // A single failed precache entry must not wedge the worker.
      .catch(function () { return undefined; })
      .then(function () { return self.skipWaiting(); })
  );
});

self.addEventListener('activate', function (event) {
  event.waitUntil(
    caches.keys().then(function (keys) {
      return Promise.all(keys.map(function (k) {
        return k === CACHE ? undefined : caches.delete(k);
      }));
    }).then(function () { return self.clients.claim(); })
  );
});

self.addEventListener('fetch', function (event) {
  var req = event.request;
  if (req.method !== 'GET') return;

  var url;
  try { url = new URL(req.url); } catch (e) { return; }
  if (url.origin !== self.location.origin) return;

  /* Hashed assets are immutable: if we have it, it is correct. */
  if (url.pathname.indexOf('/assets/') === 0) {
    event.respondWith(
      caches.match(req).then(function (hit) {
        return hit || fetch(req).then(function (res) {
          if (res && res.ok) {
            var copy = res.clone();
            caches.open(CACHE).then(function (c) { c.put(req, copy); });
          }
          return res;
        });
      })
    );
    return;
  }

  /* The shell: serve the cached copy immediately, refresh in the background.
   * A deploy is picked up on the next visit rather than blocking this one,
   * which is the right trade when the origin is 300ms away. Asset URLs are
   * hashed, so an older shell still references assets that are still cached. */
  if (req.mode === 'navigate' || url.pathname === '/') {
    event.respondWith(
      caches.match('/').then(function (hit) {
        var network = fetch(req).then(function (res) {
          if (res && res.ok) {
            var copy = res.clone();
            caches.open(CACHE).then(function (c) { c.put('/', copy); });
          }
          return res;
        }).catch(function () { return hit; });
        return hit || network;
      })
    );
  }
});
