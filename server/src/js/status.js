/**
 * status.js: instance traffic and status center client engine
 * Includes WebTransport fast direct connection, compact binary hardware telemetry
 * parsing, Zstd=1 streaming history-log decompression, full status-code cyclic
 * offset grouping sort, and automatic local-environment mock preview detection.
 */
(function () {
    'use strict';

    const STORAGE_KEY = 'silphuu_status_logs_v4';
    let rawLogs = [];
    let sortField = 'time';
    let sortAsc = false;
    let isPaused = false;
    let wtTransport = null;
    let isUsingWT = false;
    let statusSortIdx = -1;
    let viaSortIdx = -1;
    let selectedIp = '';
    let selectedPath = '';

    // Utility: hex string to Uint8Array (for the certificate hash)
    function hexToBytes(hex) {
        if (!hex) return new Uint8Array(0);
        const bytes = new Uint8Array(hex.length / 2);
        for (let i = 0; i < bytes.length; i++) {
            bytes[i] = parseInt(hex.substr(i * 2, 2), 16);
        }
        return bytes;
    }

    const downArrowSvg = '<svg viewBox="0 0 24 24" width="0.75em" height="0.75em" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" style="margin-right: 4px; align-self: center; flex-shrink: 0;"><line x1="12" y1="2" x2="12" y2="22"></line><polyline points="19 15 12 22 5 15"></polyline></svg>';
    const upArrowSvg = '<svg viewBox="0 0 24 24" width="0.75em" height="0.75em" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" style="margin-right: 4px; align-self: center; flex-shrink: 0;"><line x1="12" y1="22" x2="12" y2="2"></line><polyline points="5 9 12 2 19 9"></polyline></svg>';

    function formatKbps(kbps, arrowSvg) {
        let prefix = arrowSvg || '';
        if (kbps >= 1024) {
            return prefix + (kbps / 1024).toFixed(1) + ' <small>MB/s</small>';
        }
        return prefix + kbps.toFixed(1) + ' <small>KB/s</small>';
    }

    function formatTrafficMb(mb) {
        if (mb >= 1024) {
            return (mb / 1024).toFixed(2) + ' GB';
        }
        return mb.toFixed(2) + ' MB';
    }

    // Update core DOM metrics (WebTransport 32-byte datagram)
    function updateMetricsUI(rxKbps, txKbps, dailyRxMb, dailyTxMb, totalRxMb, totalTxMb, cdtV6Mb, cdtV4Mb, cdtUsedMb, cpu, ramUsedMb, ramTotalMb, diskUsedMb, diskTotalMb, load5m) {
        const rxEl = document.getElementById('speed-rx');
        const txEl = document.getElementById('speed-tx');
        const dailyRxEl = document.getElementById('traffic-rx-daily');
        const totalRxEl = document.getElementById('traffic-rx-total');
        const dailyTxEl = document.getElementById('traffic-tx-daily');
        const totalTxEl = document.getElementById('traffic-tx-total');
        const utEl = document.getElementById('update-time');
        const cpuEl = document.getElementById('sys-cpu');
        const cpuBar = document.getElementById('cpu-bar');
        const load5mEl = document.getElementById('sys-load5m');
        const diskEl = document.getElementById('sys-disk');
        const ramPct = document.getElementById('sys-ram-pct');
        const ramBar = document.getElementById('ram-bar');
        const ramEl = document.getElementById('sys-ram');
        const cdtUsed = document.getElementById('cdt-used');
        const cdtRem = document.getElementById('cdt-remaining');
        const cdtPct = document.getElementById('cdt-percent');
        const cdtBar = document.getElementById('cdt-bar');
        const v6El = document.getElementById('traffic-v6-tx');
        const v4El = document.getElementById('traffic-v4-tx');

        if (rxEl) rxEl.innerHTML = formatKbps(rxKbps, downArrowSvg);
        if (txEl) txEl.innerHTML = formatKbps(txKbps, upArrowSvg);
        if (dailyRxEl && typeof dailyRxMb === 'number') dailyRxEl.textContent = formatTrafficMb(dailyRxMb);
        if (totalRxEl) totalRxEl.textContent = formatTrafficMb(totalRxMb);
        if (dailyTxEl && typeof dailyTxMb === 'number') dailyTxEl.textContent = formatTrafficMb(dailyTxMb);
        if (totalTxEl) totalTxEl.textContent = formatTrafficMb(totalTxMb);
        if (utEl) utEl.textContent = new Date().toTimeString().split(' ')[0];

        // Official CDT real data (bound directly to OpenAPI, no estimation or subtraction)
        if (v6El && typeof cdtV6Mb === 'number') {
            v6El.textContent = formatTrafficMb(cdtV6Mb);
        }
        if (v4El && typeof cdtV4Mb === 'number') {
            v4El.textContent = formatTrafficMb(cdtV4Mb);
        }

        if (cpuEl) cpuEl.textContent = cpu.toFixed(2) + '%';
        if (cpuBar) cpuBar.style.width = Math.min(cpu, 100) + '%';
        if (load5mEl && typeof load5m === 'number') {
            load5mEl.textContent = load5m.toFixed(2);
        }
        if (diskEl && typeof diskUsedMb === 'number' && typeof diskTotalMb === 'number') {
            const pct = diskTotalMb > 0 ? ((diskUsedMb / diskTotalMb) * 100).toFixed(0) : 0;
            diskEl.textContent = `${pct}%`;
        }

        // Dynamic physical total memory (zero hardcoded values)
        const totalRam = (typeof ramTotalMb === 'number' && ramTotalMb > 0) ? ramTotalMb : 512;
        const ramPercent = Math.min((ramUsedMb / totalRam) * 100, 100);
        if (ramPct) ramPct.textContent = ramPercent.toFixed(1) + '%';
        if (ramBar) ramBar.style.width = ramPercent + '%';
        if (ramEl) ramEl.textContent = `${ramUsedMb}M / ${totalRam}M`;

        const cdtTotalMb = 20480;
        const cdtUsedGb = (typeof cdtUsedMb === 'number' ? cdtUsedMb : 0) / 1024;
        const cdtRemGb = Math.max((cdtTotalMb - (cdtUsedMb || 0)) / 1024, 0);
        const cdtUsagePct = Math.min(((cdtUsedMb || 0) / cdtTotalMb) * 100, 100);

        if (cdtUsed) cdtUsed.textContent = `${cdtUsedGb.toFixed(2)} GB`;
        if (cdtRem) cdtRem.textContent = `${cdtRemGb.toFixed(2)} GB`;
        if (cdtPct) cdtPct.textContent = `${cdtUsagePct.toFixed(1)}%`;
        if (cdtBar) {
            cdtBar.style.width = cdtUsagePct + '%';
            if (cdtUsagePct > 85) cdtBar.classList.add('status-progress-fill--danger');
            else if (cdtUsagePct > 60) cdtBar.classList.add('status-progress-fill--warn');
        }

        // As long as real telemetry keeps arriving, the badge stays Live
        if (!hasConnectionError && !isPaused && isUsingWT) {
            const badgeText = document.getElementById('poll-status');
            if (badgeText && badgeText.textContent !== 'WebTransport') {
                updateBadgeState('live', 'WebTransport');
                updateActionBtnState('pause');
            }
        }
    }

    // Decoupled badge and button state control
    const PAUSE_ICON = '<svg width="11" height="11" viewBox="0 0 24 24" fill="currentColor"><use href="#status-icon-pause"/></svg>';
    const PLAY_ICON = '<svg width="11" height="11" viewBox="0 0 24 24" fill="currentColor"><use href="#status-icon-play"/></svg>';
    const RETRY_ICON = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><use href="#status-icon-sync"/></svg>';

    let hasConnectionError = false;

    function updateBadgeState(state, text) {
        const badge = document.getElementById('poll-badge');
        const textEl = document.getElementById('poll-status');
        if (!badge || !textEl) return;

        badge.className = 'status-badge';
        if (state === 'live') {
            badge.classList.add('status-badge--live');
            textEl.textContent = text || 'WebTransport';
        } else if (state === 'warn') {
            badge.classList.add('status-badge--warn');
            textEl.textContent = text || '连接中...';
        } else if (state === 'paused') {
            badge.classList.add('status-badge--paused');
            textEl.textContent = text || '已暂停';
        } else {
            badge.classList.add('status-badge--error');
            textEl.textContent = text || '连接中断';
        }
    }

    function updateActionBtnState(state) {
        const btn = document.getElementById('stream-action-btn');
        if (!btn) return;
        btn.disabled = false;
        btn.style.pointerEvents = '';
        btn.classList.remove('status-btn-success', 'status-btn-error');

        if (state === 'pause') {
            btn.className = 'status-action-btn status-action-btn--pause';
            btn.innerHTML = `${PAUSE_ICON}<span>暂停</span>`;
            btn.title = '暂停接收实时数据流';
        } else if (state === 'continue') {
            btn.className = 'status-action-btn status-action-btn--continue';
            btn.innerHTML = `${PLAY_ICON}<span>继续</span>`;
            btn.title = '恢复接收实时数据流';
        } else if (state === 'retry') {
            btn.className = 'status-action-btn status-action-btn--retry';
            btn.innerHTML = `${RETRY_ICON}<span>重试</span>`;
            btn.title = '重新连接 WebTransport';
        }
    }

    function setOfflineState(reason) {
        hasConnectionError = true;
        isUsingWT = false;
        if (wtTransport) {
            try { wtTransport.close(); } catch(e){}
            wtTransport = null;
        }
        updateBadgeState('error', reason || '连接中断');
        updateActionBtnState('retry');
    }

    // 3. WebTransport core connection function
    let isConnecting = false;
    let reconnectTimer = null;
    let reconnectAttempts = 0;

    async function tryConnectWebTransport() {
        if (typeof WebTransport === 'undefined') {
            return { success: false, reason: '浏览器不支持' };
        }

        // Reuse an existing healthy connection directly
        if (wtTransport && isUsingWT) {
            return { success: true };
        }

        // Clean up stale leftover instances
        if (wtTransport) {
            try { wtTransport.close(); } catch(e){}
            wtTransport = null;
        }
        isUsingWT = false;

        try {
            const controller = new AbortController();
            const fetchTimeout = setTimeout(() => controller.abort(), 3000);
            let res;
            try {
                var B = window.SITE_URLS;
                res = await fetch(B.admin.sentinel, { signal: controller.signal });
            } finally {
                clearTimeout(fetchTimeout);
            }
            if (!res.ok) {
                return { success: false, reason: '获取配置失败' };
            }
            const info = await res.json();
            if (!info.enabled || !info.cert_hash) {
                return { success: false, reason: '未启用' };
            }

            const targetIp = info.ip_v4 || info.ip || info.ip_v6;
            if (!targetIp) {
                return { success: false, reason: '未配置 IP' };
            }
            const hostPart = targetIp.includes(':') && !targetIp.startsWith('[') ? `[${targetIp}]` : targetIp;
            const url = `https://${hostPart}:${info.port}/status`;
            const transport = new WebTransport(url, {
                serverCertificateHashes: [{
                    algorithm: 'sha-256',
                    value: hexToBytes(info.cert_hash)
                }]
            });

            // 3s fast handshake timeout guard
            const wtTimeout = new Promise((_, reject) =>
                setTimeout(() => reject(new Error('握手超时 (3s)')), 3000)
            );
            await Promise.race([transport.ready, wtTimeout]);

            wtTransport = transport;
            isUsingWT = true;
            hasConnectionError = false;
            isPaused = false;
            reconnectAttempts = 0;

            // Watch the underlying lifecycle: self-healing reconnect triggers only on disconnect
            transport.closed.then(() => {
                if (wtTransport === transport) {
                    isUsingWT = false;
                    wtTransport = null;
                    if (!isPaused) scheduleAutoReconnect();
                }
            }).catch(() => {
                if (wtTransport === transport) {
                    isUsingWT = false;
                    wtTransport = null;
                    if (!isPaused) scheduleAutoReconnect();
                }
            });

            // Listen to the datagram broadcast stream (binary hardware telemetry +
            // 0x01 real-time access-log incremental preemption packets)
            const reader = transport.datagrams.readable.getReader();
            (async () => {
                try {
                    while (!isPaused && wtTransport === transport) {
                        const { value, done } = await reader.read();
                        if (done) break;
                        if (!value || value.byteLength === 0) continue;

                        const firstByte = value[0];
                        if (value.byteLength === 52) {
                            // Hardware telemetry packet (52-byte high-precision struct)
                            const dv = new DataView(value.buffer, value.byteOffset, value.byteLength);
                            const rxKbps = dv.getFloat32(0, true);
                            const txKbps = dv.getFloat32(4, true);
                            const dailyRxMb = dv.getFloat32(8, true);
                            const dailyTxMb = dv.getFloat32(12, true);
                            const totalRxMb = dv.getFloat32(16, true);
                            const totalTxMb = dv.getFloat32(20, true);
                            const cdtV6Mb = dv.getFloat32(24, true);
                            const cdtV4Mb = dv.getFloat32(28, true);
                            const cdtUsedMb = dv.getFloat32(32, true);
                            const cpu = dv.getUint16(36, true) / 100.0;
                            const ramUsedMb = dv.getUint16(38, true);
                            const ramTotalMb = dv.getUint16(40, true);
                            const diskUsedMb = dv.getUint32(42, true);
                            const diskTotalMb = dv.getUint32(46, true);
                            const load5m = dv.getUint16(50, true) / 100.0;
                            updateMetricsUI(rxKbps, txKbps, dailyRxMb, dailyTxMb, totalRxMb, totalTxMb, cdtV6Mb, cdtV4Mb, cdtUsedMb, cpu, ramUsedMb, ramTotalMb, diskUsedMb, diskTotalMb, load5m);
                        } else if (firstByte === 0x01) {
                            // Real-time access-log preemption frame (0x01 + JSON)
                            try {
                                const jsonText = new TextDecoder('utf-8').decode(value.subarray(1));
                                const row = JSON.parse(jsonText);
                                if (Array.isArray(row) && row.length >= 9) {
                                    const newLog = {
                                        time: row[0],
                                        ip: row[1],
                                        method: row[2],
                                        uri: row[3],
                                        status: row[4],
                                        size_str: row[5],
                                        cost_str: row[6],
                                        ua: row[7],
                                        via: row[8]
                                    };
                                    // Insert at top and dedupe
                                    if (!rawLogs.some(l => l.time === newLog.time && l.ip === newLog.ip && l.uri === newLog.uri)) {
                                        rawLogs.unshift(newLog);
                                        if (rawLogs.length > 5000) rawLogs.pop();
                                        filterLogs();
                                    }
                                }
                            } catch (err) {
                                console.warn('[Status] Parse real-time log error:', err);
                            }
                        }
                    }
                } catch (e) {
                    if (!isPaused && wtTransport === transport) {
                        setOfflineState('连接中断');
                        scheduleAutoReconnect();
                    }
                }
            })();

            return { success: true };
        } catch (e) {
            const isTimeout = (e && (e.name === 'AbortError' || String(e.message).includes('3s')));
            if (wtTransport) {
                try { wtTransport.close(); } catch(err){}
                wtTransport = null;
            }
            isUsingWT = false;
            return { success: false, reason: isTimeout ? '握手超时' : '握手失败' };
        }
    }

    // Auto-reconnect engine (fixed 1s interval, up to 3 attempts, stops at the cap)
    function scheduleAutoReconnect() {
        if (isPaused || isUsingWT || isConnecting || reconnectAttempts >= 3) return;
        if (reconnectTimer) clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(async () => {
            if (isPaused || isUsingWT || isConnecting || reconnectAttempts >= 3) return;
            await executeConnectFlow(false);
        }, 1000);
    }

    // Self-heal on page wake or network recovery
    function tryResumeConnection() {
        if (!isPaused && (!wtTransport || !isUsingWT)) {
            reconnectAttempts = 0;
            scheduleAutoReconnect();
        }
    }
    document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') tryResumeConnection();
    });
    window.addEventListener('online', tryResumeConnection);

    // Unified connection control flow (smooth transition, no millisecond flicker)
    async function executeConnectFlow(isManualClick) {
        if (isConnecting) return;
        isConnecting = true;
        if (isManualClick) reconnectAttempts = 0;
        if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
        }

        const btn = document.getElementById('stream-action-btn');
        if (btn) {
            btn.disabled = true;
            btn.style.pointerEvents = 'none';
            const svg = btn.querySelector('svg');
            if (svg) svg.classList.add('status-spin');
        }
        updateBadgeState('warn', '连接中...');

        const minWait = new Promise(r => setTimeout(r, 400));
        let connResult;
        try {
            [connResult] = await Promise.all([
                tryConnectWebTransport(),
                minWait
            ]);
        } finally {
            isConnecting = false;
        }

        if (btn) {
            const svg = btn.querySelector('svg');
            if (svg) svg.classList.remove('status-spin');
        }

        if (!connResult || !connResult.success) {
            reconnectAttempts += 1;
            const reachedMax = reconnectAttempts >= 3;
            const failReason = connResult ? connResult.reason : '连接失败';
            setOfflineState(reachedMax ? `${failReason} (已停)` : failReason);

            if (!reachedMax && !isPaused) {
                scheduleAutoReconnect();
            }

            if (btn && isManualClick) {
                btn.innerHTML = `<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="#f43f5e" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><use href="#status-icon-cross"/></svg><span>重试</span>`;
                btn.classList.add('status-btn-error');
                setTimeout(() => {
                    btn.classList.remove('status-btn-error');
                    btn.disabled = false;
                    btn.style.pointerEvents = '';
                    updateActionBtnState('retry');
                }, 1000);
            } else if (btn) {
                btn.disabled = false;
                btn.style.pointerEvents = '';
                updateActionBtnState('retry');
            }
        } else {
            reconnectAttempts = 0;
            updateBadgeState('live', 'WebTransport');
            if (btn && isManualClick) {
                btn.innerHTML = `<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><use href="#status-icon-check"/></svg><span>成功</span>`;
                btn.classList.add('status-btn-success');
                setTimeout(() => {
                    btn.classList.remove('status-btn-success');
                    btn.disabled = false;
                    btn.style.pointerEvents = '';
                    updateActionBtnState('pause');
                }, 1000);
            } else if (btn) {
                btn.disabled = false;
                btn.style.pointerEvents = '';
                updateActionBtnState('pause');
            }
        }
    }

    // 5. Stream control (three-state switch: pause / continue / retry)
    window.handleStreamAction = async function () {
        if (hasConnectionError) {
            await executeConnectFlow(true);
            return;
        }

        if (!isPaused) {
            isPaused = true;
            if (wtTransport) {
                try { wtTransport.close(); } catch(e){}
                wtTransport = null;
            }
            updateBadgeState('paused', '实时 · 已暂停');
            updateActionBtnState('continue');
        } else {
            isPaused = false;
            await executeConnectFlow(true);
        }
    };

    // Button micro-interaction feedback: green check or red cross lingers 1s after the spin ends
    const SYNC_ORIG_SVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><use href="#status-icon-sync"/></svg>';
    const CHECK_SVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><use href="#status-icon-check"/></svg>';
    const CROSS_SVG = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#f43f5e" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><use href="#status-icon-cross"/></svg>';

    function flashButtonResult(btn, isSuccess) {
        if (!btn) return;
        const currentSvg = btn.querySelector('svg');
        if (currentSvg) {
            currentSvg.outerHTML = isSuccess ? CHECK_SVG : CROSS_SVG;
        }
        if (isSuccess) {
            btn.classList.add('status-btn-success');
        } else {
            btn.classList.add('status-btn-error');
        }

        setTimeout(() => {
            const resultSvg = btn.querySelector('svg');
            if (resultSvg) {
                resultSvg.outerHTML = SYNC_ORIG_SVG;
            }
            btn.classList.remove('status-btn-success', 'status-btn-error');
            btn.disabled = false;
            btn.style.pointerEvents = '';
            btn.style.opacity = '1';
        }, 1000);
    }

    function getDayStartEndTs(offsetDays = 0) {
        const d = new Date();
        d.setHours(0, 0, 0, 0);
        if (offsetDays !== 0) {
            d.setDate(d.getDate() + offsetDays);
        }
        const startTs = Math.floor(d.getTime() / 1000);
        const endTs = startTs + 86399;
        return [startTs, endTs];
    }

    function getLocalDateStr(offsetDays = 0) {
        const d = new Date();
        if (offsetDays !== 0) {
            d.setDate(d.getDate() + offsetDays);
        }
        const y = d.getFullYear();
        const m = String(d.getMonth() + 1).padStart(2, '0');
        const day = String(d.getDate()).padStart(2, '0');
        return `${y}-${m}-${day}`;
    }

    function formatDateStrFromDate(d) {
        const y = d.getFullYear();
        const m = String(d.getMonth() + 1).padStart(2, '0');
        const day = String(d.getDate()).padStart(2, '0');
        return `${y}-${m}-${day}`;
    }

    let [defaultStartTs, defaultEndTs] = getDayStartEndTs(0);
    let selectedStartTs = defaultStartTs;
    let selectedEndTs = defaultEndTs;
    let selectedPresetKey = 'today';

    // 2. Manual historical log fetch (WebTransport stream + Zstd=1 fast compression)
    window.fetchManualHistoricalLogs = async function (btnTrigger) {
        const btn = btnTrigger || document.querySelector('.status-fetch-btn');
        const svg = btn?.querySelector('svg');
        if (btn) {
            btn.disabled = true;
            btn.style.pointerEvents = 'none';
            btn.style.opacity = '0.7';
            if (svg) svg.classList.add('status-spin');
        }

        const runFetch = async () => {
            if (!wtTransport || !isUsingWT) {
                const conn = await tryConnectWebTransport();
                if (!conn.success || !wtTransport) {
                    throw new Error('WebTransport 未连接: ' + (conn.reason || ''));
                }
            }

            // Ensure the underlying QUIC handshake has fully completed
            await wtTransport.ready;

            const stream = await wtTransport.createBidirectionalStream();
            const writer = stream.writable.getWriter();
            const reader = stream.readable.getReader();

            let reqBuf;
            if (currentFetchMode === 'time') {
                // Timestamp range mode: 0x01 + 8-byte start_ts (i64) + 8-byte end_ts (i64)
                reqBuf = new Uint8Array(17);
                reqBuf[0] = 0x01;
                const dv = new DataView(reqBuf.buffer);
                dv.setBigInt64(1, BigInt(selectedStartTs), true);
                dv.setBigInt64(9, BigInt(selectedEndTs), true);
            } else {
                // Count mode: 0x00 + 2-byte limit
                const limitInput = document.getElementById('log-limit-input');
                let limit = parseInt(limitInput?.value || limitInput?.placeholder || '100', 10);
                if (isNaN(limit) || limit <= 0) limit = 100;
                reqBuf = new Uint8Array([0x00, limit & 0xFF, (limit >> 8) & 0xFF]);
            }

            await writer.write(reqBuf);
            writer.close().catch(() => {});

            const chunks = [];
            while (true) {
                const { value, done } = await reader.read();
                if (done) break;
                if (value) chunks.push(value);
            }
            if (chunks.length > 0) {
                let totalLen = chunks.reduce((acc, c) => acc + c.length, 0);
                let merged = new Uint8Array(totalLen);
                let offset = 0;
                for (let c of chunks) {
                    merged.set(c, offset);
                    offset += c.length;
                }

                // History logs come back zstd-compressed (level 1) over a raw
                // WebTransport stream. That bypasses the HTTP layer entirely, so no
                // automatic content-encoding decompression applies — the bytes have
                // to be decoded here, in JS.
                //
                // fzstd is the only dependable choice. The native
                // DecompressionStream('zstd') does exist in the compression spec,
                // but it is not implemented everywhere and throws a TypeError on
                // browsers that lack it, so it is not used here. Shipping the 8 KB
                // library is cheaper than a status page that breaks depending on
                // which browser happens to be used.
                let text = '';
                if (window.fzstd && typeof window.fzstd.decompress === 'function') {
                    const decompressed = window.fzstd.decompress(merged);
                    text = new TextDecoder('utf-8').decode(decompressed);
                } else {
                    // Fail loudly instead of feeding compressed bytes to JSON.parse.
                    throw new Error('fzstd is not loaded; cannot decode zstd history logs');
                }

                const json = JSON.parse(text);
                if (json.data && Array.isArray(json.data)) {
                    rawLogs = json.data.map(row => ({
                        time: row[0],
                        ip: row[1],
                        method: row[2],
                        uri: row[3],
                        status: row[4],
                        size_str: row[5],
                        cost_str: row[6],
                        ua: row[7],
                        via: row[8],
                    }));
                    saveLogsToStorage();
                    filterLogs();
                }
            }
        };

        const timeout = new Promise((_, reject) => setTimeout(() => reject(new Error('timeout')), 8000));
        let success = false;

        try {
            await Promise.race([runFetch(), timeout]);
            success = true;
        } catch(e) {
            console.warn('拉取日志失败或超时:', e);
            success = false;
        } finally {
            if (svg) svg.classList.remove('status-spin');
            flashButtonResult(btn, success);
        }
    };

    // Enter key in the input triggers a fetch
    document.getElementById('log-limit-input')?.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
            switchFetchMode('count');
            fetchManualHistoricalLogs();
        }
    });

    // 3. Local log cache management
    function loadLogsFromStorage() {
        try {
            const cached = sessionStorage.getItem(STORAGE_KEY);
            if (cached) {
                rawLogs = JSON.parse(cached) || [];
                filterLogs();
            }
        } catch (e) {}
    }

    function saveLogsToStorage() {
        try {
            sessionStorage.setItem(STORAGE_KEY, JSON.stringify(rawLogs.slice(0, 300)));
        } catch (e) {}
    }

    // 4. Unified IP copy interaction (same for V4 and V6)
    window.copyIp = function (ip, el) {
        if (!ip || ip === 'CDN') return;
        navigator.clipboard.writeText(ip).then(() => {
            const orig = el.textContent;
            el.textContent = '已复制 ✓';
            el.style.color = 'var(--accent)';
            setTimeout(() => {
                el.textContent = orig;
                el.style.color = '';
            }, 1200);
        }).catch(() => {});
    };

    window.syncOfficialCDT = async function (btnTrigger) {
    const btn = btnTrigger || document.querySelector('.status-sync-btn');
    const svg = btn?.querySelector('svg');
    if (btn) {
        btn.disabled = true;
        btn.style.pointerEvents = 'none';
        btn.style.opacity = '0.7';
        if (svg) svg.classList.add('status-spin');
    }

    // Attempt to trigger CDT sync via WebTransport control datagram (0xFF)
    const attemptWT = async () => {
        if (!wtTransport) throw new Error('WT not connected');
        const writer = wtTransport.datagrams.writable.getWriter();
        try {
            await writer.write(new Uint8Array([0xFF]));
            // Success: UI will update on next telemetry datagram
            return true;
        } finally {
            writer.releaseLock();
        }
    };

    // WT is the only data channel; failures throw (no silent degradation / HTTP fallback)
    const timeout = new Promise((_, reject) => setTimeout(() => reject(new Error('timeout')), 5000));
    let success = false;
    try {
        await Promise.race([ (async () => {
            await attemptWT();
            return true;
        })(), timeout ]);
        success = true;
    } catch (e) {
        console.warn('CDT sync failed or timed out:', e);
        success = false;
    } finally {
        if (svg) svg.classList.remove('status-spin');
        flashButtonResult(btn, success);
    }
};

    // 6. Status-code cyclic-offset sorting and filtering
    function getStatusSequence() {
        const set = new Set(rawLogs.map(l => l.status));
        const list = Array.from(set).filter(c => c !== 200).sort((a, b) => a - b);
        if (set.has(200)) {
            list.push(200);
        }
        return list;
    }

    // Normalize via labels (sorting and rendering share the same algorithm)
    function getViaLabel(item) {
        let t = item.via;
        if (!t) t = item.ip ? 'CLIENT' : 'DIRECT';
        t = String(t).toUpperCase();
        if (t === 'PROXY' || t === 'VIACDN') t = 'CLIENT';
        return t;
    }
    function getViaSequence() {
        const order = ['MASTER', 'CLIENT', 'DIRECT', 'BANNED'];
        const present = new Set(rawLogs.map(getViaLabel));
        return order.filter((t) => present.has(t));
    }

    function escapeHtml(str) {
        if (!str) return '';
        return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }

    function parseBytes(str) {
        if (!str) return 0;
        if (typeof str === 'number') return str;
        const s = String(str).trim().toUpperCase();
        const num = parseFloat(s) || 0;
        if (s.endsWith('GB') || s.endsWith('G')) return num * 1073741824;
        if (s.endsWith('MB') || s.endsWith('M')) return num * 1048576;
        if (s.endsWith('KB') || s.endsWith('K')) return num * 1024;
        return num;
    }

    function parseCost(str) {
        if (!str) return 0;
        if (typeof str === 'number') return str;
        const s = String(str).trim().toLowerCase();
        const num = parseFloat(s) || 0;
        if (s.endsWith('s') && !s.endsWith('ms') && !s.endsWith('µs') && !s.endsWith('us')) return num * 1000;
        if (s.endsWith('µs') || s.endsWith('us')) return num / 1000;
        return num;
    }

    let currentFetchMode = 'time'; // 'time' or 'count'
    let currentTimeFilter = 'all'; // show all logs by default on entry

    window.switchFetchMode = function (mode) {
        currentFetchMode = mode;
        const btnCount = document.getElementById('mode-btn-count');
        const btnTime = document.getElementById('mode-btn-time');
        if (btnCount) btnCount.classList.toggle('active', mode === 'count');
        if (btnTime) btnTime.classList.toggle('active', mode === 'time');

        const countWrap = document.getElementById('fetch-count-wrap');
        const timeWrap = document.getElementById('fetch-time-wrap');
        if (countWrap) countWrap.style.display = mode === 'count' ? 'inline-flex' : 'none';
        if (timeWrap) timeWrap.style.display = mode === 'time' ? 'inline-flex' : 'none';
    };

    window.toggleTimePopoverMenu = function (e) {
        e.stopPropagation();
        closeAllFilterMenus();
        const menu = document.getElementById('time-popover-menu');
        if (!menu) return;
        menu.style.display = (menu.style.display === 'flex' || menu.style.display === 'block') ? 'none' : 'flex';
    };

    window.selectTimePreset = function (key, label) {
        selectedPresetKey = key;
        currentTimeFilter = key;
        updateTimeFilterMenuUI();
        const now = Math.floor(Date.now() / 1000);
        if (key === '5m') {
            selectedStartTs = now - 300;
            selectedEndTs = now;
        } else if (key === '1h') {
            selectedStartTs = now - 3600;
            selectedEndTs = now;
        } else if (key === 'today') {
            const [s, e] = getDayStartEndTs(0);
            selectedStartTs = s;
            selectedEndTs = e;
        } else if (key === 'yesterday') {
            const [s, e] = getDayStartEndTs(-1);
            selectedStartTs = s;
            selectedEndTs = e;
        }

        const labelEl = document.getElementById('time-popover-label');
        if (labelEl) labelEl.textContent = label;

        document.querySelectorAll('.status-popover-seg-btn').forEach(el => el.classList.remove('active'));
        document.getElementById(`pop-pill-${key}`)?.classList.add('active');

        const menu = document.getElementById('time-popover-menu');
        if (menu) menu.style.display = 'none';

        fetchManualHistoricalLogs();
    };

    window.onRelativeTimeChange = function () {
        const numInput = document.getElementById('relative-num-input');
        const unitSelect = document.getElementById('relative-unit-select');
        let num = parseInt(numInput?.value || '15', 10);
        if (isNaN(num) || num <= 0) num = 15;
        const unit = unitSelect?.value || 'm';
        let mult = 60;
        let unitText = '分';
        if (unit === 'h') {
            mult = 3600;
            unitText = '时';
        } else if (unit === 'd') {
            mult = 86400;
            unitText = '天';
        }

        const now = Math.floor(Date.now() / 1000);
        selectedStartTs = now - (num * mult);
        selectedEndTs = now;
        selectedPresetKey = 'relative';

        const labelEl = document.getElementById('time-popover-label');
        if (labelEl) labelEl.textContent = `${num}${unitText}`;

        document.querySelectorAll('.status-popover-seg-btn').forEach(el => el.classList.remove('active'));
    };

    window.onCustomRangeChange = function () {
        const startInput = document.getElementById('range-start-input');
        const endInput = document.getElementById('range-end-input');
        if (!startInput?.value || !endInput?.value) return;

        const sDate = new Date(startInput.value);
        const eDate = new Date(endInput.value);
        if (isNaN(sDate.getTime()) || isNaN(eDate.getTime())) return;

        selectedStartTs = Math.floor(sDate.getTime() / 1000);
        selectedEndTs = Math.floor(eDate.getTime() / 1000);
        selectedPresetKey = 'custom';

        const labelEl = document.getElementById('time-popover-label');
        if (labelEl) labelEl.textContent = '区间';

        document.querySelectorAll('.status-popover-seg-btn').forEach(el => el.classList.remove('active'));
    };

    window.setTimeFilter = function (filterVal, btn) {
        currentTimeFilter = filterVal;
        filterLogs();
        updateTimeFilterMenuUI();
    };

    window.onLimitInputChange = function () {
        filterLogs();
    };

    window.clearSearch = function () {
        const input = document.getElementById('search-input');
        if (input) {
            input.value = '';
            filterLogs();
        }
    };

    let selectedVia = '';
    let selectedStatus = '';

    window.filterLogs = function () {
        const searchInput = document.getElementById('search-input');
        const q = (searchInput?.value || '').toLowerCase().trim();
        const clearBtn = document.getElementById('search-clear-btn');
        if (clearBtn) clearBtn.style.display = q ? 'block' : 'none';

        const todayStr = getLocalDateStr(0);
        const yesterdayStr = getLocalDateStr(-1);
        const now = Date.now();

        let filtered = rawLogs.filter(item => {
            if (selectedIp && item.ip !== selectedIp) return false;
            if (selectedPath) {
                const basePath = (item.uri || '').split('?')[0] || '/';
                if (basePath !== selectedPath) return false;
            }
            if (selectedVia && getViaLabel(item).toUpperCase() !== selectedVia.toUpperCase()) return false;
            if (selectedStatus && String(item.status) !== String(selectedStatus)) return false;
            if (currentTimeFilter !== 'all') {
                let logMs = 0;
                let itemDayStr = '';
                if (item.time) {
                    if (item.time.length >= 10) {
                        itemDayStr = item.time.slice(0, 10);
                    }
                    try {
                        const d = new Date(item.time);
                        if (!isNaN(d.getTime())) {
                            logMs = d.getTime();
                            itemDayStr = formatDateStrFromDate(d);
                        }
                    } catch(e) {}
                }
                const logSec = Math.floor(logMs / 1000);

                if (currentTimeFilter === '5m') {
                    if (logMs < now - 5 * 60 * 1000) return false;
                } else if (currentTimeFilter === '1h') {
                    if (logMs < now - 60 * 60 * 1000) return false;
                } else if (currentTimeFilter === 'today') {
                    if (itemDayStr !== todayStr) return false;
                } else if (currentTimeFilter === 'yesterday') {
                    if (itemDayStr !== yesterdayStr) return false;
                } else if (currentTimeFilter !== 'all') {
                    if (itemDayStr !== currentTimeFilter) return false;
                }
            }
            if (!q) return true;
            const text = `${item.ip} ${item.uri} ${item.status} ${item.method} ${item.ua}`.toLowerCase();
            return text.includes(q);
        });

        if (sortField === 'status') {
            const seq = getStatusSequence();
            if (seq.length > 0) {
                const startIdx = statusSortIdx % seq.length;
                const getRank = (code) => {
                    const idx = seq.indexOf(code);
                    if (idx === -1) return 999;
                    return (idx - startIdx + seq.length) % seq.length;
                };

                filtered.sort((a, b) => {
                    const rankA = getRank(a.status);
                    const rankB = getRank(b.status);
                    if (rankA !== rankB) return rankA - rankB;
                    return (b.time || '').localeCompare(a.time || '');
                });
            } else {
                filtered.sort((a, b) => (b.time || '').localeCompare(a.time || ''));
            }
        } else if (sortField === 'via') {
            const seq = getViaSequence();
            if (seq.length > 0) {
                const startIdx = viaSortIdx % seq.length;
                const getRank = (label) => {
                    const idx = seq.indexOf(label);
                    if (idx === -1) return 999;
                    return (idx - startIdx + seq.length) % seq.length;
                };
                filtered.sort((a, b) => {
                    const rankA = getRank(getViaLabel(a));
                    const rankB = getRank(getViaLabel(b));
                    if (rankA !== rankB) return rankA - rankB;
                    return (b.time || '').localeCompare(a.time || '');
                });
            } else {
                filtered.sort((a, b) => (b.time || '').localeCompare(a.time || ''));
            }
        } else if (sortField === 'bytes' || sortField === 'size' || sortField === 'size_str') {
            filtered.sort((a, b) => {
                const valA = parseBytes(a.bytes || a.size_str);
                const valB = parseBytes(b.bytes || b.size_str);
                return sortAsc ? valA - valB : valB - valA;
            });
        } else if (sortField === 'cost' || sortField === 'cost_str') {
            filtered.sort((a, b) => {
                const valA = parseCost(a.cost || a.cost_str);
                const valB = parseCost(b.cost || b.cost_str);
                return sortAsc ? valA - valB : valB - valA;
            });
        } else if (sortField === 'path' || sortField === 'uri') {
            if (pathSortMode === 0) {
                // Top frequency descending (most-visited first, identical URLs grouped
                // together, avoiding ABABAB interleaving)
                const pathCounts = {};
                rawLogs.forEach(l => {
                    const u = l.uri || '/';
                    pathCounts[u] = (pathCounts[u] || 0) + 1;
                });
                filtered.sort((a, b) => {
                    const countA = pathCounts[a.uri || '/'] || 0;
                    const countB = pathCounts[b.uri || '/'] || 0;
                    if (countB !== countA) return countB - countA;
                    const uriA = a.uri || '';
                    const uriB = b.uri || '';
                    if (uriA !== uriB) return uriA.localeCompare(uriB);
                    return (b.time || '').localeCompare(a.time || '');
                });
            } else {
                filtered.sort((a, b) => {
                    let valA = a.uri || '';
                    let valB = b.uri || '';
                    return sortAsc ? valA.localeCompare(valB) : valB.localeCompare(valA);
                });
            }
        } else {
            filtered.sort((a, b) => {
                let valA = a[sortField] || '';
                let valB = b[sortField] || '';
                if (typeof valA === 'string') {
                    return sortAsc ? valA.localeCompare(valB) : valB.localeCompare(valA);
                }
                return sortAsc ? valA - valB : valB - valA;
            });
        }

        // Count-limit slice (when a numeric limit is entered, live-truncate to the first N)
        const limitInput = document.getElementById('log-limit-input');
        let limitVal = parseInt(limitInput?.value || '', 10);
        if (!isNaN(limitVal) && limitVal > 0 && filtered.length > limitVal) {
            filtered = filtered.slice(0, limitVal);
        }

        renderTable(filtered);
    };

    // 7. Dropdown filter menu unified manager
    // Menu items starting with a CJK character are marked data-ink-zh: CJK glyph ink
    // left edges sit a fixed delta right of Latin/digit ones; status.css shifts them
    // by --font-mono-ink-left-delta for alignment (measured on the admin UI, applies
    // to this menu only).
    function markMenuInkZh(menu) {
        if (!menu) return;
        const items = menu.querySelectorAll('.status-dropdown-item > span');
        for (const sp of items) {
            const first = (sp.textContent || '').trim().charAt(0);
            if (first && /[\u4e00-\u9fff]/.test(first)) {
                sp.setAttribute('data-ink-zh', '1');
            }
        }
    }
    function closeAllFilterMenus() {
        const timePop = document.getElementById('time-popover-menu');
        if (timePop) {
            timePop.style.display = 'none';
        }
        const timeMenu = document.getElementById('time-filter-menu');
        if (timeMenu) {
            timeMenu.style.display = 'none';
            timeMenu.closest('th')?.classList.remove('status-th-active-menu');
        }
        const ipMenu = document.getElementById('ip-filter-menu');
        if (ipMenu) {
            ipMenu.style.display = 'none';
            ipMenu.closest('th')?.classList.remove('status-th-active-menu');
        }
        const pathMenu = document.getElementById('path-filter-menu');
        if (pathMenu) {
            pathMenu.style.display = 'none';
            pathMenu.closest('th')?.classList.remove('status-th-active-menu');
        }
        const viaMenu = document.getElementById('via-filter-menu');
        if (viaMenu) {
            viaMenu.style.display = 'none';
            viaMenu.closest('th')?.classList.remove('status-th-active-menu');
        }
        const statusMenu = document.getElementById('status-filter-menu');
        if (statusMenu) {
            statusMenu.style.display = 'none';
            statusMenu.closest('th')?.classList.remove('status-th-active-menu');
        }
    }

    // 8. Time dropdown filter control (relative presets + dynamic log date list)
    window.toggleTimeFilterMenu = function (e) {
        e.stopPropagation();
        const menu = document.getElementById('time-filter-menu');
        if (!menu) return;

        if (menu.style.display === 'block') {
            closeAllFilterMenus();
            return;
        }
        closeAllFilterMenus();

        const todayStr = getLocalDateStr(0);
        const yesterdayStr = getLocalDateStr(-1);

        const now = Date.now();
        let count5m = 0;
        let count1h = 0;
        const dayMap = new Map();
        for (const l of rawLogs) {
            if (l.time) {
                try {
                    const d = new Date(l.time);
                    if (!isNaN(d.getTime())) {
                        const ms = d.getTime();
                        if (ms >= now - 5 * 60 * 1000) count5m++;
                        if (ms >= now - 60 * 60 * 1000) count1h++;
                        const dayStr = formatDateStrFromDate(d);
                        dayMap.set(dayStr, (dayMap.get(dayStr) || 0) + 1);
                    }
                } catch(e) {}
            }
        }

        const sortedDays = Array.from(dayMap.keys()).sort((a, b) => b.localeCompare(a));

        let html = `<div class="status-dropdown-item ${currentTimeFilter === 'all' ? 'status-dropdown-item--active' : ''}" onclick="selectTimeFilter('all')"><span>全部</span><small>${rawLogs.length}</small></div>` +
            `<div class="status-dropdown-item ${currentTimeFilter === '5m' ? 'status-dropdown-item--active' : ''}" onclick="selectTimeFilter('5m')"><span>5 分钟</span><small>${count5m}</small></div>` +
            `<div class="status-dropdown-item ${currentTimeFilter === '1h' ? 'status-dropdown-item--active' : ''}" onclick="selectTimeFilter('1h')"><span>1 小时</span><small>${count1h}</small></div>`;

        if (sortedDays.length > 0) {
            html += `<div class="status-dropdown-divider"></div>`;
            for (const dayStr of sortedDays) {
                const count = dayMap.get(dayStr);
                let label = dayStr;
                let key = dayStr;
                let isChinese = false;

                if (dayStr === todayStr) {
                    label = '今日';
                    key = 'today';
                    isChinese = true;
                } else if (dayStr === yesterdayStr) {
                    label = '昨日';
                    key = 'yesterday';
                    isChinese = true;
                } else {
                    const parts = dayStr.split('-');
                    if (parts.length === 3) {
                        const m = parseInt(parts[1], 10);
                        const d = parseInt(parts[2], 10);
                        label = `${m}/${d}`;
                    }
                }

                const activeCls = (currentTimeFilter === key || currentTimeFilter === dayStr) ? 'status-dropdown-item--active' : '';
                const spanStyle = !isChinese ? ' style="font-family: var(--font-mono);"' : '';
                html += `<div class="status-dropdown-item ${activeCls}" onclick="selectTimeFilter('${key}')" title="${dayStr}"><span${spanStyle}>${label}</span><small>${count}</small></div>`;
            }
        }

        menu.innerHTML = html;
        markMenuInkZh(menu);
        openFilterMenu(menu);
    };

    window.selectTimeFilter = function (key) {
        currentTimeFilter = key;
        updateTimeFilterMenuUI();
        closeAllFilterMenus();
        filterLogs();
    };

    function updateTimeFilterMenuUI() {
        const btn = document.getElementById('time-filter-trigger');
        if (btn) {
            if (currentTimeFilter !== 'all') btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
    }

    function openFilterMenu(menu) {
        if (!menu) return;
        const tbody = document.getElementById('logs-tbody');
        const rowCount = tbody ? tbody.querySelectorAll('tr').length : 0;
        if (rowCount >= 6) {
            menu.style.maxHeight = 'none';
        } else {
            menu.style.maxHeight = '299px'; // 8px padding + 10 * 29px + 1px border-bottom (exactly 10 rows, zero overflow, no fake scrollbar)
        }
        menu.style.display = 'block';
        menu.closest('th')?.classList.add('status-th-active-menu');
    }

    // 9. IP dropdown filter control (count ranking by default; side SVG button toggles alphabetical order)
    let ipMenuSortMode = 'count'; // 'count' (count ranking, default) or 'alpha' (alphabetical)

    function renderIpFilterMenuContent(menu) {
        if (!menu) return;
        const ipMap = new Map();
        for (const l of rawLogs) {
            if (l.ip) ipMap.set(l.ip, (ipMap.get(l.ip) || 0) + 1);
        }

        let sortedIps = Array.from(ipMap.keys());
        if (ipMenuSortMode === 'count') {
            sortedIps.sort((a, b) => {
                const diff = (ipMap.get(b) || 0) - (ipMap.get(a) || 0);
                return diff !== 0 ? diff : a.localeCompare(b);
            });
        } else {
            sortedIps.sort((a, b) => a.localeCompare(b));
        }

        let html = `<div class="status-dropdown-item ${selectedIp === '' ? 'status-dropdown-item--active' : ''}" onclick="selectIpFilter('')"><span>全部 IP</span><small>${rawLogs.length}</small></div>`;

        for (const ip of sortedIps) {
            const isV6 = ip.includes(':');
            const short = isV6 ? (ip.slice(0, 4) + '...' + ip.slice(-4)) : ip;
            const count = ipMap.get(ip) || 0;
            html += `<div class="status-dropdown-item ${selectedIp === ip ? 'status-dropdown-item--active' : ''}" onclick="selectIpFilter('${ip}')" title="${ip}"><span style="font-family: var(--font-mono);">${short}</span><small>${count}</small></div>`;
        }
        menu.innerHTML = html;
        markMenuInkZh(menu);
    }

    window.toggleIpMenuSortMode = function (e) {
        e.stopPropagation();
        ipMenuSortMode = ipMenuSortMode === 'count' ? 'alpha' : 'count';
        const btn = document.getElementById('ip-sort-mode-trigger');
        if (btn) {
            if (ipMenuSortMode === 'alpha') btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
        const menu = document.getElementById('ip-filter-menu');
        if (menu && menu.style.display === 'block') {
            renderIpFilterMenuContent(menu);
        }
    };

    window.toggleIpFilterMenu = function (e) {
        e.stopPropagation();
        const menu = document.getElementById('ip-filter-menu');
        if (!menu) return;

        if (menu.style.display === 'block') {
            closeAllFilterMenus();
            return;
        }
        closeAllFilterMenus();
        renderIpFilterMenuContent(menu);
        openFilterMenu(menu);
    };

    window.selectIpFilter = function (ip) {
        selectedIp = ip;
        const btn = document.getElementById('ip-filter-trigger');
        if (btn) {
            if (ip) btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
        closeAllFilterMenus();
        filterLogs();
    };

    // 10. Request path dropdown filter control (TOP count ranking by default; side SVG button toggles alphabetical order)
    let pathMenuSortMode = 'count'; // 'count' (count ranking, default) or 'alpha' (alphabetical)

    function renderPathFilterMenuContent(menu) {
        if (!menu) return;
        const pathMap = new Map();
        for (const l of rawLogs) {
            const base = (l.uri || '').split('?')[0] || '/';
            pathMap.set(base, (pathMap.get(base) || 0) + 1);
        }

        let sortedPaths = Array.from(pathMap.keys());
        if (pathMenuSortMode === 'count') {
            sortedPaths.sort((a, b) => {
                const diff = (pathMap.get(b) || 0) - (pathMap.get(a) || 0);
                return diff !== 0 ? diff : a.localeCompare(b);
            });
        } else {
            sortedPaths.sort((a, b) => a.localeCompare(b));
        }

        let html = `<div class="status-dropdown-item ${selectedPath === '' ? 'status-dropdown-item--active' : ''}" onclick="selectPathFilter('')"><span>全部路径</span><small>${rawLogs.length}</small></div>`;

        for (const p of sortedPaths) {
            const count = pathMap.get(p);
            html += `<div class="status-dropdown-item ${selectedPath === p ? 'status-dropdown-item--active' : ''}" onclick="selectPathFilter('${p}')" title="${p}"><span style="font-family: var(--font-mono); overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${p}</span><small>${count}</small></div>`;
        }
        menu.innerHTML = html;
        markMenuInkZh(menu);
    }

    window.togglePathMenuSortMode = function (e) {
        e.stopPropagation();
        pathMenuSortMode = pathMenuSortMode === 'count' ? 'alpha' : 'count';
        const btn = document.getElementById('path-sort-mode-trigger');
        if (btn) {
            if (pathMenuSortMode === 'alpha') btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
        const menu = document.getElementById('path-filter-menu');
        if (menu && menu.style.display === 'block') {
            renderPathFilterMenuContent(menu);
        }
    };

    window.togglePathFilterMenu = function (e) {
        e.stopPropagation();
        const menu = document.getElementById('path-filter-menu');
        if (!menu) return;

        if (menu.style.display === 'block') {
            closeAllFilterMenus();
            return;
        }
        closeAllFilterMenus();
        renderPathFilterMenuContent(menu);
        openFilterMenu(menu);
    };

    window.selectPathFilter = function (p) {
        selectedPath = p;
        const btn = document.getElementById('path-filter-trigger');
        if (btn) {
            if (p) btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
        closeAllFilterMenus();
        filterLogs();
    };

    // 11. Via dropdown filter control
    window.toggleViaFilterMenu = function (e) {
        e.stopPropagation();
        const menu = document.getElementById('via-filter-menu');
        if (!menu) return;

        if (menu.style.display === 'block') {
            closeAllFilterMenus();
            return;
        }
        closeAllFilterMenus();

        const viaMap = new Map();
        for (const l of rawLogs) {
            const v = getViaLabel(l);
            if (v) viaMap.set(v, (viaMap.get(v) || 0) + 1);
        }

        const viaOrder = getViaSequence();

        let html = `<div class="status-dropdown-item ${selectedVia === '' ? 'status-dropdown-item--active' : ''}" onclick="selectViaFilter('')"><span>全部源</span><small>${rawLogs.length}</small></div>`;

        for (const key of viaOrder) {
            const count = viaMap.get(key) || 0;
            const activeCls = selectedVia.toUpperCase() === key ? 'status-dropdown-item--active' : '';
            html += `<div class="status-dropdown-item ${activeCls}" onclick="selectViaFilter('${key}')"><span style="font-family: var(--font-mono);">${key}</span><small>${count}</small></div>`;
        }

        menu.innerHTML = html;
        markMenuInkZh(menu);
        openFilterMenu(menu);
    };

    window.selectViaFilter = function (v) {
        selectedVia = v;
        const btn = document.getElementById('via-filter-trigger');
        if (btn) {
            if (v) btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
        closeAllFilterMenus();
        filterLogs();
    };

    // 12. Status code dropdown filter control
    window.toggleStatusFilterMenu = function (e) {
        e.stopPropagation();
        const menu = document.getElementById('status-filter-menu');
        if (!menu) return;

        if (menu.style.display === 'block') {
            closeAllFilterMenus();
            return;
        }
        closeAllFilterMenus();

        const statusMap = new Map();
        for (const l of rawLogs) {
            if (l.status) statusMap.set(String(l.status), (statusMap.get(String(l.status)) || 0) + 1);
        }

        let sortedStatuses = Array.from(statusMap.keys()).sort((a, b) => (statusMap.get(b) || 0) - (statusMap.get(a) || 0));

        let html = `<div class="status-dropdown-item ${selectedStatus === '' ? 'status-dropdown-item--active' : ''}" onclick="selectStatusFilter('')"><span>All</span><small>${rawLogs.length}</small></div>`;

        for (const st of sortedStatuses) {
            const count = statusMap.get(st) || 0;
            const activeCls = selectedStatus === st ? 'status-dropdown-item--active' : '';
            html += `<div class="status-dropdown-item ${activeCls}" onclick="selectStatusFilter('${st}')"><span style="font-family: var(--font-mono);">${st}</span><small>${count}</small></div>`;
        }

        menu.innerHTML = html;
        markMenuInkZh(menu);
        openFilterMenu(menu);
    };

    window.selectStatusFilter = function (st) {
        selectedStatus = st;
        const btn = document.getElementById('status-filter-trigger');
        if (btn) {
            if (st) btn.classList.add('status-filter-btn--active');
            else btn.classList.remove('status-filter-btn--active');
        }
        closeAllFilterMenus();
        filterLogs();
    };

    document.addEventListener('click', (e) => {
        if (!e.target.closest('.status-col-ip') && !e.target.closest('.status-col-path') && !e.target.closest('.status-col-time') && !e.target.closest('.status-col-via') && !e.target.closest('.status-col-status') && !e.target.closest('.status-fetch-time-wrap')) {
            closeAllFilterMenus();
        }
    });

    function renderTable(logs) {
        const tbody = document.getElementById('logs-tbody');
        const countEl = document.getElementById('log-count');
        if (countEl) {
            if (logs.length === rawLogs.length) {
                countEl.textContent = `${rawLogs.length}条`;
                countEl.classList.remove('has-filter');
            } else {
                countEl.textContent = `${logs.length}/${rawLogs.length}`;
                countEl.classList.add('has-filter');
            }
        }
        if (!tbody) return;

        if (logs.length === 0) {
            tbody.innerHTML = '<tr><td colspan="8" style="text-align: center; padding: 24px 0; color: var(--text-dim);">暂无符合条件的访问记录</td></tr>';
            return;
        }

        let html = '';
        const todayStr = getLocalDateStr(0);
        const yesterdayStr = getLocalDateStr(-1);

        for (const item of logs) {
            let timeStr = '--:--:--';
            if (item.time) {
                try {
                    const d = new Date(item.time);
                    if (!isNaN(d.getTime())) {
                        const itemDayStr = formatDateStrFromDate(d);
                        const fullTime = d.toTimeString().split(' ')[0]; // "14:20:35"
                        const shortTime = fullTime.slice(0, 5); // "14:20"
                        if (itemDayStr === todayStr) {
                            timeStr = fullTime;
                        } else if (itemDayStr === yesterdayStr) {
                            timeStr = `昨日 ${shortTime}`;
                        } else {
                            const m = d.getMonth() + 1;
                            const day = d.getDate();
                            timeStr = `${m}/${day} ${shortTime}`;
                        }
                    } else {
                        timeStr = item.time.split('T')[1]?.split('+')[0] || item.time;
                    }
                } catch(e) {
                    timeStr = item.time.split('T')[1]?.split('+')[0] || item.time;
                }
            }
            let badgeClass = 'status-code-200';
            if (item.status === 304) badgeClass = 'status-code-304';
            else if (item.status === 418) badgeClass = 'status-code-418';
            else if (item.status >= 400 && item.status < 500) badgeClass = 'status-code-404';
            else if (item.status >= 500) badgeClass = 'status-code-500';

            let displayIp = item.ip || '-';
            let isV6 = displayIp.includes(':');
            let shortIp = isV6 ? `${displayIp.slice(0, 4)}...${displayIp.slice(-4)}` : displayIp;
            let uaStr = (item.ua || '-').replace(/^\(|\)$/g, '').trim();

            // Via labels: MASTER purple / CLIENT green / DIRECT orange / BANNED red
            let viaLabel = getViaLabel(item);
            let viaCls = viaLabel === 'MASTER' ? 'via-master' : viaLabel === 'BANNED' ? 'via-banned' : viaLabel === 'DIRECT' ? 'via-direct' : 'via-client';

            html += `
                <tr>
                    <td class="status-col-time" style="color: var(--text-dim);">${timeStr}</td>
                    <td class="status-col-ip" style="color: var(--text-primary); cursor: pointer;" onclick="copyIp('${displayIp}', this)" title="${displayIp} (点击复制)">${shortIp}</td>
                    <td class="status-col-via"><span class="via-badge ${viaCls}">${viaLabel}</span></td>
                    <td class="status-col-ua" title="${escapeHtml(uaStr)}">${escapeHtml(uaStr)}</td>
                    <td class="status-col-status"><span class="status-code-badge ${badgeClass}">${item.status}</span></td>
                    <td class="status-col-path"><span style="opacity:0.7;font-weight:600;margin-right:6px;">${item.method || 'GET'}</span><span title="${item.uri}">${item.uri}</span></td>
                    <td class="status-col-bytes">${item.size_str || '0 B'}</td>
                    <td class="status-col-cost" style="color: var(--text-dim);">${item.cost_str || '0ms'}</td>
                </tr>
            `;
        }
        tbody.innerHTML = html;
        updateSortIndicators();
    }

    let userSorted = false;
    let pathSortMode = 0; // 0: Top frequency, 1: A-Z ascending, 2: Z-A descending

    function updateSortIndicators() {
        const fields = ['time', 'bytes', 'cost', 'path'];
        const ascSvg = '<svg viewBox="0 0 24 24" class="status-arrow-svg"><use href="#status-icon-arrow"/></svg>';
        const descSvg = '<svg viewBox="0 0 24 24" class="status-arrow-svg status-arrow-down"><use href="#status-icon-arrow"/></svg>';

        for (const f of fields) {
            const el = document.getElementById(`sort-icon-${f}`);
            if (!el) continue;
            if (userSorted && (sortField === f || (f === 'path' && sortField === 'path'))) {
                if (f === 'path' && pathSortMode === 0) {
                    el.innerHTML = '<span style="font-size:9px;font-weight:700;letter-spacing:0.5px;">TOP</span>';
                } else {
                    el.innerHTML = sortAsc ? ascSvg : descSvg;
                }
                el.style.opacity = '1';
                el.style.color = 'var(--accent)';
            } else {
                el.innerHTML = '';
                el.style.opacity = '0';
            }
        }
    }

    window.sortTable = function (field) {
        userSorted = true;
        if (field === 'status') {
            sortField = 'status';
            viaSortIdx = -1;
            const seq = getStatusSequence();
            if (seq.length === 0) return;
            statusSortIdx = (statusSortIdx + 1) % seq.length;
        } else if (field === 'via') {
            sortField = 'via';
            statusSortIdx = -1;
            const seq = getViaSequence();
            if (seq.length === 0) return;
            viaSortIdx = (viaSortIdx + 1) % seq.length;
        } else if (field === 'path' || field === 'uri') {
            statusSortIdx = -1;
            viaSortIdx = -1;
            if (sortField === 'path') {
                pathSortMode = (pathSortMode + 1) % 3;
            } else {
                sortField = 'path';
                pathSortMode = 0; // first click prefers Top frequency
            }
            if (pathSortMode === 0) {
                sortAsc = true;
            } else if (pathSortMode === 1) {
                sortAsc = true; // A-Z
            } else {
                sortAsc = false; // Z-A
            }
        } else {
            statusSortIdx = -1;
            viaSortIdx = -1;
            if (sortField === field) {
                sortAsc = !sortAsc;
            } else {
                sortField = field;
                sortAsc = false;
            }
        }
        filterLogs();
        updateSortIndicators();
    };

    async function startPipeline() {
        loadLogsFromStorage();
        await executeConnectFlow(false);
    }

    startPipeline();
})();
