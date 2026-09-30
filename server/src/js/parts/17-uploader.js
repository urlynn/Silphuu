// Shared image uploader.
//
// XHR rather than fetch: fetch reports no upload progress, and separating the upload phase
// from the transcode phase is the whole point of the indicator — the bytes land in
// milliseconds, the server-side transcode is what takes seconds.
//
// There is no total-request timeout. xhr.timeout would bound the whole exchange — upload,
// server-side transcode and response — so a slow transcode reported "timed out" on an upload
// that had already finished. What is bounded instead is the upload phase alone, by a stall
// watchdog: the bytes are the only part with a client-side signal, so they are the only part
// that can be judged. Everything after them waits as long as the server needs.
//
// The response is parsed as JSON only when the server said it was JSON. nginx answers
// 413/502/504 with an HTML error page, and JSON.parse on that throws "Unexpected token '<'",
// which tells the user nothing about what to do next. The status line decides the message.
//
// The indicator itself lives in _base_layout.html and carries its copy on data-* attributes,
// so this file holds no UI literal and every string still reaches the font subset collector.
//
// Loaded by the comment, editor and photo bundles; see concat.go.

window.silphuuUpload = (function () {
    var HIDE_DONE_MS = 1200;
    var HIDE_FAIL_MS = 5000;
    // How long the upload may make no progress at all before it is called dead. A working
    // connection reports progress continuously, however slowly, so silence means silence.
    var STALL_MS = 30000;
    var hideTimer = null;
    var reserveSeq = 0;

    function box() {
        return document.getElementById('upload-progress');
    }

    function copy(name) {
        var el = box();
        return el ? (el.getAttribute('data-' + name) || '') : '';
    }

    function show(fileName) {
        var el = box();
        if (!el) return null;
        if (hideTimer) {
            clearTimeout(hideTimer);
            hideTimer = null;
        }
        el.hidden = false;
        el.querySelector('.upload-progress__name').textContent = fileName;
        return el;
    }

    // phase: null (uploading) | "transcoding" | "done" | "failed".
    // pct is null when the phase has no measurable progress; the CSS animation takes over.
    function setPhase(el, phase, pct) {
        if (!el) return;
        el.classList.remove('is-transcoding', 'is-done', 'is-failed');
        if (phase) el.classList.add('is-' + phase);
        var label = copy(phase || 'uploading');
        el.querySelector('.upload-progress__state').textContent = pct === null ? label : label + ' ' + pct + '%';
        el.querySelector('.upload-progress__fill').style.width =
            phase === 'done' ? '100%' : (pct === null ? '' : pct + '%');
    }

    function setMessage(el, text) {
        if (el) el.querySelector('.upload-progress__state').textContent = text;
    }

    function hideAfter(ms) {
        hideTimer = setTimeout(function () {
            var el = box();
            if (el) el.hidden = true;
            hideTimer = null;
        }, ms);
    }

    // A JSON body carries the application's own reason, and so does a plain-text one: Go's
    // http.Error sends text/plain, and its message is the actionable part ("表情包名称不合法").
    // An HTML body is a proxy's page, which tells the user nothing — that becomes the generic
    // server line instead.
    function reason(xhr) {
        var ct = xhr.getResponseHeader('Content-Type') || '';
        if (ct.indexOf('application/json') !== -1) {
            try {
                var data = JSON.parse(xhr.responseText);
                if (data && data.error) return data.error;
            } catch (e) { /* not JSON after all — fall through */ }
        } else if (ct.indexOf('text/plain') !== -1) {
            var text = (xhr.responseText || '').trim();
            if (text) return text.length > 120 ? text.slice(0, 120) + '...' : text;
        }
        return copy('server');
    }

    // Replaces value[start:end] and restores the caret where the user left it. The response
    // can land while the user is typing, and a caret that jumps to the end would send the
    // next keystroke to the wrong place -- the same defect as inserting at the wrong offset.
    function splice(ta, start, end, text) {
        var from = ta.selectionStart;
        var to = ta.selectionEnd;
        var delta = text.length - (end - start);
        ta.value = ta.value.slice(0, start) + text + ta.value.slice(end);
        ta.selectionStart = from > end ? from + delta : (from > start ? start + text.length : from);
        ta.selectionEnd = to > end ? to + delta : (to > start ? start + text.length : to);
        ta.dispatchEvent(new Event('input'));
    }

    // opts: { field: <form field name>, fields: { <extra form field>: <value> },
    //         files: <array of files, appended under "field", when one request carries several>,
    //         transcodes: <false when the endpoint returns without a server-side transcode> }.
    // The comment endpoint reads "image" plus a display width; the article and photo endpoints
    // read "file" alone — the field name is part of each endpoint's contract. "transcoding" is
    // not a server signal, only the inference that everything left after the bytes is the
    // server's work, so an endpoint that does no such work turns the phase off.
    var upload = function (url, file, opts) {
        opts = opts || {};
        return new Promise(function (resolve, reject) {
            var batch = opts.files || (file ? [file] : []);
            var el = show(batch.length ? batch[0].name : '');
            setPhase(el, null, 0);

            var fd = new FormData();
            if (opts.files) {
                opts.files.forEach(function (one) { fd.append(opts.field || 'file', one); });
            } else {
                fd.append(opts.field || 'file', file);
            }
            if (opts.fields) {
                Object.keys(opts.fields).forEach(function (key) {
                    fd.append(key, opts.fields[key]);
                });
            }

            var xhr = new XMLHttpRequest();
            xhr.open('POST', url, true);

            // Re-armed by every byte that moves; disarmed for good once they have all moved.
            var stallTimer = null;
            function armStall() {
                clearTimeout(stallTimer);
                stallTimer = setTimeout(function () { xhr.abort(); }, STALL_MS);
            }
            function disarmStall() {
                clearTimeout(stallTimer);
                stallTimer = null;
            }

            xhr.upload.onprogress = function (e) {
                // Without a computable length there is no progress to judge, so no watchdog.
                if (!e.lengthComputable) { disarmStall(); return; }
                var pct = Math.round(e.loaded / e.total * 100);
                if (pct >= 100) {
                    disarmStall();
                    if (opts.transcodes !== false) setPhase(el, 'transcoding', null);
                    else setPhase(el, null, pct);
                } else {
                    armStall();
                    setPhase(el, null, pct);
                }
            };

            xhr.onload = function () {
                disarmStall();
                if (xhr.status >= 200 && xhr.status < 300) {
                    var ct = xhr.getResponseHeader('Content-Type') || '';
                    if (ct.indexOf('application/json') !== -1) {
                        var data = null;
                        try { data = JSON.parse(xhr.responseText); } catch (e) { data = null; }
                        if (data) {
                            setPhase(el, 'done', null);
                            hideAfter(HIDE_DONE_MS);
                            resolve(data);
                            return;
                        }
                    }
                }
                setPhase(el, 'failed', null);
                setMessage(el, copy('failed') + '：' + reason(xhr));
                hideAfter(HIDE_FAIL_MS);
                reject(new Error(reason(xhr)));
            };

            xhr.onerror = xhr.ontimeout = function () {
                disarmStall();
                setPhase(el, 'failed', null);
                setMessage(el, copy('failed') + '：' + copy('network'));
                hideAfter(HIDE_FAIL_MS);
                reject(new Error(copy('network')));
            };

            // Only the watchdog aborts, so reaching here always means the bytes stopped.
            xhr.onabort = function () {
                disarmStall();
                setPhase(el, 'failed', null);
                setMessage(el, copy('failed') + '：' + copy('stalled'));
                hideAfter(HIDE_FAIL_MS);
                reject(new Error(copy('stalled')));
            };

            armStall();
            xhr.send(fd);
        });
    };

    // Takes the insertion point for an upload that has not landed yet. The caller reserves
    // when the file is picked and settles when the response arrives; a caller that instead
    // waits writes the markdown wherever the caret has drifted to by then.
    //
    // The placeholder is ordinary image markdown whose alt text is the indicator's own
    // "uploading" label, so it needs no copy of its own and reads as a state in the preview
    // rather than as a stray token. The token in the URL is what makes several uploads in
    // flight settle independently.
    upload.reserve = function (ta) {
        var token = 'upload-pending-' + (++reserveSeq);
        var placeholder = '![' + copy('uploading') + '](' + token + ')';
        var at = typeof ta.selectionStart === 'number' ? ta.selectionStart : ta.value.length;

        function settle(replacement) {
            var from = ta.value.indexOf(placeholder);
            if (from < 0) return;
            splice(ta, from, from + placeholder.length, replacement);
        }

        splice(ta, at, at, placeholder);
        ta.focus();
        return {
            fill: function (url) { settle('![](' + url + ')'); },
            drop: function () { settle(''); }
        };
    };

    return upload;
})();
