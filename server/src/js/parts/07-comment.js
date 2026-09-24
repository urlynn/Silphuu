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

initLikes();
document.addEventListener('htmx:after:swap', initLikes);

// Comment delete (unified: admin or owner via LocalStorage token)
document.addEventListener('click', function(e) {
    var btn = e.target.closest('.cmt-del-btn');
    if (!btn) return;
    e.preventDefault();
    var id = btn.dataset.id;
    if (!id) return;
    if (!confirm('删除这条评论？')) return;

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
                alert(d.error || '删除失败');
            }
        }).catch(function() {
            alert('网络错误，删除失败');
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
                alert(d.error || '删除失败，凭证已失效');
            }
        }).catch(function() {
            alert('网络错误，删除失败');
        });
    }
});

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
    var m = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/.exec(ts || "");
    if (!m) return null;
    return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
}
function formatRelTime(ts) {
    var d = parseCmtDate(ts);
    if (!d) return ts || "";
    var diff = Date.now() - d.getTime();
    var res = "";
    if (diff < 60 * 1000) res = "刚刚";
    else if (diff < 3600 * 1000) res = Math.floor(diff / 60000) + " 分钟前";
    else if (diff < 24 * 3600 * 1000) res = Math.floor(diff / 3600000) + " 小时前";
    else if (diff < 7 * 24 * 3600 * 1000) res = Math.floor(diff / 86400000) + " 天前";
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
    var countEl = document.getElementById('cmt-count');
    if (!countEl) return;
    var targetArea = area || document.getElementById('cmt-comment-area');
    if (targetArea) {
        var items = targetArea.querySelectorAll('.cmt-item');
        countEl.textContent = String(items ? items.length : 0);
    }
}

// Sort link active state sync
// The header (including sort links) is persistent, server-rendered by post.html, and
// not rebuilt with fragments; syncing active highlighting with URL ?sort= happens here
// (the old structure relied on server-side classes).
// Sort hx-get is a local swap that does not change the address bar -> on click,
// manually pushState + toggle active.
function syncSortActive() {
    var m = location.search.match(/[?&]sort=(oldest|newest|hottest)/);
    var cur = m ? m[1] : 'oldest';
    document.querySelectorAll('.cmt-sort-link').forEach(function(a) {
        var am = a.getAttribute('hx-get').match(/sort=(oldest|newest|hottest)/);
        a.classList.toggle('active', !!(am && am[1] === cur));
    });
}

(function initSortClick() {
    if (window.__sortClickBound) return;
    window.__sortClickBound = true;
    document.addEventListener('click', function(e) {
        var a = e.target.closest && e.target.closest('.cmt-sort-link');
        if (!a || !a.hasAttribute('hx-get')) return;
        // htmx already handled the local swap; here we only sync the address bar and
        // active state (replaceState creates no history entry, matching the old
        // href=?sort=x behavior — history belongs to comment switching)
        var am = a.getAttribute('hx-get').match(/sort=(oldest|newest|hottest)/);
        if (!am) return;
        var url = location.pathname + '?sort=' + am[1];
        if (location.search.indexOf('sort=' + am[1]) === -1) {
            history.replaceState(null, '', url);
        }
        document.querySelectorAll('.cmt-sort-link').forEach(function(x) {
            x.classList.toggle('active', x === a);
        });
    });
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

    // Sort param comes from URL ?sort= (keeps the user's chosen sort on hard navigation
    // / shared links); defaults to oldest
    var sortMatch = location.search.match(/[?&]sort=(oldest|newest|hottest)/);
    var sortParam = sortMatch ? sortMatch[1] : 'oldest';
    var B = window.SITE_URLS;
    fetch(B.comment.list + '?post_id=' + encodeURIComponent(postId) + '&sort=' + sortParam)
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
            if (incoming) {
                area.innerHTML = incoming.innerHTML;
                var pid = incoming.getAttribute('data-post-id');
                if (pid) area.setAttribute('data-post-id', pid);
            } else {
                area.innerHTML = html;
            }
            // Defense against stale SWR fragments: an old structure adds an extra
            // .cmt-list-head to the dynamic area -> remove on sight.

            area.querySelectorAll('.cmt-list-head').forEach(function(el) { el.remove(); });
            // htmx 4.0 official API: manually injected nodes are not processed by htmx;
            // the sort links' hx-get/hx-target/hx-select attributes inside the fragment
            // would not take effect, and clicks degrade to plain <a> full-page hard
            // navigation (jump to top + aborting in-flight requests with ERR_ABORTED +
            // sort always default). htmx.process re-applies the enhancements, restoring
            // sort clicks to local swaps.
            if (window.htmx) htmx.process(area);
            // The header (sort links) is persistent in post.html and not rebuilt with the
            // fragment -> the URL sort param and active state need JS sync (when the old
            // fragment carried the header, server classes handled it; now the header does
            // not change, so highlight manually).
            syncSortActive();
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
            host.innerHTML = '<span class="cmt-load-error" style="color:var(--text-dim);cursor:pointer;">评论加载失败，点击重试</span>';
            host.querySelector('.cmt-load-error').onclick = function() { loadAsyncComments(); };
        });
}

reinitOnSwap(function initAsyncComments() {
    loadAsyncComments();
    syncCommentCount();
});

