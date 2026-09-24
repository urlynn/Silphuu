// Image upload
reinitOnSwap(function initImgUpload() {
    var btn = document.getElementById('cmt-img-btn');
    if (!btn || btn._imgInit) return;
    btn._imgInit = true;
    var input = document.getElementById('cmt-img-input');
    var ta = document.getElementById('cmt-content');
    if (!input || !ta) return;

    btn.addEventListener('click', function() {
        input.click();
    });

    input.addEventListener('change', function() {
        var file = input.files[0];
        if (!file) return;

        // Measure the article content width as the scale target
        var contentEl = document.querySelector('.article-content');
        var targetW = contentEl ? contentEl.offsetWidth : 650;

        var formData = new FormData();
        formData.append('file', file);
        formData.append('image', file); // compatibility
        formData.append('width', targetW);

        btn.disabled = true;
        btn.style.opacity = '0.4';

        var baseURL = (window.getBaseURL ? window.getBaseURL() : '');
        var B = window.SITE_URLS;
        fetch(baseURL + B.comment.uploadImg, {
            method: 'POST',
            body: formData
        }).then(function(r) {
            return r.json().then(function(data) {
                if (!r.ok) throw new Error(data.error || 'upload failed');
                return data;
            });
        }).then(function(data) {
            if (data.url) {
                // Insert Markdown image syntax at the cursor or at the end
                var cursor = ta.selectionStart || ta.value.length;
                var before = ta.value.substring(0, cursor);
                var after = ta.value.substring(cursor);
                ta.value = before + '![](' + data.url + ')' + after;
                ta.dispatchEvent(new Event('input'));
                ta.focus();
            }
        }).catch(function(e) {
            alert(e.message || '上传失败');
        }).finally(function() {
            btn.disabled = false;
            btn.style.opacity = '';
            input.value = '';
        });
    });
});

// Paste image
reinitOnSwap(function initPaste() {
    var tas = [];
    var ta1 = document.getElementById('cmt-content');
    var ta2 = document.getElementById('gb-cmt-content');
    if (ta1 && !ta1._pasteInit) { ta1._pasteInit = true; tas.push(ta1); }
    if (ta2 && !ta2._pasteInit) { ta2._pasteInit = true; tas.push(ta2); }
    if (!tas.length) return;

    tas.forEach(function(ta) {
        ta.addEventListener('paste', function(e) {
            var items = e.clipboardData.items;
            var files = [];
            for (var i = 0; i < items.length; i++) {
                if (items[i].type.indexOf('image') === 0) {
                    var file = items[i].getAsFile();
                    if (file) files.push(file);
                }
            }
            if (!files.length) return;
            e.preventDefault();

            ta.style.opacity = '0.5';
            var uploaded = 0;

            files.forEach(function(file) {
                var contentEl = document.querySelector('.article-content');
                var targetW = contentEl ? contentEl.offsetWidth : 650;
                var formData = new FormData();
                formData.append('file', file);
                formData.append('image', file); // compatibility
                formData.append('width', targetW);

                var baseURL = (window.getBaseURL ? window.getBaseURL() : '');
                var B = window.SITE_URLS;
                fetch(baseURL + B.comment.uploadImg, {
                    method: 'POST',
                    body: formData
                }).then(function(r) {
                    return r.json().then(function(data) {
                        if (!r.ok) throw new Error(data.error || 'upload failed');
                        return data;
                    });
                }).then(function(data) {
                    if (data.url) {
                        var cursor = ta.selectionStart || ta.value.length;
                        var before = ta.value.substring(0, cursor);
                        var after = ta.value.substring(cursor);
                        ta.value = before + '![](' + data.url + ')' + after;
                        ta.dispatchEvent(new Event('input'));
                        ta.focus();
                    }
                }).catch(function(e) {
                    alert('粘贴上传失败: ' + e.message);
                }).finally(function() {
                    uploaded++;
                    if (uploaded === files.length) ta.style.opacity = '';
                });
            });
        });
    });
});

