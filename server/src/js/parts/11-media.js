// Lazy image spinner: wrap lazy <img>, remove the spinner when loaded
// <img> is a replaced element without ::after, so it needs a wrapping <span class="lazy-spin">
// <picture> can use ::after directly via .lazy-spin--picture
reinitOnSwap(function setupLazyImages() {
    document.querySelectorAll('img[loading="lazy"]:not(.lazy-spin-setup)').forEach(function(img) {
        img.classList.add('lazy-spin-setup');
        // Already loaded (cache): skip
        if (img.complete && img.naturalWidth > 0) return;

        var parent = img.parentElement;
        var isPicture = parent && parent.tagName === 'PICTURE';

        if (isPicture) {
            // <picture> can use ::after directly, no wrapping
            if (parent.classList.contains('lazy-spin--picture')) return;
            parent.classList.add('lazy-spin', 'lazy-spin--picture');
            function picDone() { parent.classList.add('lazy-spin--done'); }
            img.addEventListener('load', picDone, { once: true });
            img.addEventListener('error', picDone, { once: true });
        } else {
            // Plain <img>: wrap in a <span>
            if (parent && parent.classList.contains('lazy-spin')) return;
            var wrap = document.createElement('span');
            wrap.className = 'lazy-spin';
            if (img.classList.contains('sticker-inline')) {
                wrap.classList.add('lazy-spin--inline');
            }
            var display = getComputedStyle(img).display;
            if (display === 'block') {
                wrap.classList.add('lazy-spin--block');
            }
            img.parentNode.insertBefore(wrap, img);
            wrap.appendChild(img);
            function done() { wrap.classList.add('lazy-spin--done'); }
            img.addEventListener('load', done, { once: true });
            img.addEventListener('error', done, { once: true });
        }
    });
});

// Sticker size: read the alt number to set width dynamically
(function() {
    function applySize() {
        document.querySelectorAll('.cmt-content img[src*="/static/sticker/"][alt]').forEach(function(img) {
            var size = parseInt(img.getAttribute('alt'), 10);
            if (size > 0) {
                img.style.maxWidth = size + 'px';
                img.style.maxHeight = size + 'px';
            }
        });
    }
    applySize();
    // Re-apply after HTMX replaces content
    document.addEventListener('htmx:after:swap', applySize);
})();

// @nickname mention jump
(function() {
    function processMentions() {
        var nickMap = {};
        document.querySelectorAll('.cmt-nick').forEach(function(el) {
            var nick = (el.textContent || '').trim();
            if (nick) nickMap[nick.toLowerCase()] = nick;
        });
        // Process by nick length descending, so a short nick (prefix) cannot match
        // first and eat the head of a longer nick
        var nicks = Object.keys(nickMap).sort(function(a, b) {
            return b.length - a.length;
        });
        document.querySelectorAll('.cmt-content').forEach(function(el) {
            if (el.dataset.mentionDone) return;
            el.dataset.mentionDone = '1';
            // Find the owning comment; if a reply (data-rid present and not 0), its
            // parent comment id becomes the precise jump target
            var item = el.closest('.cmt-item, .profile-card');
            var rid = item ? item.getAttribute('data-rid') : null;
            if (rid === '0') rid = null;
            var html = el.innerHTML;
            var hasChange = false;
            var ridUsed = false; // in a reply only the first @ points to the parent comment (rid); the rest fall back to nick matching
            for (var i = 0; i < nicks.length; i++) {
                var orig = nickMap[nicks[i]];
                // Word-boundary guard: @nick must not be followed by letters/digits
                // (prevents prefixes eating longer nicks) nor an already-wrapped </a>
                var re = new RegExp('@' + orig.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '(?![A-Za-z0-9]|</a>)', 'g');
                if (re.test(html)) {
                    hasChange = true;
                    html = html.replace(re, function() {
                        var attrs = 'class="cmt-mention" data-mention="' + orig + '"';
                        if (rid && !ridUsed) { attrs += ' data-mention-id="' + rid + '"'; ridUsed = true; }
                        return '<a href="#" ' + attrs + '>@' + orig + '</a>';
                    });
                }
            }
            if (hasChange) el.innerHTML = html;
        });
    }
    // click handler for mentions
    document.addEventListener('click', function(e) {
        var target = e.target.closest('.cmt-mention');
        if (!target) return;
        e.preventDefault();
        var item = null;
        // Prefer a precise jump by reply parent comment id (accurate even after
        // re-sorting); data-rid="0" means not a reply, ignore
        var id = target.dataset.mentionId;
        if (id && id !== '0') {
            item = document.querySelector('.cmt-item[data-cmt-id="' + id + '"], .profile-card[data-cmt-id="' + id + '"]');
        }
        // Fallback: without an id, match the first by nick (top-level mentions /
        // orphaned replies whose parent was deleted)
        if (!item) {
            var nick = target.dataset.mention;
            if (!nick) return;
            var items = document.querySelectorAll('.cmt-item');
            for (var i = 0; i < items.length; i++) {
                var nickEl = items[i].querySelector('.cmt-nick');
                if (nickEl && (nickEl.textContent || '').trim() === nick) { item = items[i]; break; }
            }
        }
        if (!item) return;
        item.scrollIntoView({ behavior: 'smooth', block: 'center' });
        item.style.transition = 'background 0.5s';
        item.style.background = 'var(--accent-soft)';
        setTimeout(function() { item.style.background = ''; }, 1000);
    });
    processMentions();
    document.addEventListener('htmx:after:swap', processMentions);
})();
