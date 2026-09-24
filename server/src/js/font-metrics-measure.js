// font-metrics-measure.js — standalone font metrics measurement module (called once
// on admin font save)
//
// CJK is measured with the fixed exemplar glyphs, never Latin letters — see
// docs/GOTCHAS.md §font-metrics-cjk.
//
// True measurement:
//   Ink center: canvas.measureText actualBoundingBox (ink, accurate)
//   Line box center: DOM-measured baseline position (the browser computes from real
//     font metrics), not canvas fontBoundingBox. Canvas fontBoundingBox is the font's
//     "claimed placeholder box", not the same thing as the real DOM line box; shifts
//     computed from it do not match real rendering — the root cause of one font
//     being pushed an extra 1px.
// Does not read :root, does not echo, does not depend on the client preset system.
//
// Exposes:
//   window.BlogFontMetrics.measure(formVals, onProgress?)
//     -> Promise<{ metrics: {base:{shift,ink_height}}, log: [{base,ok,msg}] }>
//   formVals: { base_or_formkey: 'Family|weight', ... }
//   onProgress(entry): per-base live callback {base, ok, msg}
(function (global) {
    'use strict';

    // Sample string for the ink-shift definition (see the audit note in
    // src/13-ink-transition.js); every measurement path must use this same sample so
    // re-measuring the same font yields consistent values
    var SAMPLE = '中文永字';
    // Latin supplemental sample: for "Latin text running inside a CJK font" cases
    //   (typical: comment UA text "Chrome 150.0.0 / macOS 10.15.7" rendered with
    //   --font-caption (LemiMuhe) but purely Latin — compensation computed from the CJK
    //   sample does not apply to it and would misalign it with the CSS dots on the
    //   same line).
    //   Sample 'Hx0': contains NO descender characters (g/y/p/q/j), honoring the
    //   "no descenders in measurement" principle.
    var LATIN_SAMPLE = 'Hx0';
    // The footer's real content is digits + Latin only (no CJK); it must be sampled
    // with a digit string to get correct ink height/offset — CJK glyphs ≈ full em
    // (ink_height≈0.93) while digits are cap-height only (≈0.7, no descender); their
    // ink_height/shift differ, and the CJK sample would make the SVG ~33% taller than
    // the digits, overflowing the digit ink at top and bottom.
    var BASE_SAMPLE = { footer: '2026', 'data-num': '0123456789', symbol: '(・ω<)*', 'display-jp': 'アタシ再生産', 'display-i18n': '永水心明湖字雪国区这林器的在你', 'display-i18n-jp': 'れねわかふやぬすみなおせサヤネホ', 'fun-pill': '404', 'fun-title-sym': '！' };
    var F = 100; // px, canvas reference size for the sampled measurement
    // Measure stroke weight (digit strings, system kaomoji, error-code pills);
    var BASE_INK_WEIGHT = { footer: true, symbol: true, caption: true, body: true, 'display-i18n': true, 'display-i18n-jp': true, 'fun-pill': true };

    // Bases participating in measurement/injection (CSS variable names, hyphenated). data_override_* is not measured separately.
    var BASES = [
        'heading', 'subheading', 'display-heading', 'display-body', 'display-mono', 'display-jp', 'display-i18n', 'display-i18n-jp',
        'article', 'body', 'caption', 'footer', 'data', 'data-num', 'mono', 'serif', 'cmt', 'symbol',
        'fun-pill', 'fun-title', 'fun-title-sym', 'fun-desc', 'fun-btn', 'fun-reroll'
    ];

    function parseFontVal(v) {
        if (!v) return { family: '', weight: '', bevl: '' };
        var parts = String(v).split('|');
        if (parts.length >= 3) return { family: parts[0].trim(), weight: parts[1].trim(), bevl: parts[2].trim() };
        if (parts.length === 2) return { family: parts[0].trim(), weight: parts[1].trim(), bevl: '' };
        return { family: String(v).trim(), weight: '', bevl: '' };
    }

    function getAdminFontFamily(family) {
        if (!family) return '';
        var cleanFam = family.replace(/^['"]|['"]$/g, '');
        if (cleanFam.indexOf('-Admin') > 0 || cleanFam.indexOf(',') > 0) return family;
        return "'" + cleanFam + "-Admin', " + family;
    }

    // Ensure fonts are loaded (admin-page subset first, then source fonts). Failures continue silently.
    function loadFont(family, weight) {
        if (!family || !global.document || !document.fonts || !document.fonts.load) {
            return Promise.resolve();
        }
        var cleanFam = family.replace(/^['"]|['"]$/g, '');
        var spec1 = (weight ? weight + ' ' : '') + F + 'px ' + family;
        var spec2 = (weight ? weight + ' ' : '') + F + 'px ' + "'" + cleanFam + "-Admin'";
        return Promise.all([
            document.fonts.load(spec1).catch(function () {}),
            document.fonts.load(spec2).catch(function () {})
        ]);
    }

    // DOM-measured baseline position: line box (line-height:1) center relative to the
    // baseline (em, + = baseline below the box center).
    function measureBaseRelCenter(family, weight, sample) {
        try {
            var FS = 128; // px
            var fontVal = getAdminFontFamily(family);
            var container = document.createElement('div');
            container.style.cssText = 'position:fixed;left:-9999px;top:0;visibility:hidden;display:inline-flex;align-items:center;line-height:1;' +
                'font-family:' + fontVal + ';font-weight:' + (weight || '400') + ';font-size:' + FS + 'px;';
            var textSpan = document.createElement('span');
            textSpan.textContent = sample || SAMPLE;
            var ref = document.createElement('span');
            ref.style.cssText = 'display:inline-block;width:1px;height:0;vertical-align:baseline;';
            textSpan.appendChild(ref);
            container.appendChild(textSpan);
            document.body.appendChild(container);

            var cr = container.getBoundingClientRect();
            var baselineY = ref.getBoundingClientRect().bottom;
            var baseFromTop = baselineY - cr.top;
            var rel = (baseFromTop - cr.height / 2) / FS;

            container.remove();
            return rel;
        } catch (e) {
            return null;
        }
    }

    // Single-font measurement: returns {shift, inkHeight} or null (not ready / no glyphs)
    // Also measures inkLeftDelta: the fixed left-edge ink difference between CJK and
    // Latin in the same font (em, negative = CJK ink sits right; translate uses it
    // directly as a left shift).
    // Sampling: the CJK representative is "全", the actual first character of the status
    //   menus (all / all IPs / all paths / all sources); Latin uses 'A' (first char of
    //   All); canvas actualBoundingBoxLeft is the browser's real rendered value.
    // Background: the CJK and ASCII glyph systems each have a fixed "ink-to-left-side
    //   bearing" (independent of ink shape); status dropdown CJK items must shift left
    //   by that delta to align with the Latin/digit items.
    var ZN_LEFT_SAMPLE = '全';
    var EN_LEFT_SAMPLE = 'A';
    function measureOne(family, weight, sample, measureWeight, weightSample) {
        try {
            var canvas = document.createElement('canvas');
            var ctx = canvas.getContext('2d');
            if (!ctx || typeof ctx.measureText !== 'function') return null;
            var fontVal = getAdminFontFamily(family);
            ctx.font = (weight ? weight + ' ' : '') + F + 'px ' + fontVal;
            var m = ctx.measureText(sample || SAMPLE);
            var aB = m.actualBoundingBoxAscent, aD = m.actualBoundingBoxDescent;
            if (typeof aB !== 'number') return null;
            if (aB === 0 && aD === 0) return null; // font not ready / no glyphs
            var inkCenter = (aB - aD) / 2 / F;
            var baseRelCenter = measureBaseRelCenter(family, weight, sample);
            if (baseRelCenter === null) return null;
            var out = {
                shift: baseRelCenter - inkCenter,
                inkHeight: (aB + aD) / F,
                inkCenter: inkCenter,
                inkAdvance: m.width / F / sample.length
            };
            // Latin supplemental (Hx0, no descenders): the Latin text's own shift /
            // inkHeight under the same font. Produced only when the font really has
            // Latin glyphs (a Latin-only font's CJK sample falls back, in which case
            // out.* is already the Latin value; measuring again is harmless and keeps
            // things consistent).
            try {
                var mL = ctx.measureText(LATIN_SAMPLE);
                var aBl = mL.actualBoundingBoxAscent, aDl = mL.actualBoundingBoxDescent;
                if (typeof aBl === 'number' && !(aBl === 0 && aDl === 0)) {
                    var baseRelL = measureBaseRelCenter(family, weight, LATIN_SAMPLE);
                    if (baseRelL !== null) {
                        out.shiftLatin = baseRelL - ((aBl - aDl) / 2 / F);
                        out.inkHeightLatin = (aBl + aDl) / F;
                    }
                }
            } catch (e) { /* Latin supplemental failure does not affect the main measurement */ }
            // Ink left-edge delta: CJK representative left edge - Latin representative left edge (em)
            try {
                var mZh = ctx.measureText(ZN_LEFT_SAMPLE);
                var mEn = ctx.measureText(EN_LEFT_SAMPLE);
                var leftZh = typeof mZh.actualBoundingBoxLeft === 'number' ? mZh.actualBoundingBoxLeft : NaN;
                var leftEn = typeof mEn.actualBoundingBoxLeft === 'number' ? mEn.actualBoundingBoxLeft : NaN;
                if (isFinite(leftZh) && isFinite(leftEn)) {
                    out.inkLeftDelta = (leftZh - leftEn) / F;
                }
            } catch (eLeft) {}
            if (measureWeight) {
                try {
                    var HF = 4096, pad = 64;
                    var cv = document.createElement('canvas');
                    cv.width = HF + pad * 2; cv.height = HF;
                    var c2 = cv.getContext('2d');
                    var wSample = weightSample || '0';
                    // Coverage rasterization: single-shot (short sample, original path) /
                    // chunked (long samples: 4 chars per chunk at fixed 800px, averaged
                    // per chunk).
                    // NEVER shrink the font size to cram a long sample into the canvas —
                    // shrinking to ~40px drops strokes below the anti-aliasing threshold
                    // and distorts coverage.
                    c2.fillStyle = '#fff'; c2.fillRect(0, 0, cv.width, cv.height);
                    c2.fillStyle = '#000';
                    c2.font = (weight ? weight + ' ' : '') + HF + 'px ' + fontVal;
                    var pre0 = c2.measureText(wSample);
                    function rasterizeChunk(text, fs) {
                        c2.fillStyle = '#fff'; c2.fillRect(0, 0, cv.width, cv.height);
                        c2.fillStyle = '#000';
                        c2.font = (weight ? weight + ' ' : '') + fs + 'px ' + fontVal;
                        c2.textBaseline = 'alphabetic';
                        c2.fillText(text, pad, HF);
                        var img = c2.getImageData(0, 0, cv.width, cv.height).data;
                        var sum = 0, minY = -1, maxY = -1, minX = cv.width, maxX = -1;
                        for (var y = 0; y < cv.height; y++) {
                            for (var x = 0; x < cv.width; x++) {
                                var idx = (y * cv.width + x) * 4;
                                if (img[idx] < 128) { sum++; if (minY < 0) minY = y; maxY = y; if (x < minX) minX = x; if (x > maxX) maxX = x; }
                            }
                        }
                        if (sum === 0 || maxY < minY) return null;
                        var inkH = (maxY - minY);
                        var density = sum / (inkH * (maxX - minX + 1));
                        return density * inkH / fs;
                    }
                    var strokeEm = null;
                    if (pre0.width <= cv.width - pad * 2) {
                        strokeEm = rasterizeChunk(wSample, HF);
                    } else {
                        var chars = wSample.split('');
                        var total = 0, count = 0;
                        for (var ci = 0; ci < chars.length; ci += 4) {
                            var v = rasterizeChunk(chars.slice(ci, ci + 4).join(''), 800);
                            if (v !== null) { total += v; count++; }
                        }
                        if (count) strokeEm = total / count;
                    }
                    if (strokeEm !== null) out.inkWeight = strokeEm;
                } catch (e2) {}
            }
            return out;
        } catch (e) {
            return null;
        }
    }

    function round5(n) { return Math.round(n * 1e5) / 1e5; }

    function formValFor(formVals, base) {
        if (base === 'data-num') {
            return formValFor(formVals, 'data');
        }
        // display-i18n-jp (kana segments) and display-i18n (kanji segments) are two
        // roles of the same mixed run; each selects its own font and uses its own form key
        var key = base.replace(/-/g, '_');
        var val = formVals[key];
        if (!val && base === 'data') {
            val = formVals['data'] || formVals['data_override_en'] ||
                  formVals['data_override_num'] || formVals['data_override_sym'];
        }
        return val;
    }

    function measure(formVals, onProgress) {
        formVals = formVals || {};
        var log = [];

        var loadTasks = [];
        BASES.forEach(function (base) {
            var p = parseFontVal(formValFor(formVals, base));
            if (p.family) loadTasks.push(loadFont(p.family, p.weight));
        });

        return Promise.all(loadTasks)
            .then(function () {
                return (global.document && document.fonts && document.fonts.ready)
                    ? document.fonts.ready : Promise.resolve();
            })
            .then(function () {
                var acc = {};
                BASES.forEach(function (base) {
                    var val = formValFor(formVals, base);
                    var p = parseFontVal(val);
                    if (!p.family && base === 'symbol') {
                        p.family = 'system-ui, -apple-system, Segoe UI, Noto Sans, sans-serif';
                    }
                    var entry;
                    if (!p.family) {
                        entry = { base: base, ok: true, msg: '未指定独立字体，跳过' };
                    } else {
                        var wSample = (base === 'display-i18n' || base === 'display-i18n-jp') ? (BASE_SAMPLE[base] || SAMPLE) : undefined;
                        var r = measureOne(p.family, p.weight, BASE_SAMPLE[base] || SAMPLE, !!BASE_INK_WEIGHT[base], wSample);
                        if (r) {
                            var rec = { shift: round5(r.shift), ink_height: round5(r.inkHeight) };
                            var msg = 'shift=' + r.shift.toFixed(5) + ' ink_height=' + r.inkHeight.toFixed(4);
                            if (typeof r.inkWeight === 'number') {
                                rec.ink_weight = round5(r.inkWeight);
                                msg += ' ink_weight=' + r.inkWeight.toFixed(4);
                            }
                            if (typeof r.inkLeftDelta === 'number') {
                                rec.ink_left_delta = round5(r.inkLeftDelta);
                                msg += ' ink_left_delta=' + r.inkLeftDelta.toFixed(5);
                            }
                            // Latin supplemental results (for Latin text landing on a CJK font)
                            if (typeof r.shiftLatin === 'number') {
                                rec.shift_latin = round5(r.shiftLatin);
                                rec.ink_height_latin = round5(r.inkHeightLatin);
                                msg += ' shift_latin=' + r.shiftLatin.toFixed(5) +
                                    ' ink_height_latin=' + r.inkHeightLatin.toFixed(4);
                            }
                            // Mixed CJK/Japanese calibration fields (ink center / per-char advance, both baseline-space em)
                            if (typeof r.inkCenter === 'number') {
                                rec.ink_center = round5(r.inkCenter);
                                msg += ' ink_center=' + r.inkCenter.toFixed(5);
                            }
                            if (typeof r.inkAdvance === 'number') {
                                rec.ink_advance = round5(r.inkAdvance);
                                msg += ' ink_advance=' + r.inkAdvance.toFixed(5);
                            }
                            acc[base] = rec;
                            entry = { base: base, ok: true, msg: msg };
                        } else {
                            // Graceful fallback: do not block when measurement is not ready, record a notice
                            entry = { base: base, ok: true, msg: '未测得特殊墨心，保持默认' };
                        }
                    }
                    log.push(entry);
                    if (typeof onProgress === 'function') onProgress(entry);
                });
                return { metrics: acc, log: log };
            });
    }

    global.BlogFontMetrics = { measure: measure, BASES: BASES };
})(window);
