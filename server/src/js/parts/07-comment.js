// Copy the comment area reads at runtime rides on <body>'s data-* attributes, which the
// template fills from ui_strings.json — see _base_layout.html. No literal lives here.
function bodyCopy(name) {
    var b = document.body;
    return b ? (b.getAttribute('data-' + name) || '') : '';
}

// Admin activation (zero-flash lighting)
function initAdminState() {
    if (document.cookie.includes('is_admin=1')) {
        document.documentElement.classList.add('is-admin');
        document.body.classList.add('is-admin');
    }
}
initAdminState();
document.addEventListener('htmx:after:swap', initAdminState);

// Own comments token sync & activation
function syncNewCommentCookie() {
    document.cookie.split('; ').forEach(function(c) {
        var p = c.indexOf('=');
        if (p > 0 && c.substring(0, p) === 'new_cmt') {
            var val = decodeURIComponent(c.substring(p + 1));
            var dot = val.indexOf('.');
            if (dot > 0) {
                var cmtId = val.substring(0, dot);
                var token = val.substring(dot + 1);
                var myCmts = JSON.parse(localStorage.getItem('my_cmts') || '{}');
                myCmts[cmtId] = token;
                localStorage.setItem('my_cmts', JSON.stringify(myCmts));
            }
            document.cookie = 'new_cmt=; Path=/; Max-Age=0; SameSite=Lax';
        }
    });
}

function initOwnComments() {
    syncNewCommentCookie();
    var myCmts = JSON.parse(localStorage.getItem('my_cmts') || '{}');
    document.querySelectorAll('.cmt-item[data-cmt-id], .profile-card[data-cmt-id]').forEach(function(el) {
        var id = el.dataset.cmtId;
        if (myCmts[id]) {
            el.classList.add('is-owner');
        }
    });
}
initOwnComments();
document.addEventListener('htmx:after:swap', initOwnComments);

// Comment like
function initLikes() {
    // Read the cookie to restore liked state
    var likedIds = {};
    document.cookie.split('; ').forEach(function(c) {
        var p = c.indexOf('=');
        if (p > 0 && c.substring(0, p) === 'liked') {
            c.substring(p+1).split('_').forEach(function(id) {
                if (id) likedIds[id] = true;
            });
        }
    });
    document.querySelectorAll('.cmt-like-btn').forEach(function(btn) {
        if (likedIds[btn.dataset.id]) {
            btn.classList.add('liked');
        }
    });
}

// Bound once, for the same reason as the delete handler below: htmx's head merge replays this
// bundle on the next comment page, and an unguarded listener would send the like twice.
if (!document._likeClickInit) {
    document._likeClickInit = true;
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.cmt-like-btn');
        if (btn) {
            var id = btn.dataset.id;
            var cnt = btn.querySelector('.cmt-like-cnt');
            var B = window.SITE_URLS;
            fetch(B.comment.like, {
                method: 'POST',
                headers: {'Content-Type': 'application/x-www-form-urlencoded'},
                body: 'id=' + id
            }).then(function(r) { return r.json(); }).then(function(d) {
                if (cnt) cnt.textContent = d.likes;
                if (d.liked) {
                    btn.classList.add('liked');
                } else {
                    btn.classList.remove('liked');
                }
            });
        }
    });
}

initLikes();
document.addEventListener('htmx:after:swap', initLikes);

// Comment delete (unified: admin or owner via LocalStorage token).
//
// Bound once. htmx's head merge drops the bundles of the page being left and adds this one
// back on the way in, so the whole file runs again on the next comment page; an unguarded
// document listener would then fire twice and raise two confirmations for one click.
if (!document._delClickInit) {
    document._delClickInit = true;
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.cmt-del-btn');
        if (!btn) return;
        e.preventDefault();
        var id = btn.dataset.id;
        if (!id) return;
        if (!confirm(bodyCopy('cmt-del-confirm'))) return;

        // The guestbook carries data-cmt-id on the bubble, not on the row: matching that
        // attribute would take the bubble alone and leave the name, date and avatar behind.
        // Its row is therefore matched by position, the comment list's by the attribute.
        var item = btn.closest('.cmt-item[data-cmt-id], .gb-list > .glass-wrap');
        var isAdmin = document.documentElement.classList.contains('is-admin') || document.body.classList.contains('is-admin');
        var myCmts = JSON.parse(localStorage.getItem('my_cmts') || '{}');
        var ownerToken = myCmts[id];

        if (isAdmin) {
            // Admin delete
            var B = window.SITE_URLS;
            fetch(B.comment.del.replace('{id}', encodeURIComponent(id)), {
                method: 'DELETE'
            }).then(function(r) { return r.json(); }).then(function(d) {
                if (d.ok) {
                    var area = item ? item.closest('#cmt-comment-area') : null;
                    var postId = (area && area.dataset.postId) || '';
                    invalidateCommentSWCache(postId);
                    removeCommentDOM(item);
                } else {
                    alert(d.error || bodyCopy('cmt-del-failed'));
                }
            }).catch(function() {
                alert(bodyCopy('cmt-del-network'));
            });
        } else if (ownerToken) {
            // Owner delete via local token
            var B = window.SITE_URLS;
            fetch(B.comment.delOwner, {
                method: 'POST',
                headers: {'Content-Type': 'application/x-www-form-urlencoded'},
                body: 'id=' + encodeURIComponent(id) + '&token=' + encodeURIComponent(ownerToken)
            }).then(function(r) { return r.json(); }).then(function(d) {
                if (d.ok) {
                    delete myCmts[id];
                    localStorage.setItem('my_cmts', JSON.stringify(myCmts));
                    var area = item ? item.closest('#cmt-comment-area') : null;
                    var postId = (area && area.dataset.postId) || '';
                    invalidateCommentSWCache(postId);
                    removeCommentDOM(item);
                } else {
                    alert(d.error || bodyCopy('cmt-del-token'));
                }
            }).catch(function() {
                alert(bodyCopy('cmt-del-network'));
            });
        }
    });
}

function invalidateCommentSWCache(postId) {
    if ('serviceWorker' in navigator && navigator.serviceWorker.controller) {
        navigator.serviceWorker.controller.postMessage({
            type: 'invalidate-comment-cache',
            postId: postId
        });
    }
}

function removeCommentDOM(item) {
    if (item) {
        item.style.transition = 'opacity 0.2s';
        item.style.opacity = '0';
        setTimeout(function() { item.remove(); }, 200);
    }
}

// Form prefill & draft autosave (LocalStorage)
reinitOnSwap(function initCommentStorage() {
    var form = document.getElementById('cmt-form');
    if (!form || form._storageInit) return;
    form._storageInit = true;

    var nickInput = form.querySelector('input[name="nick"]');
    var emailInput = form.querySelector('input[name="email"]');
    var siteInput = form.querySelector('input[name="website"]');
    var ta = document.getElementById('cmt-content');
    var postIdInput = document.getElementById('cmt-post-id');
    var postKey = (postIdInput && postIdInput.value && postIdInput.value !== '0') ? postIdInput.value : 'guestbook';
    var draftKey = 'cmt_draft_' + postKey;

    // Prefill nick/email/website
    var savedUser = JSON.parse(localStorage.getItem('cmt_user') || '{}');
    if (nickInput && savedUser.nick && !nickInput.value) nickInput.value = savedUser.nick;
    if (emailInput && savedUser.email && !emailInput.value) emailInput.value = savedUser.email;
    if (siteInput && savedUser.site && !siteInput.value) siteInput.value = savedUser.site;

    // Restore draft
    if (ta && !ta.value) {
        var savedDraft = localStorage.getItem(draftKey);
        if (savedDraft) {
            ta.value = savedDraft;
            ta.dispatchEvent(new Event('input'));
        }
    }

    // Persist user identity on input. Merged into the stored record rather than rebuilt:
    // the avatar dialog writes its own field into the same record, and rebuilding here
    // would drop it on the next keystroke in one of these three inputs.
    function saveUser() {
        var u = JSON.parse(localStorage.getItem('cmt_user') || '{}');
        u.nick = nickInput ? nickInput.value.trim() : '';
        u.email = emailInput ? emailInput.value.trim() : '';
        u.site = siteInput ? siteInput.value.trim() : '';
        localStorage.setItem('cmt_user', JSON.stringify(u));
    }
    if (nickInput) nickInput.addEventListener('input', saveUser);
    if (emailInput) emailInput.addEventListener('input', saveUser);
    if (siteInput) siteInput.addEventListener('input', saveUser);

    // Debounced draft save on input
    var draftTimer = null;
    if (ta) {
        ta.addEventListener('input', function() {
            clearTimeout(draftTimer);
            draftTimer = setTimeout(function() {
                if (ta.value.trim()) {
                    localStorage.setItem(draftKey, ta.value);
                } else {
                    localStorage.removeItem(draftKey);
                }
            }, 300);
        });
    }

    // On submit: save identity, clear draft, invalidate the SW cache
    form.addEventListener('submit', function() {
        saveUser();
        localStorage.removeItem(draftKey);
        invalidateCommentSWCache(postKey === 'guestbook' ? '0' : postKey);
    });

    // After a successful async HTMX submit: reset the textarea, cancel reply mode,
    // and sync own-comment tokens.
    //
    // The event carries a `{ ctx }` payload: there is no `detail.successful`, the status
    // lives at `ctx.response.status`, and htmx itself treats >= 400 as an error.
    form.addEventListener('htmx:after:request', function(evt) {
        var ctx = (evt.detail && evt.detail.ctx) || {};
        var status = (ctx.response && ctx.response.status) || 0;
        if (status >= 200 && status < 400) {
            if (ta) {
                ta.value = '';
                ta.dispatchEvent(new Event('input'));
            }
            var cancelBtn = document.getElementById('cmt-cancel-reply');
            if (cancelBtn && cancelBtn.style.display !== 'none') {
                cancelBtn.click();
            }
            initOwnComments();
            initLikes();
        }
    });
});

// Comment char count (min 2, max 300)
reinitOnSwap(function initCharCount() {
    var ta = document.getElementById('cmt-content');
    if (!ta || ta._charInit) return;
    ta._charInit = true;
    var cnt = document.getElementById('cmt-chars');
    var limit = document.getElementById('cmt-limit');
    var wrap = document.getElementById('cmt-char-wrap');
    if (!cnt || !limit || !wrap) return;
    var form = document.getElementById('cmt-form');
    function update() {
        var len = ta.value.length;
        cnt.textContent = len;
        if (len < 2) {
            limit.textContent = '2';
            wrap.style.color = 'rgba(230,126,128,0.70)';
        } else if (len > 300) {
            limit.textContent = '300';
            wrap.style.color = 'rgba(230,126,128,0.70)';
        } else {
            limit.textContent = '300';
            wrap.style.color = '';
        }
    }
    ta.addEventListener('input', update);
    update();
    if (form) {
        form.addEventListener('submit', function(e) {
            var len = ta.value.trim().length;
            if (len < 2 || len > 300) {
                e.preventDefault();
                wrap.style.color = 'rgba(230,126,128,0.70)';
                ta.focus();
            }
        });
    }
});

// Dynamic comment relative time: the server renders absolute timestamps as data-ts;
// the client computes relative text live and refreshes periodically.
var CMT_MONTHS = ["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"];
function parseCmtDate(ts) {
    ts = ts || "";
    // A stamp carrying a zone is absolute: hand the offset to the platform.
    if (/(?:Z|[+-]\d{2}:?\d{2})$/.test(ts)) {
        var d = new Date(ts);
        return isNaN(d.getTime()) ? null : d;
    }
    // Zone-less stamps lost their zone when written; browser-local is the only reading left.
    var m = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/.exec(ts);
    if (!m) return null;
    return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
}
function formatRelTime(ts) {
    var d = parseCmtDate(ts);
    if (!d) return ts || "";
    var diff = Date.now() - d.getTime();
    var res = "";
    if (diff < 60 * 1000) res = bodyCopy("cmt-just-now");
    else if (diff < 3600 * 1000) res = Math.floor(diff / 60000) + bodyCopy("cmt-minutes");
    else if (diff < 24 * 3600 * 1000) res = Math.floor(diff / 3600000) + bodyCopy("cmt-hours");
    else if (diff < 7 * 24 * 3600 * 1000) res = Math.floor(diff / 86400000) + bodyCopy("cmt-days");
    else res = d.getDate() + " " + CMT_MONTHS[d.getMonth()] + " " + d.getFullYear();
    return res;
}
function updateRelTimes(scope) {
    var root = scope || document;
    root.querySelectorAll(".cmt-date time[data-ts]").forEach(function (el) {
        var txt = formatRelTime(el.getAttribute("data-ts"));
        if (el.getAttribute("data-rel") === txt) return;
        el.setAttribute("data-rel", txt);
        var parts = txt.split(/\s+/).filter(Boolean);
        while (el.firstChild) el.removeChild(el.firstChild);
        parts.forEach(function (p, i) {
            if (i === 0) el.appendChild(document.createTextNode(p));
            else { var it = document.createElement("i"); it.textContent = p; el.appendChild(it); }
        });
    });
}
reinitOnSwap(function initRelTimes() { updateRelTimes(); });
setInterval(function () { updateRelTimes(); }, 60000);

// Async comment loader for decoupled article pages
function syncCommentCount(area) {
    var targetArea = area || document.getElementById('cmt-comment-area');
    var n = targetArea ? targetArea.querySelectorAll('.cmt-item').length : 0;
    var countEl = document.getElementById('cmt-count');
    if (countEl) countEl.textContent = String(n);
    // The list head sits in the persistent header: rendered once with the page, never swapped
    // with the fragments, so its number would stay at whatever the render saw. Only the number
    // is written here — the unit beside it is a ui_strings entry and stays in the template.
    document.querySelectorAll('.cmt-list-num').forEach(function(el) {
        el.textContent = String(n);
    });
}

// Comment order lives in the browser; the server ships one canonical list.
//
// The fragment carries every comment of the post in commentsForPost's "oldest" order, which
// already promotes orphan replies and keeps each reply after its parent. Every other order is
// a permutation of the top-level sequence alone, so a sort click is a DOM move: no request,
// no second URL to cache, and nothing to purge when a comment changes.
// The order rides in the URL fragment, which the browser never sends, so a sort click adds no
// request and a copied link still restores the reader's order.
function currentSort() {
    var m = location.hash.match(/^#sort=(oldest|newest|hottest)$/);
    return m ? m[1] : 'oldest';
}

function sortLikes(el) {
    var n = el.querySelector('.cmt-like-cnt');
    return n ? (parseInt(n.textContent, 10) || 0) : 0;
}

// ULIDs are monotonic, so a string compare is the creation order — the key commentsForPost
// sorts on.
function cmpULID(a, b) {
    var x = a.getAttribute('data-cmt-id');
    var y = b.getAttribute('data-cmt-id');
    return x < y ? -1 : (x > y ? 1 : 0);
}

// Reorder in place. The list is flat — a reply follows its parent — so the move is emitted as a
// pre-order walk of the same tree and a reply never leaves its parent. Replies keep the ULID
// order the server emitted; only the roots are permuted, which is what commentsForPost does for
// "newest" and "hottest".
function applySort(mode) {
    var area = document.getElementById('cmt-comment-area');
    if (!area) return;
    var items = Array.prototype.slice.call(area.querySelectorAll('.cmt-item[data-cmt-id]'));
    if (items.length < 2) return;

    var byId = {};
    items.forEach(function(el) { byId[el.getAttribute('data-cmt-id')] = el; });

    var roots = [];
    var kids = {};
    items.forEach(function(el) {
        var rid = el.getAttribute('data-rid');
        if (rid && byId[rid]) {
            (kids[rid] || (kids[rid] = [])).push(el);
        } else {
            roots.push(el);
        }
    });

    // Absolute, not relative: every mode sorts the whole root list to one fixed order, so
    // pressing the same button twice cannot toggle. Mirrors commentsForPost's three modes.
    if (mode === 'newest') {
        roots.sort(function(a, b) { return cmpULID(b, a); });
    } else if (mode === 'hottest') {
        roots.sort(function(a, b) {
            var d = sortLikes(b) - sortLikes(a);
            return d !== 0 ? d : cmpULID(b, a);
        });
    } else {
        roots.sort(cmpULID);
    }

    var host = items[0].parentNode;
    var frag = document.createDocumentFragment();
    (function emit(list) {
        list.forEach(function(el) {
            frag.appendChild(el);
            var sub = kids[el.getAttribute('data-cmt-id')];
            if (sub) emit(sub);
        });
    })(roots);
    host.appendChild(frag);
}

// The posted comment is re-rendered into this list by the server, which reads this field to
// pick the order — so a local sort change has to reach it or the morph reorders the list under
// the reader.
function sortFormField(mode) {
    var f = document.querySelector('#cmt-form input[name="sort"]');
    if (f) f.value = mode;
}

function syncSortActive() {
    var cur = currentSort();
    document.querySelectorAll('.cmt-sort-link').forEach(function(a) {
        a.classList.toggle('active', a.getAttribute('data-sort') === cur);
    });
}

function applyCurrentSort() {
    var mode = currentSort();
    applySort(mode);
    syncSortActive();
    sortFormField(mode);
}

(function initSortClick() {
    if (window.__sortClickBound) return;
    window.__sortClickBound = true;
    // Capture phase: the body's boosted navigation listens on body, which the event reaches on
    // the way down, so the click has to be taken here or it becomes a page load.
    document.addEventListener('click', function(e) {
        var a = e.target.closest && e.target.closest('.cmt-sort-link');
        if (!a) return;
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        e.stopPropagation();
        var mode = a.getAttribute('data-sort');
        var area = document.getElementById('cmt-comment-area');
        if (area && window.cmtSortFlip) {
            window.cmtSortFlip(area, function() { applySort(mode); });
        } else {
            applySort(mode);
        }
        // replaceState keeps no history entry: history belongs to comment switching.
        history.replaceState(null, '', location.pathname + '#sort=' + mode);
        syncSortActive();
        sortFormField(mode);
    }, true);
})();

// Old-content stand-in (Mode B visual continuity)
function isPostPage() {
    var cw = document.getElementById('content-wrapper');
    var cls = cw ? (cw.getAttribute('data-body-class') || '') : '';
    return cls.split(/\s+/).indexOf('page-article') !== -1;
}
// After a page switch the new area only contains the server placeholder (a 150ms
// anti-flash delay = invisible); during the fetch window the content is blank -> even
// with identical "no comments", it flickers hidden->blank->reappear.
// Solution: at before:swap grab the old innerHTML; after the swap, pad it into the new
// area on top of the placeholder -> visual continuity; normal replacement once the
// fetch returns. Empty->empty is fully seamless; with comments, the old list briefly
// stands in (the glass is already pinned at the old height, self-consistent). Only
// post->post internal switches pad.
var _prevCmtHTML = null;
document.addEventListener('htmx:before:swap', function() {
    var area = document.getElementById('cmt-comment-area');
    _prevCmtHTML = null;
    // Page type cannot be read from body's class: navbar.js's before:swap listener on
    // body removeAttribute('class') before the event bubbles to document; read
    // #content-wrapper's data-body-class instead (see isPostPage).
    if (!area || !isPostPage()) return;
    // Only fully loaded old content (no placeholder) is worth standing in
    if (area.querySelector('.cmt-loading-placeholder')) return;
    _prevCmtHTML = area.innerHTML;
});

function loadAsyncComments() {
    var area = document.getElementById('cmt-comment-area');
    if (!area) return;
    var postId = area.dataset.postId;
    if (postId === undefined || postId === null || postId === '' || postId === '0') return;

    var placeholder = area.querySelector('.cmt-loading-placeholder');
    if (!placeholder && area.children.length > 0) {
        syncCommentCount(area);
        // A swap into this area (a posted comment) arrives already ordered by the form's sort
        // field; re-applying costs nothing and keeps the two in step if they ever disagree.
        applyCurrentSort();
        return;
    }
    // Stand-in: the placeholder is replaced by the old comments from the post->post
    // switch (visual continuity, see the _prevCmtHTML comment); normal replacement once
    // the fetch returns. Non-switch (first visit) has no stand-in and uses the
    // placeholder anti-flash path.
    if (_prevCmtHTML) {
        area.innerHTML = _prevCmtHTML;
        _prevCmtHTML = null;
    }

    var B = window.SITE_URLS;
    // One URL per post: the list is fetched in the server's canonical order and the reader's
    // order is applied in the browser, so there is no per-order cache entry to keep fresh.
    fetch(B.comment.list + '?post_id=' + encodeURIComponent(postId))
        .then(function(r) { return r.text(); })
        .then(function(html) {
            if (!area.isConnected) return; // after a page switch the old node is detached; discard
            // Never replace via outerHTML: it kills the #cmt-comment-area element
            // itself, and the Mode B height animation attached to the element dies with
            // it -> the comment glass block "teleports". Keep the element and swap only
            // innerHTML so the animation and the inline height pin all survive.
            var tmp = document.createElement('div');
            tmp.innerHTML = html;
            var incoming = tmp.querySelector('#cmt-comment-area');
            area.innerHTML = incoming.innerHTML;
            var pid = incoming.getAttribute('data-post-id');
            if (pid) area.setAttribute('data-post-id', pid);
            // Nodes written with innerHTML are outside htmx's bookkeeping, so they miss the
            // attributes they inherit from the body — hx-boost above all. htmx.process brings
            // them in.
            if (window.htmx) htmx.process(area);
            // The fragment arrives in one canonical order; put it in the reader's order before
            // the browser paints, so nothing moves after paint.
            applyCurrentSort();
            syncCommentCount(area);
            initAdminState();
            initOwnComments();
            initLikes();
            updateRelTimes(area);
            // Mode B comment block bottom-stretch signal (listened for by 12-lifecycle.js)
            area.dispatchEvent(new CustomEvent('cmt:loaded', { bubbles: true }));
        })
        .catch(function() {
            // After the stand-in the placeholder may already be replaced -> fall back to
            // showing the retry notice directly on the area
            var ph = area.querySelector('.cmt-loading-placeholder');
            var host = ph || area;
            host.innerHTML = '<span class="cmt-load-error" style="color:var(--text-dim);cursor:pointer;">' + bodyCopy('cmt-load-error') + '</span>';
            host.querySelector('.cmt-load-error').onclick = function() { loadAsyncComments(); };
        });
}

// The comment list belongs to the moment the page is opened: a prerendered document is not
// that moment, and the fetch it would start is for a page that may never be shown.
function initAsyncComments() {
    if (document.prerendering) return;
    loadAsyncComments();
    syncCommentCount();
}
reinitOnSwap(initAsyncComments);
document.addEventListener('prerenderingchange', initAsyncComments);

