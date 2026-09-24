// Ink Transition Engine (shattered-ink). Three coordinated schemes:
//   A Article switch: old content shatters per line (per-character Range measurement,
//     diagonal wavefront); new content wipes in per block via clip-path expansion.
//   B Comment sorting: hx-swap=innerMorph keeps the glass body animation-free; items
//     FLIP to new positions keyed by data-cmt-id; new items float in staggered.
//   C TOC switch: old entries shatter per character; new entries pop in per character.
// Iron rule: animated transforms go only on the glass body or leaf descendants of the
// glass, never on glass ancestors; all ghosts are glass children so the glass's own
// backdrop sampling is undisturbed and the frost persists throughout.
// Timeline: before:swap (old DOM) -> DOM swap -> before:settle (pre-paint, play point)
// -> after:settle -> after:swap -> toc.js rebuild -> toc:rebuilt (C entry point).
//
// Ink alignment (measured by the admin tool in font-metrics-measure.js):
//   shift = ((fontAsc−fontDesc)/2 − (inkAsc−inkDesc)/2) / fontSize   [em]
// Target invariant: canonical ink center == element box center (line-height independent).
//
// Ink shift is character- and font-size-dependent and canvas font metrics are
// quantized, so a measured table drifts; shifts must come from a first-paint channel:
// CSS role rules --font-*-shift baked server-side into <head> (preferred), or
// build-time re-measurement at real font sizes delivered by the server.
//
// The JS runtime channel is a flicker channel, not an alignment channel: shifts apply
// only after document.fonts.ready -> one uncompensated frame then a jump. JS auto-scan
// is demoted to gap reporting (window.__INK_SCAN_GAPS__); centered flex/grid containers
// join the alignment system automatically — never maintain class whitelists.
//
// Font config rule: when a role font is NOT configured, fall back only to system fonts,
// never to another role font. var(--x, fallback) triggers only when the variable is
// UNDEFINED — "defined but empty" does not, so such fallbacks never actually worked.
//
// Archive year/month labels ("9月"/"2026年") mount CJK on --font-data (MapleMono,
// Latin-only) and fall to the system font; archive.css appends --font-caption as fallback.
//
// Structure blind spots: text as direct flex/grid children is invisible to JS
// traversal; wrapping it in spans destroys the flex-item sizing of small dots like
// .cmt-ua-sep — add the whole-line shift on the CSS side instead.
(function inkTransitionEngine() {
    var reduceMQ = window.matchMedia('(prefers-reduced-motion: reduce)');
    var ghosts = [];
    var armedArticle = null;   // A: frags array
    var armedMeta = null;      // A: meta row snapshot
    var armedTitle = null;     // A: title snapshot
    var armedTags = null;      // A: tag snapshot
    var prevGlassH = 0;        // A: previous glass height (ghost mode selection)
    var armedCmtFlip = null;   // B: {id: oldTop}
    var armedToc = null;       // C: captured data
    var tocEnterArmed = false;
    var pendingMetaHTML = null; // A: new content for the persistent #article-meta (from swap fragment)

    function purgeGhosts() {
        ghosts.forEach(function(g) { if (g.parentElement) g.parentElement.removeChild(g); });
        ghosts = [];
    }

    // Same as 12-lifecycle.js: body's class is wiped by navbar.js before the event
    // bubbles to document; page type must be read from #content-wrapper's
    // data-body-class.
    function currentIsPostPage() {
        var cw = document.getElementById('content-wrapper');
        var cls = cw ? (cw.getAttribute('data-body-class') || '') : '';
        return cls.split(/\s+/).indexOf('page-article') !== -1;
    }

    function pagePath(evt) {
        var ctx = evt.detail && evt.detail.ctx;
        var action = ctx && ctx.request && ctx.request.action;
        if (!action) return null;
        try { return new URL(action, location.href).pathname; } catch (e) { return null; }
    }

    function fontCssOf(el) {
        var cs = getComputedStyle(el);
        return 'font-family:' + cs.fontFamily + ';font-size:' + cs.fontSize + ';font-weight:' + cs.fontWeight +
            ';line-height:' + cs.lineHeight + ';letter-spacing:' + cs.letterSpacing + ';color:' + cs.color + ';';
    }

    // Line splitting: per-character Range rects, grouped by "line top ±3px" (works for CJK and Latin)
    // Cost = character count; blocks over charBudget degrade to whole-block shattering (long-post protection).
    function splitLines(el, baseRect, fontCss) {
        var frags = [];
        var walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
        var chars = [];
        var node;
        while ((node = walker.nextNode())) {
            var arr = Array.from(node.textContent);
            for (var i = 0; i < arr.length; i++) chars.push({ ch: arr[i], node: node, start: i });
        }
        if (!chars.length) return frags;
        var range = document.createRange();
        var lines = [];
        for (var j = 0; j < chars.length; j++) {
            var c = chars[j];
            try {
                range.setStart(c.node, c.start);
                range.setEnd(c.node, c.start + 1);
            } catch (e) { continue; }
            var r = range.getBoundingClientRect();
            if (r.width === 0 && r.height === 0) continue;
            var top = r.top - baseRect.top;
            var line = null;
            for (var k = lines.length - 1; k >= 0; k--) {
                if (Math.abs(lines[k].top - top) < 3) { line = lines[k]; break; }
            }
            if (!line) { line = { top: top, left: Infinity, right: -Infinity, text: '' }; lines.push(line); }
            line.left = Math.min(line.left, r.left - baseRect.left);
            line.right = Math.max(line.right, r.right - baseRect.left);
            line.text += c.ch;
        }
        lines.forEach(function(l) {
            if (!l.text.trim()) return;
            frags.push({ type: 'line', text: l.text, left: l.left, top: l.top, width: Math.max(1, l.right - l.left), fontCss: fontCss });
        });
        return frags;
    }

    // Flatten top-level blocks: UL/OL descend to li granularity, others stay top-level
    function flattenBlocks(content) {
        var blocks = [];
        Array.prototype.forEach.call(content.children, function(child) {
            if (child.tagName === 'UL' || child.tagName === 'OL') {
                Array.prototype.forEach.call(child.children, function(li) { blocks.push(li); });
            } else blocks.push(child);
        });
        return blocks;
    }

    // A: article capture (before:swap, old DOM still present)
    // prevGlassH drives ghost mode: on long->short the rising bottom edge acts as the
    // eraser and the ghost fades out silently.
    function captureArticle() {
        var glass = document.querySelector('.post-center .post-article');
        var content = glass ? glass.querySelector('.article-content') : null;
        if (!content) return;
        var glassRect = glass.getBoundingClientRect();
        prevGlassH = glass.offsetHeight;
        var frags = [];
        var charBudget = 3000;
        flattenBlocks(content).forEach(function(block) {
            var texty = block.querySelector('pre, table, img, figure, svg') === null && (block.textContent || '').trim() !== '';
            if (texty && charBudget > 0) {
                charBudget -= block.textContent.length;
                frags = frags.concat(splitLines(block, glassRect, fontCssOf(block)));
            } else {
                var r = block.getBoundingClientRect();
                // strip ids: prevent transient id collisions between ghost clones and new content
                frags.push({ type: 'block', html: block.outerHTML.replace(/\sid="[^"]*"/g, ''), left: r.left - glassRect.left, top: r.top - glassRect.top, width: r.width });
            }
        });
        frags.sort(function(a, b) { return a.top - b.top; });
        armedArticle = frags;
        armedMeta = captureMeta(glassRect);
        armedTitle = captureTitle();
        armedTags = captureTags();
        captureCmtCount();
    }

    // Persistent #article-meta: extract the new meta row content from the swap tasks' main fragment.
    // hx-preserve keeps the #article-meta element itself across swaps (with stale content);
    // at before:settle this new content is synced in and buildReel rolls the real element
    // from old to new values -> no clones, no ghosting.
    function capturePendingMetaHTML(evt) {
        pendingMetaHTML = null;
        var tasks = evt.detail && evt.detail.tasks;
        if (!tasks) return;
        var frag = null;
        for (var i = 0; i < tasks.length; i++) {
            if (tasks[i].type === 'main' && tasks[i].fragment) { frag = tasks[i].fragment; break; }
        }
        if (!frag) return;
        var newMeta = frag.querySelector('#article-meta');
        if (newMeta) pendingMetaHTML = newMeta.innerHTML;
    }

    // Meta row capture: semantic pairing of children + text snapshot (orthogonal architecture: container FLIP + text change)
    function metaKey(el) {
        if (el.classList.contains('post-cat')) return 'cat';
        if (el.classList.contains('article-date')) return 'date';
        if (el.classList.contains('article-updated')) return 'updated';
        if (el.classList.contains('article-words')) return 'words';
        if (el.classList.contains('article-views')) return 'views';
        if (el.classList.contains('article-edit-btn')) return 'edit';
        return null;
    }

    // Logical text: the element's actually displayed text, ignoring reel/odometer
    // internal cells. An unsettled .ink-reel's textContent holds the WHOLE digit strip
    // ("76" reads "765432106543210"), so building reels from textContent grows the
    // strip exponentially across fast consecutive switches; each reel therefore records
    // its target character at creation (data-ink-ch) and logical text uses that. The
    // odometer's true value lives in aria-label.
    function logicalText(root) {
        var out = '';
        Array.prototype.forEach.call(root.childNodes, function(n) {
            if (n.nodeType === 3) { out += n.nodeValue; return; }
            if (n.nodeType !== 1) return;
            var cls = n.classList;
            if (cls) {
                if (cls.contains('ink-reel')) {
                    var ch = n.getAttribute('data-ink-ch');
                    if (ch) out += ch; // padding slot has no attribute -> skip (not part of logical text)
                    return;
                }
                if (cls.contains('odometer')) { out += n.getAttribute('aria-label') || ''; return; }
                if (cls.contains('views-slot-spinner')) return; // loading placeholder, no real value
            }
            out += logicalText(n);
        });
        return out;
    }

    function captureMeta(glassRect) {
        var meta = document.getElementById('article-meta');
        if (!meta) return null;
        var metaRect = meta.getBoundingClientRect();
        var items = {};
        Array.prototype.forEach.call(meta.children, function(el) {
            var key = metaKey(el);
            if (!key) return;
            var r = el.getBoundingClientRect();
            var textEl = el.querySelector('.pill-label') || el.querySelector('.meta-text') || el.querySelector('b');
            items[key] = {
                left: r.left - glassRect.left,
                top: r.top - glassRect.top,
                width: r.width,
                height: r.height,
                text: textEl ? logicalText(textEl) : ''
            };
        });
        return { left: metaRect.left - glassRect.left, top: metaRect.top - glassRect.top, items: items };
    }

    // Per-digit reel (date/updated/words)
    // Requirement: 1->8 must not jump but roll through 2,3,...,7 digit by digit -> real
    // wheel/clock feel. Implementation: each changed digit builds a "vertical digit
    // strip" (old, old±1, ..., new); translateY rolls from 0 to the target cell at
    // ~110ms per cell, cascading. Static digits get no strip (zero cost).
    // The .ink-reel container's overflow clipping comes from CSS (ink.css
    // overflow:hidden); without clipping, characters visibly ghost outside the meta
    // row (measured bug).
    function digitStep(oldCh, newCh) {
        var o = parseInt(oldCh, 10), n = parseInt(newCh, 10);
        if (!isNaN(o) && !isNaN(n)) return n > o ? 1 : -1; // digit: shortest-path direction
        return 1; // non-digit (e.g. - / CJK): single step
    }

    // Padding slots must NOT be removed instantly:
    // when the digit count shrinks, a padding slot occupies one character width;
    // removing it directly makes elements to the right (especially `.article-views`)
    // teleport by one character width (measured 8.4px).
    // Approach: smoothly animate the clipping window's width down to 0 — width is a
    // layout property -> right-side elements move left frame-by-frame continuously,
    // giving a true position animation.
    function collapsePaddingSlot(s) {
        var w = s.getBoundingClientRect().width;
        if (!w) { s.remove(); return; }
        var a = s.animate(
            [{ width: w + 'px' }, { width: '0px' }],
            { duration: 220, easing: 'cubic-bezier(0.45, 0, 0.15, 1)', fill: 'forwards' }
        );
        var done = function() { if (s.isConnected) s.remove(); };
        if (a && a.finished) a.finished.then(done).catch(done);
        else setTimeout(done, 320);
    }

    function buildReel(el, oldText) {
        var newText = logicalText(el);
        if (newText === oldText) return;
        // Right-aligned pairing (width change, e.g. 0->36): align pure digit strings at
        // the units digit, left-pad the old string with 0s to the new length -> the
        // added digit (the tens 3) rolls from 0 to 3 while the units 0->6 rolls normally
        // — any width change gets a continuous animation, no leading-zero placeholders
        // or magnitude knowledge needed.
        var isNum = /^\d+$/.test((oldText || '').trim()) && /^\d+$/.test(newText.trim());
        var oldChars, newChars;
        var padNew = 0; // number of left-padded 0s on the new string (must be removed after the animation)
        if (isNum && oldText.trim().length !== newText.trim().length) {
            // Right-aligned pairing: left-pad the SHORTER side with 0s to equal length
            // so digits pair up one-to-one; padStart (not repeat) so a shrinking digit
            // count can never go negative.
            var L = Math.max(oldText.trim().length, newText.trim().length);
            oldChars = Array.from(oldText.trim().padStart(L, '0'));
            newChars = Array.from(newText.trim().padStart(L, '0'));
            padNew = L - newText.trim().length;
        } else {
            oldChars = Array.from(oldText || '');
            newChars = Array.from(newText);
        }
        el.textContent = '';
        var FLIP_EASE = 'cubic-bezier(0.45, 0, 0.15, 1)';
        newChars.forEach(function(ch, i) {
            var oldCh = oldChars[i];
            var isPadding = i < padNew; // new-string padding: must be removed after rolling, otherwise leading 0s remain
            var changed = isPadding || i >= oldChars.length || oldCh !== ch;
            // Whitespace must NOT be wrapped in .ink-reel: the clipping window is
            // inline-block, so a window holding a single space is always 0 wide and the
            // space visually disappears. Emit a plain text node instead.
            if (/^\s$/.test(ch)) {
                el.appendChild(document.createTextNode(ch));
                return;
            }
            var s = document.createElement('span');
            s.className = 'ink-reel';
            // Record this window's TARGET character -> logicalText uses it instead of the
            // whole digit strip (otherwise the next page switch treats the cell string
            // as the old value, causing exponential digit growth; see the logicalText comment)
            if (!isPadding) s.setAttribute('data-ink-ch', ch);
            if (!changed) {
                s.textContent = ch;
                el.appendChild(s);
                return;
            }
            // Odometer pattern: s = static clipping window, track = animated scroll track.
            // The animation must never attach to s (the window) — the window would fly
            // out of the container together with its content.
            var track = document.createElement('span');
            track.className = 'ink-reel-track';
            // digit strip: from old value to new value cell by cell (same-direction
            // shortest path), capped at 10 cells to prevent infinite loops
            var dir = oldCh != null ? digitStep(oldCh, ch) : 1;
            var seq = [];
            if (oldCh != null && /\d/.test(oldCh) && /\d/.test(ch)) {
                var cur = parseInt(oldCh, 10);
                var target = parseInt(ch, 10);
                var steps = 0;
                seq.push(String(cur));
                while (cur !== target && steps < 10) {
                    cur = (cur + dir + 10) % 10;
                    seq.push(String(cur));
                    steps++;
                }
            } else {
                if (oldCh != null) seq.push(oldCh);
                seq.push(ch);
            }
            seq.forEach(function(d) {
                var c = document.createElement('span');
                c.className = 'ink-reel-cell';
                c.textContent = d;
                track.appendChild(c);
            });
            s.appendChild(track);
            el.appendChild(s);
            var cells = track.children.length;
            var per = 90; // per-cell duration
            // em, not %: % resolves against the track's own total height (the whole strip); one cell = 1em.
            var anim = track.animate(
                [{ transform: 'translateY(0)' }, { transform: 'translateY(-' + (cells - 1) + 'em)' }],
                { duration: per * cells, delay: 100 + i * 60, easing: FLIP_EASE, fill: 'backwards' }
            );
            // Settle the character immediately when the animation ends (anim.finished,
            // not a setTimeout guess): removes the old-value flash-back window between
            // finish and the character swap.
            var settle = function() {
                if (!s.isConnected) return;
                if (isPadding) { collapsePaddingSlot(s); return; } // padding slot: collapse smoothly, then remove
                s.textContent = ch;
            };
            if (anim && anim.finished) {
                anim.finished.then(settle).catch(settle);
            } else {
                setTimeout(settle, per * cells + 100 + i * 60 + 80);
            }
        });
    }

    // Meta row playback (persistent layer): #article-meta is hx-preserved across page
    // switches; only content is synced and animations play on the real elements ->
    // no old clones, zero ghosting.
    // Paired children: position change -> translateX(-dx->0) slide to the new spot (FLIP);
    // text change -> wipe-in for cat / per-digit reel for date/words (buildReel);
    // added element (updated appears) -> wipe-in, right-side elements yield via their own FLIP;
    // removed element (updated disappears) -> already removed by the innerHTML sync,
    // right-side FLIP closes up.
    function playMeta(glass) {
        var cap = armedMeta; armedMeta = null;
        var meta = document.getElementById('article-meta');
        if (!cap || !meta) return;
        // Sync new content into the persistent element (before:settle, no paint yet ->
        // zero naked frame): children are rebuilt fresh but not cloned; buildReel rolls
        // the real new text element from old to new values.
        if (pendingMetaHTML != null && pendingMetaHTML !== '') {
            meta.innerHTML = pendingMetaHTML;
        }
        pendingMetaHTML = null;
        var glassRect = glass.getBoundingClientRect();
        var FLIP_DUR = 480;
        var FLIP_EASE = 'cubic-bezier(0.45, 0, 0.15, 1)';

        Array.prototype.forEach.call(meta.children, function(el) {
            var key = metaKey(el);
            if (!key) return;
            var old = cap.items[key];
            var r = el.getBoundingClientRect();
            var newLeft = r.left - glassRect.left;

            if (!old) {
                // Added (updated appears): left->right wipe-in; right-side elements yield via their own FLIP
                el.animate(
                    [{ clipPath: 'inset(0 100% 0 0)', opacity: 0.4 }, { clipPath: 'inset(0 0 0 0)', opacity: 1 }],
                    { duration: 420, delay: 140, easing: 'cubic-bezier(0.16,1,0.3,1)', fill: 'backwards' }
                );
                return;
            }
            var dx = old.left - newLeft;
            if (Math.abs(dx) >= 2) {
                // Container FLIP: element slides from old position to new
                el.animate(
                    [{ transform: 'translateX(' + dx + 'px)' }, { transform: 'translateX(0)' }],
                    { duration: FLIP_DUR, delay: 60, easing: FLIP_EASE, fill: 'backwards' }
                );
            }

            // Text change animations (no ghost clones; performed on the real elements):
            // views does not roll — stats.js re-seeds the reel slots on page switch and
            // owns the view-count animation.
            var newPill = el.querySelector('.pill-label');
            var newTextEl = newPill || el.querySelector('.meta-text') || el.querySelector('b');
            var textChanged = newTextEl && logicalText(newTextEl) !== old.text;
            if (!textChanged || key === 'views') return;
            // Text change: cat -> wipe-in; date/updated/words -> per-digit reel
            if (newPill) {
                newPill.animate(
                    [{ clipPath: 'inset(0 100% 0 0)' }, { clipPath: 'inset(0 0 0 0)' }],
                    { duration: 360, delay: 120, easing: 'cubic-bezier(0.16,1,0.3,1)', fill: 'backwards' }
                );
            } else if (newTextEl) {
                buildReel(newTextEl, old.text);
            }
        });
    }

    // Title shatter capture (before:swap): same data shape as nav _shatter
    function captureTitle() {
        var t = document.getElementById('article-title');
        if (!t) return null;
        var glass = document.querySelector('.post-center .post-article');
        var glassRect = glass.getBoundingClientRect();
        var tRect = t.getBoundingClientRect();
        return {
            text: t.textContent || '',
            fontCss: fontCssOf(t),
            left: tRect.left - glassRect.left,
            top: tRect.top - glassRect.top,
            width: tRect.width,
            lineHeight: getComputedStyle(t).lineHeight
        };
    }

    // Title playback: old title shatters per character (same as nav) + new title
    // clip-path left->right wipe-in
    // Prefix width measurement: nav _shatter technique; hidden spans measure each
    // character's x offset.
    function playTitle(glass) {
        var cap = armedTitle; armedTitle = null;
        var title = document.getElementById('article-title');
        if (!cap || !title || cap.text === (title.textContent || '')) return;

        // ghost: per-character spans, each scale->0 + random rotation, staggered left-to-right within the line
        var ghost = document.createElement('div');
        ghost.className = 'ink-ghost';
        glass.appendChild(ghost);
        ghosts.push(ghost);
        var holder = document.createElement('div');
        holder.className = 'ink-title-ghost';
        holder.style.left = cap.left + 'px';
        holder.style.top = cap.top + 'px';
        holder.style.width = cap.width + 'px';
        holder.style.cssText += cap.fontCss;
        holder.style.lineHeight = cap.lineHeight;
        ghost.appendChild(holder);

        var chars = Array.from(cap.text);
        var prefix = '';
        var SPEED = 256;
        chars.forEach(function(ch, ci) {
            var w = measureText(prefix, cap.fontCss);
            var s = document.createElement('span');
            s.className = 'ink-char';
            s.textContent = ch;
            s.style.left = w + 'px';
            holder.appendChild(s);
            s.animate(
                [
                    { transform: 'scale(1) rotate(0deg)', opacity: 1 },
                    { transform: 'scale(0) rotate(' + ((Math.random() - 0.5) * 80) + 'deg)', opacity: 0 }
                ],
                { duration: Math.max(200, cap.text.length * 12), delay: ci * (SPEED / Math.max(1, cap.text.length)) , easing: 'ease-in', fill: 'forwards' }
            );
            prefix += ch;
        });
        setTimeout(function() { if (ghost.parentElement) ghost.parentElement.removeChild(ghost); }, 1200);

        // New title: left->right wipe-in (same clip-path wipe as the nav label)
        title.animate(
            [{ clipPath: 'inset(0 100% 0 0)' }, { clipPath: 'inset(0 0 0 0)' }],
            { duration: 600, delay: 160, easing: 'cubic-bezier(0.16, 1, 0.3, 1)', fill: 'backwards' }
        );
    }

    // Tag capture/playback: old tags shatter apart + new tags pop in one by one (offset from the title timing)
    function captureTags() {
        var wrap = document.getElementById('article-tags');
        if (!wrap) return null;
        var glass = document.querySelector('.post-center .post-article');
        var glassRect = glass.getBoundingClientRect();
        var tags = [];
        wrap.querySelectorAll('.tag').forEach(function(tag, i) {
            var r = tag.getBoundingClientRect();
            var label = tag.querySelector('.pill-label') || tag;
            tags.push({
                text: label.textContent || '',
                html: tag.outerHTML.replace(/\sid="[^"]*"/g, ''),
                fontCss: fontCssOf(label),
                left: r.left - glassRect.left,
                top: r.top - glassRect.top,
                width: r.width,
                height: r.height,
                idx: i
            });
        });
        if (!tags.length) return null;
        var wrapRect = wrap.getBoundingClientRect();
        return { left: wrapRect.left - glassRect.left, top: wrapRect.top - glassRect.top, tags: tags };
    }

    function playTags(glass) {
        var cap = armedTags; armedTags = null;
        var wrap = document.getElementById('article-tags');
        if (!cap) return;

        // Old tags: exactly the same per-character shatter as the title (prefix width
        // measurement -> per-character spans -> scale->0 + random rotation, same
        // parameters/curve as playTitle)
        var ghost = document.createElement('div');
        ghost.className = 'ink-ghost';
        glass.appendChild(ghost);
        ghosts.push(ghost);
        cap.tags.forEach(function(t) {
            var holder = document.createElement('div');
            holder.className = 'ink-tag-ghost';
            holder.style.left = t.left + 'px';
            holder.style.top = t.top + 'px';
            holder.style.width = t.width + 'px';
            holder.style.height = t.height + 'px';
            ghost.appendChild(holder);

            var chars = Array.from(t.text);
            var prefix = '';
            var SPEED = 256;
            chars.forEach(function(ch, ci) {
                var w = measureText(prefix, t.fontCss);
                var s = document.createElement('span');
                s.className = 'ink-char';
                s.textContent = ch;
                s.style.left = w + 'px';
                if (t.fontCss) s.style.cssText += t.fontCss;
                holder.appendChild(s);
                s.animate(
                    [
                        { transform: 'scale(1) rotate(0deg)', opacity: 1 },
                        { transform: 'scale(0) rotate(' + ((Math.random() - 0.5) * 80) + 'deg)', opacity: 0 }
                    ],
                    { duration: Math.max(200, chars.length * 12), delay: ci * (SPEED / Math.max(1, chars.length)), easing: 'ease-in', fill: 'forwards' }
                );
                prefix += ch;
            });
        });
        setTimeout(function() { if (ghost.parentElement) ghost.parentElement.removeChild(ghost); }, 1000);

        // New tags: same clip-path left->right wipe as the title (applied to the tag's inner pill text layer)
        if (!wrap) return;
        wrap.querySelectorAll('.tag').forEach(function(tag, i) {
            var label = tag.querySelector('.pill-label') || tag;
            label.animate(
                [{ clipPath: 'inset(0 100% 0 0)' }, { clipPath: 'inset(0 0 0 0)' }],
                { duration: 600, delay: 160 + i * 60, easing: 'cubic-bezier(0.16, 1, 0.3, 1)', fill: 'backwards' }
            );
        });
    }

    // Comment count digit: per-digit reel on cmt:loaded (same buildReel as dates)
    var prevCmtCount = null;
    function captureCmtCount() {
        var el = document.getElementById('cmt-count');
        prevCmtCount = el ? logicalText(el) : null;
    }
    function playCmtCount() {
        var el = document.getElementById('cmt-count');
        if (!el || prevCmtCount == null) return;
        if (!/^[\d\s]*$/.test(prevCmtCount)) { prevCmtCount = null; return; } // non-numeric (contains the "comments" label) -> do not roll
        var oldText = prevCmtCount.trim();
        prevCmtCount = null;
        if (oldText && logicalText(el) && logicalText(el).trim() !== oldText) {
            buildReel(el, oldText);
        }
    }

    // A: ghost shatter + new content wipe-in (before:settle, new DOM in place but not yet painted)
    function playArticle() {
        var frags = armedArticle; armedArticle = null;
        if (!frags || !frags.length) return;
        var glass = document.querySelector('.post-center .post-article');
        var content = glass ? glass.querySelector('.article-content') : null;
        if (!glass || !content) return;

        // Title: old per-character shatter + new wipe-in (same as nav)
        if (armedTitle) playTitle(glass);

        // Tags: old shatter apart + new pop in one by one (offset from the title for layering)
        if (armedTags) playTags(glass);

        // Meta row orthogonal animation (container FLIP × text replacement)
        if (armedMeta) playMeta(glass);

        // Ghost mode: on long->short (shrink) the rising bottom edge is the eraser and
        // the ghost only fades out silently; on short->long (expand) line-level
        // shattering with falling debris is kept (rightward drift + rotation,
        // top-left->bottom-right wavefront).
        var isShrink = glass.offsetHeight < prevGlassH - 4;

        var ghost = document.createElement('div');
        ghost.className = 'ink-ghost';
        glass.appendChild(ghost);
        ghosts.push(ghost);
        var step = Math.min(12, Math.round(450 / frags.length));
        frags.forEach(function(f, i) {
            var d = document.createElement('div');
            d.className = f.type === 'line' ? 'ink-line' : 'ink-block';
            if (f.type === 'line') d.textContent = f.text;
            else d.innerHTML = f.html;
            d.style.left = f.left + 'px';
            d.style.top = f.top + 'px';
            d.style.width = f.width + 'px';
            if (f.fontCss) d.style.cssText += f.fontCss;
            ghost.appendChild(d);
            if (isShrink) {
                d.animate(
                    [{ opacity: 1 }, { opacity: 0 }],
                    { duration: 300, delay: i * step, easing: 'ease-out', fill: 'forwards' }
                );
            } else {
                var drift = 10 + Math.random() * 16;
                var fall = 8 + Math.random() * 12;
                var rot = (Math.random() - 0.5) * 10;
                d.animate(
                    [
                        { transform: 'translate(0,0) rotate(0deg) scale(1)', opacity: 1 },
                        { transform: 'translate(' + drift + 'px,' + fall + 'px) rotate(' + rot + 'deg) scale(0.93)', opacity: 0 }
                    ],
                    { duration: 300, delay: i * step, easing: 'ease-in', fill: 'forwards' }
                );
            }
        });
        setTimeout(function() { if (ghost.parentElement) ghost.parentElement.removeChild(ghost); }, step * frags.length + 450);

        // New content: per-block clip-path corner expansion (top-left sweeping to
        // bottom-right), staggered top-down across blocks
        var blocks = flattenBlocks(content);
        var bStep = Math.min(70, Math.round(420 / Math.max(1, blocks.length)));
        blocks.forEach(function(el, i) {
            el.animate(
                [
                    { clipPath: 'polygon(0 0, 0 0, 0 0, 0 0)' },
                    { clipPath: 'polygon(0 0, 100% 0, 100% 100%, 0 100%)' }
                ],
                { duration: 360, delay: 120 + i * bStep, easing: 'cubic-bezier(0.16,1,0.3,1)', fill: 'backwards' }
            );
        });
    }

    // B: comment FLIP capture (before:swap, glass body preserved by morph, not rebuilt)
    function captureCmtFlip(target) {
        var map = {};
        target.querySelectorAll('.cmt-item[data-cmt-id], .gb-list .profile-card[data-cmt-id]').forEach(function(el) {
            map[el.getAttribute('data-cmt-id')] = el.getBoundingClientRect().top;
        });
        armedCmtFlip = map;
    }

    // B: FLIP playback (before:settle)
    // Sorting semantics: sort links and header copy stay fixed; item reordering is
    // expressed as positional FLIP. Item entrance animations were removed entirely
    // (comments load dynamically; loading itself is the animation), so no is-sorting
    // suppression is needed.
    function playCmtFlip() {
        var map = armedCmtFlip; armedCmtFlip = null;
        if (!map) return;
        var area = document.getElementById('cmt-comment-area');
        if (!area) return;
        // Defense against stale SWR fragments (removed before paint, invisible): a
        // cached copy of the old structure brought through morph can carry an extra
        // .cmt-list-head into the dynamic area (double-stacking with the persistent
        // header) -> remove on sight.
        area.querySelectorAll('.cmt-list-head').forEach(function(el) { el.remove(); });
        area.querySelectorAll('.cmt-item[data-cmt-id], .gb-list .profile-card[data-cmt-id]').forEach(function(el) {
            var id = el.getAttribute('data-cmt-id');
            var oldTop = map[id];
            if (oldTop == null) return;
            var newTop = el.getBoundingClientRect().top;
            if (Math.abs(oldTop - newTop) > 2) {
                // Displaced items: old position -> new (transform on the item itself; on
                // the guestbook the profile-card is itself the glass, so a self
                // transform is safe)
                el.animate(
                    [{ transform: 'translateY(' + (oldTop - newTop) + 'px)' }, { transform: 'translateY(0)' }],
                    { duration: 480, easing: 'cubic-bezier(0.45, 0, 0.15, 1)', fill: 'none' }
                );
            }
        });
    }

    // C: TOC capture (before:swap, old items still present)
    function captureToc() {
        var sidebar = document.querySelector('.post-sidebar-right');
        var list = document.getElementById('tocList');
        if (!sidebar || !list) return;
        var sideRect = sidebar.getBoundingClientRect();
        var listRect = list.getBoundingClientRect();
        var items = [];
        list.querySelectorAll('.toc-item').forEach(function(a) {
            var lab = a.querySelector('.toc-label');
            if (!lab) return;
            var r = a.getBoundingClientRect();
            items.push({
                text: lab.textContent || '',
                fontCss: fontCssOf(lab),
                left: r.left - listRect.left,
                top: r.top - listRect.top,
                width: r.width
            });
        });
        armedToc = { offLeft: listRect.left - sideRect.left, offTop: listRect.top - sideRect.top, width: listRect.width, items: items };
        tocEnterArmed = true;
    }

    // Prefix width measurement (same technique as nav _shatter)
    function measureText(text, fontCss) {
        var m = document.createElement('span');
        m.style.cssText = 'position:absolute;left:-9999px;top:-9999px;visibility:hidden;white-space:pre;' + fontCss;
        m.textContent = text;
        document.body.appendChild(m);
        var w = m.getBoundingClientRect().width;
        document.body.removeChild(m);
        return w;
    }

    // C: old TOC ghost per-character shatter (before:settle)
    function playTocGhost() {
        var cap = armedToc; armedToc = null;
        if (!cap || !cap.items.length) return;
        var sidebar = document.querySelector('.post-sidebar-right');
        if (!sidebar) return;
        var ghost = document.createElement('div');
        ghost.className = 'ink-ghost';
        ghost.style.left = cap.offLeft + 'px';
        ghost.style.top = cap.offTop + 'px';
        ghost.style.width = cap.width + 'px';
        sidebar.appendChild(ghost);
        ghosts.push(ghost);
        cap.items.forEach(function(it, ii) {
            var line = document.createElement('div');
            line.className = 'ink-toc-line';
            line.style.left = it.left + 'px';
            line.style.top = it.top + 'px';
            line.style.width = it.width + 'px';
            line.style.cssText += it.fontCss;
            ghost.appendChild(line);
            var chars = Array.from(it.text);
            var prefix = '';
            chars.forEach(function(ch, ci) {
                var w = measureText(prefix, it.fontCss);
                var s = document.createElement('span');
                s.className = 'ink-char';
                s.textContent = ch;
                s.style.left = w + 'px';
                line.appendChild(s);
                s.animate(
                    [
                        { transform: 'scale(1) rotate(0deg)', opacity: 1 },
                        { transform: 'scale(0) rotate(' + ((Math.random() - 0.5) * 80) + 'deg)', opacity: 0 }
                    ],
                    { duration: 280, delay: ii * 60 + ci * 24, easing: 'ease-in', fill: 'forwards' }
                );
                prefix += ch;
            });
        });
        setTimeout(function() { if (ghost.parentElement) ghost.parentElement.removeChild(ghost); }, 1200);
    }

    // C: new TOC per-character pop-in (toc:rebuilt, after toc.js rebuild completes)
    function playTocEnter() {
        if (!tocEnterArmed) return;
        tocEnterArmed = false;
        var list = document.getElementById('tocList');
        if (!list) return;
        list.querySelectorAll('.toc-item .toc-label').forEach(function(lab, ii) {
            var text = Array.from(lab.textContent || '');
            lab.textContent = '';
            text.forEach(function(ch, ci) {
                var s = document.createElement('span');
                s.className = 'ink-char-in';
                s.textContent = ch;
                lab.appendChild(s);
                s.animate(
                    [{ opacity: 0, transform: 'translateX(-5px)' }, { opacity: 1, transform: 'translateX(0)' }],
                    { duration: 220, delay: ii * 45 + ci * 18, easing: 'cubic-bezier(0.16,1,0.3,1)', fill: 'backwards' }
                );
            });
        });
    }

    document.addEventListener('htmx:before:swap', function(evt) {
        purgeGhosts();
        armedArticle = null; armedMeta = null; armedTitle = null; armedTags = null; armedCmtFlip = null; armedToc = null; tocEnterArmed = false; prevGlassH = 0; pendingMetaHTML = null;
        if (reduceMQ.matches) return;
        var ctx = evt.detail && evt.detail.ctx;
        var target = ctx && ctx.target;
        // B: comment-area local swap (sort/post) -> glass morph keeps the body, items FLIP
        if (target && target.id === 'cmt-comment-area') { captureCmtFlip(target); return; }
        // A/C: page-level internal post->post switch
        if (!currentIsPostPage()) return;
        var path = pagePath(evt);
        var B = window.SITE_URLS;
        if (!path || path.indexOf(B.pages.post + '/') !== 0) return;
        captureArticle();
        capturePendingMetaHTML(evt);
        captureToc();
    });

    document.addEventListener('htmx:before:settle', function() {
        if (armedCmtFlip) playCmtFlip();
        if (armedArticle) playArticle();
        if (armedToc) playTocGhost();
    });

    document.addEventListener('toc:rebuilt', playTocEnter);
    // Comment count reel: plays after comments arrive asynchronously (syncCommentCount updates the count)
    document.addEventListener('cmt:loaded', function() {
        if (reduceMQ.matches) return;
        playCmtCount();
    });
})();
