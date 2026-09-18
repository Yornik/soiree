/* Service worker — the closest thing to a CDN this deployment has.
 *
 * The origin is in one place and the people using this are not. A first visit
 * pays the full round trip; every visit after that is served from the local
 * cache and is effectively instant regardless of distance. It also means the
 * planner keeps working with no connection at all.
 *
 * Rendered from a template: VERSION changes whenever the HTML shell changes,
 * which is what retires the previous cache. Everything outside the two
 * substitutions reaches the browser byte for byte.
 */
var VERSION = {{ json .Version }};
var CACHE = 'soiree-' + VERSION;

/* Content-addressed asset URLs, safe to cache forever. */
var PRECACHE = [
{{- range .Assets }}
  {{ json . }},
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

/* Web Push.
 *
 * The payload is composed by the server (internal/reminders) and arrives as
 * JSON: a title, a line of body, the URL to open and a tag.
 *
 * The tag is the part that is easy to drop and unpleasant to leave out.
 * Without it a second digest stacks on top of the first and the shade fills up
 * with near-identical notifications; with it, the newer one replaces the older,
 * which is what somebody actually wants from a list of what is coming up.
 *
 * showNotification is not optional: the subscription was made with
 * userVisibleOnly, and a push handled without showing anything spends the
 * browser's patience and eventually the subscription.
 */
self.addEventListener('push', function (event) {
  var payload = {};
  try { payload = (event.data && event.data.json()) || {}; } catch (e) { payload = {}; }
  event.waitUntil(self.registration.showNotification(payload.title || 'soiree', {
    body: payload.body || '',
    tag: payload.tag || 'soiree-deadlines',
    data: { url: payload.url || '/' }
  }));
});

/* Bring the planner to the front rather than opening a second copy of it. A
 * notification that spawns another tab every time is one nobody taps twice —
 * and on a phone the planner is very often already open behind it. */
self.addEventListener('notificationclick', function (event) {
  event.notification.close();
  var url = new URL((event.notification.data || {}).url || '/', self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function (windows) {
      for (var i = 0; i < windows.length; i++) {
        var w = windows[i];
        if (w.url.indexOf(self.location.origin) !== 0) continue;
        // navigate() rejects on a client this worker does not control — which
        // includeUncontrolled deliberately turns up, a tab opened before the
        // worker activated being the ordinary case. Unhandled, that rejection
        // reaches waitUntil and the tap does nothing at all: no focus, no new
        // window, no error anybody sees. Bringing the tab forward at the wrong
        // page is a far better answer than a notification that ignores you.
        //
        // Compared without the fragment when the notification names none. The
        // planner is one page and its screens are fragments (/#/account,
        // /#/admin), while the digest opens "/": to a plain comparison every
        // planner not sitting exactly at "/" is somewhere else, and navigating
        // it there is a full reload — the screen somebody was on gone, and
        // whatever they were half way through typing with it.
        var here = url.indexOf('#') === -1 ? w.url.split('#')[0] : w.url;
        if (here !== url && 'navigate' in w) {
          return w.navigate(url).then(
            function (moved) { return (moved || w).focus(); },
            function () { return w.focus(); }
          );
        }
        return w.focus();
      }
      return self.clients.openWindow(url);
    })
  );
});

/* Anything that is not an asset or a navigation falls through this handler
 * without respondWith, which is what keeps /api/v1/events out of it. Do not
 * add a default branch: an event stream buffered through a cache handler
 * delivers nothing, looks like a server fault, and is miserable to find. */
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
   * hashed, so an older shell still references assets that are still cached.
   *
   * Only "/" is the shell. Not every navigation is: a link to a file
   * (/api/v1/attachments/<id>/content) is one too, and answered from here it
   * would open the planner where the file should be. The server has no other
   * page - the screens are all behind the "#" - so everything else goes to
   * the network untouched. */
  if (url.pathname === '/') {
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
