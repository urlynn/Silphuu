// Hero settings panel: every value the first screen shows, edited in one form.
//
// Opened from #hero-settings-fab, the floating entry that takes the read-mode slot on the home
// page. It carries admin-control, so the panel has no entry outside an admin session.
//
// Saving posts the four identity fields to the config gateway, the tag field to the hero
// gateway and, when one was picked, the avatar to the hero avatar gateway; the avatar
// position goes to the background gateway, which owns that field. The page then reloads:
// every one of those values is already baked into the markup on screen, and the tag row is
// markup the server renders. Reloading is what makes the page and the stored record agree.
//
// The avatar goes through the shared uploader (17-uploader.js) so the page-level indicator
// in _base_layout.html is the only progress UI in the engine.
//
// Copy arrives in the markup; this file holds no user-facing literal.
//
// reinitOnSwap, not a plain call: the FAB and the panel can arrive through an htmx swap, which
// replaces #content-wrapper and with it every element this binds to.
(function () {
    function panel() {
        return document.getElementById('heroPanel');
    }

    function base() {
        return window.getBaseURL ? window.getBaseURL() : '';
    }

    function formBody(fields) {
        return Object.keys(fields).map(function (key) {
            return encodeURIComponent(key) + '=' + encodeURIComponent(fields[key]);
        }).join('&');
    }

    // The admin responder answers every commit with {ok, msg}, including its failures, so the
    // body is the only place the reason lives.
    function post(url, body) {
        return fetch(base() + url, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
                'X-Requested-With': 'fetch'
            },
            body: body
        }).then(function (r) {
            return r.json().catch(function () { return null; });
        }).then(function (d) {
            if (!d || d.ok !== 'true') throw new Error((d && d.msg) || '');
            return d;
        });
    }

    function value(id) {
        var el = document.getElementById(id);
        return el ? el.value : '';
    }

    function pressedPos() {
        var p = panel();
        var on = p && p.querySelector('.hero-panel-pos .hero-panel-btn[aria-pressed="true"]');
        return on ? on.getAttribute('data-pos') : '';
    }

    function syncPos() {
        var p = panel();
        if (!p) return;
        var current = p.getAttribute('data-avatar-pos');
        p.querySelectorAll('.hero-panel-pos .hero-panel-btn').forEach(function (b) {
            b.setAttribute('aria-pressed', b.getAttribute('data-pos') === current ? 'true' : 'false');
        });
    }

    function open() {
        var overlay = document.getElementById('hero-panel');
        var p = panel();
        if (!overlay || !p) return;
        var msg = document.getElementById('heroPanelMsg');
        if (msg) msg.textContent = '';
        var file = document.getElementById('heroPanelAvatar');
        var fileName = document.getElementById('heroPanelAvatarName');
        if (file) file.value = '';
        if (fileName) fileName.textContent = '';
        syncPos();
        overlay.classList.add('show');
    }

    function save(event) {
        event.preventDefault();
        var p = panel();
        if (!p) return;
        var msg = document.getElementById('heroPanelMsg');
        var submit = p.querySelector('button[type="submit"]');
        var file = document.getElementById('heroPanelAvatar');
        if (msg) msg.textContent = '';
        if (submit) submit.disabled = true;

        var jobs = [];
        if (file && file.files && file.files[0]) {
            jobs.push(window.silphuuUpload(base() + p.getAttribute('data-commit-avatar'), file.files[0], { field: 'file' }));
        }
        jobs.push(post(p.getAttribute('data-commit-config'), formBody({
            site_name: value('heroPanelSiteName'),
            author: value('heroPanelAuthor'),
            signature: value('heroPanelSignature'),
            motto: value('heroPanelMotto')
        })));
        // One field holding the serialized chips, in the order the chips are in: that is the
        // order the hero renders them in.
        jobs.push(post(p.getAttribute('data-commit-hero'), formBody({
            tags: value('heroPanelTagsValue')
        })));
        var pos = pressedPos();
        if (pos && pos !== p.getAttribute('data-avatar-pos')) {
            jobs.push(post(p.getAttribute('data-commit-avatar-pos'), formBody({
                src: p.getAttribute('data-bg-src'),
                avatar_pos: pos
            })));
        }

        Promise.all(jobs).then(function () {
            location.reload();
        }, function () {
            if (submit) submit.disabled = false;
            if (msg) msg.textContent = p.getAttribute('data-err-save') || '';
        });
    }

    reinitOnSwap(function initHeroPanel() {
        var p = panel();
        if (!p || p._panelInit) return;
        p._panelInit = true;

        // The tag field is the shared chip input, so the panel and the editor behave alike.
        window.initTagInput({
            field: 'heroPanelTags',
            input: 'heroPanelTagInput',
            menu: 'heroPanelTagMenu',
            value: 'heroPanelTagsValue',
            template: 'heroPanelTagTemplate',
            measure: 'heroPanelTagMeasure'
        });

        // The control prints no name of its own, so the span beside it carries what was
        // picked. Without it nothing on screen changes until the save.
        var avatarFile = document.getElementById('heroPanelAvatar');
        var avatarName = document.getElementById('heroPanelAvatarName');
        if (avatarFile && avatarName) {
            avatarFile.addEventListener('change', function () {
                var picked = avatarFile.files && avatarFile.files[0];
                avatarName.textContent = picked ? picked.name : '';
            });
        }

        p.querySelectorAll('.hero-panel-pos .hero-panel-btn').forEach(function (btn) {
            btn.addEventListener('click', function () {
                p.querySelectorAll('.hero-panel-pos .hero-panel-btn').forEach(function (other) {
                    other.setAttribute('aria-pressed', 'false');
                });
                btn.setAttribute('aria-pressed', 'true');
            });
        });

        p.addEventListener('submit', save);
    });

    reinitOnSwap(function initHeroSettingsFab() {
        var fab = document.getElementById('hero-settings-fab');
        if (!fab || fab._panelFab) return;
        fab._panelFab = true;
        fab.addEventListener('click', function (e) {
            e.stopPropagation();
            open();
        });
    });
})();
