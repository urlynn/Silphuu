/* navbar.js: top nav bubble dynamics, signature shattering, and measurement engine */

/* Dropdown hover listeners (standalone IIFE, not guarded by _navInitialized) */
(function() {
    document.querySelectorAll('.nav-bubble--center .nav-group').forEach(function(g) {
        if (g._navHoverInit) return;
        g._navHoverInit = true;
        g.addEventListener('mouseenter', function() { g.classList.add('nav-group--hover'); });
        g.addEventListener('mouseleave', function() {
            g.classList.remove('nav-group--hover');
            if (g.contains(document.activeElement)) document.activeElement.blur();
        });
    });
    /* Hover isolation: add .nav-hovering when the mouse enters the center bubble, remove on leave */
    var center = document.querySelector('.nav-bubble--center');
    if (center && !center._navHoveringInit) {
        center._navHoveringInit = true;
        center.addEventListener('mouseenter', function() { center.classList.add('nav-hovering'); });
        center.addEventListener('mouseleave', function() { center.classList.remove('nav-hovering'); });
    }
})();

(function() {
    var nb = document.getElementById('navbar');
    if (!nb) return;
    if (window._navInitialized) return;
    window._navInitialized = true;

    /* Page table: l = signature (mood line), w = signature pixel width (0 = auto-measured on first entry) */
    var NAV_SIG = [];
    try { NAV_SIG = JSON.parse(document.getElementById('nav-signatures').textContent || '[]'); } catch (e) { console.error('[nav] 解析 nav-signatures 失败', e); }
    var PAGES = {};
    NAV_SIG.forEach(function (s) {
        PAGES[s.path] = { l: s.label.text + (s.label.kaomoji || ''), w: s.w || 0 };
    });

    /* Animation: per-character shatter + clip-path sweep-in (WAAPI) */
    function _shatter(oldEl, oldDur, fn) {
        var text = oldEl.textContent, chars = text.split(''), len = chars.length, frag = [];
        oldEl.style.color = 'transparent';
        var cs = getComputedStyle(oldEl);
        var fontCss = 'font-family:' + cs.fontFamily + ';font-size:' + cs.fontSize + ';font-weight:' + cs.fontWeight + ';';
        var dur = oldDur * 1000, step = len > 1 ? Math.floor(dur / len) : 0;
        for (var i = 0; i < len; i++) {
            var s = document.createElement('span');
            s.innerHTML = symHTML(chars[i]);
            s.style.cssText = 'position:absolute;top:0;left:0;' + fontCss + 'white-space:pre;opacity:1;pointer-events:none;color:var(--text-primary)';
            oldEl.appendChild(s);
            var p = document.createElement('span');
            p.innerHTML = symHTML(text.slice(0, i));
            p.style.cssText = 'position:absolute;left:-9999px;top:-9999px;visibility:hidden;white-space:nowrap;' + fontCss;
            document.body.appendChild(p);
            s.style.left = p.getBoundingClientRect().width + 'px';
            document.body.removeChild(p);
            fn(s, i, len, step, dur);
            frag.push(s);
        }
        oldEl._fragments = frag;
    }
    var _newKF = function() { return [{ clipPath: 'inset(0 99% 0 0)' }, { clipPath: 'inset(0 0 0 0)' }]; };
    var _newOpt = function(d) { return { duration: d * 1000, delay: 200, easing: 'linear', fill: 'both' }; };

    /* Page identification: pathname */
    function getPage() { return location.pathname; }

    /* UI update: active states + signature text + width application */
    function updateUI() {
        var p = getPage();
        var P = window.SITE_URLS.pages;
        document.querySelectorAll('.nav-bubble--center a.nav-item').forEach(function(a) {
            var pp = new URL(a.href).pathname;
            a.classList.toggle('active', pp === p);
        });
        var archiveBtn = document.querySelector('.nav-bubble--center .nav-item[data-icon="archive"]');
        if (archiveBtn) archiveBtn.classList.toggle('active', p === P.archive || p.indexOf(P.post + '/') === 0);
        var aboutBtn = document.querySelector('.nav-bubble--center .nav-item[data-icon="about"]');
        if (aboutBtn) aboutBtn.classList.toggle('active', [P.about, P.friends, P.sponsor].indexOf(p) >= 0);
        var funBtn = document.querySelector('.nav-bubble--center .nav-item[data-icon="fun"]');
        if (funBtn) funBtn.classList.toggle('active', [P.guestbook, P.funError].indexOf(p) >= 0);
        var lb = document.querySelector('.nav-bubble--left .labl');
        if (lb) { var pg = PAGES[p] || PAGES['/']; if (pg) lb.innerHTML = symHTML(pg.l); }
        applyLablWidth(p);
    }

    function applyLablWidth(p) {
        var pg = PAGES[p] || PAGES['/'];
        if (nb && pg && pg.w) { nb.style.setProperty('--labl-w', (pg.w + 10) + 'px'); }
    }

    function navRemeasure(silent) {
        var real = document.querySelector('.nav-bubble--left .labl');
        if (!real) return PAGES;
        var cs = getComputedStyle(real);
        var probe = document.createElement('span');
        probe.style.cssText = 'position:absolute;left:-9999px;top:-9999px;visibility:hidden;white-space:nowrap;'
            + 'font-family:' + cs.fontFamily + ';font-size:' + cs.fontSize + ';font-weight:' + cs.fontWeight + ';letter-spacing:' + cs.letterSpacing;
        document.body.appendChild(probe);
        Object.keys(PAGES).forEach(function(k) {
            probe.innerHTML = symHTML(PAGES[k].l);
            PAGES[k].w = Math.ceil(probe.getBoundingClientRect().width);
        });
        document.body.removeChild(probe);
        applyLablWidth(getPage());
        if (!silent) {
            var out = {};
            Object.keys(PAGES).forEach(function(k) { out[k] = PAGES[k].w; });
            console.log('[nav] 重测完成: ' + JSON.stringify(out));
        }
        return PAGES;
    }
    window.navRemeasure = navRemeasure;

    function measureViewport() {
        var ns = document.getElementById('navSegments');
        if (!ns || !nb) return;
        var rw = ns.getBoundingClientRect().width;
        var half = parseFloat(getComputedStyle(nb).getPropertyValue('--center-half')) || 140;
        var gap = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--nav-gap')) || 8;
        var reach = rw / 2 - half - gap;
        // Both bubbles anchor on their own edge, so each travels (reach - collapsed width).
        nb.style.setProperty('--collapse-left-tx', (reach - 38) + 'px');
        nb.style.setProperty('--collapse-right-tx', (-(reach - 38)) + 'px');
    }

    function measureCenterHalf() {
        var center = document.getElementById('navCenter');
        if (center && nb) {
            var cw = center.getBoundingClientRect().width;
            var half = cw / 2;
            nb.style.setProperty('--center-half', half + 'px');
            measureViewport();
        }
    }
    window.__navApplyCenterHalf = measureCenterHalf;

    var scrollLocked = false;
    var scrollLockTimer = null;

    (function() {
        var SI = 80, SO = 40;
        var isCompact = nb.classList.contains('scrolled');
        var scrollFrame = null;
        function syncCompactState() {
            if (scrollLocked) return;
            var next = isCompact;
            if (scrollY > SI) next = true;
            else if (scrollY < SO) next = false;
            if (next === isCompact) return;
            isCompact = next;
            nb.classList.toggle('scrolled', isCompact);
            if (isCompact && window.__navLeftCleanup) window.__navLeftCleanup();
        }
        function handleScroll() {
            if (scrollFrame === null) {
                scrollFrame = requestAnimationFrame(function() {
                    scrollFrame = null;
                    syncCompactState();
                    syncHeroOff();
                });
            }
        }
        // Hero wallpaper state (Nav consumes --hero-*) applies only at page top; past
        // half a screen the Nav smoothly returns to theme material.
        // (Class on documentElement: .chromium also sits on html, so CSS selectors must
        // compound at the same level)
        var heroOff = false;
        function syncHeroOff() {
            var next = scrollY > innerHeight * 0.5;
            if (next === heroOff) return;
            heroOff = next;
            document.documentElement.classList.toggle('hero-off', next);
        }
        addEventListener('scroll', handleScroll, { passive: true });
        syncCompactState();
    })();

    var resizeTimer;
    addEventListener('resize', function() {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(function() {
            measureViewport();
        }, 200);
    });

    var navWasScrolled = false;
    var savedOldPage = '/';

    function isBrowsePath(p) {
        var P = window.SITE_URLS.pages;
        return p === P.archive || p === P.posts || p.indexOf(P.topic) === 0;
    }
    window.isBrowsePath = isBrowsePath;

    document.addEventListener('click', function(e) {
        var a = e.target && e.target.closest ? e.target.closest('a') : null;
        if (!a) return;

        var curHref = a.getAttribute('href') || '';
        if (curHref && curHref.charAt(0) === '/') {
            try {
                var curUrl = new URL(curHref, location.href);
                if (curUrl.pathname === location.pathname && curUrl.search === location.search) {
                    e.preventDefault();
                    e.stopPropagation();
                    // A fragment-only difference is a same-document move: setting the hash fires
                    // hashchange (the /posts tag filter listens for it) without htmx re-fetching a
                    // page it already has.
                    if (curUrl.hash !== location.hash) location.hash = curUrl.hash;
                    return;
                }
            } catch (err) {}
        }
    }, true);

    document.body.addEventListener('htmx:before:request', function() {
        navWasScrolled = !!(nb && nb.classList.contains('scrolled'));
        savedOldPage = getPage();
    });

    document.body.addEventListener('htmx:before:swap', function(evt) {
        var ctx = evt.detail && evt.detail.ctx;
        var src = ctx && ctx.sourceElement;
        var isPageSwap = !!(ctx && ctx.target && ctx.target.id === 'content-wrapper');
        var href = src && src.getAttribute ? (src.getAttribute('href') || '') : '';
        var isBrowseNav = false;
        try {
            isBrowseNav = window.__wasBrowse && window.isBrowsePath(new URL(href, location.href).pathname);
        } catch (err) {}
        if (!isPageSwap || isBrowseNav) return;
        scrollLocked = true;
        clearTimeout(scrollLockTimer);
        scrollLockTimer = setTimeout(function() { scrollLocked = false; }, 2000);
        var segs = document.querySelectorAll('.nav-bubble');
        for (var i = 0; i < segs.length; i++) { segs[i].style.transition = 'none'; }
        if (document.body.dataset.persistClass) {
            document.body.className = document.body.dataset.persistClass;
        } else {
            // Wipes page-type classes too; read data-body-class instead — docs/GOTCHAS.md §body-class-swap
            document.body.removeAttribute('class');
        }
        document.body.classList.add('app-ready');
    });

    document.body.addEventListener('htmx:after:settle', function(evt) {
        var isBrowseNav = window.__wasBrowse && !!document.getElementById('browse-body-area');

        document.querySelectorAll('.nav-group--hover').forEach(function(g) {
            g.classList.remove('nav-group--hover');
        });

        var swapTarget = evt.target;
        var swapTargetId = swapTarget && swapTarget.id;
        if (swapTargetId !== 'content-wrapper') return;

        var cw = document.getElementById('content-wrapper');
        if (cw && cw.dataset.pageTitle) {
            document.title = cw.dataset.pageTitle;
            delete cw.dataset.pageTitle;
        }
        document.documentElement.dataset.visited = '1';

        if (isBrowseNav) {
            if (navWasScrolled) { updateUI(); return; }
        } else {
            var lb = document.getElementById('navLeft');
            if (!nb || !lb) { scrollLocked = false; return; }

            clearTimeout(scrollLockTimer);
            scrollLocked = false;

            if (cw && cw.dataset.bodyClass) {
                document.body.className = cw.dataset.bodyClass;
            }
            document.body.classList.add('app-ready');
            // Must pass explicit behavior:'instant': fonts.css has
            // `html { scroll-behavior: smooth }`, which would turn this jump-to-top into a
            // long animation from the previous page's scroll position (most visible on
            // Safari). Entering from outside = discard the scroll position entirely; only
            // internal post/* switches smooth-scroll (pendingPageScroll in 12-lifecycle.js).
            // 'instant' overrides only this call, not the global smooth scrolling that
            // TOC anchor jumps rely on.
            window.scrollTo({ top: 0, behavior: 'instant' });

            var segs = document.querySelectorAll('.nav-bubble');
            for (var i = 0; i < segs.length; i++) { segs[i].style.transition = ''; }
        }

        if (!navWasScrolled) {
            var p = getPage(), pg = PAGES[p] || PAGES['/'];
            var labl = document.querySelector('.nav-bubble--left .labl');
            var lablWrap = labl ? labl.parentElement : null;
            var link = lablWrap ? lablWrap.parentElement : null;
            var seg = document.getElementById('navLeft');

            if (!labl || !lablWrap || !link || !seg) { updateUI(); return; }

            seg.getAnimations().forEach(function(a) { a.cancel(); });
            seg.style.width = ''; seg.style.maxWidth = ''; seg.style.transition = '';
            labl.style.clipPath = ''; labl.style.transition = ''; labl.style.opacity = '';
            labl.style.transform = ''; labl.style.filter = '';
            lablWrap.style.transition = ''; lablWrap.style.maxWidth = ''; lablWrap.style.overflow = '';
            link.style.overflow = '';
            seg.style.overflow = '';
            var stale = lablWrap.querySelector('.labl-old');
            if (stale) {
                if (stale._fragments) { stale._fragments.forEach(function(s) { s.remove(); }); stale._fragments = null; }
                stale.remove();
            }

            // A site without config/nav_signatures.json ships an empty table: there is no
            // signature to slide to, so keep the server-rendered label.
            if (!pg) { updateUI(); return; }

            var oldText = labl.textContent;
            if (oldText === pg.l) { updateUI(); return; }

            var oldPG = PAGES[savedOldPage] || PAGES['/'] || pg;

            seg.style.maxWidth = 'none'; seg.style.width = '';
            void seg.offsetWidth;
            var oldSegW = seg.getBoundingClientRect().width;

            document.querySelectorAll('.nav-bubble--center a.nav-item').forEach(function(a) {
                var pp = new URL(a.href).pathname;
                a.classList.toggle('active', pp === p);
            });
            var P = window.SITE_URLS.pages;
            var archiveBtn = document.querySelector('.nav-bubble--center .nav-item[data-icon="archive"]');
            if (archiveBtn) archiveBtn.classList.toggle('active', p === P.archive || p.indexOf(P.topic) === 0);
            var aboutBtn = document.querySelector('.nav-bubble--center .nav-item[data-icon="about"]');
            if (aboutBtn) aboutBtn.classList.toggle('active', [P.about, P.friends, P.sponsor].indexOf(p) >= 0);
            var funBtn = document.querySelector('.nav-bubble--center .nav-item[data-icon="fun"]');
            if (funBtn) funBtn.classList.toggle('active', [P.guestbook, P.funError].indexOf(p) >= 0);

            applyLablWidth(p);
            labl.innerHTML = symHTML(pg.l);
            var newSegW = seg.getBoundingClientRect().width;

            seg.style.transition = 'none'; seg.style.width = oldSegW + 'px'; seg.style.maxWidth = oldSegW + 'px';
            lablWrap.style.transition = 'none'; lablWrap.style.maxWidth = '';

            var oldEl = document.createElement('span');
            oldEl.className = 'labl-old';
            oldEl.innerHTML = symHTML(oldText);
            lablWrap.appendChild(oldEl);

            link.style.overflow = 'visible';
            lablWrap.style.overflow = 'visible';
            if (newSegW < oldSegW) seg.style.overflow = 'hidden';
            void seg.offsetWidth;

            var SPEED = 256, oldDur = oldPG.w / SPEED, newDur = pg.w / SPEED;
            var animMax = Math.max(1.5, oldDur, newDur + 0.2) + 0.5;
            var cleaned = false;
            var lablAnim = null, segAnim = null;

            function cleanup() {
                if (cleaned) return; cleaned = true;
                seg.style.transition = 'none';
                lablWrap.style.transition = 'none';
                seg.style.width = newSegW + 'px';
                seg.style.maxWidth = newSegW + 'px';
                if (lablAnim) { lablAnim.cancel(); lablAnim = null; }
                if (segAnim) { segAnim.cancel(); segAnim = null; }
                if (oldEl._fragments) { oldEl._fragments.forEach(function(s) { if (s.parentElement) s.remove(); }); oldEl._fragments = null; }
                link.style.overflow = '';
                lablWrap.style.overflow = '';
                seg.style.overflow = '';
                if (oldEl.parentElement) oldEl.remove();
                window.__navLeftCleanup = null;
                void seg.offsetWidth;
                seg.style.width = ''; seg.style.maxWidth = '';
                lablWrap.style.transition = '';
                seg.style.transition = '';
                labl.style.clipPath = ''; labl.style.transition = '';
                labl.style.opacity = ''; labl.style.transform = ''; labl.style.filter = '';
            }
            window.__navLeftCleanup = cleanup;

            var isExpand = newSegW > oldSegW;
            var bubbleDur = Math.max(1000, Math.max(oldDur, newDur + 0.2) * 1000);

            _shatter(oldEl, oldDur, function(s, i, l, step) {
                s.animate([
                    { transform: 'scale(1) rotate(0deg)', opacity: 1 },
                    { transform: 'scale(0) rotate(' + ((Math.random() - 0.5) * 60) + 'deg)', opacity: 0 }
                ], { duration: Math.max(step * 2.5, 200), delay: i * step, easing: 'ease-in', fill: 'forwards' });
            });

            requestAnimationFrame(function() {
                if (cleaned) return;
                if (isExpand) {
                    var slack = (oldSegW - 60) / SPEED * 1000;
                    var grow = (newSegW - oldSegW) / SPEED * 1000;
                    segAnim = seg.animate([
                        { width: oldSegW + 'px', maxWidth: oldSegW + 'px' },
                        { width: newSegW + 'px', maxWidth: newSegW + 'px' }
                    ], { duration: Math.max(1, grow), delay: 200 + slack, easing: 'linear', fill: 'both' });
                } else {
                    segAnim = seg.animate([
                        { width: oldSegW + 'px', maxWidth: oldSegW + 'px' },
                        { width: newSegW + 'px', maxWidth: newSegW + 'px' }
                    ], { duration: bubbleDur, easing: 'cubic-bezier(0,0,.1,1)', fill: 'forwards' });
                }
                oldEl.style.clipPath = 'inset(0 0 0 0)'; oldEl.style.opacity = '1';
                lablAnim = labl.animate(_newKF(), _newOpt(newDur));
            });

            setTimeout(cleanup, animMax * 1000);
            return;
        }

        var reduceMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;
        if (reduceMotion) { nb.classList.remove('scrolled'); updateUI(); return; }

        if (!nb.classList.contains('scrolled')) nb.classList.add('scrolled');
        updateUI();
        void lb.offsetWidth;

        var done = false;
        function reveal() { if (done) return; done = true; lb.removeEventListener('transitionend', onEnd); updateUI(); }
        function onEnd(e) { if (e.target !== lb) return; reveal(); }
        lb.addEventListener('transitionend', onEnd);
        nb.classList.remove('scrolled');
        setTimeout(reveal, 1200);
    });

    function onAppReady() {
        updateUI();
        navRemeasure(true);
        measureCenterHalf();
        requestAnimationFrame(function() {
            if (nb) nb.classList.add('intro');
            setTimeout(function() { if (nb) { nb.classList.remove('intro'); nb.classList.add('nav-ready'); } }, 1600);
        });
    }
    if (document.body.classList.contains('app-ready')) {
        onAppReady();
    } else {
        document.addEventListener('app-ready', onAppReady);
    }
})();
