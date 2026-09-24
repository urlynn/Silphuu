// TOC & Reading Progress Indicator Engine (Article Bundle)
(function() {
    'use strict';

    function tocSlug(text, idx, used) {
        text = (text || '').trim();
        if (!text) return 'sec-' + idx;
        var slug = text
            .replace(/[^0-9A-Za-z\u3040-\u30FF\u3400-\u4DBF\u4E00-\u9FFF\uFF00-\uFFEF]/g, '-')
            .replace(/[-_]+/g, '-')
            .replace(/^-+|-+$/g, '');
        if (!slug) return 'sec-' + idx;
        var base = slug, n = 1;
        while (used[slug]) { n++; slug = base + '-' + n; }
        used[slug] = true;
        return slug;
    }

    function rebuildTOC() {
        var list = document.getElementById('tocList');
        var content = document.getElementById('article-content');
        if (!content || !list) return;
        list.innerHTML = '';
        var headings = content.querySelectorAll('h1, h2, h3');
        var tocItems = [];
        var used = {};

        headings.forEach(function(h, i) {
            var id = h.id || tocSlug(h.textContent, i, used);
            h.id = id;
            used[id] = true;
            var a = document.createElement('a');
            a.href = '#' + id;
            a.className = 'toc-item';
            a.dataset.headingId = id;
            var label = document.createElement('span');
            label.className = 'toc-label';
            label.textContent = h.textContent;
            a.appendChild(label);
            if (h.tagName === 'H1') a.style.paddingLeft = '4px';
            if (h.tagName === 'H2') a.style.paddingLeft = '16px';
            if (h.tagName === 'H3') a.style.paddingLeft = '28px';
            list.appendChild(a);
            tocItems.push(a);
        });

        var bar = document.createElement('div');
        bar.className = 'toc-indicator-bar';
        list.appendChild(bar);

        function getVisibleHeadingIds() {
            var visible = [];
            headings.forEach(function(h) {
                if (!h.id) return;
                var rect = h.getBoundingClientRect();
                if (rect.top < window.innerHeight && rect.bottom > 0) {
                    visible.push(h.id);
                }
            });
            if (visible.length === 0 && headings.length > 0) {
                var closest = null, minDist = Infinity;
                headings.forEach(function(h) {
                    if (!h.id) return;
                    var dist = Math.abs(h.getBoundingClientRect().top);
                    if (dist < minDist) { minDist = dist; closest = h.id; }
                });
                if (closest) visible.push(closest);
            }
            return visible;
        }

        function updateIndicator() {
            if (tocItems.length === 0) return;
            var visibleIds = getVisibleHeadingIds();
            var activeItems = [];
            tocItems.forEach(function(item) {
                var id = item.dataset.headingId;
                if (id && visibleIds.indexOf(id) !== -1) {
                    activeItems.push(item);
                }
            });
            if (activeItems.length === 0) {
                bar.style.opacity = '0';
                return;
            }
            var listRect = list.getBoundingClientRect();
            var first = activeItems[0];
            var last = activeItems[activeItems.length - 1];
            var firstRect = first.getBoundingClientRect();
            var lastRect = last.getBoundingClientRect();
            bar.style.top = (firstRect.top - listRect.top) + 'px';
            bar.style.height = (lastRect.bottom - firstRect.top) + 'px';
            bar.style.opacity = '1';
        }

        var observer = new IntersectionObserver(function() { updateIndicator(); }, { rootMargin: '0px', threshold: 0 });
        headings.forEach(function(h) { if (h.id) observer.observe(h); });
        window.addEventListener('scroll', updateIndicator, { passive: true });
        updateIndicator();

        // TOC rebuild-complete signal: Mode B height stretch depends on the real new
        // natural height, measurable only now (after the after:swap rebuild); see 12-lifecycle.js.
        document.dispatchEvent(new CustomEvent('toc:rebuilt'));
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', rebuildTOC);
    } else {
        rebuildTOC();
    }
    document.addEventListener('htmx:after:swap', rebuildTOC);

    // Event timing: attaching only after:swap leaves the TOC always one frame late — the
    // user sees the TOC box shrink, then bounce back. before:settle fires AFTER the
    // DOM swap and BEFORE the first paint, so rebuilding here makes the first frame
    // the final state. after:swap remains as a fallback: rebuildTOC is idempotent
    // (clears first, rebuilds from #article-content), and 12-lifecycle.js's tocArmed
    // is one-shot, so the height animation cannot fire twice.
    document.addEventListener('htmx:before:settle', rebuildTOC);
})();
