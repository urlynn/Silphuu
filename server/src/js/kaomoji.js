/* kaomoji.js — unified kaomoji component
 * Wraps "system symbol glyphs" (characters not owned by web fonts) in text with
 * <span class="sym">; the global .sym rule applies --font-symbol plus equal-height
 * scaling and an ink lift so they match the neighboring web font's height and are
 * not clipped.
 *
 * Detection: character-level Unicode range classification (symbols, fullwidth forms,
 * Greek, decorations etc. not owned by web fonts). No data scanning, no regex JSON
 * detection, no dependence on font load timing (deterministic, correct on the first
 * frame). nav signatures use symHTML() to build HTML strings (for innerHTML / probe
 * measurement); already-structured splits such as the photo wall (text/kaomoji) use
 * the template {{.Label.Kaomoji}} -> <span class="sym"> directly.
 */
(function () {
  'use strict';

  // Whether the character should be wrapped as a kaomoji (via --font-symbol):
  // basic ASCII / CJK ideographs / kana / CJK punctuation have glyphs in the web
  // rounded font -> not kaomoji; everything else (fullwidth forms, Greek, symbols,
  // arrows, decorations, ´· in Latin-1 Supplement etc.) -> kaomoji.
  function isKaomoji(ch) {
    if (!ch || ch.length === 0) return false;
    var c = ch.codePointAt(0);
    if (c >= 0x20 && c <= 0x7E) return false;        // basic ASCII (incl. ()!? etc.) -> owned by web fonts
    if (c >= 0x3000 && c <= 0x303F) return false;    // CJK symbols and punctuation (，。！？〃 etc.)
    if (c >= 0x3040 && c <= 0x30FF) return false;    // hiragana / katakana (つ ゞ ・ etc.)
    if (c >= 0x3400 && c <= 0x9FFF) return false;    // CJK unified ideographs + Extension A
    if (c >= 0xF900 && c <= 0xFAFF) return false;    // compatibility ideographs
    return true;                                      // everything else -> system symbol glyph
  }

  function escapeHTML(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // Split the string by isKaomoji into plain/kaomoji runs and build HTML with <span class="sym">
  function symHTML(text) {
    if (!text) return '';
    var runs = [], buf = '', inSym = false;
    for (var i = 0; i < text.length; i++) {
      var ch = text[i];
      var k = isKaomoji(ch);
      if (k && !inSym) { if (buf) { runs.push({ t: buf, sym: false }); buf = ''; } inSym = true; }
      else if (!k && inSym) { if (buf) { runs.push({ t: buf, sym: true }); buf = ''; } inSym = false; }
      buf += ch;
    }
    if (buf) runs.push({ t: buf, sym: inSym });
    return runs.map(function (r) {
      return r.sym ? '<span class="sym">' + escapeHTML(r.t) + '</span>' : escapeHTML(r.t);
    }).join('');
  }

  // Wrap kaomoji runs inside text nodes under root in place (for already-rendered DOM such as the photo wall)
  function wrapKaomoji(root) {
    if (!root || root.nodeType !== 1) return;
    var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode: function (n) {
        if (!n.nodeValue || !n.nodeValue.trim()) return NodeFilter.FILTER_REJECT;
        if (n.parentElement && n.parentElement.closest('.sym')) return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      }
    });
    var nodes = [];
    var n;
    while ((n = walker.nextNode())) nodes.push(n);
    nodes.forEach(function (textNode) {
      var s = textNode.nodeValue;
      var runs = [], buf = '', inSym = false;
      for (var i = 0; i < s.length; i++) {
        var ch = s[i];
        var k = isKaomoji(ch);
        if (k && !inSym) { if (buf) { runs.push({ t: buf, sym: false }); buf = ''; } inSym = true; }
        else if (!k && inSym) { if (buf) { runs.push({ t: buf, sym: true }); buf = ''; } inSym = false; }
        buf += ch;
      }
      if (buf) runs.push({ t: buf, sym: inSym });
      if (runs.length <= 1 && !(runs[0] && runs[0].sym)) return; // no kaomoji, leave untouched
      var frag = document.createDocumentFragment();
      runs.forEach(function (r) {
        if (r.sym) {
          var sp = document.createElement('span');
          sp.className = 'sym';
          sp.textContent = r.t;
          frag.appendChild(sp);
        } else {
          frag.appendChild(document.createTextNode(r.t));
        }
      });
      textNode.parentNode.replaceChild(frag, textNode);
    });
  }

  window.symHTML = symHTML;
  window.wrapKaomoji = wrapKaomoji;
})();
