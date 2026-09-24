// Sponsor card hover material effects (sfx)
// Interaction semantics: every hover randomly draws ONE set from the theme-matched pool
// and plays it; the draw reuses the site-wide non-repeating picker window.pickNoRepeat()
// (shuffle bag, defined in 01-core.js):
//     Light pool -> glass bubbles fx-bubbles / confetti balloons fx-balloons
//                / lucky clovers fx-clovers
//     Dark pool  -> meteors on 30°/38° dual-angle belts fx-meteo
//                / vivid starry sky fx-starry / dual-arc sweep fx-ring-duo
// On hover end the fx-* class is removed -> the whole animation cancels and particles
// return to the opacity:0 resting state.
// Layers are built lazily per drawn token and cached (a repeated token reuses and replays);
// light/dark detection: a probe measures --bg-base's computed color and applies the WCAG
// relative luminance formula (no guessing from theme names — coffee-dark's base is
// actually light), line-for-line equivalent to the bench-verified scheme.
// Style contract: assets/css/components/sponsor-fx.css.
(function() {
    var LIGHT_FX = ['fx-bubbles', 'fx-balloons', 'fx-clovers'];
    var DARK_FX = ['fx-meteo', 'fx-starry', 'fx-ring-duo'];
    var ALL_FX = LIGHT_FX.concat(DARK_FX);

    // Real SVG assets (Iconify: mdi:balloon / three-leaf-clover-svgrepo /
    // clip-art / illustration-svgrepo clover), fill=currentColor adapts to the theme color
    var SVG = {
        clover3Stem: '<svg viewBox="0 0 29 29" fill="currentColor" aria-hidden="true"><path d="M13.673 12.018c.355.35.925.35 1.28 0c1.146-1.132 3.485-3.482 4.068-4.384c.839-1.3 1.676-2.373 1.676-4.245S19.181 0 17.31 0C16 0 14.877.75 14.312 1.837C13.749.75 12.625 0 11.316 0C9.444 0 7.928 1.517 7.928 3.389s.838 2.945 1.677 4.245C10.188 8.535 12.526 10.886 13.673 12.018zM13.056 12.969c-1.423-.756-4.364-2.288-5.399-2.575c-1.491-.413-2.765-.891-4.552-.332c-1.786.559-2.781 2.459-2.223 4.246c.391 1.25 1.443 2.097 2.649 2.311c-.869.863-1.25 2.158-.86 3.408c.559 1.787 2.459 2.78 4.247 2.223c1.786-.559 2.56-1.678 3.55-2.868c.687-.825 2.232-3.759 2.97-5.192C13.667 13.747 13.498 13.203 13.056 12.969zM25.522 10.062c-1.787-.559-3.061-.081-4.552.332c-1.035.287-3.978 1.818-5.399 2.575c-.438.234-.609.778-.382 1.222c.738 1.433 2.283 4.368 2.97 5.192c.99 1.189 1.766 2.31 3.552 2.868c1.787.56 3.688-.437 4.247-2.224c.391-1.25.009-2.545-.86-3.407c1.206-.214 2.258-1.062 2.648-2.311C28.303 12.521 27.309 10.621 25.522 10.062zM17.59 28.312c.375-.588.754-1.176 1.131-1.77c.188-.296.167-.689-.071-.948c-2.698-2.928-3.561-10.005-3.561-10.005c-.031-.367-.321-.673-.701-.71c-.428-.041-.808.272-.849.7c0 0-.087.887-.101 2.188c-.08.578-.237 7.185 3.082 10.647C16.826 28.734 17.352 28.686 17.59 28.312z"/></svg>',
        clover3NoStem: '<svg viewBox="-18 -18 36 36" fill="currentColor" aria-hidden="true"><g transform="translate(0, 1.8) translate(-14.3135, -14.3135)"><path d="M13.673,12.018c0.355,0.35,0.925,0.35,1.28,0c1.146-1.132,3.485-3.482,4.068-4.384c0.839-1.3,1.676-2.373,1.676-4.245S19.181,0,17.31,0C16,0,14.877,0.75,14.312,1.837C13.749,0.75,12.625,0,11.316,0C9.444,0,7.928,1.517,7.928,3.389s0.838,2.945,1.677,4.245C10.188,8.535,12.526,10.886,13.673,12.018z"/></g><g transform="rotate(120) translate(0, 1.8) translate(-14.3135, -14.3135)"><path d="M13.673,12.018c0.355,0.35,0.925,0.35,1.28,0c1.146-1.132,3.485-3.482,4.068-4.384c0.839-1.3,1.676-2.373,1.676-4.245S19.181,0,17.31,0C16,0,14.877,0.75,14.312,1.837C13.749,0.75,12.625,0,11.316,0C9.444,0,7.928,1.517,7.928,3.389s0.838,2.945,1.677,4.245C10.188,8.535,12.526,10.886,13.673,12.018z"/></g><g transform="rotate(240) translate(0, 1.8) translate(-14.3135, -14.3135)"><path d="M13.673,12.018c0.355,0.35,0.925,0.35,1.28,0c1.146-1.132,3.485-3.482,4.068-4.384c0.839-1.3,1.676-2.373,1.676-4.245S19.181,0,17.31,0C16,0,14.877,0.75,14.312,1.837C13.749,0.75,12.625,0,11.316,0C9.444,0,7.928,1.517,7.928,3.389s0.838,2.945,1.677,4.245C10.188,8.535,12.526,10.886,13.673,12.018z"/></g></svg>',
        clover4Stem1: '<svg viewBox="0 0 334 432" fill="currentColor" aria-hidden="true"><path d="M278.609,178.21l-0.808,0.002c0,0-66.043-6.843-65.935-10.413c0.096-3.161,69.04-9.623,69.04-9.623l0.807-0.002c28.287-0.072,51.159-23.061,51.087-51.347c-0.072-28.287-23.061-51.159-51.348-51.087l-0.807,0.002l-0.002-0.807c-0.072-28.287-23.061-51.16-51.347-51.088c-28.287,0.072-51.159,23.061-51.087,51.347L178.212,56c0,0-6.506,68.24-9.869,68.346c-3.363,0.106-10.167-71.45-10.167-71.45l-0.002-0.808C158.101,23.801,135.112,0.928,106.826,1C78.539,1.072,55.666,24.061,55.738,52.348l0.002,0.807l-0.807,0.002c-28.287,0.072-51.16,23.061-51.088,51.347c0.072,28.287,23.061,51.159,51.347,51.087L56,155.59c0,0,71.683,5.825,71.536,8.877c-0.177,3.67-74.641,11.159-74.641,11.159l-0.808,0.002C23.801,175.7,0.928,198.689,1,226.976c0.072,28.287,23.061,51.159,51.348,51.088l0.807-0.002l0.002,0.807c0.072,28.287,23.214,54.03,51.347,51.088c47.295-4.947,40.541-43.363,53.205-72.07c0,0-16.464,110.183,52.027,170.235c10.079,8.837,29.728-4.274,29.921-17.676c-43.566-23.71-72.757-113.861-62.951-142.005c0,0,0.281,66.982,50.269,64.361c28.248-1.481,51.159-23.061,51.087-51.347l-0.002-0.807l0.807-0.002c28.287-0.072,51.16-23.061,51.088-51.348C329.885,201.01,306.896,178.138,278.609,178.21z"/></svg>',
        clover4Stem2: '<svg viewBox="0 0 322 432" fill="currentColor" aria-hidden="true"><path d="M176.292,159.229c-1.769,0.348-2.774-1.988-1.3-3.025c24.685-17.377,74.218-15.837,74.373-15.845c31.778-1.662,48.29-21.017,50.161-46.827c1.818-25.079-17.538-45.608-42.683-45.589l-0.696,0.001l0.036-0.447c1.764-21.685-13.018-41.36-34.384-45.466c-66.41-12.763-77.35,96.433-66.794,144.728c0.349,1.597-1.852,2.38-2.632,0.943c-14.255-26.254-9.752-72.972-9.75-73.123c0.4-31.82-17.847-49.551-43.482-53.089c-24.91-3.438-46.649,14.549-48.256,39.644l-0.044,0.691l-0.723-0.106C29.167,58.636,8.947,71.409,3.166,91.782c-19.286,67.969,95.284,85.592,142.822,76.858c2.079-0.382,3.007,2.525,1.096,3.428c-27.265,12.877-75.558,3.224-75.712,3.206c-31.616-3.62-51.104,12.738-57.22,37.883c-5.943,24.435,9.751,47.883,34.556,52.021l0.681,0.114l-0.111,0.439c-5.327,21.091,5.992,42.936,26.379,50.522c54.546,20.298,83.525-55.278,89.007-108.22c1.477,63.334-13.852,179.929-67.174,206.176c-0.396,10.285,14.398,21.451,22.226,14.769c62-52.922,62.164-177.338,53.042-237.761c9.933,26.164,8.003,62.327,8.007,62.459c0.95,31.808,19.932,48.75,45.695,51.197c25.034,2.378,45.99-16.515,46.53-41.656l0.015-0.691l0.374,0.039c21.62,2.248,41.667-12.019,46.277-33.261C334.039,163.038,224.746,149.685,176.292,159.229z"/></svg>',
        clover4NoStem: '<svg viewBox="0 0 512 512" fill="currentColor" aria-hidden="true"><path d="M384.666,268.189c-40.783,3.87-74.411-2.838-100.612-12.41c26.045-10.036,59.55-17.293,100.392-14.116c81.211,6.336,138.581-45.509,123.556-100.81c-14.721-54.126-75.111-57.666-90.2-49.379c8.043-15.24,3.481-75.559-50.886-89.364c-55.537-14.1-106.412,44.116-98.72,125.217c3.861,40.788-2.851,74.411-12.435,100.616c-10.028-26.046-17.293-59.559-14.104-100.396c6.328-81.202-45.5-138.582-100.806-123.544C86.725,18.711,83.172,79.106,91.455,94.194c-15.222-8.034-75.551-3.48-89.342,50.883C-11.991,200.63,46.238,251.48,127.322,243.8c40.783-3.861,74.41,2.847,100.625,12.435c-26.046,10.02-59.564,17.293-100.406,14.108C46.343,264.016-11.04,315.845,3.985,371.137c14.721,54.135,75.107,57.684,90.2,49.387c-8.031,15.224-3.468,75.56,50.886,89.364c55.55,14.108,106.412-44.116,98.724-125.217c-3.865-40.788,2.856-74.402,12.431-100.616c10.028,26.045,17.306,59.567,14.116,100.404c-6.344,81.195,45.501,138.574,100.794,123.545c54.135-14.717,57.679-75.112,49.383-90.2c15.236,8.042,75.564,3.481,89.368-50.882C523.99,311.376,465.75,260.519,384.666,268.189z"/></svg>',
        balloon: '<svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M13.16 12.74L14 14h-1.5c-.15 2.71-.5 5.41-1 8.08l-1-.16c.5-2.62.84-5.26 1-7.92H10l.84-1.26C8.64 11.79 7 8.36 7 6a5 5 0 0 1 5-5a5 5 0 0 1 5 5c0 2.36-1.64 5.79-3.84 6.74"/></svg>'
    };

    // Meteors, original direction ↘ (dual-angle belts 30°/38°)
    // Start points spread evenly along the vertical axis (fixes the original's
    // top-right-biased coordinate bug); the angle belt spread narrows to 8°.
    // Elements rotate(--ang) collinear with their motion; --gdir:270deg = bright core
    // at the right end (↘).
    function meteorHTML() {
        var out = '';
        [[30, 13], [38, 13]].forEach(function (belt, bi) {
            var th = belt[0] * Math.PI / 180, cnt = belt[1];
            var travel = 300, tx = travel * Math.cos(th), ty = travel * Math.sin(th);
            for (var i = 0; i < cnt; i++) {
                var k = bi * 13 + i;
                var u = (i + 0.5) / cnt;
                var s = (u - 0.5) * 2 * 132;
                var sx = -s * Math.sin(th) - 0.5 * tx;
                var sy = s * Math.cos(th) - 0.5 * ty;
                var len = 64 + (k % 3) * 15;
                var thh = (k % 3 === 1) ? 3 : 2;
                var dur = (1.5 + (k % 4) * 0.3).toFixed(2);
                var d = -(k * 0.093).toFixed(2);
                var op = (0.4 + (k % 3) * 0.13).toFixed(2);
                out += '<i style="left:calc(50% + ' + sx.toFixed(1) + 'px);top:calc(50% + ' + sy.toFixed(1) + 'px);'
                     + '--len:' + len + 'px;--th:' + thh + 'px;--dur:' + dur + 's;--d:' + d + 's;--op:' + op + ';'
                     + '--tx:' + tx.toFixed(1) + 'px;--ty:' + ty.toFixed(1) + 'px;'
                     + '--ang:' + belt[0] + 'deg;--gdir:270deg"></i>';
            }
        });
        return out;
    }

    // Vivid starry sky: rounded star dots + 4/5-point star sparks (no 6-point)
    function starryHTML() {
        var out = '';
        var layers = [
            { n: 20, w: 2.2, wj: 1.2, dur: '1.9s',  op: 0.45, sx: 6,  my: 62, my2: 134, tk: '0.9s'  },
            { n: 10, w: 3.2, wj: 0.8, dur: '1.45s', op: 0.7,  sx: 8,  my: 64, my2: 136, tk: '1.15s' },
            { n: 16, w: 7,   wj: 3,   dur: '2.1s',  op: 0.95, sx: 10, my: 66, my2: 138, tk: '1.4s', star: true }
        ];
        var k = 0;
        var shapes = ['star', 'star5'];
        layers.forEach(function(L) {
            for (var j = 0; j < L.n; j++, k++) {
                var left = (3 + ((k * 23) % 94)).toFixed(1);
                var w = (L.w + (j % 3) * L.wj * 0.4).toFixed(1);
                var sx = ((k % 2 ? 1 : -1) * (L.sx * 0.6 + (j % 3) * 2)).toFixed(1);
                var shapeCls = L.star ? shapes[k % shapes.length] : '';
                out += '<i class="' + shapeCls + '" style="left:' + left + '%;top:' + (-4 - (k % 4) * 7) + 'px;width:' + w + 'px;height:' + w + 'px;'
                     + '--sx:' + sx + 'px;--my:' + (L.my + (j % 3) * 6) + 'px;--my2:' + (L.my2 + (j % 3) * 4) + 'px;'
                     + '--dur:' + (parseFloat(L.dur) + (j % 3) * 0.22).toFixed(2) + 's;--op:' + L.op + ';--d:-' + (k * 0.107).toFixed(2) + 's;">'
                     + '<b style="--tk:' + L.tk + ';--tkd:-' + ((k % 7) * 0.17).toFixed(2) + 's"></b></i>';
            }
        });
        return out;
    }

    // Lucky clovers: 5 real 3/4-leaf variants mixed (5/3/4 pairwise coprime
    // against lockstep repetition)
    function cloversHTML() {
        var out = '';
        var icons = [
            SVG.clover3Stem,    // 3-leaf, stemmed, separated
            SVG.clover3NoStem,  // 3-leaf, stemless, clustered
            SVG.clover4Stem1,   // 4-leaf, stemmed, clustered solid
            SVG.clover4Stem2,   // 4-leaf, stemmed, radial micro-gaps
            SVG.clover4NoStem   // 4-leaf, stemless, clustered micro-heart
        ];
        var cols = [
            'color-mix(in srgb, var(--accent, #8DA101) 72%, #FFFFFF)',
            'var(--accent, #8DA101)',
            'color-mix(in srgb, var(--accent, #8DA101) 84%, #2FBF71)',
            'color-mix(in srgb, var(--accent, #8DA101) 58%, #FFFFFF)'
        ];
        var sizes = [16, 20, 18];   // strictly limited to the 16/18/20 three sizes
        for (var k = 0; k < 25; k++) {
            var left = (3 + ((k * 37) % 94)).toFixed(1);
            var sz = sizes[k % 3];
            var dur = (3.0 + (k % 5) * 0.5).toFixed(2);
            var del = (-(k * 0.21)).toFixed(2);
            var rz = ((k * 61) % 360).toFixed(0);
            var sx = ((k % 2 ? 1 : -1) * (10 + (k % 4) * 7)).toFixed(1);
            out += '<i style="left:' + left + '%;width:' + sz + 'px;height:' + sz + 'px;'
                 + '--c:' + cols[k % 4] + ';--dur:' + dur + 's;--d:' + del + 's;--rz:' + rz + 'deg;--sx:' + sx + 'px;'
                 + 'color:var(--c);">' + icons[k % 5] + '</i>';
        }
        return out;
    }

    // Confetti balloons: real balloon SVGs (with tether; sizes locked to avoid
    // UA-default 300×150 overflow)
    function balloonsHTML() {
        var out = '';
        var colors = ['#FF6B8B', '#FFD166', '#48DBFB', '#A29BFE', '#1DD1A1', 'var(--accent)'];
        for (var k = 0; k < 12; k++) {
            var left = (6 + ((k * 31) % 88)).toFixed(1);
            var sz = (16 + (k % 4) * 4).toFixed(1);
            var dur = (4.2 + (k % 4) * 0.7).toFixed(2);
            var del = (-(k * 0.62)).toFixed(2);
            var sw = ((k % 2 ? 1 : -1) * (8 + (k % 3) * 6)).toFixed(1);
            var rz = ((k % 2 ? 1 : -1) * (4 + (k % 3) * 4)).toFixed(0);
            out += '<i style="left:' + left + '%;top:100%;width:' + sz + 'px;height:' + (sz * 1.5).toFixed(1) + 'px;'
                 + '--c:' + colors[k % colors.length] + ';--dur:' + dur + 's;--d:' + del + 's;'
                 + '--sw:' + sw + 'px;--rz:' + rz + 'deg;color:var(--c);">' + SVG.balloon + '</i>';
        }
        return out;
    }

    // Glass bubbles
    function bubblesHTML() {
        var out = '';
        var colors = ['var(--accent)', '#48DBFB', '#A29BFE', '#FF9FF3'];
        for (var k = 0; k < 15; k++) {
            var left = (5 + Math.random() * 90).toFixed(1);
            var top = (70 + Math.random() * 20).toFixed(1);
            var sz = (10 + Math.random() * 25).toFixed(1);
            var dur = (2 + Math.random() * 2).toFixed(2);
            var del = (Math.random() * -2).toFixed(2);
            var sw = ((Math.random() > 0.5 ? 1 : -1) * (10 + Math.random() * 20)).toFixed(1);
            out += '<i style="left:' + left + '%;top:' + top + '%;width:' + sz + 'px;height:' + sz + 'px;--c:' + colors[k % 4] + ';--dur:' + dur + 's;--d:' + del + 's;--sw:' + sw + 'px"></i>';
        }
        return out;
    }

    var LAYER_OF = {
        'fx-meteo':    { cls: 'sfx-meteo',    html: meteorHTML },
        'fx-starry':   { cls: 'sfx-starry',   html: starryHTML },
        'fx-bubbles':  { cls: 'sfx-bubbles',  html: bubblesHTML },
        'fx-balloons': { cls: 'sfx-balloons', html: balloonsHTML },
        'fx-clovers':  { cls: 'sfx-clovers',  html: cloversHTML }
    };

    // Theme light/dark probe: probe --bg-base's computed color, WCAG relative
    // luminance decision (result cached)
    function detectLight() {
        var probe = document.createElement('i');
        probe.style.cssText = 'position:absolute;left:-9999px;top:0;width:1px;height:1px;'
            + 'background-color:var(--bg-base, #ffffff)';
        (document.body || document.documentElement).appendChild(probe);
        var rgb = getComputedStyle(probe).backgroundColor;
        probe.remove();
        var m = rgb && rgb.match(/[\d.]+/g);
        if (!m || m.length < 3) return false;
        var L = (0.2126 * (+m[0]) + 0.7152 * (+m[1]) + 0.0722 * (+m[2])) / 255;
        return L > 0.55;
    }

    var lightMode = null;   // null = not probed yet

    function currentPool() {
        if (lightMode === null) lightMode = detectLight();
        return lightMode ? LIGHT_FX : DARK_FX;
    }

    function poolKey() {
        return 'sponsor-fx:' + (lightMode ? 'light' : 'dark');
    }

    function addLayer(card, cls, html) {
        var n = document.createElement('span');
        n.className = cls;
        n.setAttribute('aria-hidden', 'true');
        n.innerHTML = html;
        card.appendChild(n);
        return n;
    }

    // Lazily build the layer for the drawn token and cache it (built on first use, reused+replayed after)
    function ensureLayer(card, token) {
        var spec = LAYER_OF[token];
        if (spec) {
            if (!card.querySelector(':scope > .' + spec.cls)) addLayer(card, spec.cls, spec.html());
        } else if (token === 'fx-ring-duo') {
            if (!card.querySelector(':scope > .sfx-ring')) addLayer(card, 'sfx-ring', '');
        }
    }

    function enter(card) {
        var pool = currentPool();
        var token = window.pickNoRepeat(poolKey(), pool);
        card.classList.toggle('sfx-light', lightMode);
        card.classList.toggle('sfx-dark', !lightMode);
        ALL_FX.forEach(function(t) { if (t !== token) card.classList.remove(t); });
        card.classList.add(token);
        ensureLayer(card, token);
    }

    function leave(card) {
        // Remove the fx-* class -> the hover animation cancels as a whole, particles
        // return to the opacity:0 resting state; the layer stays as cache
        ALL_FX.forEach(function(t) { card.classList.remove(t); });
    }

    // Theme switch / re-attach: clear the light/dark cache, strip classes and recycle
    // all layers (the pool changed, old layers are void)
    function resetAll() {
        lightMode = null;
        document.querySelectorAll('.sponsor-public-card').forEach(function(card) {
            ALL_FX.forEach(function(t) { card.classList.remove(t); });
            card.classList.remove('sfx-light', 'sfx-dark');
            Array.prototype.slice.call(card.children).forEach(function(n) {
                if (n.nodeType === 1 && /^sfx-/.test(n.className)) n.remove();
            });
        });
    }

    function bind() {
        var grid = document.getElementById('sponsor-grid');
        if (!grid) return;
        grid.querySelectorAll('.sponsor-public-card').forEach(function(card) {
            if (card.__sfxBound) return;
            card.__sfxBound = true;
            card.addEventListener('mouseenter', function() { enter(card); });
            card.addEventListener('mouseleave', function() { leave(card); });
        });
    }

    bind();
    document.addEventListener('htmx:after:swap', bind);

    // Theme switch (html data-theme change) -> clear cache and recycle layers; the next
    // hover re-draws from the new pool
    if (window.MutationObserver && !document.documentElement.__sfxThemeMO) {
        var mo = new MutationObserver(function() {
            resetAll();
            bind();
        });
        mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
        document.documentElement.__sfxThemeMO = true;
    }
})();
