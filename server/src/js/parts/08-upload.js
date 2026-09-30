// Comment image upload: the toolbar button and pasting into a comment box.
//
// Both go through the shared uploader (17-uploader.js), which owns the page-level progress
// indicator and the response parsing. This file only decides where the resulting markdown
// lands. The button is deliberately left enabled: the indicator reports progress, and a
// greyed-out control only says "something is happening somewhere", not what.
reinitOnSwap(function initImgUpload() {
    // The article column width is the scale target, so the stored image fits the page it is
    // dropped into. The endpoint reads the file from "image" and the width from "width".
    //
    // The insertion point is reserved when the file is picked, not when the response lands:
    // the transcode takes seconds, and by then the caret is wherever the user has typed to.
    function uploadOne(ta, file) {
        var contentEl = document.querySelector('.article-content');
        var baseURL = (window.getBaseURL ? window.getBaseURL() : '');
        var slot = window.silphuuUpload.reserve(ta);
        return window.silphuuUpload(baseURL + window.SITE_URLS.comment.uploadImg, file, {
            field: 'image',
            fields: { width: contentEl ? contentEl.offsetWidth : 650 }
        }).then(function (data) {
            if (data && data.url) slot.fill(data.url);
            else slot.drop();
        }).catch(function () {
            // The indicator already carries the reason; the placeholder just goes away.
            slot.drop();
        });
    }

    // 1. Toolbar button
    var btn = document.getElementById('cmt-img-btn');
    var input = document.getElementById('cmt-img-input');
    var ta = document.getElementById('cmt-content');
    if (btn && input && ta && !btn._imgInit) {
        btn._imgInit = true;
        btn.addEventListener('click', function () {
            input.click();
        });
        input.addEventListener('change', function () {
            var file = input.files[0];
            input.value = '';
            if (file) uploadOne(ta, file);
        });
    }

    // 2. Paste. The guestbook box is a separate element, so it needs its own binding.
    [document.getElementById('cmt-content'), document.getElementById('gb-cmt-content')].forEach(function (box) {
        if (!box || box._pasteInit) return;
        box._pasteInit = true;
        box.addEventListener('paste', function (e) {
            var files = [];
            var items = e.clipboardData.items;
            for (var i = 0; i < items.length; i++) {
                if (items[i].type.indexOf('image') !== 0) continue;
                var file = items[i].getAsFile();
                if (file) files.push(file);
            }
            if (!files.length) return;
            e.preventDefault();
            files.forEach(function (file) {
                uploadOne(box, file);
            });
        });
    });
});
