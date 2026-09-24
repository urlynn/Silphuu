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

        function updateSizeBtns() {
            panel.querySelectorAll('.sticker-size-btn').forEach(function(b) {
                b.textContent = sizeMode === 'sm' ? '大' : '小';
            });
        }

        // Lazily load a sticker group's images: data-src -> src
        function loadGroup(groupEl) {
            if (!groupEl || groupEl.dataset.loaded) return;
            groupEl.dataset.loaded = '1';
            groupEl.querySelectorAll('.cmt-sticker-item[data-src]').forEach(function(img) {
                var src = img.getAttribute('data-src');
                if (src) {
                    img.removeAttribute('data-src');
                    img.setAttribute('data-static', src);
                    img.src = src;
                }
            });
        }

        function applyAnimMode() {
            panel.querySelectorAll('.cmt-sticker-item').forEach(function(img) {
                if (animMode) {
                    var anim = img.getAttribute('data-anim');
                    if (anim) img.src = anim + '?t=' + Date.now();
                } else {
                    var st = img.getAttribute('data-static');
                    if (st) img.src = st;
                }
            });
            panel.querySelectorAll('.sticker-mode-btn').forEach(function(b) {
                b.textContent = animMode ? '静' : '动';
                b.classList.toggle('active', animMode);
            });
        }

        // Render the sticker panel (build HTML from JSON data)
        function renderPanel(sets) {
            if (!sets || !sets.length) return;
            var html = '<div class="cmt-sticker-groups">';
            sets.forEach(function(set, i) {
                var first = set.Stickers[0];
                html += '<button type="button" class="cmt-sticker-group-tab' + (i === 0 ? ' active' : '') +
                    '" data-group="' + set.Name + '" title="' + set.DisplayName + '">' +
                    '<img src="' + first.ThumbURL + '" data-anim="' + (first.AnimURL || '') + '" alt="' + set.DisplayName + '">' +
                    '</button>';
            });
            var plusTpl = document.getElementById('sticker-plus-icon');
            html += '<button type="button" class="sticker-add-btn" title="贡献表情包">' +
                (plusTpl ? plusTpl.innerHTML : '+') + '</button>';
            html += '<button type="button" class="sticker-mode-btn" title="动">动</button>';
            html += '<button type="button" class="sticker-size-btn" title="切换大小">大</button>';
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
            panel.querySelectorAll('.cmt-sticker-group-body').forEach(loadGroup);
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

    // Submit
    form.addEventListener('submit', function(e) {
        e.preventDefault();
        var fd = new FormData(form);
        var xhr = new XMLHttpRequest();
        var progressWrap = document.getElementById('sticker-upload-progress-wrap');
        var progressFill = document.getElementById('sticker-upload-progress-fill');
        var progressText = document.getElementById('sticker-upload-progress-text');
        var msg = document.getElementById('sticker-upload-msg');
        var submitBtn = document.getElementById('sticker-upload-submit');

        msg.style.display = 'none';
        msg.className = 'sticker-upload-msg';
        submitBtn.disabled = true;

        xhr.upload.addEventListener('progress', function(e) {
            if (e.lengthComputable) {
                var pct = Math.round(e.loaded / e.total * 100);
                progressWrap.style.display = 'flex';
                progressFill.style.width = pct + '%';
                if (pct < 100) {
                    progressText.textContent = '上传中 ' + pct + '%';
                } else {
                    progressText.textContent = '上传完成，转码中 (JXL+AVIF)...';
                }
            }
        });

        xhr.addEventListener('load', function() {
            submitBtn.disabled = false;
            if (xhr.status >= 200 && xhr.status < 300) {
                progressFill.style.width = '100%';
                progressText.textContent = '✓ 转码完成！';
                msg.textContent = '提交成功！';
                msg.className = 'sticker-upload-msg success';
                msg.style.display = 'block';
                form.reset();
                setTimeout(function() {
                    overlay.style.display = 'none';
                    progressWrap.style.display = 'none';
                    progressFill.style.width = '0%';
                    progressText.textContent = '0%';
                    msg.style.display = 'none';
                }, 1500);
            } else {
                msg.textContent = xhr.responseText || '上传失败';
                msg.className = 'sticker-upload-msg error';
                msg.style.display = 'block';
            }
        });

        xhr.addEventListener('error', function() {
            submitBtn.disabled = false;
            msg.textContent = '网络错误，请重试';
            msg.className = 'sticker-upload-msg error';
            msg.style.display = 'block';
        });

        var baseURL = (window.getBaseURL ? window.getBaseURL() : '');
        var B = window.SITE_URLS;
        xhr.open('POST', baseURL + B.stickerUpload);
        xhr.send(fd);
    });
})();

