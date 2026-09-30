// Tag input: a chip box with autocomplete. One implementation for the editor's meta bar and
// the hero settings panel.
//
// Enter (or a comma) commits a tag, so the author never types the separator and each tag is an
// item to delete rather than a substring to edit. The suggestions are the site's own tags, most
// used first, which is what keeps a near-duplicate from being typed. They arrive in the field's
// data-tag-vocab attribute, so the value is content and the template escaper is the one that
// quotes it.
//
// opts carries the element ids: field, input, menu, value, template, measure. The chips are
// serialized into the hidden input named by `value`, which is what the form submits.
window.initTagInput = function (opts) {
    var field = document.getElementById(opts.field);
    var input = document.getElementById(opts.input);
    var menu = document.getElementById(opts.menu);
    var value = document.getElementById(opts.value);
    var tpl = document.getElementById(opts.template);
    var measure = document.getElementById(opts.measure);
    if (!field || !input || !menu || !value || !tpl || !measure) return;

    var vocab = [];
    try {
        vocab = JSON.parse(field.getAttribute('data-tag-vocab') || '[]') || [];
    } catch (e) {
        vocab = [];
    }
    var tags = [];
    var matches = [];
    var active = -1;
    var moved = false;

    // The field is as wide as the word in it, measured on a hidden copy of the same word in the
    // same font, plus the two pixels the caret needs.
    function sizeField() {
        measure.textContent = input.value || input.placeholder || '';
        input.style.width = (measure.offsetWidth + 2) + 'px';
    }

    // Every write to the field goes through here, because its width follows its value.
    function setInput(text) {
        input.value = text;
        sizeField();
    }

    // The save path splits the field on an ASCII comma, so a comma can never be part of a tag;
    // stripping it here is what keeps one chip from becoming two on the way back.
    function add(text) {
        text = text.replace(/,/g, '').trim();
        if (!text || tags.indexOf(text) !== -1) return;
        tags.push(text);
        paint();
    }

    function drop(text) {
        var at = tags.indexOf(text);
        if (at < 0) return;
        tags.splice(at, 1);
        paint();
    }

    function paint() {
        Array.prototype.forEach.call(field.querySelectorAll('.tag-input__chip'), function (chip) {
            chip.remove();
        });
        tags.forEach(function (text) {
            var chip = tpl.content.firstElementChild.cloneNode(true);
            chip.querySelector('.tag-input__chip-text').textContent = text;
            chip.querySelector('.tag-input__chip-remove').addEventListener('click', function () {
                drop(text);
            });
            field.insertBefore(chip, input);
        });
        value.value = tags.join(',');
    }

    function commit() {
        add(input.value);
        setInput('');
        hideMenu();
    }

    function hideMenu() {
        menu.hidden = true;
        menu.innerHTML = '';
        matches = [];
        active = -1;
        moved = false;
    }

    function showMenu() {
        var needle = input.value.trim().toLowerCase();
        matches = [];
        for (var i = 0; i < vocab.length && matches.length < 8; i++) {
            var tag = vocab[i];
            if (tags.indexOf(tag) !== -1) continue;
            if (tag.toLowerCase().indexOf(needle) === -1) continue;
            matches.push(tag);
        }
        menu.innerHTML = '';
        if (!needle || !matches.length) { hideMenu(); return; }
        matches.forEach(function (tag) {
            var item = document.createElement('li');
            item.className = 'tag-input__item';
            item.setAttribute('role', 'option');
            item.textContent = tag;
            // mousedown, not click: the input must not blur before the pick is applied.
            item.addEventListener('mousedown', function (e) {
                e.preventDefault();
                add(tag);
                setInput('');
                hideMenu();
                input.focus();
            });
            menu.appendChild(item);
        });
        active = -1;
        moved = false;
        menu.hidden = false;
    }

    function move(delta) {
        if (menu.hidden || !matches.length) return;
        active = (active + delta + matches.length) % matches.length;
        moved = true;
        Array.prototype.forEach.call(menu.children, function (item, i) {
            item.classList.toggle('is-active', i === active);
        });
    }

    input.addEventListener('input', function () {
        sizeField();
        showMenu();
    });
    field.addEventListener('mousedown', function (e) {
        if (e.target === field) input.focus();
    });

    input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') {
            e.preventDefault();   // Enter commits a tag; it must not submit the form
            // The highlighted suggestion is only taken when the author moved to it, so Enter on
            // typed text creates that text. Both branches clear the field: a half-typed word left
            // behind becomes a second tag on the next commit.
            add(moved && matches[active] ? matches[active] : input.value);
            setInput('');
            hideMenu();
        } else if (e.key === ',' || e.key === '，') {
            e.preventDefault();
            commit();
        } else if (e.key === 'Tab' && moved && matches[active]) {
            // Tab keeps its job of leaving the field, so there is no preventDefault here: it only
            // agrees with Enter about which tag the author picked. With nothing highlighted the
            // blur that follows commits the typed text.
            add(matches[active]);
            setInput('');
            hideMenu();
        } else if (e.key === 'Backspace' && !input.value) {
            if (tags.length) drop(tags[tags.length - 1]);
        } else if (e.key === 'ArrowDown') {
            e.preventDefault();
            move(1);
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            move(-1);
        } else if (e.key === 'Escape') {
            hideMenu();
        }
    });

    input.addEventListener('paste', function (e) {
        var text = (e.clipboardData || window.clipboardData).getData('text');
        if (text.indexOf(',') === -1) return;   // one tag: let the field take it as typed
        e.preventDefault();
        text.split(',').forEach(add);
        setInput('');
        hideMenu();
    });

    // A tag typed but never committed would otherwise be dropped by the save.
    input.addEventListener('blur', commit);

    (value.value || '').split(',').forEach(add);
    sizeField();
};
