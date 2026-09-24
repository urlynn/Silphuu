// Right column "mode shape" transition
// Animates the #navRight shape switch between browse/non-browse pages
// (⌘K+🔍 ↔ 🔍+⌘K+grid), fixed on the pop scheme (overshoot spring + scale snap).
// Event order: before:swap(old DOM) ->
// DOM swap -> after:swap(new DOM, updateVisibility has already toggled .browse in an
// earlier listener) -> before:settle -> after:settle.
// FLIP captures the old rect at before:swap and measures the new rect + plays at after:swap.
(function() {
    var navRight = document.getElementById('navRight');
    var navbar = document.getElementById('navbar');
    var ELS = ['.nav-right-icons', '#navFilterBtn'];
    var reduceMQ = window.matchMedia('(prefers-reduced-motion: reduce)');

    // FLIP state
    var oldRects = null;     // old positions captured at before:swap
    var wasBrowse = false;   // browse mode at before:swap

    function collectRects() {
        var r = {};
        r.right = navRight.getBoundingClientRect();
        ELS.forEach(function(sel) {
            var el = navRight.querySelector(sel);
            if (el) r[sel] = el.getBoundingClientRect();
        });
        return r;
    }

    function canAnimate() {
        if (reduceMQ.matches) return false;
        if (navbar && navbar.classList.contains('scrolled')) return false;
        return true;
    }

    document.addEventListener('htmx:before:swap', function(e) {
        if (!navRight) return;
        wasBrowse = window.__browseMode ? window.__browseMode() : false;
        if (!canAnimate()) { oldRects = null; return; }
        oldRects = collectRects();
    });

    document.addEventListener('htmx:after:swap', function() {
        if (!navRight || !oldRects) { oldRects = null; return; }
        var isBrowse = window.__browseMode ? window.__browseMode() : false;
        if (wasBrowse === isBrowse) { oldRects = null; return; } // mode unchanged -> no animation

        // Key: freeze the bubble width transition first so child elements are measured
        // at their "final width" resting spots. Otherwise, measuring while the --rw
        // 70↔108 transition is running reads positions at the transition's start
        // (width still ≈ old value) and FLIP measures wrong -> slide misalignment /
        // failed settle.
        var oldBubble = oldRects.right;
        navRight.style.transition = 'none';
        void navRight.offsetWidth;
        var news = collectRects();
        var newBubble = news.right;

        // Bubble width smooth stretch (pop: overshoot spring; fill:none returns to the CSS var(--rw) final value)
        if (oldBubble.width !== newBubble.width) {
            navRight.animate(
                [{ width: oldBubble.width + 'px' }, { width: newBubble.width + 'px' }],
                { duration: 550, easing: 'cubic-bezier(0.34,1.56,0.64,1)', fill: 'none' }
            );
        }

        // FLIP-lite: pin at the old spot (no transition) -> force reflow -> release, and
        // the CSS transition springs to the new spot (pop adds an extra scale)
        ELS.forEach(function(sel) {
            var el = navRight.querySelector(sel);
            var o = oldRects[sel], n = news[sel];
            if (!el || !o || !n) return;
            var dx = o.left - n.left;
            var dy = o.top - n.top;
            if (dx === 0 && dy === 0) return;
            el.style.transition = 'none';
            el.style.transform = 'translate(' + dx + 'px,' + dy + 'px) scale(0.85)';
        });
        void navRight.offsetWidth; // commit the pins
        requestAnimationFrame(function() {
            ELS.forEach(function(sel) {
                var el = navRight.querySelector(sel);
                if (!el) return;
                el.style.transition = '';
                el.style.transform = '';
            });
        });

        // Release the bubble width transition (WAAPI still controls width; the CSS
        // final value matches the WAAPI endpoint -> no jump)
        requestAnimationFrame(function() {
            navRight.style.transition = '';
            navRight.style.width = '';
        });
        oldRects = null;
    });
})();

// F5 first load: article cards + info card each play one "no old card" entrance.
// htmx page switches do not re-dispatch app-ready, so this fires only on a full load.
document.addEventListener('app-ready', function() {
    var area = document.getElementById('browse-body-area');
    if (!area) return;

    // Article cards F5 fly-in: reuse the swap's setupCardAnimations with oldCards=null
    // (no old-card clone, pure fly-in). rhythm/direction read from the same source as
    // before:swap (data-anim-*), falling back to arc / right-bottom when missing.
    // The function adds the animation class only after first paint (double rAF),
    // matching htmx switch behavior -> does not trigger the frosted-glass transparency bug.
    // The archive page has no .card-row-wrapper; setupCardAnimations returns directly
    // inside, safe to skip.
    var wrappers = area.querySelectorAll('.card-row-wrapper');
    if (wrappers.length && typeof window.setupCardAnimations === 'function') {
        var f5Rhythm = document.documentElement.getAttribute('data-anim-rhythm') || 'damped';
        var f5Direction = document.documentElement.getAttribute('data-anim-direction') || 'right-only';
        window.setupCardAnimations(area, f5Rhythm, f5Direction, null);
    }

    // Info card: keep the existing F5 behavior, unchanged.
    var header = area.querySelector('.browse-header');
    if (header && typeof window.setupBrowseHeaderAnimation === 'function') {
        window.setupBrowseHeaderAnimation(area, null);
    }
});


