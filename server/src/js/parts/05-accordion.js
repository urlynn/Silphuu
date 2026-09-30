// Category accordion: click to expand/collapse (mutually exclusive)
function initCatAccordion() {
    document.querySelectorAll('.cat-header').forEach(function(header) {
        if (header._catAccordionInit) return;
        header._catAccordionInit = true;
        header.addEventListener('click', function() {
            var currentGroup = this.closest('.cat-group');
            var currentBody = currentGroup.querySelector('.cat-body');
            var currentArrow = currentGroup.querySelector('.cat-arrow');
            var isOpen = currentBody.classList.contains('open');

            // Close all categories first (mutually exclusive accordion)
            document.querySelectorAll('.cat-group').forEach(function(g) {
                g.classList.remove('open');
                var b = g.querySelector('.cat-body');
                var a = g.querySelector('.cat-arrow');
                if (b) b.classList.remove('open');
                if (a) a.classList.remove('open');
            });

            // If it was closed, expand it; otherwise keep it closed
            if (!isOpen) {
                currentGroup.classList.add('open');
                currentBody.classList.add('open');
                if (currentArrow) currentArrow.classList.add('open');
            }
        });
    });
}
initCatAccordion();
document.addEventListener('htmx:after:swap', initCatAccordion);

// With unified navigation semantics the post sidebar is a full-page swap: the active
// state and accordion expansion are server-rendered, no JS sync needed; the old
// post-main fragment listener is gone.

// After htmx page switches, new elements like the comment form have no listeners:
// init runs immediately + re-runs after swap; per-element flags prevent duplicate binding
function reinitOnSwap(init) { init(); document.addEventListener('htmx:after:swap', init); }

// Body class sync (fixes body class leakage after HTMX page switches)
// A normal page switch replaces #content-wrapper and leaves <body> alone, but history
// restore swaps the entire <body> (target=body, swap=outerSync), clearing page-level
// and session-level classes together, so everything is rebuilt here. Otherwise body's
// class stays at the initial full-page-load page class (e.g. home), keeping page-level
// styles like `body.home > .bg-shapes { display:none }` active on non-home pages and
// breaking home-specific styles like `body.home main.main-content` padding / scroll-snap.
// After every swap, sync body's class to #content-wrapper's data-body-class while
// preserving JS-injected app-ready (and other non-page classes if any).
function syncBodyClass() {
    var wrapper = document.getElementById('content-wrapper');
    if (!wrapper) return;
    var newClass = (wrapper.getAttribute('data-body-class') || '').trim();
    var classes = newClass ? newClass.split(/\s+/) : [];
    // app-ready is session-level (see markAppReady in 12-lifecycle.js): testing the
    // current body would drop it for good on a restore that replaces the body.
    if (window.__appReady) classes.push('app-ready');
    document.body.className = classes.join(' ');
}
syncBodyClass();
document.addEventListener('htmx:after:swap', syncBodyClass);
document.addEventListener('htmx:before:swap', function(e) {
    // Pre-clean home-specific classes and background styles at the swap instant, so the
    // new page is not polluted by body.home styles or the home wallpaper at mount
    // (which would leave a ghost)
    if (document.body.classList.contains('home')) {
        document.body.classList.remove('home');
        var homeBgStyle = document.getElementById('home-bg-style');
        if (homeBgStyle) homeBgStyle.remove();
    }
});

// Post list page switches: unified boost semantics
//   Cards and chips are plain links: the body's hx-boost takes over and swaps the whole
//   #content-wrapper. Every swap goes through htmx:before:swap / after:swap, with
//   setupCardAnimations playing the switch animation.
//   The post page sidebar works the same way: plain <a href>, active state
//   server-rendered, scrolling handled by navbar after:settle.

