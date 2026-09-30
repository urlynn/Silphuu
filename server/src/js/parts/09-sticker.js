// Sticker picker (lazy-loaded; first click fetches the index at SITE_URLS.stickersJSON)
var stickerDataCache = null;  // module-level cache; no re-fetch after htmx swaps

reinitOnSwap(function initStickerPicker() {
    function setupStickerPicker(btnId, panelId, taId) {
        var btn = document.getElementById(btnId);
        if (!btn || btn._stickerInit) return;
        btn._stickerInit = true;
        var panel = document.getElementById(panelId);
        var ta = document.getElementById(taId);
        if (!panel || !ta) return;

        var sizeMode = 'sm';
        var animMode = false;
        var panelRendered = false;

        // The panel's copy comes from its data-* attributes, which the template fills from
        // ui_strings.json. This file holds no UI literal of its own, so a wording change is
        // a one-place edit and every string still reaches the font subset collector.
        function copy(name) {
            return panel.getAttribute('data-' + name) || '';
        }

        function updateSizeBtns() {
            panel.querySelectorAll('.sticker-size-btn').forEach(function(b) {
                b.textContent = sizeMode === 'sm' ? copy('size-large') : copy('size-small');
            });
        }

        // Load a group's images. It runs for the group on screen and for the group a tab switch
        // brings in, so it has to be safe to call again: an image the edge refused sits at
        // naturalWidth 0 and never recovers on its own, and re-setting src is the only way to
        // ask for it a second time. A group that is already whole costs one querySelectorAll.
        function loadGroup(groupEl) {
            if (!groupEl) return;
            groupEl.querySelectorAll('.cmt-sticker-item').forEach(function(img) {
                var src = img.getAttribute('data-src');
                if (src) {
                    img.removeAttribute('data-src');
                    img.setAttribute('data-static', src);
                } else if (img.complete && img.naturalWidth > 0) {
                    return;
                } else {
                    src = img.getAttribute('data-static');
                }
                if (!src) return;
                // A failed load leaves a failed entry behind, so a plain re-assignment of the
                // same URL is answered from it. The retry gets a URL of its own instead.
                if (img.complete && img.naturalWidth === 0) {
                    src += (src.indexOf('?') >= 0 ? '&' : '?') + 'r=' + Date.now();
                }
                img.src = src;
            });
        }

        function applyAnimMode() {
            // Only the group on screen. The hidden ones are most of the panel (179 of 204 at
            // this writing), and touching them all is a burst of requests for images nobody is
            // looking at. A group that becomes visible is covered: every caller sets the
            // group's display before calling this.
            panel.querySelectorAll('.cmt-sticker-item').forEach(function(img) {
                if (img.offsetParent === null) return;
                var want = animMode ? img.getAttribute('data-anim') : img.getAttribute('data-static');
                if (!want) return;
                // An item already on this URL is left alone. The thumbnails are content-addressed,
                // so a regenerated one arrives as a different URL; re-assigning the same one would
                // only re-ask a request the browser may hold a refusal for.
                if (img.getAttribute('src') === want && img.complete && img.naturalWidth > 0) return;
                // That refusal needs loadGroup's bypass: a repeat of a refused URL is answered
                // from the entry it left behind.
                if (img.complete && img.naturalWidth === 0) {
                    want += (want.indexOf('?') >= 0 ? '&' : '?') + 'r=' + Date.now();
                }
                img.src = want;
            });
            panel.querySelectorAll('.sticker-mode-btn').forEach(function(b) {
                b.textContent = animMode ? copy('mode-static') : copy('mode-anim');
                b.classList.toggle('active', animMode);
            });
        }

        // Render the sticker panel (build HTML from JSON data)
        //
        // The empty set renders too: the contribute button lives only in this panel, so
        // returning early would leave a visitor with an empty box and no way to open the
        // upload dialog.
        function renderPanel(sets) {
            var plusTpl = document.getElementById('sticker-plus-icon');
            var addBtn = '<button type="button" class="sticker-add-btn" title="' + copy('contribute') + '">' +
                (plusTpl ? plusTpl.innerHTML : '+') + '</button>';

            if (!sets || !sets.length) {
                panel.innerHTML = '<div class="cmt-sticker-groups">' + addBtn + '</div>' +
                    '<div class="cmt-empty">' + (panel.getAttribute('data-empty') || '') + '</div>';
                panelRendered = true;
                return;
            }

            var html = '<div class="cmt-sticker-groups">';
            sets.forEach(function(set, i) {
                var first = set.Stickers[0];
                html += '<button type="button" class="cmt-sticker-group-tab' + (i === 0 ? ' active' : '') +
                    '" data-group="' + set.Name + '" title="' + set.DisplayName + '">' +
                    '<img src="' + first.ThumbURL + '" data-anim="' + (first.AnimURL || '') + '" alt="' + set.DisplayName + '">' +
                    '</button>';
            });
            html += addBtn;
            html += '<button type="button" class="sticker-mode-btn" title="' + copy('mode-anim') + '">' + copy('mode-anim') + '</button>';
            html += '<button type="button" class="sticker-size-btn" title="' + copy('size-toggle') + '">' + copy('size-large') + '</button>';
            html += '</div>';
            sets.forEach(function(set, i) {
                html += '<div class="cmt-sticker-group-body" data-group="' + set.Name + '"' +
                    (i !== 0 ? ' style="display:none;"' : '') + '><div class="cmt-sticker-grid">';
                set.Stickers.forEach(function(st) {
                    html += '<img data-src="' + st.ThumbURL + '" class="cmt-sticker-item"' +
                        ' data-anim="' + (st.AnimURL || '') + '" alt="' + st.Name +
                        '" title="' + st.DisplayName + '" data-shortcode="' + set.DisplayName + '_' + st.Name + '">';
                });
                html += '</div></div>';
            });
            panel.innerHTML = html;
            panelRendered = true;
        }

        // Open the panel and load images
        function openPanel() {
            panel.style.display = 'block';
            panel.querySelectorAll('.cmt-sticker-group-tab img').forEach(function(img) {
                img.setAttribute('data-static', img.src);
            });
            // Only the group on screen. The other six are most of the panel (179 of the 204
            // items at this writing), and asking for all of them at once is a burst the edge
            // answers with 429 for part of it — which is what leaves a group broken until the
            // page is reloaded. A group the reader switches to is loaded by that tab's handler.
            panel.querySelectorAll('.cmt-sticker-group-body').forEach(function(b) {
                if (b.style.display === 'none') return;
                loadGroup(b);
            });
            applyAnimMode();
            var pending = 0;
            panel.querySelectorAll('.cmt-sticker-item[src]').forEach(function(img) {
                if (!img.complete) {
                    pending++;
                    var done = function() {
                        if (--pending === 0) panel.classList.remove('sticker-loading');
                    };
                    img.addEventListener('load', done, { once: true });
                    img.addEventListener('error', done, { once: true });
                }
            });
            if (pending > 0) panel.classList.add('sticker-loading');
            else panel.classList.remove('sticker-loading');
        }

        // Panel clicks (unified delegation: size / anim-static / group switch / sticker insert)
        panel.addEventListener('click', function(e) {
            var sizeBtn = e.target.closest('.sticker-size-btn');
            if (sizeBtn) {
                sizeMode = sizeMode === 'sm' ? 'lg' : 'sm';
                updateSizeBtns();
                return;
            }
            var modeBtn = e.target.closest('.sticker-mode-btn');
            if (modeBtn) {
                animMode = !animMode;
                applyAnimMode();
                return;
            }
            var tab = e.target.closest('.cmt-sticker-group-tab');
            if (tab) {
                panel.querySelectorAll('.cmt-sticker-group-tab').forEach(function(t) {
                    t.classList.remove('active');
                });
                tab.classList.add('active');
                var group = tab.getAttribute('data-group');
                panel.querySelectorAll('.cmt-sticker-group-body').forEach(function(b) {
                    var show = b.getAttribute('data-group') === group;
                    b.style.display = show ? '' : 'none';
                    if (show) loadGroup(b);
                });
                applyAnimMode();
                return;
            }
            var img = e.target.closest('.cmt-sticker-item');
            if (!img) return;
            var sc = img.getAttribute('data-shortcode');
            if (!sc) return;
            var cursor = ta.selectionStart || ta.value.length;
            var before = ta.value.substring(0, cursor);
            var after = ta.value.substring(cursor);
            var shortcode = sizeMode === 'lg' ? ':[' + sc + ']:' : ':' + sc + ':';
            ta.value = before + shortcode + after;
            ta.dispatchEvent(new Event('input'));
            ta.focus();
        });

        // Hover to preview the animated version (static mode only, capture phase)
        panel.addEventListener('mouseenter', function(e) {
            if (animMode) return;
            var img = e.target.closest('.cmt-sticker-item');
            if (!img) return;
            var anim = img.getAttribute('data-anim');
            if (!anim) return;
            img.src = anim;
        }, true);
        panel.addEventListener('mouseleave', function(e) {
            if (animMode) return;
            var img = e.target.closest('.cmt-sticker-item');
            if (!img) return;
            var st = img.getAttribute('data-static');
            if (st) img.src = st;
        }, true);

        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            var isOpen = panel.style.display !== 'none';
            // Close all panels
            document.querySelectorAll('.cmt-sticker-panel').forEach(function(p) {
                p.style.display = 'none';
            });
            if (isOpen) {
                panel.classList.remove('sticker-loading');
                return;
            }
            // First open: fetch data and render
            if (!panelRendered) {
                if (stickerDataCache) {
                    renderPanel(stickerDataCache);
                    openPanel();
                } else {
                    panel.classList.add('sticker-loading');
                    panel.style.display = 'block';
                    var B = window.SITE_URLS;
                    fetch(B.stickersJSON).then(function(r) {
                        if (!r.ok) throw new Error('sticker fetch failed');
                        return r.json();
                    }).then(function(data) {
                        stickerDataCache = data;
                        renderPanel(data);
                        openPanel();
                    }).catch(function() {
                        panel.style.display = 'none';
                        panel.classList.remove('sticker-loading');
                    });
                }
                return;
            }
            openPanel();
        });

        updateSizeBtns();
    }

    setupStickerPicker('cmt-sticker-btn', 'cmt-sticker-panel', 'cmt-content');

    // Click outside closes panels (document-level, bound once)
    if (!document._stickerClickOutsideInit) {
        document._stickerClickOutsideInit = true;
        document.addEventListener('click', function(e) {
            document.querySelectorAll('.cmt-sticker-panel').forEach(function(panel) {
                if (!panel.contains(e.target) && !e.target.closest('#cmt-sticker-btn')) {
                    panel.style.display = 'none';
                }
            });
        });
    }
});

// Sticker upload
(function() {
    var overlay = document.getElementById('sticker-upload-overlay');
    var form = document.getElementById('sticker-upload-form');
    if (!overlay || !form) return;

    // The overlay carries the upload's own data-* set, which the template fills from
    // ui_strings.json. This file holds no UI literal of its own.
    function uploadCopy(name) {
        return overlay.getAttribute('data-' + name) || '';
    }

    // Admins publish straight away, so the address field is a visitor's only. Same test
    // the friends page uses: the class is set by the layout, not by this form.
    var emailField = document.getElementById('sticker-upload-email-field');
    if (emailField) {
        var isAdmin = document.documentElement.classList.contains('is-admin') ||
            document.body.classList.contains('is-admin');
        emailField.style.display = isAdmin ? 'none' : '';
    }

    // The control prints no name of its own, so the span beside it carries what was picked.
    // Without it a visitor sees nothing change until the upload starts.
    var fileInput = document.getElementById('sticker-upload-files');
    var fileName = document.getElementById('sticker-upload-filename');
    if (fileInput && fileName) {
        fileInput.addEventListener('change', function() {
            fileName.textContent = Array.prototype.map.call(fileInput.files || [], function(f) {
                return f.name;
            }).join(', ');
        });
    }

    // Open the modal
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.sticker-add-btn');
        if (!btn) return;
        overlay.style.display = 'flex';
    });

    // Close the modal
    document.getElementById('sticker-upload-close').addEventListener('click', function() {
        overlay.style.display = 'none';
    });
    overlay.addEventListener('click', function(e) {
        if (e.target === overlay) overlay.style.display = 'none';
    });

    // Submit. Progress belongs to the shared uploader and the page-level indicator; the modal
    // keeps only the result line, because the one thing the indicator cannot say is whether
    // the pack is on the site already or waiting for review.
    form.addEventListener('submit', function(e) {
        e.preventDefault();
        var input = document.getElementById('sticker-upload-files');
        var nameField = document.getElementById('sticker-upload-name');
        var msg = document.getElementById('sticker-upload-msg');
        var submitBtn = document.getElementById('sticker-upload-submit');
        var files = Array.prototype.slice.call(input.files || []);
        if (!files.length) return;

        msg.style.display = 'none';
        msg.className = 'sticker-upload-msg';
        submitBtn.disabled = true;

        var baseURL = window.getBaseURL ? window.getBaseURL() : '';
        // One request carries the whole pack: the endpoint reads "files" (several images, or one
        // zip) plus "name", and does no transcoding, so the bytes are the whole wait.
        window.silphuuUpload(baseURL + window.SITE_URLS.stickerUpload, null, {
            field: 'files',
            files: files,
            fields: { name: nameField.value },
            transcodes: false
        }).then(function(res) {
            // Published is the server's answer, not a guess at who is browsing: an admin
            // upload is on the site already, a visitor's is waiting for review.
            var state = (res && res.published) ? uploadCopy('published') : uploadCopy('pending');
            msg.textContent = uploadCopy('submitted') + ' ' + state;
            msg.className = 'sticker-upload-msg success';
            msg.style.display = 'block';
            // A published pack changed the index, so drop the cached copy: the next
            // open fetches it again instead of showing the panel without the new pack.
            if (res && res.published) stickerDataCache = null;
            form.reset();
            // reset() clears the control without firing change, so the echo goes by hand.
            if (fileName) fileName.textContent = '';
            setTimeout(function() {
                overlay.style.display = 'none';
                msg.style.display = 'none';
            }, 1500);
        }).catch(function(err) {
            // The indicator carries the reason as well; the modal repeats it because the modal
            // is what the author is looking at.
            msg.textContent = (err && err.message) ? err.message : uploadCopy('failed');
            msg.className = 'sticker-upload-msg error';
            msg.style.display = 'block';
        }).then(function() {
            submitBtn.disabled = false;
        });
    });
})();

