// stats.js: view statistics and per-digit mechanical reel (odometer) visual engine
(function () {
  'use strict';

  function currentPath() {
    return location.pathname;
  }

  function isReducedMotion() {
    return window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  }

  // Generic per-digit mechanical reel (odometer engine)
  // Splits the target number into independent vertical reels per digit, each column
  // holding a looping digit strip, then slides smoothly with staggered delays after render.
  function renderOdometer(el, targetNum, options) {
    if (!el) return;
    options = options || {};
    var numStr = String(Math.max(0, parseInt(targetNum, 10) || 0));
    var cycles = typeof options.cycles === 'number' ? options.cycles : 1;
    var baseDuration = options.duration || 1.1;
    var stagger = typeof options.stagger === 'number' ? options.stagger : 0.06;
    var reduced = isReducedMotion();

    var odo = document.createElement('span');
    odo.className = 'odometer';
    odo.setAttribute('aria-label', numStr);

    var ribbonElements = [];

    for (var i = 0; i < numStr.length; i++) {
      var char = numStr[i];
      if (char >= '0' && char <= '9') {
        var digitVal = parseInt(char, 10);
        var digitCol = document.createElement('span');
        digitCol.className = 'odometer-digit';

        var ribbon = document.createElement('span');
        ribbon.className = 'odometer-ribbon';

        var totalLoops = reduced ? 1 : (cycles + 1);
        var totalSpans = totalLoops * 10;
        for (var loop = 0; loop < totalLoops; loop++) {
          for (var d = 0; d <= 9; d++) {
            var s = document.createElement('span');
            s.textContent = String(d);
            ribbon.appendChild(s);
          }
        }

        digitCol.appendChild(ribbon);
        odo.appendChild(digitCol);

        var targetIdx = reduced ? digitVal : (cycles * 10 + digitVal);
        ribbonElements.push({
          ribbon: ribbon,
          targetIdx: targetIdx,
          totalSpans: totalSpans,
          colIndex: i
        });
      } else {
        var sep = document.createElement('span');
        sep.className = 'odometer-sep';
        sep.textContent = char;
        odo.appendChild(sep);
      }
    }

    el.innerHTML = '';
    el.appendChild(odo);

    if (window.alignInkElement) {
      window.alignInkElement(el);
    }

    if (reduced) {
      ribbonElements.forEach(function (item) {
        var pct = - (item.targetIdx / item.totalSpans) * 100;
        item.ribbon.style.transform = 'translateY(' + pct.toFixed(4) + '%)';
      });
      return;
    }

    // Kick reflow, then start the eased, staggered roll
    requestAnimationFrame(function () {
      requestAnimationFrame(function () {
        ribbonElements.forEach(function (item) {
          var colDuration = baseDuration + item.colIndex * 0.08;
          var colDelay = item.colIndex * stagger;
          var pct = - (item.targetIdx / item.totalSpans) * 100;
          item.ribbon.style.setProperty('--odo-duration', colDuration + 's');
          item.ribbon.style.transitionDelay = colDelay + 's';
          item.ribbon.style.transform = 'translateY(' + pct.toFixed(4) + '%)';
        });
      });
    });
  }

  // Article view counter: adaptive-width reel slots + minimum play duration + smooth transition to the real value
  function sendView() {
    var av = document.querySelector('.article-views__num') || document.querySelector('.article-views b');
    if (!av) return;

    // Skip if an odometer already rendered successfully on this DOM and it is not loading
    if (av._viewInitialized && !av.classList.contains('is-loading')) {
      return;
    }
    av._viewInitialized = true;

    var postId = av.getAttribute('data-post-id');
    var rawViews = av.getAttribute('data-views') || '';
    // Size the animation's digit count and width to the original view-count length
    // (e.g. 500 -> 3 reel slots; 0 avoids width jumps)
    var digitCount = Math.max(1, rawViews.length);
    var reqPath = postId ? (window.SITE_URLS.pages.post + '/' + postId) : currentPath();

    // 1. Start a fast multi-column reel-slot placeholder animation exactly matching the target digit width
    av.classList.add('is-loading');
    var spinnerHtml = '';
    for (var idx = 0; idx < digitCount; idx++) {
      var speed = (0.32 + (idx % 3) * 0.05).toFixed(2);
      var delay = (-(idx * 0.08)).toFixed(2);
      spinnerHtml += '<span class="views-slot-spinner" style="width:0.6em;"><span class="views-slot-track" style="--slot-speed:' + speed + 's;--slot-delay:' + delay + 's;"><span>0</span><span>1</span><span>2</span><span>3</span><span>4</span><span>5</span><span>6</span><span>7</span><span>8</span><span>9</span></span></span>';
    }
    av.innerHTML = spinnerHtml;

    if (window.alignInkElement) {
      window.alignInkElement(av);
    }

    var minPlayMs = 600; // guarantee a minimum 600ms play duration so the animation does not flash by on very fast networks
    var minDelayPromise = new Promise(function (resolve) {
      setTimeout(resolve, minPlayMs);
    });

    var fetchPromise;
    if (window.fetch) {
      var B = window.SITE_URLS;
      fetchPromise = fetch(B.view + '?p=' + encodeURIComponent(reqPath), {
        method: 'POST',
        keepalive: true
      })
      .then(function (r) { return r.json(); })
      .catch(function () { return null; });
    } else {
      var B = window.SITE_URLS;
      if (window.navigator && window.navigator.sendBeacon) {
        navigator.sendBeacon(B.view + '?p=' + encodeURIComponent(reqPath));
      }
      fetchPromise = Promise.resolve(null);
    }

    // 2. Once network data arrives AND the minimum animation duration is met, decelerate and align to the real database value
    Promise.all([fetchPromise, minDelayPromise])
      .then(function (results) {
        var data = results[0];
        av.classList.remove('is-loading');
        if (data && typeof data.views === 'number' && data.views > 0) {
          renderOdometer(av, data.views, { cycles: 1, duration: 0.8, stagger: 0.05 });
        } else {
          // Fallback display
          var fallbackVal = rawViews || '-';
          renderOdometer(av, fallbackVal, { cycles: 0, duration: 0.3 });
        }
      })
      .catch(function () {
        av.classList.remove('is-loading');
        av.textContent = rawViews || '-';
      });
  }

  // Home stats dashboard: per-digit mechanical reel entrance effect
  function refreshStats() {
    var val = document.querySelector('.stat-card--visits .stat-card__value');
    var nums = document.querySelectorAll('.stat-card--visits .stat-card__num');
    if (!val && (!nums || !nums.length)) return;

    var B = window.SITE_URLS;
    fetch(B.stats, { cache: 'no-store' })
      .then(function (r) { return r.json(); })
      .then(function (s) {
        if (!s) return;
        var section = document.querySelector('.home-section');

        var triggerOdometer = function () {
          if (val) {
            renderOdometer(val, s.TodayPV, { cycles: 2, duration: 1.2, stagger: 0.06 });
          }
          if (nums && nums.length >= 2) {
            nums[0].textContent = String(s.TotalPV || 0);
            nums[1].textContent = String(s.TotalUV || 0);
          }
        };

        if (section && !section.classList.contains('is-visible') && ('IntersectionObserver' in window)) {
          var io = new IntersectionObserver(function (entries) {
            entries.forEach(function (en) {
              if (en.isIntersecting || (section && section.classList.contains('is-visible'))) {
                triggerOdometer();
                io.disconnect();
              }
            });
          }, { threshold: 0.1 });
          io.observe(section);
        } else {
          triggerOdometer();
        }
      })
      .catch(function () {});
  }

  function onEntered() {
    sendView();
    refreshStats();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', onEntered);
  } else {
    onEntered();
  }

  // Listen globally for htmx:after:swap so it fires 100% of the time after HTMX partial or full page switches
  document.addEventListener('htmx:after:swap', function () {
    onEntered();
  });

  window.renderOdometer = renderOdometer;
})();

