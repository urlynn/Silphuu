// Non-repeating random picker (shuffle bag), shared site-wide.
// Draws for one key never repeat consecutively: the bag is reshuffled once every candidate
// has come up, and the first draw of a new round never equals the previous round's last.
// The bag survives reloads, so the guarantee holds across visits, not just within one.
//   window.pickNoRepeat('waterfall', ['cascade', 'gravity', 'melt'])
window.pickNoRepeat = function(key, items) {
    var storageKey = 'shuffle_bag:' + key;
    var deck = [];
    try { deck = JSON.parse(localStorage.getItem(storageKey) || '[]'); } catch (e) { deck = []; }
    deck = (Array.isArray(deck) ? deck : []).filter(function(m) { return items.indexOf(m) !== -1; });
    if (!deck.length) {
        deck = items.slice();
        for (var i = deck.length - 1; i > 0; i--) {
            var j = Math.floor(Math.random() * (i + 1));
            var tmp = deck[i]; deck[i] = deck[j]; deck[j] = tmp;
        }
        try {
            var last = localStorage.getItem(storageKey + ':last');
            if (last && deck.length > 1 && deck[deck.length - 1] === last) {
                var swap = deck[deck.length - 1];
                deck[deck.length - 1] = deck[0];
                deck[0] = swap;
            }
        } catch (e) {}
    }
    var pick = deck.pop();
    try {
        localStorage.setItem(storageKey, JSON.stringify(deck));
        localStorage.setItem(storageKey + ':last', pick);
    } catch (e) {}
    return pick;
};
