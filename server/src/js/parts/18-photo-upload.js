// Photo-wall upload: a batch of files, one request each, in order.
//
// Reports through the shared uploader (17-uploader.js), so the page-level indicator in
// _base_layout.html is the only progress UI in the engine. Serial rather than parallel because
// that indicator shows one file at a time, and its file-name line is what tells the author
// which one is in flight.
//
// Every file is announced before it is uploaded, under an id generated here. The two requests
// reference the same id, so a node that only renders the wall can draw the card and publish
// the media URLs without waiting for the bytes to exist. The announcement is fire-and-forget:
// the upload records the same entry, so one that is late, lost or never sent changes nothing.
//
// reinitOnSwap, not a plain call: the wall can arrive through an htmx swap, which replaces
// #content-wrapper and with it the file input this binds to.
(function () {
    // Longer than the indicator's own done display (HIDE_DONE_MS in 17-uploader.js), so the
    // last file's result is readable before the wall redraws.
    var RELOAD_MS = 1400;

    // Mirrors generateSecureID in media_pipeline.go: 8 random bytes as 16 hex digits.
    function newPhotoID() {
        var bytes = new Uint8Array(8);
        window.crypto.getRandomValues(bytes);
        var id = '';
        for (var i = 0; i < bytes.length; i++) {
            id += ('0' + bytes[i].toString(16)).slice(-2);
        }
        return id;
    }

    // The id travels in the query, the way the upload carries it: that is where the endpoint
    // reads it. A body field is the shape it does not look at.
    function announce(baseURL, commitURL, id) {
        return fetch(baseURL + commitURL + '/announce?id=' + id, {
            method: 'POST',
            credentials: 'same-origin'
        }).catch(function () { /* the upload records the same entry */ });
    }

    function uploadNext(index, files) {
        if (index >= files.length) {
            setTimeout(function () { location.reload(); }, RELOAD_MS);
            return;
        }
        var baseURL = window.getBaseURL ? window.getBaseURL() : '';
        var commitURL = window.SITE_URLS.commit.photo;
        var id = newPhotoID();

        announce(baseURL, commitURL, id);

        window.silphuuUpload(baseURL + commitURL + '?id=' + id, files[index], { field: 'file' })
            .then(function () { uploadNext(index + 1, files); },
                  function () { uploadNext(index + 1, files); });
    }

    reinitOnSwap(function initPhotoUpload() {
        var input = document.getElementById('photo-upload-input');
        if (!input || input._uploadInit) return;
        input._uploadInit = true;
        input.addEventListener('change', function () {
            var files = Array.prototype.slice.call(this.files || []);
            this.value = '';
            if (files.length) uploadNext(0, files);
        });
    });
})();
