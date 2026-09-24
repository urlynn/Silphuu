// backdrop-filter engine — dual-kernel, two code paths.
// 1) Chromium (<html class="chromium">, class added by head.html UA sniffing):
//    replication engine (Scene A static alignment + Scene B three-mode scroll following).
// 2) WebKit/Safari: native backdrop-filter baseline plus an area-budget workaround on
//    the article body only — WebKit caps page-cumulative backdrop area and silently
//    strips a layer's backdrop once exceeded (see initWebKitArticleFrost below).
// 3) Firefox: native backdrop-filter untouched; this file does not touch it.
// Corresponding CSS: the .chromium triplet section in assets/css/components/glass.css.
//
// Layer ownership: this engine only touches the .g-blur/.g-content layers and never wraps
// content; ghosts attach to the glass card body as siblings of the blur layer, so
// animations are unaffected by the filter containing block.
//
// Scene B three modes (auto-detected at runtime from ancestor position, written to data-g-mode):
//   sd     (default) card in normal scroll flow -> scroll-driven, zero main-thread cost.
//   static           ancestor position:fixed -> constant values, zero computation.
//   raf  (fallback)  older Chrome without scroll-driven, or ancestor position:sticky
//                    (piecewise non-linear rect.top -> linear keyframes break).
//
// Key design: per-card constants go on the translate property (var updates take effect
// immediately); keyframes interpolate only the global delta --g-max-scroll. Both are pure
// translations and commutative, so the most common recompute trigger — fonts and images
// shifting docY — needs no animation refresh.
//
// ?g-mode=sd|raf|static forces a single mode, for comparing them on real hardware.
//        ?frost=1|0         forces WebKit article frost on/off (see the ?frost note below).
(function() {
    // Triplet injector (for glass cards created dynamically from JS).
    // Defined before the UA check: outside .chromium the triplet has no CSS rules ->
    // zero side effects, so any kernel can call it safely without re-checking UA.
    // Callers: the sw-update overlay in 12-lifecycle.js / fun-error cards,
    //          and admin-toast in admin.html.
    window.__glassKit = function(host) {
        host.setAttribute('data-backdrop-scene', 'b');
        var mk = function(cls) {
            var d = document.createElement('div');
            d.className = cls;
            return d;
        };
        var blur = mk('g-blur');
        var filter = mk('g-filter');
        filter.appendChild(mk('g-content'));   // the clone layer must nest inside the expander layer
        blur.appendChild(filter);
        host.appendChild(blur);
        host.appendChild(mk('g-tint'));
        host.appendChild(mk('g-noise'));
    };

    // WebKit branch: backdrop-filter area-budget workaround.
    // WebKit caps the page-cumulative backdrop-filter area (GraphicsLayerCA.cpp
    // cMaxTotalBackdropFilterArea, ~10 screens); exceeding it silently removes the
    // backdrop layer — no exception, no console. The counted area is the element's full
    // border box, unclipped to the viewport, so a long article (measured: 800x35005px
    // ≈ 102% of budget) loses its frost entirely, and splitting a post into segments
    // does not help (the budget is page-cumulative). Countermeasure: carry the article
    // blur on a fixed viewport-sized layer (~4% of budget) whose geometry tracks the
    // .post-article rect every frame; frost sits below .post-article so tint draw order
    // still matches native backdrop-filter. Safari only.
    function initWebKitArticleFrost(force) {
        // Reuses the Safari detection from 12-lifecycle.js (excludes Chrome/Chromium/Android/CriOS/FxiOS/Edge/Opera)
        if (!force && !/^((?!chrome|chromium|android|crios|fxios|edg|opr).)*safari/i.test(navigator.userAgent)) return;

        var style = document.createElement('style');
        style.id = 'webkit-frost-style';
        style.textContent =
            'html.webkit-frost .post-article{' +
                'backdrop-filter:none !important;-webkit-backdrop-filter:none !important;}' +
            'html.webkit-frost .post-frost{' +
                'position:fixed;pointer-events:none;z-index:0;display:none;' +
                'border-radius:var(--radius-xl,16px);' +
                'backdrop-filter:blur(var(--article-blur,var(--panel-blur,20px)));' +
                '-webkit-backdrop-filter:blur(var(--article-blur,var(--panel-blur,20px)));}' +
            '@media print{html.webkit-frost .post-frost{display:none !important;}}';
        document.head.appendChild(style);

        var frost = document.createElement('div');
        frost.className = 'post-frost';
        frost.setAttribute('aria-hidden', 'true');
        document.body.appendChild(frost);
        // Takes effect as soon as the class lands: the article's backdrop-filter is
        // stripped and the frost layer takes over
        document.documentElement.classList.add('webkit-frost');

        var article = null, ticking = false, holdUntil = 0;

        function pick() {
            return document.querySelector('.post-layout .post-center .post-article');
        }

        function sync() {
            if (!article || !article.isConnected) article = pick();
            if (!article) { frost.style.display = 'none'; return; }
            var r = article.getBoundingClientRect();
            var vh = window.innerHeight || document.documentElement.clientHeight;
            var top = Math.max(0, r.top);
            var bottom = Math.min(vh, r.bottom);
            if (r.width <= 0 || bottom - top <= 0) { frost.style.display = 'none'; return; }
            frost.style.display = 'block';
            frost.style.top = top + 'px';
            frost.style.left = r.left + 'px';
            frost.style.width = r.width + 'px';
            frost.style.height = (bottom - top) + 'px';
            // Round corners only while the article's real top/bottom edges are inside the
            // viewport; otherwise clear the inline value -> falls back to CSS --radius-xl
            // (square corners mid-scroll)
            var atTop = r.top >= -0.5, atBottom = r.bottom <= vh + 0.5;
            frost.style.borderTopLeftRadius = atTop ? '' : '0px';
            frost.style.borderTopRightRadius = atTop ? '' : '0px';
            frost.style.borderBottomLeftRadius = atBottom ? '' : '0px';
            frost.style.borderBottomRightRadius = atBottom ? '' : '0px';
        }

        function frame() {
            ticking = false;
            sync();
            var running = false;
            if (article && article.getAnimations) {
                var list = article.getAnimations();
                for (var i = 0; i < list.length; i++) {
                    if (list[i].playState === 'running') { running = true; break; }
                }
            }
            // While the article itself is animating (Mode A entrance / Mode B height),
            // keep tracking frame-by-frame and sync opacity, otherwise the glass arrives
            // before the panel during entry and they misalign.
            frost.style.opacity = running ? getComputedStyle(article).opacity : '';
            if (running || performance.now() < holdUntil) schedule();
        }

        function schedule() {
            if (ticking) return;
            ticking = true;
            requestAnimationFrame(frame);
        }

        function kick(ms) {
            holdUntil = Math.max(holdUntil, performance.now() + (ms || 0));
            schedule();
        }

        window.addEventListener('scroll', function () { kick(0); }, { passive: true });
        window.addEventListener('resize', function () { kick(240); });
        window.addEventListener('orientationchange', function () { kick(400); });
        window.addEventListener('load', function () { kick(400); });
        document.addEventListener('htmx:after:swap', function () { article = null; kick(600); });
        document.addEventListener('htmx:after:settle', function () { kick(600); });
        document.addEventListener('transitionrun', function () { kick(700); }, true);
        document.addEventListener('animationstart', function () { kick(700); }, true);

        kick(400);
    }

    // ?frost=1|0
    //   Safari   ?frost=0 -> back to native backdrop-filter
    //   Chromium ?frost=1 -> reproduces the Safari end state
    //     (no Safari pixel-verification channel is available, so frost geometry /
    //      stacking / area budget can only be checked in Chromium; in this mode the
    //      native backdrop-filter of .post-article is stripped by html.webkit-frost
    //      too, so any blur that shows up comes from the frost layer)
    var frostParam = (location.search.match(/[?&]frost=([01])/) || [])[1];
    var isChromium = document.documentElement.classList.contains('chromium');
    var wantFrost = frostParam === '1' || (frostParam !== '0' && !isChromium);

    if (wantFrost) {
        // Deferred scripts run after DOM parsing so body is normally ready; fallback for edge cases
        var bootFrost = function () { initWebKitArticleFrost(frostParam === '1'); };
        if (document.body) bootFrost();
        else document.addEventListener('DOMContentLoaded', bootFrost, { once: true });
    }

    if (!isChromium) return;

    var SD = !!(window.CSS && CSS.supports &&
                CSS.supports('animation-timeline', 'scroll(root)'));
    var FORCE = (location.search.match(/[?&]g-mode=(sd|raf|static)/) || [])[1] || '';

    var sceneACards = [];   // Scene A: hero-style (background inside the container, static relative to the card -> static alignment)
    var cards = [];         // Scene B: [{el, content, mode, x, y0}]
    var rafId = 0, recalcId = 0, lastMax = -1;

    // Ancestor position detection (permanent property, stable and reliable)
    function hasPos(card, pos) {
        for (var el = card; el && el !== document.documentElement; el = el.parentElement) {
            if (getComputedStyle(el).position === pos) return true;
        }
        return false;
    }

    function classify(card) {
        if (FORCE) return FORCE;
        if (!SD) return 'raf';                      // fallback for older Chrome
        if (hasPos(card, 'fixed')) return 'static'; // navbar/.dd/FAB/overlays: does not move with scrolling
        if (hasPos(card, 'sticky')) return 'raf';   // piecewise non-linear, linear keyframes break
        return 'sd';
    }

    function collectCards() {
        sceneACards = Array.prototype.slice.call(
            document.querySelectorAll('[data-backdrop-scene="a"]'));
        cards = [];
        var list = document.querySelectorAll('[data-backdrop-scene="b"]');
        for (var i = 0; i < list.length; i++) {
            var content = list[i].querySelector(':scope > .g-blur > .g-filter > .g-content');
            if (!content) continue;
            var mode = classify(list[i]);
            list[i].setAttribute('data-g-mode', mode);  // cards created by swap lack this attribute, must re-set
            cards.push({ el: list[i], content: content, mode: mode, x: NaN, y0: NaN });
        }
    }

    // Scene A: blur layer background alignment (hero coordinate system is static,
    // recomputed only on resize)
    // Modeled after static/debug-hero-cmp-real.html alignBlur, but based on the hero
    // container's coordinate system (hero is also 100vw×100vh; while scrolling the blur
    // layer stays static relative to the background -> no scroll following needed).
    function alignSceneA(card) {
        // Alignment basis: .g-blur is a clip layer sized to the card; the background and
        // filter live on the inner expander .g-filter -> alignment must be based on .g-filter.
        var blur = card.querySelector(':scope > .g-blur > .g-filter');
        if (!blur) return false;
        // Background image: use the .hero__bg img src (avif) — this engine only runs on
        // Chrome, which does not decode jxl; never take the picture's jxl source (breaks the image)
        var hero = card.closest('.hero') || document.querySelector('.hero');
        if (!hero) return false;
        var bgImg = card.querySelector('.hero__bg') || hero.querySelector('.hero__bg');
        if (!bgImg) return false;
        var bgSrc = bgImg.getAttribute('src');
        if (!bgSrc) return false;
        // Natural size: use the hero__bg img element (Chrome's picture negotiation selects
        // the AVIF source, naturalWidth is most accurate; same URL as the .g-blur background
        // -> pixel-identical dimensions)
        if (!bgImg.naturalWidth) return false;      // not loaded yet, wait for load
        var W = hero.offsetWidth, H = hero.offsetHeight;
        var scale = Math.max(W / bgImg.naturalWidth, H / bgImg.naturalHeight);
        var dw = bgImg.naturalWidth * scale, dh = bgImg.naturalHeight * scale;
        var ox = (W - dw) / 2, oy = (H - dh) / 2;
        var hRect = hero.getBoundingClientRect();
        var r = blur.getBoundingClientRect();
        var bx = r.left - hRect.left, by = r.top - hRect.top;  // blur layer position in hero coordinates
        blur.style.backgroundImage = 'url("' + bgSrc + '")';
        blur.style.backgroundSize = dw + 'px ' + dh + 'px';
        blur.style.backgroundPosition = (ox - bx) + 'px ' + (oy - by) + 'px';
        return true;
    }

    // Scene B: inject a bg-shapes DOM clone into the content layer, a DOM clone rather
    // than an SVG so its sizing can use native vw/vh.
    // Clone only what is actually visible; which shape layer that is depends on the page
    // — see the source list in fetchShapes and docs/GOTCHAS.md §backdrop-shape-layers.
    // Home wallpaper: the home page has #wpSharp (wallpaper) + #wpBlur (blurred copy);
    //   native backdrop-filter samples the composite "wallpaper + bgShapes colored
    //   shapes" layer. Cloning only #bgShapes renders solid on the home page
    //   (fetchShapes skips display:none sources), diverging from native by 9%+ RMSE.
    //   Fix: when #wpSharp is visible, wrap it as a .g-wp div (absolute, inset:0,
    //   cover) and inject it too.
    //
    //   Injection order must match the real stacking order — #wpSharp
    //     (z-index:-2) paints above #bgShapes (z-index:-3). In the clone both are
    //     position:absolute + z-index:auto, so DOM order is paint order: append shapes
    //     first, wallpaper last. Otherwise every first-screen glass card blurs an
    //     actually-invisible tangram.
    //   Quote nesting: do not assemble style="...url("...")..." HTML strings — the first
    //     inner " closes the attribute and kills the background. Use createElement +
    //     style.cssText to avoid quote nesting.
    var shapesHTML = '';
    function extractAvifUrl(imageSet) {
        // getComputedStyle returns image-set(url("a.jxl") 1dppx type("image/jxl"),
        //                                    url("a.avif") 1dppx type("image/avif"))
        // including density descriptors like 1dppx; the regex must tolerate them. This
        // engine only runs on Chrome, which supports jxl, but for compatibility prefer
        // avif, falling back to any image/* url.
        var m = imageSet.match(/url\("([^"]+)"\)\s*(?:\d+dppx\s+)?type\("image\/avif"\)/);
        if (m) return m[1];
        m = imageSet.match(/url\("([^"]+)"\)\s*(?:\d+dppx\s+)?type\("image\/(?:jxl|webp|heic|png|jpeg)"\)/);
        if (m) return m[1];
        m = imageSet.match(/url\("([^"]+)"\)/);
        return m ? m[1] : null;
    }
    function buildWpDiv(url, isFixed) {
        var d = document.createElement('div');
        d.className = 'g-wp' + (isFixed ? ' is-fixed' : '');
        d.style.cssText = 'position:absolute;inset:0;background-image:url("' + url + '");background-size:cover;background-position:center;background-repeat:no-repeat;pointer-events:none;';
        return d;
    }

    function buildForegroundTargets() {
        var frag = document.createDocumentFragment();
        var targets = document.querySelectorAll('[data-glass-sync="true"]');
        for (var i = 0; i < targets.length; i++) {
            var el = targets[i];
            var rect = el.getBoundingClientRect();
            if (rect.width === 0 || rect.height === 0) continue;
            
            var docX = rect.left + window.scrollX;
            var docY = rect.top + window.scrollY;
            
            var clone = el.cloneNode(true);
            clone.removeAttribute('id');
            clone.removeAttribute('data-glass-sync');
            clone.classList.add('g-fg-target');
            
            clone.style.setProperty('position', 'absolute', 'important');
            clone.style.setProperty('left', docX + 'px', 'important');
            clone.style.setProperty('top', docY + 'px', 'important');
            clone.style.setProperty('width', rect.width + 'px', 'important');
            clone.style.setProperty('height', rect.height + 'px', 'important');
            clone.style.setProperty('margin', '0', 'important');
            
            frag.appendChild(clone);
        }
        return frag;
    }
    function fetchShapes() {
        if (shapesHTML) return true;
        var frag = document.createDocumentFragment();

        // 1) Decorative shapes (tangram) — must be appended first.
        //    Real stacking: shapes z-index:-3 < #wpSharp z-index:-2, i.e. wallpaper over
        //    shapes. In the clone both are z-index:auto -> DOM order is paint order, so
        //    shapes go in first, wallpaper last. See the injection-order note above: reversed
        //    order makes every glass card on the first home screen blur an invisible
        //    tangram.
        //    Two layers carry this markup and only one of them is visible — the source
        //    list below is not optional. See docs/GOTCHAS.md §backdrop-shape-layers.
        var shapeLayers = ['#bgShapes', '.home-section__bg'];
        for (var s = 0; s < shapeLayers.length; s++) {
            var layer = document.querySelector(shapeLayers[s]);
            if (!layer || getComputedStyle(layer).display === 'none') continue;
            var tmp = document.createElement('div');
            tmp.innerHTML = layer.innerHTML;
            while (tmp.firstChild) frag.appendChild(tmp.firstChild);
            break;
        }
        // 2) Wallpaper layer (home/guestbook background carousel; strictly excludes
        //    local miniature components such as the font preview stage)
        //    Appended last -> paints above the shapes, replicating the real first-paint
        //    "wallpaper over tangram" look
        var wp = document.getElementById('wpSharp');
        if (wp && !wp.closest('.font-preview__hero-stage') && getComputedStyle(wp).display !== 'none') {
            var bg = getComputedStyle(wp).backgroundImage;
            var url = (bg && bg !== 'none') ? extractAvifUrl(bg) : null;
            if (url) {
                var isFixed = !wp.closest('.hero');
                frag.appendChild(buildWpDiv(url, isFixed));
            }
        }

        // 3) High visual-value foreground sync nodes (avatar, photo-wall thumbnails)
        frag.appendChild(buildForegroundTargets());

        if (!frag.childNodes.length) return false;
        // Serialize into a cached HTML string, injected via innerHTML in setLive
        var div = document.createElement('div');
        div.appendChild(frag);
        shapesHTML = div.innerHTML;
        return !!shapesHTML;
    }

    // Clone gating: every card gets a 100vw×100vh compositing layer, and the browse page
    // easily has 20 post-items, all resident ≈ 100MB+ GPU memory. Gated with
    // IntersectionObserver: only cards near
    // the viewport (rootMargin 150% ≈ 1.5 screens) get clones; on exit the innerHTML
    // is released and the will-change promotion dropped. measure() only depends on
    // the .g-blur rect itself, regardless of whether a clone exists -> gating does not
    // affect positioning accuracy.
    var io = null;
    function setLive(content, on) {
        if (on) {
            if (!shapesHTML) return;
            if (content.childNodes.length && content.__cachedHTML === shapesHTML) return;
            content.innerHTML = shapesHTML;
            content.__cachedHTML = shapesHTML;
            content.style.willChange = 'transform';
        } else if (content.childNodes.length) {
            content.innerHTML = '';
            content.__cachedHTML = '';
            content.style.willChange = '';
        }
    }

    // Same-frame attach: the clone layer must be attached within the same frame, not left
    // to the IO — IO callbacks arrive at the NEXT rendering opportunity, so htmx-swapped
    // cards paint as transparent glass until the blur arrives. Within the same task: batch-read rects
    // (read phase) -> one-shot setLive (write phase); layout fires only once, so clones
    // are in place before the swap's first paint. The IO keeps releasing/restoring
    // clones as cards scroll out of/into the viewport.
    function injectShapes() {
        if (!fetchShapes()) {
            // When background layers are hidden or the page has no wallpaper/shapes
            // (e.g. navigating to /admin): clear the leftover clone layers of all
            // resident cards (e.g. #navbar) to fully cut off home-page wallpaper leakage
            for (var i = 0; i < cards.length; i++) setLive(cards[i].content, false);
            if (io) io.disconnect();
            return;
        }
        if (!window.IntersectionObserver) {         // fallback: without IO, attach everything
            for (var i = 0; i < cards.length; i++) setLive(cards[i].content, true);
            return;
        }
        if (io) io.disconnect();
        io = new IntersectionObserver(function(entries) {
            for (var i = 0; i < entries.length; i++) {
                var content = entries[i].target.__gContent;
                if (content) setLive(content, entries[i].isIntersecting);
            }
        }, { rootMargin: '150% 0px' });

        // Read phase: find cards within rootMargin('150% 0px') coverage (±1.5 screens)
        // equivalent to: rect.top < 2.5*vh && rect.bottom > -1.5*vh
        var vh = window.innerHeight || document.documentElement.clientHeight;
        var hot = [];
        for (var j = 0; j < cards.length; j++) {
            cards[j].el.__gContent = cards[j].content;
            io.observe(cards[j].el);
            var r = cards[j].el.getBoundingClientRect();
            if (r.width > 0 && r.height > 0 &&
                r.top < vh * 2.5 && r.bottom > vh * -1.5) {
                hot.push(cards[j].content);
            }
        }
        // Write phase: fill in one pass (setLive short-circuits via __cachedHTML,
        // zero cost for already-attached cards)
        for (var k = 0; k < hot.length; k++) setLive(hot[k], true);
    }

    // Parameter computation: per-card variables land on .g-content itself
    // var() inside keyframes resolves on the animated element (.g-content) -> must be
    // set on the element itself or an ancestor.
    // Returns whether maxScroll changed (refreshSd is only needed on change).
    function measure() {
        var de = document.documentElement, changed = false;
        var newMaxScroll = Math.max(0, document.body.scrollHeight - window.innerHeight);
        if (Math.abs(newMaxScroll - lastMax) > 1) {
            de.style.setProperty('--g-max-scroll', newMaxScroll + 'px');
            de.style.setProperty('--g-max-scroll-neg', -newMaxScroll + 'px');
            lastMax = newMaxScroll;
            changed = true;
        }
        // Scrollbar width: the clone layer size must align exactly with the fixed background
        // layer (inset:0 = 100vw/100vh). clientWidth shrinks to exclude the scrollbar,
        // ending up one scrollbar width short of the background layer. Use
        // innerWidth/innerHeight with or without a scrollbar, matching 100vw/100vh.
        de.style.setProperty('--g-vw', window.innerWidth + 'px');
        de.style.setProperty('--g-vh', window.innerHeight + 'px');
        for (var i = 0; i < cards.length; i++) {
            var c = cards[i];
            if (!c.el.isConnected) continue;
            // Blur rect alignment: use the .g-blur rect itself. .g-blur has an inset:-2σ
            // expansion, so its rect is 4σ larger than the card; but translate applies
            // to .g-content (origin at the .g-blur top-left), and computing from the
            // .g-blur rect cancels the expansion in the equation, aligning the clone's
            // top-left exactly with the viewport (0,0).
            var r = c.content.parentNode.getBoundingClientRect();   // = .g-filter (expander layer)
            var cardRect = c.el.getBoundingClientRect();
            var x, y0;
            if (c.mode === 'static') {
                // Viewport coordinates: fixed cards use viewport coordinates, never add scroll
                // offsets. Adding scrollY makes docY drift with scrolling, and measure()
                // also fires on non-scroll triggers (hover transitionend /
                // ResizeObserver) -> the card background would instantly jump to a wrong
                // position. A fixed card's rect is constant -> -r.left/-r.top is the
                // constant value. (A display:none overlay's zeroed rect also yields
                // 0,0, still correct once expanded)
                x = -r.left; y0 = -r.top;
            } else {
                // sd/raf use document coordinates: the card moves with the document,
                // and sd compensates +scrollY via keyframes.
                x = -(r.left + window.scrollX); y0 = -(r.top + window.scrollY);
            }
            if (Math.abs(x - c.x) < 0.5 && Math.abs(y0 - c.y0) < 0.5) continue;  // short-circuit: skip style writes when unchanged
            c.x = x; c.y0 = y0;
            if (c.mode !== 'raf') {
                c.content.style.setProperty('--g-card-x', x + 'px');
                c.content.style.setProperty('--g-card-y0', y0 + 'px');
            }
        }
        return changed;
    }

    // Keyframe refresh: batch animationName:none -> one reflow -> restore
    // scroll-driven keyframes cache var() at creation time; after a resize the var
    // updates but the animation does not refresh -> the CSS animation must be rebuilt
    // to re-parse the keyframes. One reflow total across all cards.
    function refreshSd() {
        var i, n = 0, wps = [];
        for (i = 0; i < cards.length; i++) {
            if (cards[i].mode === 'sd') { cards[i].content.style.animationName = 'none'; n++; }
            var wp = cards[i].content.querySelector('.g-wp');
            if (wp) { wp.style.animationName = 'none'; wps.push(wp); }
        }
        if (!n && !wps.length) return;
        void document.body.offsetWidth;     // force reflow (only this once)
        for (i = 0; i < cards.length; i++) {
            if (cards[i].mode === 'sd') cards[i].content.style.animationName = '';
        }
        for (i = 0; i < wps.length; i++) {
            wps[i].style.animationName = '';
        }
    }

    // raf fallback tick (runs only mode=raf cards)
    function tickRaf() {
        for (var i = 0; i < cards.length; i++) {
            var c = cards[i];
            if (c.mode !== 'raf' || !c.el.isConnected) continue;
            var r = c.content.parentNode.getBoundingClientRect();
            c.content.style.setProperty('--g-offset-x', (-r.left) + 'px');
            c.content.style.setProperty('--g-offset-y', (-r.top) + 'px');
        }
    }

    function scheduleTick() {          // only needed by raf cards (scroll-driven)
        if (rafId) return;
        rafId = requestAnimationFrame(function() { rafId = 0; tickRaf(); });
    }

    function scheduleRecalc() {        // layout changes: recompute parameters (+ refresh animations if needed)
        if (recalcId) return;
        recalcId = requestAnimationFrame(function() {
            recalcId = 0;
            // On layout changes (window resize, rewrapping), the absolute Y of fixed
            // foreground elements shifts. The cache must be cleared and re-injected so
            // clones keep aligning with real document coordinates.
            shapesHTML = '';
            injectShapes();
            
            if (measure()) refreshSd();
            tickRaf();                 // static/sd cards converge along the way, zero cost
            for (var i = 0; i < sceneACards.length; i++) {
                alignSceneA(sceneACards[i]);
            }
        });
    }

    // Re-collect after page switches (htmx swap resets scroll to top, sd animations sync automatically)
    // Stale shape cache: the shapesHTML cache must be cleared -> across pages #wpSharp/#bgShapes
    // may appear/disappear, and a stale cache could wrongly pour the wallpaper into
    // non-home cards. fetchShapes re-detects and rebuilds.
    function rescan() {
        shapesHTML = '';
        collectCards();
        injectShapes();
        scheduleRecalc();
    }

    function boot() {
        collectCards();
        injectShapes();
        scheduleRecalc();
        // Scene A wallpaper: recompute + re-attach clones after lazy/decode
        // (Lazy wallpaper: the wallpaper URL can only be extracted from
        // #wpSharp.backgroundImage after hero__bg loads, otherwise shapesHTML stays empty)
        sceneACards.forEach(function(card) {
            var img = card.querySelector('.hero__bg');
            if (img && !img.complete) {
                var rewire = function() {
                    shapesHTML = '';      // clear cache -> fetchShapes re-reads from #wpSharp
                    injectShapes();
                    scheduleRecalc();
                };
                img.addEventListener('load', rewire, { once: true });
                img.addEventListener('error', rewire, { once: true });
            }
        });
    }

    // Recompute triggers (complete list, all merged via rAF)
    window.addEventListener('resize', scheduleRecalc);                  // viewport vw/vh
    window.addEventListener('orientationchange', scheduleRecalc);
    if (window.ResizeObserver) {                                        // content height changes: swap/comments/collapse/image growth
        var ro = new ResizeObserver(scheduleRecalc);
        ro.observe(document.body);
        var w = document.getElementById('content-wrapper');
        if (w) ro.observe(w);
    }
    document.addEventListener('load', scheduleRecalc, true);            // capture phase: img load does not bubble
    if (document.fonts && document.fonts.ready) {
        document.fonts.ready.then(scheduleRecalc);                      // fonts settle -> docY moves
    }
    window.addEventListener('load', scheduleRecalc);                    // all resources ready
    document.addEventListener('htmx:after:swap', rescan);
    document.addEventListener('htmx:after:settle', scheduleRecalc);     // final layout state

    // Event timing: htmx events alone are always one frame late — htmx:after:swap arrives
    //   after the frame that paints the new cards, so attaching there leaves them visibly
    //   transparent for that frame. Attach at the END of the same task as the DOM
    //   insertion instead: MutationObserver callbacks are microtasks, which meets that
    //   timing naturally. The filter keeps a rescan down to cases where a glass card was
    //   really inserted, so setLive's own innerHTML writes never recurse.
    if (window.MutationObserver) {
        new MutationObserver(function(records) {
            for (var i = 0; i < records.length; i++) {
                var added = records[i].addedNodes;
                for (var j = 0; j < added.length; j++) {
                    var node = added[j];
                    if (node.nodeType !== 1) continue;
                    if (node.hasAttribute('data-backdrop-scene') ||
                        node.querySelector('[data-backdrop-scene]')) {
                        rescan();
                        return;
                    }
                }
            }
        }).observe(document.body, { childList: true, subtree: true });
    }
    // Track CSS transitions in real time (fixes blur desync during expand/collapse)
    var activeTransitions = 0;
    function loopTransition() {
        if (activeTransitions > 0) {
            scheduleRecalc();
            requestAnimationFrame(loopTransition);
        }
    }
    
    document.addEventListener('transitionstart', function(e) {
        var tag = (e.target.tagName || '').toLowerCase();
        // Ignore common small UI elements; only capture structural containers that may change layout or position
        if (tag === 'a' || tag === 'button' || tag === 'svg' || tag === 'path' || tag === 'span' || tag === 'li') return;
        if (activeTransitions === 0) requestAnimationFrame(loopTransition);
        activeTransitions++;
    }, true);
    
    function endTransition(e) {
        var tag = (e.target.tagName || '').toLowerCase();
        if (tag === 'a' || tag === 'button' || tag === 'svg' || tag === 'path' || tag === 'span' || tag === 'li') return;
        activeTransitions = Math.max(0, activeTransitions - 1);
        if (activeTransitions === 0) scheduleRecalc();
    }
    
    document.addEventListener('transitionend', endTransition, true);
    document.addEventListener('transitioncancel', endTransition, true);
    document.addEventListener('animationend', scheduleRecalc, true);    // converge when entrance animations end
    if (window.MutationObserver) {
        new MutationObserver(scheduleRecalc).observe(document.documentElement,
            { attributes: true, attributeFilter: ['class', 'data-theme'] });  // theme/body class
    }
    window.__backdropRecalc = scheduleRecalc;                           // parameter recompute for comments/waterfall etc.
    window.__backdropRescan = rescan;                                   // re-collect after glass cards added dynamically from JS
    window.addEventListener('scroll', scheduleTick, { passive: true }); // only needed by raf cards

    boot();
})();
