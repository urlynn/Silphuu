// Archive page ASLIDE: globally auto-triggered, independent of the FAB rhythm
// Rows fade in top-down: year header -> month header -> post rows (timeline column
// fade + text slide-in)
function triggerArchiveAslide(scope) {
    var root = scope || document;
    var rows = root.querySelectorAll('.ap-post-row');
    if (!rows.length) return;

    var gi = 0; // global incrementing index controlling the top-down stagger

    root.querySelectorAll('.ap-year-block').forEach(function(block) {
        // Year header fade-in
        var yh = block.querySelector('.ap-year-header');
        if (yh) {
            yh.classList.remove('aslide-fade');
            yh.style.setProperty('--i', String(gi));
            void yh.offsetWidth;
            yh.classList.add('aslide-fade');
        }

        block.querySelectorAll('.ap-month-block').forEach(function(mb) {
            // Month header fade-in
            var mh = mb.querySelector('.ap-month-header');
            if (mh) {
                mh.classList.remove('aslide-fade');
                mh.style.setProperty('--i', String(gi));
                void mh.offsetWidth;
                mh.classList.add('aslide-fade');
            }

            // Post rows: timeline column fade + text slide-in
            mb.querySelectorAll('.ap-post-row').forEach(function(row) {
                var col = row.querySelector('.ap-col');
                if (col) {
                    col.classList.remove('aslide-fade');
                    col.style.setProperty('--i', String(gi));
                    void col.offsetWidth;
                    col.classList.add('aslide-fade');
                }

                var link = row.querySelector('.ap-post-link');
                if (link) {
                    link.classList.remove('aslide-enter');
                    link.style.setProperty('--i', String(gi));
                    void link.offsetWidth;
                    link.classList.add('aslide-enter');
                }

                gi++;
            });
        });
    });

    // Sync the timeline SVG growth: duration matches the total time; linear easing matches the even per-row stagger
    var totalDur = 0.42 + (gi - 1) * 0.05; // seconds
    var svg = root.querySelector('.ap-highlight-svg');
    if (svg) {
        svg.style.animation = 'none';
        void svg.offsetWidth;
        svg.style.animation = 'timeline-grow ' + totalDur + 's linear 0s forwards';
    }
}

// Initial load: auto-trigger ASLIDE on the archive page
triggerArchiveAslide();

// htmx 4.0 native events: detail = { ctx, tasks }; the target is ctx.target (not e.detail.target)
// Unified navigation semantics: all site page swaps target #content-wrapper only. The old
// page's browse state window.__wasBrowse is captured here (the capture phase runs first;
// before the swap the DOM is necessarily the old page), shared by navbar's freeze decision
// and the after:settle classification.
document.addEventListener('htmx:before:swap', function(e) {
    window.__wasBrowse = !!document.getElementById('browse-body-area');
    var ctx = e.detail && e.detail.ctx;
    if (!ctx) return;

    // Orphan swap guard: conditionally-present fragment targets (comment sorting
    // #cmt-comment-area etc.) get unloaded after a full page switch; if an in-flight
    // fragment request responds at that moment, htmx re-resolves the target to null ->
    // a later null.insertAdjacentElement throws (htmx:error, console red). Cancel the
    // orphan swap here (the page has moved on, no visible impact) and clear the
    // source element's residual htmx-request loading state.
    var tasks = e.detail && e.detail.tasks;
    if (tasks) {
        for (var ti = 0; ti < tasks.length; ti++) {
            if (!tasks[ti].target) {
                e.preventDefault();
                var src = ctx.sourceElement;
                if (src && src.classList) src.classList.remove('htmx-request');
                return;
            }
        }
    }

    var tgt = ctx.target;
    if (!tgt) return;

    // Globally clear swapSpec.show (all swap targets)
    var tasks = e.detail.tasks;
    if (tasks) {
        for (var i = 0; i < tasks.length; i++) {
            if (tasks[i] && tasks[i].swapSpec) tasks[i].swapSpec.show = false;
        }
    }

    // Old page is a browse page: capture old card snapshots + set animation intent
    // (browse->browse in-place / browse->post entrance)
    if (window.__wasBrowse) {
        var animRhythm = document.documentElement.getAttribute('data-anim-rhythm') || 'damped';
        var animDirection = document.documentElement.getAttribute('data-anim-direction') || 'right-only';

        window._swipeIntent = { rhythm: animRhythm, direction: animDirection };
        // Record the trigger source: a switch from #navbar = Path1 (jump to top); from
        // info cards/tag chips = Path2 (follow, no jump).
        window._swipeIsNav = !!(ctx.sourceElement && ctx.sourceElement.closest && ctx.sourceElement.closest('#navbar'));
        window._swipeOldCards = cardSnapshots(tgt.querySelectorAll('.card-row-wrapper'));

        // Capture the old .browse-header (bottom stats card) height + offsetTop relative
        // to the area for FLIP. No exit clone is generated anymore: the clone would paint
        // above the new cards (zIndex), occluding the transition and leaving old-card
        // residue. offsetTop is scroll-independent; on many->few the list collapses via
        // remWrap after after:swap and the article card wrappers restore the height on
        // entrance; the final offsetTop is probed temporarily by setupBrowseHeaderAnimation.
        var oldHeaderEl = tgt.querySelector('.browse-header');
        window._swipeOldHeader = null;
        if (oldHeaderEl) {
            window._swipeOldHeader = {
                height: oldHeaderEl.offsetHeight,
                offsetTop: oldHeaderEl.offsetTop
            };
        }
    }
}, true);

// htmx 4.0 native event: after:swap fires once the swap completes; triggers the card entrance animations
document.addEventListener('htmx:after:swap', function(e) {
    if (window._swipeIntent) {
        var intent = window._swipeIntent;
        var oldCards = window._swipeOldCards;
        var oldHeader = window._swipeOldHeader;
        window._swipeIntent = null;
        window._swipeOldCards = null;
        window._swipeOldHeader = null;

        var realTgt = document.getElementById('browse-body-area');
        if (realTgt) {
            // Jump-to-top decision: Path1 (navbar) jumps to top; browse-internal switches
            // (browse->browse, card-sourced) follow without jumping; browse->post has no
            // realTgt and is handled by navbar's after:settle full-page branch.
            var isBrowseNav = window.__wasBrowse; /* realTgt present = the new page is a browse page */
            if (window._swipeIsNav || !isBrowseNav) window.scrollTo(0, 0);
            setupCardAnimations(realTgt, intent.rhythm, intent.direction, oldCards);
            setupBrowseHeaderAnimation(realTgt, oldHeader);
        }
        window._swipeIsNav = false;
    }

    // Archive page ASLIDE: globally auto-triggered, independent of the FAB rhythm
    var browseArea = document.getElementById('browse-body-area');
    if (browseArea && browseArea.querySelector('.ap-post-row')) {
        triggerArchiveAslide(browseArea);
    }
});

// Bottom stats card .browse-header switch animation: FLIP (translate + height) smooth
// transition + title/stats text wipe left->right.
// Constraint: this card has backdrop-filter, so the shell's own opacity must stay 1
// (otherwise the frost goes transparent).
// Therefore FLIP uses transform+height only; the text wipe applies to the inner
// .browse-header__title-wrap (transform+opacity only).
// No exit clone is generated, and no remWrap collapses surplus old cards either (that
// carried an extra trailing-collapse side effect dragging on the cards).
// htmx has already replaced the old DOM with the new card count at after:swap, so the
// list is in its final state; both directions use the same transform FLIP:
// deltaY = oldTop - newTop, the card glides from oldTop to newTop.
// Offsets use offsetTop relative to the area (scroll-independent).
function setupBrowseHeaderAnimation(tgt, oldInfo) {
    var newHeader = tgt.querySelector('.browse-header');
    if (!newHeader) return;

    var newH = newHeader.offsetHeight;
    var deltaY = 0;
    if (oldInfo && typeof oldInfo.offsetTop === 'number') {
        deltaY = oldInfo.offsetTop - newHeader.offsetTop;
    }
    var needFlip = oldInfo
        && (Math.abs(deltaY) > 1 || Math.abs(newH - oldInfo.height) > 1);

    if (needFlip) {
        var dur = 0.5;
        // First disable transitions and instantly pin the new card to the "old position /
        // old height" (inverted state), then re-enable transitions back to the final
        // state; otherwise the card's own transition:all 0.3s pollutes the start values
        // and causes a jump.
        newHeader.style.transition = 'none';
        newHeader.style.transform = 'translateY(' + deltaY + 'px)';
        newHeader.style.height = oldInfo.height + 'px';
        newHeader.style.overflow = 'hidden';
        void newHeader.offsetWidth; // force reflow to commit the inverted state
        newHeader.style.transition = 'transform ' + dur + 's cubic-bezier(0.16, 1, 0.3, 1), height ' + dur + 's cubic-bezier(0.16, 1, 0.3, 1)';
        newHeader.style.transform = 'translateY(0)';
        newHeader.style.height = newH + 'px';
        var cleanMs = Math.round(dur * 1000) + 100;
        setTimeout(function() {
            newHeader.style.transform = '';
            newHeader.style.height = '';
            newHeader.style.overflow = '';
            newHeader.style.transition = '';
        }, cleanMs);
    }

    // Title + stats text: wipe in left->right (matching card-swipe-demo.html's .stats-content-wrapper)
    // Only the "#Linux / 2 posts / 14 tags" part participates; category nav and tag
    // filters stay static.
    var tw = newHeader.querySelector('.browse-header__title-wrap');
    if (tw) {
        tw.classList.remove('bh-text-in');
        void tw.offsetWidth;
        tw.classList.add('bh-text-in');
        setTimeout(function() { tw.classList.remove('bh-text-in'); }, 500);
    }
}

window.setupBrowseHeaderAnimation = setupBrowseHeaderAnimation;
