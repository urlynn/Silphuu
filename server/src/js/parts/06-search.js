// 06-search.js: 0ms Client-side Instant Fulltext Search Engine
(function() {
    var postsIndex = null;
    var isFetching = false;

    function escapeHTML(str) {
        if (!str) return '';
        return str
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#039;');
    }

    function escapeRegex(str) {
        return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    }

    // Preload or fetch the site-wide post data pool (SITE_URLS.postsJSON)
    window.loadPostsIndex = function() {
        if (postsIndex) return Promise.resolve(postsIndex);
        if (isFetching) {
            return new Promise(function(resolve) {
                var check = setInterval(function() {
                    if (postsIndex) {
                        clearInterval(check);
                        resolve(postsIndex);
                    }
                }, 20);
            });
        }
        isFetching = true;
        var B = window.SITE_URLS;
        return fetch(B.postsJSON)
            .then(function(r) { return r.json(); })
            .then(function(data) {
                postsIndex = data;
                isFetching = false;
                return data;
            })
            .catch(function() {
                isFetching = false;
                return [];
            });
    };

    // Extract highlight and context
    function highlightText(text, terms) {
        if (!text || !terms.length) return escapeHTML(text);
        var escaped = escapeHTML(text);
        terms.forEach(function(term) {
            if (!term) return;
            var re = new RegExp('(' + escapeRegex(escapeHTML(term)) + ')', 'gi');
            escaped = escaped.replace(re, '<mark class="search-highlight">$1</mark>');
        });
        return escaped;
    }

    function extractSnippet(fullText, terms, maxLen) {
        if (!fullText) return '';
        maxLen = maxLen || 120;
        var lower = fullText.toLowerCase();
        var firstIdx = -1;

        for (var i = 0; i < terms.length; i++) {
            var idx = lower.indexOf(terms[i].toLowerCase());
            if (idx !== -1 && (firstIdx === -1 || idx < firstIdx)) {
                firstIdx = idx;
            }
        }

        if (firstIdx === -1) {
            var raw = fullText.slice(0, maxLen);
            return highlightText(raw + (fullText.length > maxLen ? '...' : ''), terms);
        }

        var start = Math.max(0, firstIdx - 35);
        var end = Math.min(fullText.length, firstIdx + maxLen - 35);
        var snippet = fullText.slice(start, end);
        if (start > 0) snippet = '...' + snippet;
        if (end < fullText.length) snippet = snippet + '...';

        return highlightText(snippet, terms);
    }

    function searchPosts(query) {
        if (!postsIndex || !query) return [];
        var rawTerms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
        if (!rawTerms.length) return [];

        var results = [];
        for (var i = 0; i < postsIndex.length; i++) {
            var p = postsIndex[i];
            var score = 0;
            var matchedInTitle = false;
            var matchedInTags = false;
            var matchedInSummary = false;
            var matchedInText = false;

            var titleLower = (p.title || '').toLowerCase();
            var summaryLower = (p.summary || '').toLowerCase();
            var textLower = (p.text || '').toLowerCase();
            var catLower = (p.cat || '').toLowerCase();
            var tagsLower = (p.tags || []).join(' ').toLowerCase();

            for (var j = 0; j < rawTerms.length; j++) {
                var term = rawTerms[j];
                if (titleLower.indexOf(term) !== -1) { score += 20; matchedInTitle = true; }
                if (tagsLower.indexOf(term) !== -1) { score += 12; matchedInTags = true; }
                if (catLower.indexOf(term) !== -1) { score += 8; }
                if (summaryLower.indexOf(term) !== -1) { score += 5; matchedInSummary = true; }
                if (textLower.indexOf(term) !== -1) { score += 2; matchedInText = true; }
            }

            if (score > 0) {
                var snippetSource = (matchedInSummary ? p.summary : p.text) || p.summary || p.text;
                var snippetHTML = extractSnippet(snippetSource, rawTerms, 110);
                var titleHTML = highlightText(p.title, rawTerms);

                results.push({
                    item: p,
                    score: score,
                    titleHTML: titleHTML,
                    snippetHTML: snippetHTML
                });
            }
        }

        results.sort(function(a, b) {
            if (b.score !== a.score) return b.score - a.score;
            return b.item.id - a.item.id;
        });

        return results;
    }

    // One result-row markup, shared by the instant dropdown and the /search page, so the two
    // surfaces cannot drift apart.
    function searchItemHTML(h) {
        var p = h.item;
        return '<a href="' + window.SITE_URLS.pages.post + '/' + p.id + '" class="search-item">' +
            '<div class="search-item-row ink-row">' +
                '<span class="search-cat"><span class="pill-label">' + escapeHTML(p.cat) + '</span></span>' +
                '<span class="search-title">' + h.titleHTML + '</span>' +
            '</div>' +
            (h.snippetHTML ? '<div class="search-item-snippet">' + h.snippetHTML + '</div>' : '') +
        '</a>';
    }

    // The /search page is a static shell that does its own searching in the browser, so it
    // needs the same core the dropdown uses. Exposed instead of duplicated.
    window.SilphuuSearch = {
        loadIndex: window.loadPostsIndex,
        search: searchPosts,
        itemHTML: searchItemHTML
    };

    // Bind search modal interactions
    reinitOnSwap(function initSearchModal() {
        var searchModal = document.getElementById('searchModal');
        var searchInput = document.getElementById('searchModalInput');
        var searchBtn = document.getElementById('navSearchBtn');
        var searchClose = document.getElementById('searchModalClose');
        var searchBackdrop = document.getElementById('searchModalBackdrop');
        var resultsBox = document.getElementById('searchModalResults');
        if (!searchModal || !searchInput || !searchBtn || !resultsBox) return;

        // Preload the index (triggered on search button hover or focus)
        searchBtn.addEventListener('mouseenter', window.loadPostsIndex);
        searchBtn.addEventListener('focus', window.loadPostsIndex);

        function openSearch() {
            searchModal.classList.add('active');
            window.loadPostsIndex();
            setTimeout(function() { searchInput.focus(); }, 80);
        }

        function updateMask() {
            var canScrollDown = resultsBox.scrollHeight - resultsBox.scrollTop - resultsBox.clientHeight > 4;
            resultsBox.classList.toggle('has-overflow-bottom', canScrollDown);
        }

        if (!resultsBox._maskBound) {
            resultsBox._maskBound = true;
            resultsBox.addEventListener('scroll', updateMask, { passive: true });
        }

        function closeSearch() {
            searchModal.classList.remove('active');
            searchInput.value = '';
            resultsBox.innerHTML = '';
            resultsBox.classList.remove('has-overflow-bottom');
        }

        // The modal lives outside #content-wrapper (inside the navbar), so an htmx
        // full-page swap never replaces it. Therefore any navigation initiated from
        // inside the modal must explicitly collapse it, otherwise the modal would keep
        // covering the new page:
        //   - Enter submits the form -> boost to /search?q=...
        //   - Clicking a result -> boost to /post/{id}
        // The modal itself is not rebuilt on swap; a dataset guard prevents
        // reinitOnSwap from stacking duplicate listeners.
        if (!searchModal.dataset.navCloseBound) {
            searchModal.dataset.navCloseBound = '1';
            searchModal.addEventListener('submit', closeSearch);
            searchModal.addEventListener('click', function(e) {
                if (e.target && e.target.closest && e.target.closest('a[href]')) closeSearch();
            });
        }

        searchBtn.addEventListener('click', openSearch);
        if (searchClose) searchClose.addEventListener('click', closeSearch);
        if (searchBackdrop) searchBackdrop.addEventListener('click', closeSearch);

        // 0ms real-time typing response
        searchInput.addEventListener('input', function() {
            var q = searchInput.value.trim();
            if (!q) {
                resultsBox.innerHTML = '';
                updateMask();
                return;
            }

            window.loadPostsIndex().then(function() {
                var hits = searchPosts(q);
                if (!hits.length) {
                    resultsBox.innerHTML = '<div class="search-empty">没有找到相关内容</div>';
                    updateMask();
                    return;
                }

                var html = '';
                for (var i = 0; i < hits.length; i++) {
                    html += searchItemHTML(hits[i]);
                }
                resultsBox.innerHTML = html;
                resultsBox.scrollTop = 0;
                if (window.alignInkAll) window.alignInkAll(resultsBox);
                updateMask();
            });
        });

        // Shortcuts ⌘K / Ctrl+K & ESC
        document.addEventListener('keydown', function(e) {
            if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
                e.preventDefault();
                if (searchModal.classList.contains('active')) closeSearch();
                else openSearch();
            }
            if (e.key === 'Escape' && searchModal.classList.contains('active')) {
                closeSearch();
            }
        });
    });

    // /search page
    //
    // The page is a static shell: the query never reaches the server, so every ?q= variant is
    // byte-identical and a one-year CDN copy can never go stale. It ships every post's card
    // already rendered (templates/search.html) — that is what keeps a single implementation of
    // the card. This only selects which cards belong to this search and puts the highlighted
    // body excerpt in place.
    //
    // Registered through reinitOnSwap so it also runs after an htmx page swap, not just on a
    // full load.
    function postIDFromCard(card) {
        // post_card links the cover to /post/{id}; that link is the card's stable handle.
        var cover = card.querySelector('.card-cover');
        if (!cover) return 0;
        var href = cover.getAttribute('href') || '';
        return parseInt(href.slice(href.lastIndexOf('/') + 1), 10) || 0;
    }

    reinitOnSwap(function initSearchPage() {
        var box = document.getElementById('searchPageResults');
        var line = document.getElementById('searchPageCount');
        if (!box || !line) return; // not the /search page

        var q = new URLSearchParams(location.search).get('q') || '';
        if (!q.trim()) return;

        window.loadPostsIndex().then(function() {
            var hits = searchPosts(q);
            var hitByID = {};
            for (var i = 0; i < hits.length; i++) {
                hitByID[hits[i].item.id] = hits[i];
            }

            var cards = box.querySelectorAll('.card-row-wrapper');
            var shown = 0;
            for (var j = 0; j < cards.length; j++) {
                var card = cards[j];
                // The empty card is pre-rendered inside the list too, so it is a .card-row-wrapper
                // as well. It follows the count below, never the hit lookup.
                if (card.closest('#searchPageEmpty')) continue;
                var hit = hitByID[postIDFromCard(card)];
                if (!hit) {
                    card.style.display = 'none';
                    continue;
                }
                var summary = card.querySelector('.post-summary');
                if (summary) summary.innerHTML = hit.snippetHTML || '';
                card.style.display = '';
                shown++;
            }

            line.textContent = line.dataset.prefix + ' ' + shown + ' ' + line.dataset.suffix;

            // The empty card is pre-rendered by the template too (single implementation), so it
            // is only shown or hidden here.
            var empty = document.getElementById('searchPageEmpty');
            if (empty) empty.style.display = shown ? 'none' : '';

            box.style.display = '';
            if (window.alignInkAll) window.alignInkAll(box);
        });
    });
})();
