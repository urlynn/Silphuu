// Archive timeline SVG initialization + scheme-A electric-flow animation
// (standalone file, keeps main.js lean)
// The SVG <path> d attribute is computed by JS (node coordinates -> SVG path), not
// rendered server-side.
// Interaction:
//   hovering a row grows the current from the year trunk along the path to the
//   target dot (the end probe +6px aligns with the row's rightward shift);
//   the dot lights up (is-lit) and energizes the whole row (is-energized) only at
//   the arrival instant;
//   across posts the current first retracts distance-weighted to the branch
//   junction, then hands over seamlessly to the new branch;
//   mouse-leave retracts back along the same path. Re-initialized on page load and
//   after htmx:after:swap.
(function() {
    // Layered adaptive physics state machine (same-month rail probe + cross-month distance-scaled acceleration)
    var state = null;        // { svg, dashPath, hlPath, svgRect, panel, rows }
    var currentHead = 0;     // current visible current length (px)
    var targetHead = 0;      // target current length (px)
    var activeMeta = null;   // metadata of the currently carried path
    var pendingMeta = null;  // cross-branch target waiting to hand over at the junction
    var currentSpeedRatio = 0.30; // current advance rate
    var rafId = null;

    var PUSH_DIST = 6; // 0.375rem end push-out distance (matches the row hover translate3d(6px))

    function getNodeCenter(el) {
        var r = el.getBoundingClientRect();
        return {
            x: r.left + r.width / 2 - state.svgRect.left,
            y: r.top + r.height / 2 - state.svgRect.top
        };
    }

    function buildDashedPaths() {
        var vertParts = [];
        var horzParts = [];
        state.panel.querySelectorAll('.ap-year-block').forEach(function(block) {
            var yearNode = block.querySelector('.ap-year-node');
            if (!yearNode) return;
            var yc = getNodeCenter(yearNode);
            var months = block.querySelectorAll('.ap-month-block');
            if (months.length > 0) {
                var lastMn = months[months.length - 1].querySelector('.ap-month-node');
                if (lastMn) {
                    var lastMc = getNodeCenter(lastMn);
                    vertParts.push('M' + yc.x + ',' + yc.y + ' L' + yc.x + ',' + lastMc.y);
                }
            }
            months.forEach(function(mb) {
                var monthNode = mb.querySelector('.ap-month-node');
                if (!monthNode) return;
                var mc = getNodeCenter(monthNode);
                var monthPosts = mb.querySelectorAll('.ap-post-row');
                if (monthPosts.length > 0) {
                    var lastMp = monthPosts[monthPosts.length - 1].querySelector('.ap-post-node');
                    if (lastMp) {
                        var lastMc = getNodeCenter(lastMp);
                        vertParts.push('M' + mc.x + ',' + mc.y + ' L' + mc.x + ',' + lastMc.y);
                    }
                }
                horzParts.push('M' + yc.x + ',' + mc.y + ' L' + mc.x + ',' + mc.y);
                monthPosts.forEach(function(pr) {
                    var pn = pr.querySelector('.ap-post-node');
                    if (!pn) return;
                    var pc = getNodeCenter(pn);
                    horzParts.push('M' + mc.x + ',' + pc.y + ' L' + pc.x + ',' + pc.y);
                });
            });
        });
        return vertParts.join(' ') + ' ' + horzParts.join(' ');
    }

    function getPathMeta(postRow) {
        var block = postRow.closest('.ap-year-block');
        if (!block) return null;
        var yearNode = block.querySelector('.ap-year-node');
        var monthBlock = postRow.closest('.ap-month-block');
        if (!monthBlock) return null;
        var monthNode = monthBlock.querySelector('.ap-month-node');
        var postNode = postRow.querySelector('.ap-post-node');
        if (!yearNode || !monthNode || !postNode) return null;

        var yc = getNodeCenter(yearNode);
        var mc = getNodeCenter(monthNode);
        var pc = getNodeCenter(postNode); // fully static base coordinates
        var lx = yc.x;

        // Finalized (follows the original): the dot glides right by 6px together with
        // the card's glass frame (pc.x + PUSH_DIST)
        var endX = pc.x + PUSH_DIST;

        // 1. Length to the month node (year trunk + horizontal connector)
        var lMonth = Math.abs(mc.y - yc.y) + Math.abs(mc.x - lx);
        // 2. Vertical rail segment length
        var lVert = Math.abs(pc.y - mc.y);
        // 3. Length to the post turn point (rail and short horizontal junction)
        var lTurn = lMonth + lVert;
        // 4. Length of the short horizontal probe into the post, up to the dot
        var lNeedle = Math.abs(endX - mc.x);
        // 5. Total length when the current reaches the dot
        var lTotal = lTurn + lNeedle;

        // SVG polyline endpoint locked to the target dot (endX, pc.y); never overshoots
        // the dot with extra segments
        var d = 'M' + lx + ',' + yc.y +
            ' L' + lx + ',' + mc.y +
            ' L' + mc.x + ',' + mc.y +
            ' L' + mc.x + ',' + pc.y +
            ' L' + endX + ',' + pc.y;

        return {
            postRow: postRow,
            postNode: postNode,
            d: d,
            monthNode: monthNode,
            yc: yc,
            mc: mc,
            pc: pc,
            endX: endX,
            lMonth: lMonth,
            lVert: lVert,
            lTurn: lTurn,
            lNeedle: lNeedle,
            lTotal: lTotal
        };
    }

    function refreshDash() {
        state.svgRect = state.svg.getBoundingClientRect();
        state.dashPath.setAttribute('d', buildDashedPaths());
    }

    function renderFrame() {
        if (!activeMeta || !state || !state.svg.isConnected) {
            rafId = null;
            return;
        }

        if (pendingMeta) {
            // Waiting to retract to the junction
            var diffToCommon = pendingMeta.commonLen - currentHead;
            // When retraction is near the junction (error < 2.5px) or has passed it:
            if (currentHead <= pendingMeta.commonLen + 2.5) {
                // Exactly back at the junction! Old and new paths overlap seamlessly up
                // to this length; swap d directly!
                currentHead = pendingMeta.commonLen;
                activeMeta = pendingMeta.meta;
                state.hlPath.setAttribute('d', activeMeta.d);
                targetHead = activeMeta.lTotal;
                // Growing: same-month close range speeds to 0.36 for a smooth sprint
                // (~85ms); cross-month long range uses 0.40 to punch through fast
                currentSpeedRatio = pendingMeta.isSameMonth ? 0.36 : 0.40;
                pendingMeta = null;
            } else {
                // Accelerated retraction: same-month snaps the horizontal back at 0.40;
                // cross-month retracts fast at 0.48~0.60 scaled by distance
                currentHead += diffToCommon * pendingMeta.retractRatio;
            }
        } else {
            // Normal growth toward targetHead or natural retraction
            var diff = targetHead - currentHead;
            if (Math.abs(diff) < 0.8) {
                currentHead = targetHead;
            } else {
                currentHead += diff * currentSpeedRatio;
            }
        }

        // Render to SVG: endpoint strictly clipped at the dot center (lTotal)
        var total = activeMeta.lTotal;
        state.hlPath.style.strokeDasharray = total + ' ' + total;
        state.hlPath.style.strokeDashoffset = Math.max(0, total - Math.min(total, currentHead));

        if (currentHead > 1.5) {
            state.hlPath.classList.add('active');
        } else {
            state.hlPath.classList.remove('active');
        }

        // Energy state sync (is-energized and is-lit fire strictly only at the instant the current reaches the dot)
        var isArrived = currentHead >= activeMeta.lTotal - 2.5;
        var energizedRow = isArrived ? activeMeta.postRow : null;
        var activeNode = isArrived ? activeMeta.postNode : null;

        state.rows.forEach(function(r) {
            var node = r.querySelector('.ap-post-node');
            if (r === energizedRow) {
                r.classList.add('is-energized');
            } else if (!r.classList.contains('force-row-hover')) {
                r.classList.remove('is-energized');
            }
            if (node) {
                if (node === activeNode || r.classList.contains('force-row-hover')) {
                    node.classList.add('is-lit');
                } else {
                    node.classList.remove('is-lit');
                }
            }
        });

        // Keep the animation loop while not arrived or still pending
        if (Math.abs(targetHead - currentHead) >= 0.8 || pendingMeta) {
            rafId = requestAnimationFrame(renderFrame);
        } else {
            rafId = null;
        }
    }

    function startLoop() {
        if (!rafId) {
            rafId = requestAnimationFrame(renderFrame);
        }
    }

    function retractAll() {
        pendingMeta = null;
        targetHead = 0;
        currentSpeedRatio = 0.24;
        startLoop();
    }

    function initTimeline() {
        var svg = document.querySelector('.ap-highlight-svg');
        if (!svg || !svg.isConnected) { state = null; return; }
        var dashPath = svg.querySelector('.ap-dash-line');
        var hlPath = svg.querySelector('.ap-highlight');
        var panel = svg.closest('.archive-panel');
        if (!dashPath || !hlPath || !panel) { state = null; return; }

        // Page-switch rebuild: cancel the in-flight current animation and clear
        // state-machine metadata referencing the old DOM
        if (rafId) { cancelAnimationFrame(rafId); rafId = null; }
        activeMeta = null;
        pendingMeta = null;
        currentHead = 0;
        targetHead = 0;

        state = {
            svg: svg,
            dashPath: dashPath,
            hlPath: hlPath,
            panel: panel,
            svgRect: svg.getBoundingClientRect(),
            rows: Array.prototype.slice.call(panel.querySelectorAll('.ap-post-row'))
        };

        // Render the base timeline dashed skeleton
        dashPath.setAttribute('d', buildDashedPaths());

        state.rows.forEach(function(row) {
            if (row._timelineInit) return;
            row._timelineInit = true;
            row.addEventListener('mouseenter', function() {
                if (!state || !state.svg.isConnected) return;
                state.svgRect = state.svg.getBoundingClientRect();
                var newMeta = getPathMeta(this);
                if (!newMeta) return;

                if (!activeMeta || currentHead <= 3) {
                    // 1. First start from zero (zero touch latency, everything moves at the hover instant)
                    activeMeta = newMeta;
                    pendingMeta = null;
                    state.hlPath.setAttribute('d', activeMeta.d);
                    currentHead = 0;
                    targetHead = activeMeta.lTotal;
                    currentSpeedRatio = 0.30;
                    startLoop();
                    return;
                }

                if (activeMeta.d === newMeta.d) {
                    // Moved back to the same post
                    pendingMeta = null;
                    targetHead = newMeta.lTotal;
                    currentSpeedRatio = 0.30;
                    startLoop();
                    return;
                }

                // 2. Cross-post flow: distinguish same-month close range vs cross-month long range
                var isSameMonth = (activeMeta.monthNode === newMeta.monthNode);
                var commonLen = 0;
                var retractRatio = 0.40;

                if (isSameMonth) {
                    // [Same-month close range — rail probe mode] retract only the short
                    // horizontal; the vertical axis never retreats!
                    commonLen = Math.min(activeMeta.lTurn, newMeta.lTurn);
                    retractRatio = 0.42; // snap back the 20px horizontal very fast (~35ms)
                } else {
                    // [Cross-month long range — distance-weighted fast punch-through]
                    // retreat to the branch junction on the year trunk
                    var topMonthY = Math.min(activeMeta.mc.y, newMeta.mc.y);
                    commonLen = Math.abs(topMonthY - activeMeta.yc.y);

                    // Distance acceleration: the longer the span, the fiercer the retraction!
                    var dist = Math.abs(newMeta.pc.y - activeMeta.pc.y);
                    retractRatio = dist > 90 ? 0.58 : 0.46;
                }

                if (currentHead <= commonLen + 1.5) {
                    // The current is already within the junction; paths overlap
                    // seamlessly, switch directly and sprint forward!
                    activeMeta = newMeta;
                    pendingMeta = null;
                    state.hlPath.setAttribute('d', activeMeta.d);
                    targetHead = newMeta.lTotal;
                    currentSpeedRatio = isSameMonth ? 0.36 : 0.40;
                    startLoop();
                } else {
                    // The current is still on the old branch; retract to the junction at
                    // high agility first, then hand over to the new branch seamlessly at arrival!
                    pendingMeta = {
                        meta: newMeta,
                        commonLen: commonLen,
                        isSameMonth: isSameMonth,
                        retractRatio: retractRatio
                    };
                    startLoop();
                }
            });

            row.addEventListener('mouseleave', function(e) {
                if (!state || !state.svg.isConnected) return;
                // If the mouse moved to another post row, let that row's mouseenter take
                // over without interrupting the sibling/cross-month animation
                if (e.relatedTarget && e.relatedTarget.closest && e.relatedTarget.closest('.ap-post-row')) {
                    return;
                }
                // Truly left all post rows: clear the pending branch and retract back
                // along the original path
                retractAll();
            });
        });

        // Mouse fully left the archive card area: safety-net retraction (bound once at panel level)
        if (!panel._timelinePanelInit) {
            panel._timelinePanelInit = true;
            panel.addEventListener('mouseleave', function() {
                if (!state || !state.svg.isConnected) return;
                retractAll();
            });
        }
    }

    // Global scroll/resize/blur bound once, referencing module-level state (initTimeline updates state after swaps)
    if (!window._timelineGlobalInit) {
        window._timelineGlobalInit = true;
        window.addEventListener('scroll', function() {
            if (!state || !state.svg.isConnected) { state = null; return; }
            state.svgRect = state.svg.getBoundingClientRect();
        }, { passive: true });
        window.addEventListener('resize', function() {
            if (!state || !state.svg.isConnected) { state = null; return; }
            refreshDash();
        }, { passive: true });
        // Safety net for window blur or the mouse leaving the browser page
        window.addEventListener('blur', function() {
            if (!state || !state.svg.isConnected) return;
            pendingMeta = null;
            targetHead = 0;
            startLoop();
        });
    }

    // Initialize on page load (full-page load of the archive page)
    initTimeline();

    // Re-initialize after htmx page switches (fragment switch #browse-body-area + full-page switch #content-wrapper)
    document.addEventListener('htmx:after:swap', function(e) {
        var tgt = e.detail && e.detail.ctx && e.detail.ctx.target;
        if (!tgt) return;
        if (tgt.id !== 'browse-body-area' && tgt.id !== 'content-wrapper') return;
        // Delay one frame so the DOM is rendered and layout computed
        requestAnimationFrame(initTimeline);
    });
})();
