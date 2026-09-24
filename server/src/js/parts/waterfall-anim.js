// Photo wall: pure falling-water animation + parallax scroll flow.
// The entrance animation is carried solely by the fall scheme. Cards are pre-hidden by
// CSS (opacity: 0) before the script intervenes; the moment data-waterfall="fall" is
// written the animation starts from opacity:0, no jump. Integrated with the HTMX lifecycle.
//
// Layout contract (vertical arrangement, see gallery.css / templates/author.html):
//   .photo-wall is a flex row container with exactly 3 .wall-col column children; cards
//   are assigned by col = i % 3. --col is the card's REAL column index (0/1/2) and --row
//   its index within the column; both come from the template and are re-aligned by
//   author.html's syncIndices() after drag-sort settles.
//
// Known issue (unfixed): the parallax below writes inline `card.style.transform`
// while `[data-waterfall="fall"] .photo-card`'s animation has fill both, so the
// animation wins the cascade and the parallax stays overridden by translateY(0).
// Restoring it means writing `card.style.translate` instead (composes with transform).
(function() {
    'use strict';

    function initPhotoWaterfall() {
        var wall = document.getElementById('photo-wall');
        if (!wall) return;

        if (!wall.getAttribute('data-waterfall')) {
            wall.setAttribute('data-waterfall', 'fall');
        }

        var ticking = false;
        function onScroll() {
            if (ticking) return;
            ticking = true;
            requestAnimationFrame(function() {
                ticking = false;
                if (!wall || !document.body.contains(wall)) return;

                var rect = wall.getBoundingClientRect();
                var vh = window.innerHeight;
                if (rect.bottom < 0 || rect.top > vh) return;

                // Keep the original column-offset-based vertical parallax follow
                var offset = (vh / 2 - (rect.top + rect.height / 2)) * 0.04;
                var cards = wall.querySelectorAll('.photo-card');
                cards.forEach(function(card) {
                    var col = parseInt(card.style.getPropertyValue('--col') || '0', 10);
                    var speed = (col === 1) ? 0.7 : (col === 0 ? -0.5 : -0.2);
                    card.style.transform = 'translate3d(0, ' + (offset * speed).toFixed(1) + 'px, 0)';
                });
            });
        }

        window.removeEventListener('scroll', onScroll, { passive: true });
        window.addEventListener('scroll', onScroll, { passive: true });
    }

    if (typeof reinitOnSwap === 'function') {
        reinitOnSwap(initPhotoWaterfall);
    } else {
        document.addEventListener('DOMContentLoaded', initPhotoWaterfall);
        document.addEventListener('htmx:after:swap', initPhotoWaterfall);
    }
})();
