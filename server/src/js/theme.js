// theme.js - theme switching + FAB grid dynamic generation
(function() {
    function isSync() {
        return localStorage.getItem('blog-theme-sync') !== 'false';
    }
    
    function get() {
        if (!isSync()) return localStorage.getItem('blog-theme-override') || 'forest-light';
        var isDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
        return isDark ? 
            (localStorage.getItem('blog-theme-dark') || 'indigo-dark') : 
            (localStorage.getItem('blog-theme-light') || 'forest-light');
    }
    
    function set(t) {
        if (!isSync()) {
            localStorage.setItem('blog-theme-override', t);
        } else {
            if (t.includes('-dark')) {
                localStorage.setItem('blog-theme-dark', t);
            } else {
                localStorage.setItem('blog-theme-light', t);
            }
        }
        applyTheme(t);
    }

    // WebKit backdrop ghost compensation
    // A theme switch only changes the <html data-theme> attribute: Safari rebuilds
    // backdrop-filter layer content only on compositing-tree structure changes; a pure
    // repaint does not re-sample -> the second-screen glass on the home page (sampling
    // the .home-section__bg tangram's theme-colored --shape-*) shows the new theme
    // surface while the blurred content still shows the old theme, until some
    // structural change (e.g. closing the theme panel display:none) refreshes it.
    // Countermeasure: after a theme switch, promote the backdrop source layers to their
    // own compositing layers, commit across one frame, then restore — replicating the
    // "panel closed" structural change with zero pixel-level flicker.
    // Kick only the source layers, not the glass cards: cards carry entrance animations
    // (fly-in); display/transform interference would replay them. translateZ is safe
    // here: the source layers
    // are already positioned (.home-section__bg absolute / #bgShapes fixed), so the
    // containing block of their absolute child shapes does not change.
    // Safari only (same detection as 14-backdrop.js); Chrome uses the clone engine
    // (colors resolve from vars live and it already listens for data-theme); Firefox
    // native has no such issue.
    function kickWebKitBackdrop() {
        // ?nokick=1 -> back to the uncompensated native path, for comparing against it on
        // real Safari hardware (same role as ?frost=0)
        if (/[?&]nokick=1/.test(location.search)) return;
        if (!/^((?!chrome|chromium|android|crios|fxios|edg|opr).)*safari/i.test(navigator.userAgent)) return;
        var layers = document.querySelectorAll('.home-section__bg, #bgShapes');
        if (!layers.length) return;
        for (var i = 0; i < layers.length; i++) layers[i].style.transform = 'translateZ(0)';
        // Double rAF: the promoted state must truly exist in one commit (a single rAF
        // would undo before paint, so the compositor never sees the structural change);
        // restore lands on the second frame
        requestAnimationFrame(function() {
            requestAnimationFrame(function() {
                for (var j = 0; j < layers.length; j++) layers[j].style.transform = '';
            });
        });
    }

    function applyTheme(t) {
        document.documentElement.dataset.theme = t;
        updateActive();
        kickWebKitBackdrop();
    }
    
    function updateActive() {
        var cur = get();
        document.querySelectorAll('.theme-picker__cell').forEach(function(c) {
            c.classList.toggle('active', c.getAttribute('data-theme') === cur);
        });
    }

    // Apply the stored theme (the anti-flicker inline script already ran in head; this ensures consistency)
    document.documentElement.dataset.theme = get();

    // Watch live system light/dark mode switches
    if (window.matchMedia) {
        window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function(e) {
            if (!isSync()) return;
            var targetTheme = e.matches ?
                (localStorage.getItem('blog-theme-dark') || 'indigo-dark') :
                (localStorage.getItem('blog-theme-light') || 'forest-light');
            applyTheme(targetTheme);
            if (typeof updateSyncUI === 'function') updateSyncUI();
        });
    }

    // Reading mode (transparent clear ↔ paper)
    var readKey = 'blog-read-mode';
    function getReadMode() {
        return localStorage.getItem(readKey) || 'clear';
    }
    function setReadMode(m) {
        localStorage.setItem(readKey, m);
        document.documentElement.dataset.readMode = m;
    }
    document.documentElement.dataset.readMode = getReadMode();

    var readFab = document.getElementById('read-fab');
    if (readFab) {
        readFab.addEventListener('click', function(e) {
            e.stopPropagation();
            setReadMode(getReadMode() === 'paper' ? 'clear' : 'paper');
        });
    }



    // FAB and picker interaction state management
    var fab = document.getElementById('theme-fab');
    var picker = document.getElementById('themePicker');
    var leaveTimer = null;

    function openPicker() {
        if (leaveTimer) { clearTimeout(leaveTimer); leaveTimer = null; }
        if (picker) picker.classList.add('show');
    }

    function closePicker() {
        if (leaveTimer) { clearTimeout(leaveTimer); leaveTimer = null; }
        if (picker) picker.classList.remove('show');
    }

    function scheduleClose() {
        if (leaveTimer) clearTimeout(leaveTimer);
        leaveTimer = setTimeout(function() {
            closePicker();
        }, 350);
    }

    function cancelClose() {
        if (leaveTimer) {
            clearTimeout(leaveTimer);
            leaveTimer = null;
        }
    }

    if (fab && picker) {
        // Hover in/out: keep open while inside the card or FAB; smooth-collapse 350ms
        // after leaving (guards against edge mis-taps)
        picker.addEventListener('mouseenter', cancelClose);
        fab.addEventListener('mouseenter', cancelClose);
        picker.addEventListener('mouseleave', function() {
            if (picker.classList.contains('show')) scheduleClose();
        });
        fab.addEventListener('mouseleave', function() {
            if (picker.classList.contains('show')) scheduleClose();
        });

        // FAB click: switched to document capture-phase delegation — defends against
        // editor.html's initThemeFab() cloning and replaceChild-ing #theme-fab, which
        // would leave this closure's fab reference pointing at a detached node while the
        // clone (kept by hx-preserve after leaving the editor) only switches editor
        // colors and never opens the theme picker — the "button looks alive but does
        // nothing" JS-logic leak. Delegation fires in the capture phase, which the
        // editor page's cloned handler cannot stop with stopPropagation; the editor page
        // yields via the #editor-form guard.
        document.addEventListener('click', function(e) {
            if (!e.target || !e.target.closest || !e.target.closest('#theme-fab')) return;
            if (document.getElementById('editor-form')) return;
            e.stopPropagation();
            if (picker.classList.contains('show')) {
                closePicker();
            } else {
                openPicker();
            }
        }, true);

        // Interrupt 1: wheel scroll (intent to browse the page; collapse immediately)
        window.addEventListener('wheel', function() {
            if (picker.classList.contains('show')) closePicker();
        }, { passive: true });

        // Interrupt 2: page scroll
        window.addEventListener('scroll', function() {
            if (picker.classList.contains('show')) closePicker();
        }, { passive: true });

        // Interrupt 3: click outside
        document.addEventListener('pointerdown', function(e) {
            if (picker.classList.contains('show') && !picker.contains(e.target) && !fab.contains(e.target)) {
                closePicker();
            }
        });

        // Interrupt 4: mobile touch drag
        window.addEventListener('touchmove', function(e) {
            if (picker.classList.contains('show') && !picker.contains(e.target)) {
                closePicker();
            }
        }, { passive: true });

        // Interrupt 5: ESC key
        document.addEventListener('keydown', function(e) {
            if (e.key === 'Escape' && picker.classList.contains('show')) {
                closePicker();
            }
        });

        // Interrupt 6: window blur / tab switch
        window.addEventListener('blur', function() {
            if (picker.classList.contains('show')) closePicker();
        });
    }

    // Grid click switching: the card stays open after a click to support consecutive comparison
    var grid = document.getElementById('themeGrid');
    if (grid) {
        grid.addEventListener('click', function(e) {
            var cell = e.target.closest('.theme-picker__cell');
            if (!cell || cell.classList.contains('disabled')) return;
            set(cell.getAttribute('data-theme'));
            // keep the card open; do not call closePicker()
        });
    }

    // Theme display names and light/dark mapping
    var THEME_CONFIG = {
        'violet-light': { tone: 'light', name: 'Violet Garden' },
        'forest-light': { tone: 'light', name: 'Ever Forest' },
        'sakura-light': { tone: 'light', name: 'Madoka Magica' },
        'cyanic-light': { tone: 'light', name: 'Hoseki Phos' },
        'golden-light': { tone: 'light', name: 'Ijichi Nijika' },
        'summer-light': { tone: 'light', name: 'Sorakado Ao' },

        'coffee-dark':  { tone: 'dark',  name: 'Ujimatsu Chiya' },
        'oceans-dark':  { tone: 'dark',  name: 'Sonoda Umi' },
        'purple-dark':  { tone: 'dark',  name: 'Yakumo Yukari' },
        'stream-dark':  { tone: 'dark',  name: 'Ayanami Rei' },
        'frozen-dark':  { tone: 'dark',  name: 'Yuki Setsuna' },
        'indigo-dark':  { tone: 'dark',  name: 'Tokyo Night' }
    };

    var SUN_SVG = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/></svg>';
    var MOON_SVG = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/></svg>';

    // Generate the theme grid dynamically
    fetch('/assets/themes.json').then(function(r) { return r.json(); }).then(function(data) {
        if (!data.themes || !grid) return;
        grid.innerHTML = '';

        var lightCol = document.createElement('div');
        lightCol.className = 'theme-picker__col theme-picker__col--light';
        lightCol.innerHTML = '<div class="theme-picker__col-title">' +
            '<span class="col-title-main"><span class="col-icon">' + SUN_SVG + '</span>Light</span>' +
            '</div>' +
            '<div class="theme-picker__col-list"></div>';

        var darkCol = document.createElement('div');
        darkCol.className = 'theme-picker__col theme-picker__col--dark';
        darkCol.innerHTML = '<div class="theme-picker__col-title">' +
            '<span class="col-title-main"><span class="col-icon">' + MOON_SVG + '</span>Dark</span>' +
            '</div>' +
            '<div class="theme-picker__col-list"></div>';

        var lightList = lightCol.querySelector('.theme-picker__col-list');
        var darkList = darkCol.querySelector('.theme-picker__col-list');

        var LIGHT_THEMES = [
            'violet-light', 'forest-light',
            'sakura-light', 'golden-light',
            'summer-light', 'cyanic-light'
        ];

        var DARK_THEMES = [
            'oceans-dark', 'indigo-dark',
            'stream-dark', 'frozen-dark',
            'purple-dark', 'coffee-dark'
        ];

        function createCell(id) {
            var conf = THEME_CONFIG[id] || {
                tone: (id.includes('light') || id.includes('day') || id.includes('dawn') || id.includes('latte') || id.includes('lotus')) ? 'light' : 'dark',
                name: id.split('-')[0].charAt(0).toUpperCase() + id.split('-')[0].slice(1)
            };
            var btn = document.createElement('button');
            btn.type = 'button';
            btn.className = 'theme-picker__cell';
            btn.setAttribute('data-theme', id);
            btn.innerHTML = '<span class="theme-picker__swatch theme-picker__swatch--' + id + '"></span>' +
                            '<span class="theme-picker__name">' + conf.name + '</span>';
            return btn;
        }

        LIGHT_THEMES.forEach(function(id) {
            if (data.themes.indexOf(id) !== -1) {
                lightList.appendChild(createCell(id));
            }
        });

        DARK_THEMES.forEach(function(id) {
            if (data.themes.indexOf(id) !== -1) {
                darkList.appendChild(createCell(id));
            }
        });

        grid.appendChild(lightCol);
        grid.appendChild(darkCol);
        updateActive();
        
        // Once the grid is generated, refresh the disabled state fully once
        if (typeof window.updateSyncUI === 'function') window.updateSyncUI();
    });

    // Auto Sync slider logic (OS = Sync On / ME = Sync Off)
    window.updateSyncUI = function() {
        var chk = document.getElementById('theme-sync-checkbox');
        var syncOn = isSync();
        if (chk) chk.checked = !syncOn; // unchecked = OS (left), checked = ME (right)
        
        var isDarkOS = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
        document.querySelectorAll('.theme-picker__cell').forEach(function(c) {
            if (!syncOn) {
                c.classList.remove('disabled');
            } else {
                var themeIsDark = c.getAttribute('data-theme').includes('-dark');
                c.classList.toggle('disabled', themeIsDark !== isDarkOS);
            }
        });
    };
    
    var syncChk = document.getElementById('theme-sync-checkbox');
    if (syncChk) {
        syncChk.addEventListener('change', function(e) {
            var currentTheme = document.documentElement.dataset.theme; // capture the screen's actual current color up front
            var isNowME = e.target.checked;
            var syncOn = !isNowME; 
            localStorage.setItem('blog-theme-sync', syncOn ? 'true' : 'false');
            if (!syncOn) {
                // Switch to ME (right): lock the current color into the override memory
                localStorage.setItem('blog-theme-override', currentTheme);
            } else {
                // Switch to OS (left): apply the system-adapted theme immediately
                applyTheme(get());
            }
            updateSyncUI();
        });
    }
    
    // Run once synchronously so the card holds the correct state instantly after refresh, without waiting for the network
    updateSyncUI();
})();
