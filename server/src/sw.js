/* Service worker: blog-static-v@GEN@ carries the CSS/JS bundles and static JSON,
 * blog-fonts-v@GEN@ the woff2 files, blog-pages-v@GEN@ the HTML pages, which are served
 * stale-while-revalidate so a repeat visit paints without waiting for the network. */

var SITE_URLS = "@SITE_URLS@";
if (typeof SITE_URLS === 'string') { SITE_URLS = {}; }
var PAGE_RESOURCES = "@PAGE_RESOURCES@";
if (typeof PAGE_RESOURCES === 'string') { PAGE_RESOURCES = {}; }
var NO_CACHE_PREFIXES = "@NO_CACHE_PREFIXES@";
if (typeof NO_CACHE_PREFIXES === 'string') { NO_CACHE_PREFIXES = []; }

var CACHE_VERSION = 'v@GEN@';
var STATIC_CACHE = 'blog-static-' + CACHE_VERSION;
var FONT_CACHE = 'blog-fonts-' + CACHE_VERSION;
var PAGE_CACHE = 'blog-pages-' + CACHE_VERSION;

// fonts.css is concatenated into the core bundle, so that is the stylesheet the
// font prefetch mines for the @font-face woff2 URLs a site declares.
var FONT_CSS = PAGE_RESOURCES.coreBundleCss;

var NO_CACHE_PATTERNS = NO_CACHE_PREFIXES.map(function(p) {
  // simple prefix match regex
  return new RegExp('^' + p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
});
// always skip media
NO_CACHE_PATTERNS.push(/\.(png|jpg|jpeg|gif|webp|avif|jxl)$/i);

function shouldNotCache(url) {
  return NO_CACHE_PATTERNS.some(function(re) { return re.test(url); });
}

function shouldSkipCache(res) {
  var cc = res.headers.get('Cache-Control') || '';
  return /no-store/i.test(cc) || /no-cache/i.test(cc);
}

// The page a navigation is answered with when the network fails and no cached copy exists.
//
// It states what failed, because the worker serves this in place of the site: nothing else the
// site ships can reach a client in this state. It also drops this worker once the same tab has
// seen the page twice — a worker that cannot fetch cannot be replaced either, since the page
// that calls register() is one it never serves, and without that the client stays here for as
// long as the registration lives.
function offlinePage(err, req) {
  var diag = [
    'error: ' + (err && (err.name + ' / ' + err.message)),
    'url: ' + req.url,
    'mode: ' + req.mode,
    'generation: ' + CACHE_VERSION,
    'online: ' + navigator.onLine
  ].join('\n');
  return '<!DOCTYPE html><html><head><meta charset="utf-8"><title>Offline</title></head><body>' +
    '<h1>网络连接中断</h1>' +
    '<pre style="white-space:pre-wrap;word-break:break-all;font:12px/1.5 monospace">' +
    escapeHTML(diag) + '</pre>' +
    '<script>' + OFFLINE_RESET + '</script>' +
    '</body></html>';
}

function escapeHTML(s) {
  return String(s).replace(/[&<>]/g, function(c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c];
  });
}

// Drops the registration and reloads, so the next navigation reaches the network unmediated.
// Held back until the same tab has seen the offline page twice, and while the browser reports
// no connection: there the worker is doing what it was installed for.
var OFFLINE_RESET =
  '(function(){' +
  'var k="silphuu-offline",n=0;' +
  'try{n=parseInt(sessionStorage.getItem(k)||"0",10)||0}catch(e){}' +
  'n+=1;' +
  'try{sessionStorage.setItem(k,String(n))}catch(e){}' +
  'if(n<2||navigator.onLine===false)return;' +
  'try{sessionStorage.removeItem(k)}catch(e){}' +
  'navigator.serviceWorker.getRegistration()' +
  '.then(function(r){return r?r.unregister():null})' +
  '.catch(function(){})' +
  '.then(function(){location.reload()});' +
  '})();';

// Install: skip waiting immediately.
self.addEventListener('install', function(event) {
  self.skipWaiting();
});

// Activate: clean up old-generation caches and trigger P0 -> P1 -> P2 -> P3 in sequence.
self.addEventListener('activate', function(event) {
  event.waitUntil(
    Promise.all([
      caches.keys().then(function(names) {
        return Promise.all(
          names.filter(function(n) {
            return n !== STATIC_CACHE && n !== FONT_CACHE && n !== PAGE_CACHE;
          }).map(function(n) { return caches.delete(n); })
        );
      }),
      self.clients.claim(),
      self.registration.navigationPreload ? self.registration.navigationPreload.enable() : Promise.resolve()
    ]).then(function() {
      prefetchP0()
        .then(prefetchP1)
        .then(prefetchP3)
        .catch(function() {});
    })
  );
});

// The page hands the worker the pages to warm, because the list follows the posts and anything
// in this file that follows the posts would make every publish a new worker — which the client
// answers with a full-screen refresh. A page is revalidated on every navigation, so it is never
// the stale copy of the list.
self.addEventListener('message', function(event) {
  var data = event.data || {};
  if (data.type !== 'warm-pages') return;
  var urls = data.urls;
  if (!urls || !urls.length) return;
  event.waitUntil(prefetchUrls(urls, PAGE_CACHE));
});

function prefetchUrls(urls, cacheName) {
  return caches.open(cacheName).then(function(cache) {
    return Promise.all(
      urls.map(function(u) {
        return fetch(u, { credentials: 'same-origin' }).then(function(res) {
          if (res.ok && !shouldSkipCache(res)) {
            return cache.put(u, res);
          }
        }).catch(function() {});
      })
    );
  });
}

// P0: core shared bundles (site-wide CSS + common JS).
function prefetchP0() {
  var urls = [
    PAGE_RESOURCES.coreBundleCss,
    PAGE_RESOURCES.coreBundleJs
  ];
  return prefetchUrls(urls, STATIC_CACHE);
}

// P1: core sub-page HTML + page-level JS/CSS bundles + global shared fonts.
function prefetchP1() {
  var P = SITE_URLS.pages;
  var R = PAGE_RESOURCES;
  var pageUrls = [P.about, P.friends, P.sponsor, P.archive, P.guestbook];
  var staticUrls = [
    R.homeBundleCss,
    R.browseBundleCss,
    R.homeBundleJs,
    R.browseBundleJs
  ];

  return Promise.all([
    prefetchUrls(pageUrls, PAGE_CACHE),
    prefetchUrls(staticUrls, STATIC_CACHE),
    fetch(FONT_CSS, { credentials: 'same-origin' })
      .then(function(r) { return r.text(); })
      .then(function(css) {
        var fontUrls = [];
        var re = /url\(['"]([^'"]+\.woff2?)['"]\)/g;
        var m;
        while ((m = re.exec(css)) !== null) {
          var u = m[1];
          if (u.indexOf('/cmt/') === -1 && fontUrls.indexOf(u) === -1) {
            fontUrls.push(u);
          }
        }
        return prefetchUrls(fontUrls, FONT_CACHE);
      }).catch(function() {})
  ]);
}

// P3: comment/post JS/CSS bundles + the sticker index + the 3900-char comment font subset.
function prefetchP3() {
  var R = PAGE_RESOURCES;
  var staticUrls = [
    R.articleBundleCss,
    R.commentBundleCss,
    R.articleBundleJs,
    R.commentBundleJs,
    SITE_URLS.stickersJSON
  ];

  return Promise.all([
    prefetchUrls(staticUrls, STATIC_CACHE),
    fetch(FONT_CSS, { credentials: 'same-origin' })
      .then(function(r) { return r.text(); })
      .then(function(css) {
        var cmtFonts = [];
        var re = /url\(['"]([^'"]+\.woff2?)['"]\)/g;
        var m;
        while ((m = re.exec(css)) !== null) {
          var u = m[1];
          if (u.indexOf('/cmt/') !== -1 && cmtFonts.indexOf(u) === -1) {
            cmtFonts.push(u);
          }
        }
        return prefetchUrls(cmtFonts, FONT_CACHE);
      }).catch(function() {})
  ]);
}

// Fetch interception strategy.
self.addEventListener('fetch', function(event) {
  var req = event.request;
  if (req.method !== 'GET') return;

  var url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (shouldNotCache(url.pathname)) return;

  var pathname = url.pathname;

  // 1. Font files: CacheFirst
  if (/\.woff2?$/i.test(pathname) || pathname.indexOf(SITE_URLS.assets + '/font/') !== -1) {
    event.respondWith(
      caches.open(FONT_CACHE).then(function(cache) {
        return cache.match(req).then(function(cached) {
          if (cached) return cached;
          return fetch(req).then(function(res) {
            if (res.ok) cache.put(req, res.clone());
            return res;
          });
        });
      })
    );
    return;
  }

  // 2. Static assets under /assets (bundles, CSS/JS, the sticker and post indexes): SWR
  if (pathname.indexOf(SITE_URLS.assets) === 0) {
    event.respondWith(
      caches.open(STATIC_CACHE).then(function(cache) {
        return cache.match(req).then(function(cached) {
          var networkFetch = fetch(req).then(function(res) {
            if (res.ok && !shouldSkipCache(res)) {
              cache.put(req, res.clone());
            }
            return res;
          }).catch(function() { return cached; });

          return cached || networkFetch;
        });
      })
    );
    return;
  }

  // 3. HTMX dynamic fragment requests: NetworkFirst (keeps the full-page HTML cache unpolluted)
  if (req.headers.get('HX-Request') === 'true') {
    event.respondWith(
      fetch(req).catch(function() {
        return caches.open(PAGE_CACHE).then(function(cache) {
          return cache.match(req).then(function(cached) {
            if (cached) return cached;
            return new Response('', { status: 503 });
          });
        });
      })
    );
    return;
  }

  // 4. HTML full-page navigations (mode: navigate or Accept: text/html)
  if (req.mode === 'navigate' || (req.headers.get('Accept') || '').indexOf('text/html') !== -1) {
    event.respondWith(
      caches.open(PAGE_CACHE).then(function(cache) {
        var fetchPromise = (event.preloadResponse ? event.preloadResponse.catch(function() { return null; }) : Promise.resolve(null))
          .then(function(preloaded) {
            if (preloaded && preloaded.ok) return preloaded;
            return fetch(req);
          });

        return fetchPromise.then(function(res) {
          if (res && res.ok && !shouldSkipCache(res)) {
            cache.put(req, res.clone());
          }
          return res;
        }).catch(function(e) {
          return cache.match(req).then(function(cached) {
            if (cached) return cached;
            return new Response(offlinePage(e, req), {
              status: 503,
              headers: { 'Content-Type': 'text/html; charset=utf-8' }
            });
          });
        });
      })
    );
    return;
  }
});
