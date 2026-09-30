// Nav Filter Panel: category + tag dropdown panel
(function() {
    var filterBtn = document.getElementById('navFilterBtn');
    var filterPanel = document.getElementById('navFilterPanel');
    var backdrop = document.getElementById('navFilterBackdrop');
    if (!filterBtn || !filterPanel || !backdrop) return;

    function togglePanel(open) {
        if (typeof open === 'undefined') open = !filterPanel.classList.contains('nav-filter-panel--open');
        if (open) {
            filterPanel.classList.add('nav-filter-panel--open');
            filterBtn.classList.add('active');
        } else {
            filterPanel.classList.remove('nav-filter-panel--open');
            filterBtn.classList.remove('active');
        }
    }

    filterBtn.addEventListener('click', function(e) {
        e.stopPropagation();
        togglePanel();
    });

    backdrop.addEventListener('click', function() { togglePanel(false); });

    // Close the panel after clicking a link inside it
    filterPanel.addEventListener('click', function(e) {
        if (e.target.closest('a')) togglePanel(false);
    });

    // ESC closes
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape' && filterPanel.classList.contains('nav-filter-panel--open')) {
            togglePanel(false);
        }
    });

    // Browse-page mode detection (shared): decided by whether #browse-body-area
    // really exists in the DOM
    // Do not use body classes: after a full page switch, navbar's after:settle
    // (delete data-body-class + syncBodyClass) washes them down to just app-ready,
    // and this listener runs before syncBodyClass, causing browse->browse content-area
    // switches to be misread as non-browse and hide the button.
    // #browse-body-area is the only DOM marker of a browse page (archive/category/tag/list).
    window.__browseMode = function() {
        return !!document.getElementById('browse-body-area');
    };

    // Mode class drives the right-column shape (#navRight.browse) + filter button
    // visibility (CSS handles it; the display toggle was removed)
    var navRight = document.getElementById('navRight');
    function updateVisibility() {
        if (navRight) navRight.classList.toggle('browse', window.__browseMode());
    }
    updateVisibility();

    // Re-check after htmx page switches
    document.addEventListener('htmx:after:swap', function() {
        updateVisibility();
        // Close the panel after a page switch
        togglePanel(false);
    });
})();

// Tag filter: /posts carries every card, and a #<tag> fragment hides the ones without that
// tag. The fragment never reaches the server, so this is the only place the filter can run —
// and the only place that has to agree with URLTag (server/routes.go).
(function() {
    // The tag a URL points at: /posts#<tag>, percent-encoded by URLTag. Decoding both sides
    // lets a raw fragment typed into the address bar match the encoded links.
    function hrefTag(href) {
        var i = (href || '').indexOf('#');
        if (i < 0) return null;
        try { return decodeURIComponent(href.slice(i + 1)); } catch (e) { return href.slice(i + 1); }
    }

    // The outgoing cards, paired with the slots that stay visible. animateRowSwitch reads
    // oldCards by the wrapper's index in the full list, so the array keeps that shape and only
    // the surviving slots are filled, in the outgoing cards' own visible order.
    function pairOutgoing(wrappers, kept, shown) {
        var visible = [];
        for (var v = 0; v < wrappers.length && visible.length < shown; v++) {
            if (wrappers[v].style.display === 'none') continue;
            if (wrappers[v].closest('#postsListEmpty')) continue;
            visible.push(wrappers[v]);
        }
        var outgoing = window.cardSnapshots(visible);
        var out = new Array(wrappers.length);
        var rank = 0;
        for (var i = 0; i < wrappers.length; i++) {
            if (!kept[i]) continue;
            out[i] = outgoing[rank++] || null;
        }
        return out;
    }

    function applyTagFilter(animate) {
        var box = document.getElementById('postsListResults');
        if (!box) return; // not the post list page

        var area = document.getElementById('browse-body-area');
        var tag = hrefTag(location.hash);
        var wrappers = (area || box).querySelectorAll('.card-row-wrapper');

        // Decide first: the outgoing snapshots must be taken while the previous filter's
        // display values are still in place.
        var kept = new Array(wrappers.length);
        var shown = 0;
        for (var i = 0; i < wrappers.length; i++) {
            if (wrappers[i].closest('#postsListEmpty')) continue;
            var tags = (wrappers[i].getAttribute('data-tags') || '').split(',');
            kept[i] = !tag || tags.indexOf(tag) !== -1;
            if (kept[i]) shown++;
        }

        var oldCards = null;
        if (animate && area && typeof window.cardSnapshots === 'function') {
            oldCards = pairOutgoing(wrappers, kept, shown);
        }

        for (var k = 0; k < wrappers.length; k++) {
            // The pre-rendered empty card is a .card-row-wrapper too; it follows the count below.
            if (kept[k] === undefined) continue;
            wrappers[k].style.display = kept[k] ? '' : 'none';
        }

        var empty = document.getElementById('postsListEmpty');
        if (empty) empty.style.display = shown ? 'none' : '';

        var ink = document.getElementById('browseHeaderPostCount');
        if (ink) {
            var unit = document.createElement('i');
            unit.textContent = ink.getAttribute('data-suffix') || '';
            ink.textContent = shown;
            ink.appendChild(unit);
        }

        var title = document.getElementById('browseHeaderTitle');
        if (title) {
            if (!title.dataset.listTitle) title.dataset.listTitle = title.textContent.trim();
            title.textContent = tag ? '#' + tag : title.dataset.listTitle;
        }

        var chips = document.querySelectorAll('.browse-tag-chip, .nav-filter-tag');
        for (var j = 0; j < chips.length; j++) {
            chips[j].classList.toggle('active', !!tag && hrefTag(chips[j].getAttribute('href')) === tag);
        }

        // The same switch a browse-to-browse page swap plays (02-aslide.js). The header is
        // updated first because its height moves the cards setupCardAnimations measures.
        if (oldCards && typeof window.setupCardAnimations === 'function') {
            window.setupCardAnimations(
                area,
                document.documentElement.getAttribute('data-anim-rhythm') || 'damped',
                document.documentElement.getAttribute('data-anim-direction') || 'right-only',
                oldCards
            );
        }
    }

    // reinitOnSwap: the list page arrives by htmx swap as well as by full load. A swap brings
    // its own entrance animation, so only the display state is rebuilt here.
    reinitOnSwap(function() { applyTagFilter(false); });
    // A same-page tag click only changes the fragment (navbar.js routes it there), so no swap
    // hook fires for it: this is the only place that switch can be animated.
    addEventListener('hashchange', function() { applyTagFilter(true); });
})();

