// Photo-wall captions: one label per card, drawn from the pool the wall carries.
//
// The caption is decoration, not a property of the photo — it says nothing about the picture it
// covers, so it is never recorded and never travels with the album. Drawing it here is also what
// lets the same page read differently on every refresh: the wall is a rendered file, so a
// server-side draw would freeze whichever one was rendered.
//
// The pool arrives on #photo-wall as data-photo-labels, an array of [text, kaomoji] pairs. The
// draw is the site-wide non-repeating picker (shuffle bag, 01-core.js) over the pairs' indices,
// so every card in one page gets a different caption until the pool runs out.
//
// The two halves land in separate nodes: the text in .photo-card-label-text (web font), the
// kaomoji in the sibling .sym (native symbol font). They are never concatenated into one node —
// the web font would claim whichever kaomoji characters it happens to own.
(function () {
    var KEY = 'photo-label';

    function fill() {
        var wall = document.getElementById('photo-wall');
        if (!wall) return;
        var pairs;
        try { pairs = JSON.parse(wall.dataset.photoLabels || '[]'); } catch (e) { return; }
        if (!pairs.length) return;

        var indices = pairs.map(function (_, i) { return String(i); });
        wall.querySelectorAll('.photo-card-label').forEach(function (label) {
            if (label._labelFilled) return;
            label._labelFilled = true;
            var pair = pairs[Number(window.pickNoRepeat(KEY, indices))] || [];
            label.querySelector('.photo-card-label-text').textContent = pair[0] || '';
            label.querySelector('.sym').textContent = pair[1] || '';
        });
    }

    reinitOnSwap(fill);
})();
