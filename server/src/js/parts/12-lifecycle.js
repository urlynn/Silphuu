// Footer: site uptime calculation
(function() {
    var el = document.getElementById('footer-uptime-days');
    if (!el) return;

    // Site start date: 2025-01-01 (adjust as needed)
    var startDate = new Date('2025-01-01T00:00:00');
    var today = new Date();
    var diffTime = Math.abs(today.getTime() - startDate.getTime());
    var diffDays = Math.ceil(diffTime / (1000 * 60 * 60 * 24));

    el.textContent = diffDays;
})();

// app-ready: session-level state, not page-level.
// Resources are decoded once per session, but htmx history restore replaces the entire
// <body> (target=body, swap=outerSync) and #appLoading is rendered by the template on
// every response, so a body swap must never be allowed to resurrect it. markAppReady
// latches the flag once; ensureAppReady restores the class and drops any stray overlay.
// hx-head re-inserts the deferred core bundle on every htmx page switch, so this file
// runs again per switch. Session state must survive that, never reset on re-entry.
if (window.__appReady === undefined) window.__appReady = false;
function markAppReady() {
    if (window.__appReady) return;
    window.__appReady = true;
    document.body.classList.add('app-ready');
    document.dispatchEvent(new CustomEvent('app-ready'));
}
// Idempotent: safe to call on every swap.
function ensureAppReady() {
    if (!window.__appReady) return;
    document.body.classList.add('app-ready');
    var ov = document.getElementById('appLoading');
    if (ov && ov.parentNode) ov.parentNode.removeChild(ov);
}
window.markAppReady = markAppReady;
window.ensureAppReady = ensureAppReady;

// App loading: show once fonts + key images (load+decode) + CSS background images are all ready.
// Loading shows on first visit only; htmx page switches do not trigger it (not a page load)
(function() {
    var overlay = document.getElementById('appLoading');
    if (!overlay) return;
    // already ready (e.g. body still has the app-ready class after an htmx page switch)
    if (window.__appReady) {
        overlay.remove();
        return;
    }
    var readyFired = false;
    function ready() {
        if (readyFired) return;
        readyFired = true;
        markAppReady();
        setTimeout(function() { if (overlay.parentNode) overlay.parentNode.removeChild(overlay); }, 350);
    }

    // Safety net: wait at most 800ms so a stuck network/font cannot trap the page on the loading screen
    setTimeout(ready, 800);

    // fonts + non-lazy images (load+decode) + CSS background images (preload+decode)
    void document.documentElement.offsetWidth;
    var readyPromises = [document.fonts.ready];
    // non-lazy <img>: load -> decode -> resolve
    document.querySelectorAll('img[src]').forEach(function(img) {
        if (img.loading === 'lazy' || img.complete) return;
        readyPromises.push(new Promise(function(resolve) {
            img.addEventListener('load', function() {
                if (img.decode) img.decode().then(resolve).catch(resolve);
                else resolve();
            }, { once: true });
            img.addEventListener('error', resolve, { once: true });
        }));
    });
    // CSS background-image: preload+decode the URLs declared for this viewport. The
    // narrow-viewport list only exists when the two differ, so choosing here means a
    // mobile visitor never downloads the desktop picture.
    var isNarrow = window.matchMedia && window.matchMedia('(max-width: 768px)').matches;
    var preloadBgs = (isNarrow && window.__preloadBgMobile) ? window.__preloadBgMobile : (window.__preloadBg || []);
    preloadBgs.forEach(function(url) {
        var img = new Image();
        readyPromises.push(new Promise(function(resolve) {
            img.onload = function() {
                if (img.decode) img.decode().then(resolve).catch(resolve);
                else resolve();
            };
            img.onerror = resolve;
            img.src = url;
        }));
    });
    Promise.all(readyPromises).then(ready).catch(ready);
})();

// Re-latch after every swap, including the history restore that replaces the body.
reinitOnSwap(ensureAppReady);

// Scroll progress bar
(function() {
    var bar = document.querySelector('.scroll-progress__bar');
    if (!bar) return;
    function updateProgress() {
        var scrollTop = window.scrollY;
        var scrollHeight = document.documentElement.scrollHeight - window.innerHeight;
        var progress = scrollHeight > 0 ? scrollTop / scrollHeight : 0;
        bar.style.transform = 'scaleX(' + progress + ')';
    }
    window.addEventListener('scroll', updateProgress, { passive: true });
    window.addEventListener('resize', updateProgress, { passive: true });
    document.addEventListener('htmx:after:swap', updateProgress);
    updateProgress();
})();

// SW prefetch triggers: P3 comment fonts + P4 hover prediction
(function() {
    if (!('serviceWorker' in navigator)) return;

    // P3: trigger comment font prefetch on first entry into the comment area (fires once globally)
    var p3Triggered = false;
    function setupP3() {
        if (p3Triggered) return;
        var area = document.getElementById('cmt-comment-area') || document.querySelector('.cmt-list');
        if (!area) return;
        if (!('IntersectionObserver' in window)) {
            p3Triggered = true;
            if (navigator.serviceWorker.controller) {
                navigator.serviceWorker.controller.postMessage({ type: 'prefetch-cmt' });
            }
            return;
        }
        var io = new IntersectionObserver(function(entries) {
            entries.forEach(function(e) {
                if (e.isIntersecting && !p3Triggered) {
                    p3Triggered = true;
                    io.disconnect();
                    if (navigator.serviceWorker.controller) {
                        navigator.serviceWorker.controller.postMessage({ type: 'prefetch-cmt' });
                    }
                }
            });
        }, { rootMargin: '200px' });
        io.observe(area);
    }
    setupP3();
    document.addEventListener('htmx:after:swap', setupP3);

    // P4: prefetch the post fragment when hovering a post link (skipped automatically if already cached by the SW)
    var B = window.SITE_URLS;
    document.addEventListener('mouseover', function(e) {
        var a = e.target.closest('a[href^="' + B.pages.post + '/"]');
        if (!a || a._p4Triggered) return;
        var href = a.getAttribute('href') || '';
        var m = href.match('^' + B.pages.post + '/(\\d+)/?$');
        if (!m) return;
        a._p4Triggered = true;
        if (navigator.serviceWorker.controller) {
            navigator.serviceWorker.controller.postMessage({
                type: 'prefetch-hover',
                url: B.pages.post + '/' + m[1]
            });
        }
    });
})();

// Safari exception (simplest UA detection): Safari neither animates JXL nor can it
// decode 10-bit 4:4:4 animated AVIF. So for animated emoji (picture.anim-picture) the
// whole node is replaced by its inner <img> (whose src is already a gif), keeping only
// the gif. Chrome/Firefox are unaffected and keep pure MIME negotiation.
// Note: applies only to anim-picture (which has a gif fallback); static photos still
// let Safari natively choose jxl/avif. Whole-node replacement (rather than removing
// <source> children) works around Safari's "removing a source does not re-decode" pit
// that would leave it stuck on the failed jxl.
reinitOnSwap(function safariGifFallback() {
    var ua = navigator.userAgent;
    // Safari excluding Chrome/Chromium/Android/CriOS/FxiOS/Edge/Opera
    var isSafari = /^((?!chrome|chromium|android|crios|fxios|edg|opr).)*safari/i.test(ua);
    if (!isSafari) return;
    document.querySelectorAll('picture.anim-picture').forEach(function (pic) {
        var img = pic.querySelector('img');
        if (img && pic.parentNode) pic.parentNode.replaceChild(img, pic);
    });
});

// /lalafell random error-code card initialization (fires on initial load and HTMX swaps)
reinitOnSwap(function initFunError() {
    var stage = document.getElementById('funErrorStage');
    var reroll = document.getElementById('funErrorReroll');
    if (!stage || !reroll) return;

    var tpls = Array.prototype.slice.call(stage.querySelectorAll('.fun-error__tpl'));
    if (!tpls.length) return;

    // prevent duplicate binding
    if (reroll._funBound) return;
    reroll._funBound = true;

    var active = null;
    var currentCode = null;
    var busy = false;
    var deck = [];

    function pickTpl() {
        if (deck.length === 0) {
            deck = tpls.slice();
            // Fisher-Yates shuffle: fully randomize the deck order
            for (var i = deck.length - 1; i > 0; i--) {
                var j = Math.floor(Math.random() * (i + 1));
                var temp = deck[i];
                deck[i] = deck[j];
                deck[j] = temp;
            }
            // Cross-round repeat guard: if the first draw of a new round equals the last
            // card of the previous round, swap it to the top to prevent back-to-back repeats
            if (currentCode && deck.length > 1 && deck[deck.length - 1].getAttribute('data-code') === currentCode) {
                var nextCard = deck.pop();
                deck.unshift(nextCard);
            }
        }
        return deck.pop();
    }

    function show() {
        if (busy) return;
        var oldActive = active;
        var tpl = pickTpl();
        currentCode = tpl.getAttribute('data-code');
        var frag = tpl.content.cloneNode(true);

        active = document.createElement('div');
        active.className = 'fun-error__card-inner';
        // Glass triplet: this card is created by JS (the template only has <template>), so it must be attached explicitly
        if (window.__glassKit) window.__glassKit(active);
        // Card style scoping anchor (handler.go scopeCardCSS prefixes the card's
        // selectors with [data-fun-code=code], preventing rules like the reroll-overlap
        // 403 horrorGlitch from leaking across cards)
        active.setAttribute('data-fun-code', currentCode);
        active.appendChild(frag);

        // Wrap only the full-width exclamation mark in the heading with .sym
        var h2 = active.querySelector('h2');
        if (h2 && h2.innerHTML.indexOf('！') !== -1) {
            h2.innerHTML = h2.innerHTML.replace(/！/g, '<span class="sym">！</span>');
        }

        // In lalafell cards, wrap dots in .pill/.desc with .fun-dot and word spaces with .fun-space
        var pillEl = active.querySelector('.pill');
        if (pillEl) {
            pillEl.innerHTML = pillEl.innerHTML.replace(/\s*·\s*/g, '§DOT§').replace(/\s+/g, '<span class="fun-space"></span>').replace(/§DOT§/g, '<span class="fun-dot">·</span>');
        }
        var descEl = active.querySelector('.colored-desc, .desc');
        if (descEl) {
            descEl.innerHTML = descEl.innerHTML.replace(/\s*·\s*/g, '§DOT§').replace(/\s+/g, '<span class="fun-space"></span>').replace(/§DOT§/g, '<span class="fun-dot">·</span>');
        }

        if (oldActive) {
            busy = true;
            // Key: add the is-incoming class BEFORE attaching to the DOM to avoid a
            // first-frame flash (0ms snapshot flash)
            active.classList.add('is-incoming');
            stage.appendChild(active);

            oldActive.classList.remove('is-incoming');
            oldActive.classList.add('is-outgoing');

            setTimeout(function() {
                if (oldActive.parentNode) oldActive.parentNode.removeChild(oldActive);
                active.classList.remove('is-incoming');
                busy = false;
            }, 560);
        } else {
            stage.appendChild(active);
        }
        // New card in DOM -> re-collect (recalc only recomputes parameters of existing
        // cards, not this one); every reroll swaps the card, so this must re-run.
        if (window.__backdropRescan) window.__backdropRescan();
    }

    reroll.addEventListener('click', show);
    show();
});


// Fullscreen overlay refresh on content update: after publishing/editing, the SW
// generational version changes -> the new SW activates and takes over (skipWaiting+claim)
// -> controllerchange fires here -> fullscreen glass overlay -> forced reload after 600ms
// to pull fresh content.
(function contentUpdateOverlay() {
    if (!('serviceWorker' in navigator)) return;
    navigator.serviceWorker.addEventListener('controllerchange', function () {
        if (document.getElementById('sw-update-overlay')) return;
        // Triplet injection: glass created dynamically from JS must also go through the
        // CSS filter replication (under .chromium), otherwise Chrome's native
        // backdrop-filter flickers on this fullscreen overlay too. Structure in the
        // glass.css triplet section; driven by 14-backdrop.js (fixed -> static mode).
        var ov = document.createElement('div');
        ov.id = 'sw-update-overlay';
        ov.className = 'sw-update-overlay';
        if (window.__glassKit) window.__glassKit(ov);
        var box = document.createElement('div');
        box.className = 'sw-update-box';
        if (window.__glassKit) window.__glassKit(box);
        var spin = document.createElement('div');
        spin.className = 'sw-update-spin';
        var txt = document.createElement('p');
        txt.textContent = '内容已更新，正在刷新…';
        box.appendChild(spin);
        box.appendChild(txt);
        ov.appendChild(box);
        (document.body || document.documentElement).appendChild(ov);
        // Must re-collect after the new card enters the DOM (recalc only recomputes existing cards' parameters, not new ones)
        if (window.__backdropRescan) window.__backdropRescan();
        setTimeout(function () { location.reload(); }, 600);
    });
})();

// Dynamic Asset Orchestrator for HTMX page swaps.
// HTMX switches only replace DOM containers; page-specific bundles are injected into
// <head> at beforeSwap, combined with Service Worker prefetch caching, for seamless
// injection with no style loss or script errors.
(function dynamicAssetOrchestrator() {
    // Inject before the site layer: site.css must stay the last stylesheet in <head>.
    // See docs/GOTCHAS.md §site-layer-order.
    function ensureStyle(href) {
        if (!document.querySelector('link[href^="' + href + '"]')) {
            var l = document.createElement('link');
            l.rel = 'stylesheet';
            l.href = href;
            var siteLayer = document.getElementById('site-css');
            if (siteLayer && siteLayer.parentNode) {
                siteLayer.parentNode.insertBefore(l, siteLayer);
            } else {
                document.head.appendChild(l);
            }
        }
    }
    function ensureScript(src) {
        if (!document.querySelector('script[src^="' + src + '"]')) {
            var s = document.createElement('script');
            s.src = src;
            s.defer = true;
            document.head.appendChild(s);
        }
    }

    // Re-assert the site layer's position after any <head> mutation: hx-head merges the
    // incoming page's head by appending to the end of ours, which lands page bundles after
    // site.css and silently kills the site's overrides.
    // See docs/GOTCHAS.md §site-layer-order.
    function keepSiteLayerLast() {
        var siteLayer = document.getElementById('site-css');
        if (!siteLayer || siteLayer.parentNode !== document.head) return;
        var sheets = document.head.querySelectorAll('link[rel=stylesheet], style');
        var last = sheets[sheets.length - 1];
        if (!last || last === siteLayer) return; // already last: never touch the DOM
        document.head.appendChild(siteLayer);
    }
    keepSiteLayerLast();
    if (window.MutationObserver) {
        new MutationObserver(keepSiteLayerLast).observe(document.head, { childList: true });
    }

    function routeAssets(path) {
        var B = window.SITE_URLS;
        var P = B && B.pages;
        var R = window.__PAGE_RESOURCES__;
        if (!P || !R) return;

        if (path === P.home) {
            ensureStyle(R.homeBundleCss);
            ensureScript(R.homeBundleJs);
        } else if (path === P.posts || path === P.archive) {
            // /archive renders posts.html, so it needs the same browse bundle.
            ensureStyle(R.browseBundleCss);
            ensureScript(R.browseBundleJs);
        } else if (path.indexOf(P.post + '/') === 0) {
            ensureStyle(R.articleBundleCss);
            ensureScript(R.articleBundleJs);
            ensureStyle(R.commentBundleCss);
            ensureScript(R.commentBundleJs);
        } else if (path === P.about || path === P.friends || path === P.sponsor || path === P.funError) {
            ensureStyle(R.browseBundleCss);
        } else if (path === P.guestbook) {
            ensureStyle(R.commentBundleCss);
            ensureScript(R.commentBundleJs);
        } else {
            if (B.admin && (path === B.admin.dashboard || path === B.admin.root || path === B.admin.status || path.indexOf(B.admin.root) === 0)) {
                ensureStyle(R.adminBundleCss);
            }
        }
    }

    // htmx 4.0's official event name is htmx:before:swap (the old htmx:beforeSwap was
    // removed and never fires). fetch replaced XHR so there is no responseURL; the
    // request URL is taken from request.action / target first.
    document.addEventListener('htmx:before:swap', function(evt) {
        var detail = evt.detail || {};
        var ctx = detail.ctx || {};
        var req = ctx.request || {};
        var action = req.action
            || (detail.requestConfig && detail.requestConfig.path)
            || (detail.xhr && detail.xhr.responseURL)
            || (evt.target && (evt.target.getAttribute('href') || evt.target.getAttribute('hx-get')));
        var path = location.pathname;
        if (action) {
            try { path = new URL(action, location.href).pathname; } catch (e) { path = action; }
        }
        routeAssets(path);
    });

    document.addEventListener('htmx:after:swap', function() {
        routeAssets(location.pathname);
    });
})();

// Post detail dual-mode page switching + TOC bubble fluid height transition.
// htmx events: before:swap / after:settle (the camelCase spellings do not fire);
// timeline: before:swap -> DOM swap -> after:settle -> after:swap.
// is-internal-post-nav lives on <html> because navbar.js wipes <body> classes
// before page-level swaps. The flag must never be removed by a timer: restoring
// animation-name replays every Mode A entrance animation (full-column flicker).
// Mode B animates the real glass elements' height (no transforms -> no Backdrop
// Root risk). The element is border-box: keyframes use offsetHeight, and the CSS
// side needs flex:none + min-height:0 or flex:1 clamps the WAAPI height.
// TOC is rebuilt by toc.js at after:swap, so its height transition hangs on
// toc:rebuilt; measuring at after:settle reads the still-empty TOC.
// Armed one-shot: while the flag persists, each cmt:loaded/toc:rebuilt handler
// consumes its armed flag once and re-arms on the next Mode B before:swap, so
// local events never animate from stale prev values.
(function postNavOrchestrator() {
    // Unified choreography: left column static + article/comment block/TOC glasses
    // animate height together + layout cascade.
    // All three glasses share the same curve, duration, and the same animateHeight function.
    var EASE = 'cubic-bezier(0.45, 0, 0.15, 1)';
    var DUR = 600;
    var B = window.SITE_URLS;
    var POST_PREFIX = B.pages.post;
    var reduceMQ = window.matchMedia('(prefers-reduced-motion: reduce)');

    // Page type cannot be read from body's class: navbar.js's before:swap listener on
    // body removeAttribute('class') before the event bubbles to document. data-body-class
    // on #content-wrapper survives and still holds the old page's value until swap ends.
    function currentIsPostPage() {
        var cw = document.getElementById('content-wrapper');
        var cls = cw ? (cw.getAttribute('data-body-class') || '') : '';
        return cls.split(/\s+/).indexOf('page-article') !== -1;
    }

    var prevArticleH = 0;
    var prevCmtAreaH = 0;
    var prevTocH = 0;
    var cmtArmed = false;
    var tocArmed = false;
    var pendingPageScroll = false; // page-level Mode B in progress (animation/scroll guard; not set for local swaps)

    function clearInternalNavFlag() {
        document.documentElement.classList.remove('is-internal-post-nav');
    }

    // Unified glass height animation entry: from (old natural height) -> current natural height.
    // border-box: keyframe value = offsetHeight; fill:none returns natural values automatically.
    // Prerequisite: the target element is not clamped by flex:1/min-height:auto (see
    // the Mode B block in post-article.css).
    // Full ensemble: measure the natural height before playing (footer still in flow),
    // add .is-anim-height to pin .article-footer to the bottom temporarily (see the CSS
    // comment), remove at animation end, zero end-point jump.
    function animateHeight(el, fromH) {
        if (!el || !fromH) return;
        var newH = el.offsetHeight;
        if (Math.abs(newH - fromH) < 4) return;
        el.classList.add('is-anim-height');
        var anim = el.animate(
            [{ height: fromH + 'px' }, { height: newH + 'px' }],
            { duration: DUR, easing: EASE, fill: 'none' }
        );
        var done = false;
        function finish() {
            if (done) return;
            done = true;
            el.classList.remove('is-anim-height');
        }
        anim.onfinish = finish;
        anim.oncancel = finish;
        setTimeout(finish, DUR + 150); // safety net: animations pause when the page is hidden and onfinish does not fire
    }

    document.addEventListener('htmx:before:swap', function(evt) {
        var ctx = evt.detail && evt.detail.ctx;
        var currentIsPost = currentIsPostPage();
        var action = ctx && ctx.request && ctx.request.action;
        var targetIsPost = false;
        if (action) {
            try { targetIsPost = new URL(action, location.href).pathname.indexOf(POST_PREFIX + '/') === 0; } catch (e) {}
        }

        if (currentIsPost && targetIsPost) {
            // Mode B: internal post/* switch (left column anchored / center+right glass unified)
            document.documentElement.classList.add('is-internal-post-nav');
            pendingPageScroll = true; // only page-level switches scroll back to top (see after:settle)
            var article = document.querySelector('.post-center .post-article');
            var cmtArea = document.getElementById('cmt-comment-area');
            var rightSidebar = document.querySelector('.post-sidebar-right');
            // border-box: height keyframe value = offsetHeight (see the function-header coordinate system note)
            prevArticleH = article ? article.offsetHeight : 0;
            prevCmtAreaH = cmtArea ? cmtArea.offsetHeight : 0;
            prevTocH = rightSidebar ? rightSidebar.offsetHeight : 0;
            cmtArmed = true;
            tocArmed = true;
        } else {
            // Local swaps (comment sorting/posting, target = this page's #cmt-comment-area)
            // do NOT clear the flag: clearing it defeats the animation:none suppression ->
            // Mode A replays every column's animation from the start, plus a layout jump.
            var swapTarget = ctx && ctx.target;
            if (swapTarget && swapTarget.id === 'cmt-comment-area' && currentIsPost) return;
            // Mode A: entering from outside / leaving post -> clear the flag
            clearInternalNavFlag();
            cmtArmed = false;
            tocArmed = false;
        }
    });

    // Attached to before:settle, not after:settle: between DOM swap and settle there is
    // a ~40ms window where the new article paints bare at natural height before the first
    // WAAPI frame pins it to the old height. At before:settle nothing has painted yet.
    // The guard uses pendingPageScroll rather than the class flag: the class stays
    // resident during local swaps, but animation/scroll only run on page-level switches.
    document.addEventListener('htmx:before:settle', function() {
        if (!pendingPageScroll) return;
        if (!reduceMQ.matches) {
            // Article glass height animation: the layout cascade automatically carries
            // the pieces below in lockstep.
            // (relies on flex:none + min-height:0 from the Mode B block in post-article.css)
            animateHeight(document.querySelector('.post-center .post-article'), prevArticleH);
            // Comment block glass pins its old height to hold shape, then stretches when content arrives (see cmt:loaded)
            var cmtArea = document.getElementById('cmt-comment-area');
            if (cmtArea && prevCmtAreaH > 0) {
                cmtArea.style.height = prevCmtAreaH + 'px';
            }
        }
    });

    document.addEventListener('htmx:after:settle', function() {
        if (!pendingPageScroll) return;
        pendingPageScroll = false; // consume: local swaps (sorting) no longer trigger scrolling
        // If the viewport is below the article after the switch, smooth-scroll back to the top
        if (window.scrollY > 100) {
            window.scrollTo({ top: 0, behavior: 'smooth' });
        }
        // No flag removal here (see the function-header comment).
    });

    // Comment block bottom stretch: old height -> new natural height once async content arrives.
    // cmt:loaded is dispatched (bubbling) on #cmt-comment-area by 07-comment.js after
    // its innerHTML injection completes. Comment items additionally stagger in via
    // cmt-item-in, layered alongside the height stretch.
    document.addEventListener('cmt:loaded', function(evt) {
        if (!cmtArmed) return;
        cmtArmed = false; // one-shot consumption, prevents stale prev values from misfiring animations while the flag persists
        var area = evt.target && evt.target.id === 'cmt-comment-area'
            ? evt.target
            : document.getElementById('cmt-comment-area');
        if (!area || !prevCmtAreaH) return;
        area.style.height = ''; // WAAPI takes over, returns to natural height at the end
        if (reduceMQ.matches) return;
        animateHeight(area, prevCmtAreaH);
    });

    // TOC height stretch: old article's TOC height -> new article's real natural height.
    // Must wait for toc:rebuilt (TOC list rebuilt; natural height is then final);
    // same curve and function as the article/comment block, keeping the choreography unified.
    document.addEventListener('toc:rebuilt', function() {
        if (!tocArmed) return;
        tocArmed = false; // one-shot consumption
        if (reduceMQ.matches) return;
        animateHeight(document.querySelector('.post-sidebar-right'), prevTocH);
    });
})();


