// View Transitions stay disabled: page-switch animation here is entirely JS-driven, and
// htmx's VT would contend for the same pseudo-element layer.
// See docs/GOTCHAS.md §vt-disabled-by-design.
if (window.htmx) {
    htmx.config.transitions = false;
    // 4xx/5xx responses are not swapped into the DOM.
    htmx.config.noSwap.push("4xx", "5xx");
}

// No-transition stub: run the callback (the swap) directly, await its completion, and
// return the interface htmx expects.
// No screenshots/pseudo-elements -> no transition animation, no snapshot residue, no queue deadlock.
if (document.startViewTransition) {
    document.startViewTransition = function(callback) {
        var done = Promise.resolve().then(callback);
        return {
            finished: done,
            ready: done,
            updateCallbackDone: done,
            skipTransition: function() {}
        };
    };
}

// Page-switch animations: JS-driven, not VT
// With unified boost semantics, every site page switch is a full #content-wrapper
// swap; card switch animations trigger automatically via setupCardAnimations
// (arc/damped) or the archive ASLIDE, keyed on the DOM browse state
// (window.__wasBrowse) rather than the hx-target string.
// 100% match card-swipe-demo.html: old card injected INTO wrapper (not body),
// both cards position:absolute, old recedes + fades, new flies in via CSS animation
// Viewport progressive reveal engine
// On page switch / F5: only cards "inside the viewport" immediately play the
// replacement animation (including the old-card clone morph);
// "outside the viewport" cards hide first and play their entrance via
// IntersectionObserver when scrolled into view.
// The old "full stagger by --i" became "visibility-driven" -> newly scrolled-to posts
// replace/appear in sequence.
var _browseRevealIO = null;
function getRevealIO() {
    if (_browseRevealIO) return _browseRevealIO;
    _browseRevealIO = new IntersectionObserver(function(entries) {
        var rhythm = document.documentElement.getAttribute('data-anim-rhythm') || 'damped';
        entries.forEach(function(en) {
            if (!en.isIntersecting) return;
            var wrapper = en.target;
            var card = wrapper.querySelector('.post-item');
            if (card && card.dataset.revealed !== '1') {
                card.dataset.revealed = '1';
                playCardEntrance(card, rhythm);
                _browseRevealIO.unobserve(wrapper);
            }
        });
    }, { rootMargin: '0px 0px -8% 0px', threshold: 0.08 });
    return _browseRevealIO;
}

// Single-card entrance (no old-card clone): used for scroll reveal. --i set to 0 to bypass arc/damped stagger delays.
function playCardEntrance(card, rhythm) {
    card.style.setProperty('--i', '0');
    card.style.setProperty('--tx-stack', '0px');
    card.style.setProperty('--ty-stack', '0px');
    card.style.setProperty('--fly-x', '-800px');
    card.style.zIndex = '12';
    card.style.transition = 'none';
    requestAnimationFrame(function() {
        requestAnimationFrame(function() {
            card.classList.add('anim-' + rhythm + '-new');
        });
    });
    setTimeout(function() {
        card.classList.remove('anim-' + rhythm + '-new');
        card.style.opacity = '';
        card.style.removeProperty('--i');
        card.style.removeProperty('--tx-stack');
        card.style.removeProperty('--ty-stack');
        card.style.removeProperty('--fly-x');
        card.style.zIndex = '';
        card.style.transition = '';
    }, 1200);
}

function setupCardAnimations(tgt, rhythm, direction, oldCards) {
    var wrappers = Array.from(tgt.querySelectorAll('.card-row-wrapper'));
    if (!wrappers.length) return;

    // Without IntersectionObserver: fall back to the original full stagger behavior, avoiding cards hidden forever.
    if (!('IntersectionObserver' in window)) {
        var sampleCard0 = wrappers[0].querySelector('.post-item');
        var CARD_WIDTH0 = sampleCard0 ? sampleCard0.offsetWidth : 300;
        var CARD_HEIGHT0 = sampleCard0 ? sampleCard0.offsetHeight : 150;
        var scale0 = 0.96, E0 = 15;
        var Wd0 = CARD_WIDTH0 * (1 - scale0) / 2, Hd0 = CARD_HEIGHT0 * (1 - scale0) / 2;
        var tv0 = E0 + Wd0, tyv0 = E0 + Hd0;
        var tx0 = 0, ty0 = 0;
        if (direction === 'right-top') { tx0 = -tv0; ty0 = tyv0; }
        else if (direction === 'right-bottom') { tx0 = -tv0; ty0 = -tyv0; }
        else if (direction === 'right-only') { tx0 = -tv0; ty0 = 0; }
        for (var b = 0; b < wrappers.length; b++) {
            (function(idx) {
                setTimeout(function() {
                    animateRowSwitch(idx, wrappers, oldCards, rhythm, tx0, ty0, '-800px');
                }, idx * 100);
            })(b);
        }
        return;
    }

    var sampleCard = wrappers[0].querySelector('.post-item');
    var CARD_WIDTH = sampleCard ? sampleCard.offsetWidth : 300;
    var CARD_HEIGHT = sampleCard ? sampleCard.offsetHeight : 150;
    var scale = 0.96;
    var E = 15;
    var W_diff = CARD_WIDTH * (1 - scale) / 2;
    var H_diff = CARD_HEIGHT * (1 - scale) / 2;
    var tx_val = E + W_diff;
    var ty_val = E + H_diff;

    var tx = 0, ty = 0;
    if (direction === 'right-top') { tx = -tx_val; ty = ty_val; }
    else if (direction === 'right-bottom') { tx = -tx_val; ty = -ty_val; }
    else if (direction === 'right-only') { tx = -tx_val; ty = 0; }

    var flyX = '-800px';

    // FOUC guard: hide all new cards in their resting state first; reveal when the animation triggers.
    wrappers.forEach(function(w) {
        var c = w.querySelector('.post-item');
        if (c) { c.style.opacity = '0'; c.dataset.revealed = ''; }
    });

    // Split into in-viewport / out-of-viewport.
    var vh = window.innerHeight || document.documentElement.clientHeight;
    var io = getRevealIO();
    io.disconnect();

    var inView = [];
    wrappers.forEach(function(w, idx) {
        var r = w.getBoundingClientRect();
        if (r.top < vh && r.bottom > 0) {
            inView.push(idx);
        } else {
            io.observe(w);
        }
    });

    // In viewport: reuse the original swap transition (old-card clone morph + fly-in), staggered
    // in order. The rank among the cards entering now, not idx, drives the CSS delay: a tag
    // filter hides wrappers without removing them, so idx still counts the hidden ones.
    for (var k = 0; k < inView.length; k++) {
        (function(idx, rank) {
            setTimeout(function() {
                animateRowSwitch(idx, wrappers, oldCards, rhythm, tx, ty, flyX, rank);
            }, rank * 100);
        })(inView[k], k);
    }
}

// Static clone of a card with its entrance state stripped: a deep clone inherits the
// anim-*-new class and the inline transform/opacity of its source, which would make the old
// card replay its entrance instead of receding. animateRowSwitch injects the recede/fade.
function cardSnapshot(card) {
    var clone = card.cloneNode(true);
    clone.classList.remove('anim-arc-new', 'anim-damped-new');
    clone.style.animation = '';
    clone.style.animationDelay = '';
    clone.style.transform = '';
    clone.style.opacity = '';
    clone.style.position = '';
    clone.style.zIndex = '';
    clone.style.transition = '';
    clone.style.left = '';
    clone.style.top = '';
    clone.style.width = '';
    clone.style.margin = '';
    clone.style.willChange = '';
    return { clone: clone };
}

// One snapshot per wrapper, in the order given. animateRowSwitch looks oldCards up by the
// wrapper's index, so the array is positional: a caller that skips wrappers has to rebuild
// that same shape.
function cardSnapshots(wrappers) {
    return Array.from(wrappers).map(function(w) {
        var card = w.querySelector('.post-item');
        return card ? cardSnapshot(card) : null;
    });
}
window.cardSnapshots = cardSnapshots;

// Per-row switch: 1:1 replica of card-swipe-demo.html animateRowSwitch()
// stagger is the card's position in the sequence entering now and feeds animation-delay; it
// defaults to idx, which is already that position for a full page swap.
function animateRowSwitch(idx, newWrappers, oldCards, rhythm, tx, ty, flyX, stagger) {
    var wrapper = newWrappers[idx];
    if (!wrapper) return;

    var newCard = wrapper.querySelector('.post-item');
    if (!newCard) return;

    // Measure wrapper height before children go absolute
    var wrapperHeight = wrapper.offsetHeight;

    // Wrapper: relative + fixed height (demo: position:relative, height:var(--card-height))
    wrapper.style.position = 'relative';
    wrapper.style.height = wrapperHeight + 'px';
    wrapper.style.overflow = 'visible';

    // New card: absolute, full width (demo: position:absolute, left/top/width)
    newCard.style.position = 'absolute';
    newCard.style.left = '0';
    newCard.style.top = '0';
    newCard.style.width = '100%';

    // Inject old card clone INTO wrapper (demo: oldCard stays in wrapper, newCard appended)
    var oldCardData = oldCards && oldCards[idx];
    if (oldCardData && oldCardData.clone) {
        var oldClone = oldCardData.clone;
        oldClone.style.position = 'absolute';
        oldClone.style.left = '0';
        oldClone.style.top = '0';
        oldClone.style.width = '100%';
        oldClone.style.margin = '0';
        oldClone.style.zIndex = '10';
        oldClone.style.transition = 'none';
        oldClone.style.pointerEvents = 'none';
        wrapper.insertBefore(oldClone, newCard);
        void oldClone.offsetHeight; // force reflow

        // Demo: oldCard transition transform 0.5s -> translate3d(0,0,-60px) scale(0.96)
        oldClone.style.transition = 'transform 0.5s cubic-bezier(0.16, 1, 0.3, 1)';
        oldClone.style.transform = 'translate3d(0px, 0px, -60px) scale(0.96)';

        // Demo: fadeStartDelay=350ms, fadeDuration=600ms
        setTimeout(function() {
            oldClone.style.transition = 'transform 600ms cubic-bezier(0.16, 1, 0.3, 1), opacity 600ms ease';
            oldClone.style.opacity = '0';
        }, 350);

        // Demo: duration=1100ms -> remove old card
        setTimeout(function() {
            if (oldClone.parentNode) oldClone.parentNode.removeChild(oldClone);
        }, 1100);
    }

    // New card: CSS variables + trigger animation (demo: setProperty + classList.add)
    newCard.style.setProperty('--i', String(typeof stagger === 'number' ? stagger : idx));
    newCard.style.setProperty('--tx-stack', tx + 'px');
    newCard.style.setProperty('--ty-stack', ty + 'px');
    newCard.style.setProperty('--fly-x', flyX);
    newCard.style.zIndex = '12';
    newCard.style.transition = 'none';

    var animClass = 'anim-' + rhythm + '-new';
    requestAnimationFrame(function() {
        requestAnimationFrame(function() {
            newCard.classList.add(animClass);
        });
    });

    // Demo: duration=1100ms -> cleanup (reset id, className, inline styles)
    setTimeout(function() {
        newCard.classList.remove(animClass);
        newCard.style.position = '';
        newCard.style.left = '';
        newCard.style.top = '';
        newCard.style.width = '';
        newCard.style.zIndex = '';
        newCard.style.transition = '';
        newCard.style.transform = '';
        newCard.style.opacity = '';
        newCard.style.removeProperty('--i');
        newCard.style.removeProperty('--tx-stack');
        newCard.style.removeProperty('--ty-stack');
        newCard.style.removeProperty('--fly-x');
        wrapper.style.position = '';
        wrapper.style.height = '';
        wrapper.style.overflow = '';
    }, 1200);
}

// Global Base URL Resolution
window.getBaseURL = function() {
    return (window.BLOG_CONFIG && window.BLOG_CONFIG.baseURL) ||
           (document.documentElement && document.documentElement.dataset.baseUrl) ||
           '';
};

window.setupCardAnimations = setupCardAnimations;

// Non-repeating random picker (shuffle bag, reusable site-wide)
// Shuffle bag algorithm + localStorage persistence + cross-round repeat guard (a new
// round's first pick never repeats the previous round's last):
// picks for the same key never repeat consecutively across rounds until every
// candidate has appeared once, then the bag reshuffles.
// Reuse for any "random but never consecutive" scenario (waterfall effects /
// kaomoji / future animations):
//   window.pickNoRepeat('waterfall', ['cascade', 'gravity', 'melt'])
window.pickNoRepeat = function(key, items) {
    var storageKey = 'shuffle_bag:' + key;
    var deck = [];
    try { deck = JSON.parse(localStorage.getItem(storageKey) || '[]'); } catch (e) { deck = []; }
    deck = (Array.isArray(deck) ? deck : []).filter(function(m) { return items.indexOf(m) !== -1; });
    if (!deck.length) {
        deck = items.slice();
        for (var i = deck.length - 1; i > 0; i--) {
            var j = Math.floor(Math.random() * (i + 1));
            var tmp = deck[i]; deck[i] = deck[j]; deck[j] = tmp;
        }
        try {
            var last = localStorage.getItem(storageKey + ':last');
            if (last && deck.length > 1 && deck[deck.length - 1] === last) {
                var swap = deck[deck.length - 1];
                deck[deck.length - 1] = deck[0];
                deck[0] = swap;
            }
        } catch (e) {}
    }
    var pick = deck.pop();
    try {
        localStorage.setItem(storageKey, JSON.stringify(deck));
        localStorage.setItem(storageKey + ':last', pick);
    } catch (e) {}
    return pick;
};
