// Commenter avatar. The visitor supplies a link, not a file: nothing is uploaded, and the
// browser proves the link decodes as an image before it is accepted. The probe is the
// preview — one request, straight to wherever the image lives, never to this site.

reinitOnSwap(function initCommentAvatar() {
    var form = document.getElementById('cmt-form');
    if (!form || form._avatarInit) return;
    var btn = document.getElementById('cmt-avatar-btn');
    var dialog = document.getElementById('cmt-avatar-dialog');
    var urlInput = document.getElementById('cmt-avatar-url');
    var hidden = document.getElementById('cmt-avatar-value');
    var img = document.getElementById('cmt-avatar-preview-img');
    var msg = document.getElementById('cmt-avatar-preview-msg');
    if (!btn || !dialog || !urlInput || !hidden || !img || !msg) return;
    form._avatarInit = true;

    var OK = dialog.getAttribute('data-ok') || '';
    var FAIL = dialog.getAttribute('data-fail') || '';
    var HINT = dialog.getAttribute('data-hint') || '';
    var timer = null;
    var token = 0;

    function saveAvatar(value) {
        var u = JSON.parse(localStorage.getItem('cmt_user') || '{}');
        if (value) {
            u.avatar = value;
        } else {
            delete u.avatar;
        }
        localStorage.setItem('cmt_user', JSON.stringify(u));
    }

    function resetPreview() {
        img.style.display = 'none';
        img.removeAttribute('src');
        msg.textContent = HINT;
    }

    // Only a decoded image is written through: a link that does not load must not reach the
    // server, and the last accepted value stands so a half-typed URL cannot clear it.
    function probe(value) {
        var v = (value || '').trim();
        var mine = ++token;
        if (!v) {
            resetPreview();
            hidden.value = '';
            saveAvatar('');
            return;
        }
        var tester = new Image();
        tester.referrerPolicy = 'no-referrer';
        tester.onload = function () {
            if (mine !== token) return;
            img.src = v;
            img.style.display = '';
            msg.textContent = OK;
            hidden.value = v;
            saveAvatar(v);
        };
        tester.onerror = function () {
            if (mine !== token) return;
            img.style.display = 'none';
            msg.textContent = FAIL;
        };
        tester.src = v;
    }

    function openDialog() {
        var saved = JSON.parse(localStorage.getItem('cmt_user') || '{}');
        urlInput.value = hidden.value || saved.avatar || '';
        dialog.classList.add('open');
        probe(urlInput.value);
        urlInput.focus({ preventScroll: true });
    }

    function closeDialog() {
        dialog.classList.remove('open');
    }

    btn.addEventListener('click', function () {
        if (dialog.classList.contains('open')) {
            closeDialog();
        } else {
            openDialog();
        }
    });
    document.getElementById('cmt-avatar-close').addEventListener('click', closeDialog);
    document.getElementById('cmt-avatar-confirm').addEventListener('click', function () {
        probe(urlInput.value);
        closeDialog();
    });
    document.getElementById('cmt-avatar-clear').addEventListener('click', function () {
        urlInput.value = '';
        probe('');
        closeDialog();
    });
    urlInput.addEventListener('input', function () {
        clearTimeout(timer);
        timer = setTimeout(function () { probe(urlInput.value); }, 300);
    });
    urlInput.addEventListener('keydown', function (e) {
        if (e.key !== 'Enter') return;
        e.preventDefault();
        probe(urlInput.value);
        closeDialog();
    });

    // A returning visitor keeps the avatar they set last time, as they keep nick and email.
    var savedUser = JSON.parse(localStorage.getItem('cmt_user') || '{}');
    if (savedUser.avatar && !hidden.value) hidden.value = savedUser.avatar;
});
