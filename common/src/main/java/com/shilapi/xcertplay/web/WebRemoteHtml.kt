package com.shilapi.xcertplay.web

/**
 * Built-in responsive Web Remote application served to browsers.
 * Viewport-first layout optimized for browser screen sizes with dynamic resolution adaptation.
 */
object WebRemoteHtml {
    fun getHtml(port: Int): String = """
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no">
<title>DiPlay Web Remote · CarPlay 浏览器投屏与控制</title>
<style>
  :root {
    --bg-primary: #070a10;
    --bg-card: rgba(18, 25, 42, 0.78);
    --border-card: rgba(70, 110, 190, 0.22);
    --accent: #3b82f6;
    --accent-glow: rgba(59, 130, 246, 0.45);
    --text-primary: #f1f5f9;
    --text-secondary: #94a3b8;
    --success: #10b981;
    --warning: #f59e0b;
    --danger: #ef4444;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; user-select: none; -webkit-user-select: none; }
  html, body {
    width: 100vw;
    height: 100vh;
    overflow: hidden;
    background: var(--bg-primary);
    color: var(--text-primary);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  body {
    display: flex;
    flex-direction: column;
    position: relative;
  }

  /* Fullscreen Viewport Canvas */
  .viewport-canvas {
    position: absolute;
    inset: 0;
    width: 100vw;
    height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    background: radial-gradient(circle at 50% 50%, #111a2d 0%, #06090e 80%);
    overflow: hidden;
    touch-action: none;
  }
  .screen-img {
    max-width: 100vw;
    max-height: 100vh;
    width: 100%;
    height: 100%;
    object-fit: contain;
    display: block;
    pointer-events: auto;
    touch-action: none;
    transition: filter 0.2s ease;
    image-rendering: auto;
    image-rendering: -webkit-pictorial;
    image-rendering: smooth;
    will-change: contents;
    transform: translateZ(0);
    backface-visibility: hidden;
  }

  /* Top Floating Island Header */
  .floating-header {
    position: fixed;
    top: 14px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 40;
    display: flex;
    align-items: center;
    gap: 12px;
    background: rgba(14, 20, 34, 0.72);
    backdrop-filter: blur(20px);
    -webkit-backdrop-filter: blur(20px);
    border: 1px solid var(--border-card);
    border-radius: 9999px;
    padding: 6px 16px;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.45);
    transition: opacity 0.3s ease, transform 0.3s ease;
  }
  .floating-header:hover {
    background: rgba(14, 20, 34, 0.92);
  }
  .brand-chip {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 700;
    font-size: 0.92rem;
    letter-spacing: 0.4px;
    color: #fff;
  }
  .brand-logo {
    width: 22px;
    height: 22px;
    border-radius: 6px;
    background: linear-gradient(135deg, #3b82f6, #8b5cf6);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.75rem;
    font-weight: bold;
    box-shadow: 0 0 10px var(--accent-glow);
  }
  .status-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 0.75rem;
    padding: 3px 8px;
    border-radius: 9999px;
    background: rgba(16, 185, 129, 0.16);
    color: #34d399;
    font-weight: 600;
  }
  .status-chip.idle {
    background: rgba(148, 163, 184, 0.16);
    color: #94a3b8;
  }
  .status-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #10b981;
    box-shadow: 0 0 8px #10b981;
    animation: pulse 2s infinite;
  }
  .status-chip.idle .status-dot {
    background: #64748b;
    box-shadow: none;
    animation: none;
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; transform: scale(1); }
    50% { opacity: 0.4; transform: scale(0.85); }
  }
  .meta-tag {
    font-size: 0.76rem;
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .header-actions {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-left: 6px;
  }
  .icon-btn {
    background: rgba(255, 255, 255, 0.08);
    border: 1px solid rgba(255, 255, 255, 0.1);
    color: var(--text-primary);
    border-radius: 9999px;
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    font-size: 0.85rem;
    transition: all 0.2s ease;
  }
  .icon-btn:hover {
    background: rgba(59, 130, 246, 0.3);
    border-color: var(--accent);
    transform: scale(1.08);
  }

  /* Bottom Floating Control Dock */
  .control-dock {
    position: fixed;
    bottom: 20px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 40;
    display: flex;
    align-items: center;
    gap: 8px;
    background: rgba(14, 20, 34, 0.78);
    backdrop-filter: blur(20px);
    -webkit-backdrop-filter: blur(20px);
    border: 1px solid var(--border-card);
    border-radius: 20px;
    padding: 8px 14px;
    box-shadow: 0 10px 40px rgba(0, 0, 0, 0.6);
    opacity: 0.45;
    transition: all 0.25s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .control-dock:hover, .control-dock.active {
    opacity: 1;
    transform: translateX(-50%) translateY(-2px);
    background: rgba(14, 20, 34, 0.94);
  }
  .dock-btn {
    background: rgba(255, 255, 255, 0.06);
    border: 1px solid rgba(255, 255, 255, 0.08);
    color: var(--text-primary);
    padding: 8px 14px;
    border-radius: 12px;
    font-size: 0.88rem;
    font-weight: 600;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
    transition: all 0.15s ease;
  }
  .dock-btn:hover {
    background: rgba(59, 130, 246, 0.25);
    border-color: var(--accent);
    transform: translateY(-2px);
  }
  .dock-btn:active {
    transform: scale(0.95);
  }
  .dock-btn.home-btn {
    background: linear-gradient(135deg, rgba(59, 130, 246, 0.25), rgba(139, 92, 246, 0.25));
    border-color: rgba(99, 102, 241, 0.4);
  }
  .dock-btn.siri-btn {
    background: linear-gradient(135deg, rgba(236, 72, 153, 0.25), rgba(168, 85, 247, 0.25));
    border-color: rgba(236, 72, 153, 0.4);
  }
  .dock-btn.active-pop {
    background: rgba(59, 130, 246, 0.4);
    border-color: var(--accent);
  }
  .dock-btn.active-mic {
    background: rgba(239, 68, 68, 0.35);
    border-color: #ef4444;
    color: #fca5a5;
    animation: micPulse 1.5s infinite;
  }
  @keyframes micPulse {
    0%, 100% { box-shadow: 0 0 4px rgba(239, 68, 68, 0.4); }
    50% { box-shadow: 0 0 16px rgba(239, 68, 68, 0.9); }
  }
  .dock-btn.active-audio {
    border-color: #10b981;
    color: #6ee7b7;
  }
  .dock-divider {
    width: 1px;
    height: 22px;
    background: var(--border-card);
    margin: 0 2px;
  }

  /* Resolution Picker Popover */
  .res-popover {
    position: fixed;
    bottom: 80px;
    left: 50%;
    transform: translateX(-50%) scale(0.95);
    background: rgba(18, 25, 42, 0.94);
    backdrop-filter: blur(24px);
    -webkit-backdrop-filter: blur(24px);
    border: 1px solid var(--border-card);
    border-radius: 16px;
    padding: 12px;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.7);
    z-index: 50;
    width: 320px;
    display: none;
    opacity: 0;
    transition: all 0.2s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .res-popover.show {
    display: block;
    opacity: 1;
    transform: translateX(-50%) scale(1);
  }
  .res-popover-title {
    font-size: 0.85rem;
    font-weight: 700;
    color: var(--text-secondary);
    margin-bottom: 8px;
    padding: 0 4px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .res-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .res-item {
    background: rgba(255, 255, 255, 0.04);
    border: 1px solid transparent;
    border-radius: 8px;
    padding: 8px 12px;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--text-primary);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: space-between;
    transition: all 0.15s ease;
  }
  .res-item:hover {
    background: rgba(59, 130, 246, 0.2);
    border-color: var(--accent);
  }
  .res-item.current {
    background: rgba(59, 130, 246, 0.3);
    border-color: var(--accent);
    color: #fff;
    font-weight: 600;
  }
  .res-badge {
    font-size: 0.72rem;
    color: var(--text-secondary);
  }

  /* Suggestion Banner (Auto-adaptation prompt) */
  .aspect-hint {
    position: fixed;
    top: 70px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 45;
    background: rgba(30, 41, 67, 0.92);
    backdrop-filter: blur(12px);
    border: 1px solid rgba(245, 158, 11, 0.4);
    border-radius: 12px;
    padding: 8px 16px;
    font-size: 0.82rem;
    color: #fbbf24;
    display: flex;
    align-items: center;
    gap: 12px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.5);
    animation: fadeIn 0.3s ease;
  }
  @keyframes fadeIn {
    from { opacity: 0; transform: translate(-50%, -8px); }
    to { opacity: 1; transform: translate(-50%, 0); }
  }
  .aspect-hint button {
    background: #f59e0b;
    border: none;
    color: #000;
    padding: 4px 10px;
    border-radius: 6px;
    font-size: 0.78rem;
    font-weight: 700;
    cursor: pointer;
  }

  /* Multi-touch Ripple Indicators (Slot 0 Cyan & Slot 1 Magenta) */
  .touch-ripple {
    position: fixed;
    width: 36px;
    height: 36px;
    border-radius: 50%;
    transform: translate(-50%, -50%) scale(0.7);
    pointer-events: none;
    z-index: 30;
    transition: transform 0.15s cubic-bezier(0.1, 0.9, 0.2, 1), opacity 0.2s ease-out;
    box-shadow: 0 0 16px rgba(0, 0, 0, 0.5);
  }
  .touch-ripple.ripple-0 {
    border: 2.5px solid rgba(56, 189, 248, 0.95);
    background: radial-gradient(circle, rgba(56, 189, 248, 0.45) 0%, rgba(56, 189, 248, 0.1) 70%);
  }
  .touch-ripple.ripple-1 {
    border: 2.5px solid rgba(236, 72, 153, 0.95);
    background: radial-gradient(circle, rgba(236, 72, 153, 0.45) 0%, rgba(236, 72, 153, 0.1) 70%);
  }

  /* Empty / Connecting Overlay */
  .overlay-empty {
    position: absolute;
    inset: 0;
    background: rgba(7, 10, 16, 0.94);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 16px;
    z-index: 10;
    padding: 24px;
    text-align: center;
    transition: opacity 0.3s;
  }
  .overlay-empty.hidden {
    opacity: 0;
    pointer-events: none;
  }
  .spinner {
    width: 44px;
    height: 44px;
    border: 3px solid rgba(59, 130, 246, 0.2);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
</head>
<body>

<!-- Viewport-Filling Canvas -->
<div class="viewport-canvas" id="viewportCanvas">
  <img id="carplayScreen" class="screen-img" alt="CarPlay Viewport Stream" src="/stream" draggable="false">
  <canvas id="carplayVideoCanvas" class="screen-img" style="display: none; position: absolute; inset: 0; margin: auto;" draggable="false"></canvas>
  <div id="touchFeedback0" class="touch-ripple ripple-0" style="display: none;"></div>
  <div id="touchFeedback1" class="touch-ripple ripple-1" style="display: none;"></div>
  <div id="emptyOverlay" class="overlay-empty">
    <div class="spinner"></div>
    <h3 style="font-size: 1.25rem; font-weight: 600;">正在加载 CarPlay 投屏...</h3>
    <p style="color: var(--text-secondary); font-size: 0.9rem; max-width: 420px;">
      请确认 iPhone 已连接并在车机/手机端开启 CarPlay。画面将优先自适应填充当前浏览器窗口。
    </p>
  </div>
</div>

<!-- Floating Pill Header -->
<div class="floating-header">
  <div class="brand-chip">
    <div class="brand-logo">D</div>
    <span>DiPlay Web Remote</span>
  </div>
  <div id="statusBadge" class="status-chip idle">
    <span class="status-dot"></span>
    <span id="statusText">等待连接</span>
  </div>
  <div class="meta-tag" id="tagNetwork" style="color: #38bdf8; border-color: rgba(56, 189, 248, 0.3);">⚡ WS 极速已连</div>
  <div class="meta-tag" id="tagResolution">-- × --</div>
  <div class="meta-tag" id="tagFpsLatency">FPS: -- · -- ms</div>
  <div class="header-actions">
    <button class="icon-btn" id="btnSnapshot" title="保存当前画面高清快照">📸</button>
    <button class="icon-btn" id="btnFullscreen" title="切换浏览器全屏">⛶</button>
  </div>
</div>

<!-- Resolution Adaptation Prompt Banner -->
<div class="aspect-hint" id="aspectHint" style="display: none;">
  <span>💡 检测到当前为宽屏浏览器，建议一键切换为 16:9 宽屏分辨率以获得最佳排版效果</span>
  <button id="btnQuickAdapt">立即适配</button>
  <span style="cursor: pointer; opacity: 0.7;" onclick="document.getElementById('aspectHint').style.display='none'">✕</span>
</div>

<!-- Resolution Picker Popover -->
<div class="res-popover" id="resPopover">
  <div class="res-popover-title">
    <span>CarPlay 协商分辨率</span>
    <span style="cursor: pointer;" id="btnClosePopover">✕</span>
  </div>
  <div class="res-list">
    <div class="res-item" data-w="auto" data-h="auto" id="optAutoFit">
      <span>⚡ 适配当前浏览器视口</span>
      <span class="res-badge" id="autoFitDimensions">自适应</span>
    </div>
    <div class="res-item" data-w="1920" data-h="1080">
      <span>🖥️ 1080P 宽屏 (16:9)</span>
      <span class="res-badge">1920×1080</span>
    </div>
    <div class="res-item" data-w="1280" data-h="720">
      <span>🚀 720P 极速宽屏 (16:9)</span>
      <span class="res-badge">1280×720</span>
    </div>
    <div class="res-item" data-w="1920" data-h="720">
      <span>🚘 超宽车载带鱼屏 (8:3)</span>
      <span class="res-badge">1920×720</span>
    </div>
    <div class="res-item" data-w="1024" data-h="768">
      <span>📐 经典中控 (4:3)</span>
      <span class="res-badge">1024×768</span>
    </div>
    <div class="res-item" data-w="native" data-h="native">
      <span>📱 宿主真机屏幕比例</span>
      <span class="res-badge">跟随车机/手机</span>
    </div>
  </div>
</div>

<!-- Floating Action Dock -->
<div class="control-dock" id="controlDock">
  <button class="dock-btn home-btn" id="btnHome" title="CarPlay 主屏幕">
    <span>🏠</span> 主屏幕
  </button>
  <button class="dock-btn siri-btn" id="btnSiri" title="唤醒 Siri 语音">
    <span>🎙️</span> Siri
  </button>
  <div class="dock-divider"></div>
  <button class="dock-btn" id="btnPrev" title="上一曲">
    <span>⏮️</span>
  </button>
  <button class="dock-btn" id="btnPlayPause" title="播放 / 暂停">
    <span>⏯️</span>
  </button>
  <button class="dock-btn" id="btnNext" title="下一曲">
    <span>⏭️</span>
  </button>
  <div class="dock-divider"></div>
  <button class="dock-btn" id="btnAudio" title="CarPlay 声音输出至网页 (点击切换静音/播放)">
    <span>🔊</span> <span id="labelAudio">网页声音</span>
  </button>
  <button class="dock-btn" id="btnMic" title="网页麦克风输入给 CarPlay">
    <span>🎙️</span> <span id="labelMic">网页麦克风</span>
  </button>
  <div class="dock-divider"></div>
  <button class="dock-btn" id="btnToggleRes" title="切换投屏分辨率以适配浏览器">
    <span>🖥️</span> <span id="labelResBtn">画面尺寸</span>
  </button>
  <button class="dock-btn" id="btnQuality" title="画质调节 (点击切换流畅/超清/极限)">
    <span>💎</span> <span id="qualityLabel">画质: 原画超清 (90% 推荐)</span>
  </button>
</div>

<script>
  const screen = document.getElementById('carplayScreen');
  const canvas = document.getElementById('viewportCanvas');
  const touchFeedback0 = document.getElementById('touchFeedback0');
  const touchFeedback1 = document.getElementById('touchFeedback1');
  const emptyOverlay = document.getElementById('emptyOverlay');
  const statusBadge = document.getElementById('statusBadge');
  const statusText = document.getElementById('statusText');
  const tagNetwork = document.getElementById('tagNetwork');
  const tagResolution = document.getElementById('tagResolution');
  const tagFpsLatency = document.getElementById('tagFpsLatency');
  const aspectHint = document.getElementById('aspectHint');
  const resPopover = document.getElementById('resPopover');
  const btnToggleRes = document.getElementById('btnToggleRes');
  const controlDock = document.getElementById('controlDock');

  let currentWidth = 0;
  let currentHeight = 0;
  let hasCheckedAspect = false;

  // ----------------------------------------------------
  // Ultra Low-Latency WebSocket Bus
  // ----------------------------------------------------
  let ws = null;
  let wsConnected = false;
  let wsReconnectTimer = null;
  let audioPlayer = null;

  function initWebSocket() {
    if (ws) {
      try { ws.close(); } catch (_) {}
    }
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = proto + '//' + location.host + '/ws';
    ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      wsConnected = true;
      if (tagNetwork) {
        tagNetwork.textContent = '⚡ WS 极速已连';
        tagNetwork.style.borderColor = 'rgba(16, 185, 129, 0.4)';
        tagNetwork.style.color = '#34d399';
      }
      try {
        ws.send(JSON.stringify({ type: 'requestKeyFrame' }));
      } catch (_) {}
    };

    ws.onmessage = (event) => {
      try {
        if (event.data instanceof ArrayBuffer) {
          const buf = event.data;
          if (buf.byteLength >= 4) {
            const u8 = new Uint8Array(buf);
            const channel = u8[0];
            const flag = u8[1];
            if (channel === 0x00) {
              // Channel 0x00: PCM Audio
              if (audioPlayer && audioPlayer.isDedicatedWsActive && audioPlayer.isDedicatedWsActive()) {
                return; // Already streaming via dedicated zero-jitter audio connection
              }
              const rateCode = flag & 0x07;
              let sRate = 44100;
              if (rateCode === 1) sRate = 16000;
              else if (rateCode === 2) sRate = 24000;
              else if (rateCode === 3) sRate = 48000;
              const ch = (flag & 0x08) !== 0 ? 1 : 2;
              if (audioPlayer) {
                audioPlayer.feedPcmChunk(buf.slice(4), sRate, ch);
              }
              return;
            } else if (channel === 0x01) {
              // Channel 0x01: H.264/H.265 Video Configuration (avcC / SPS+PPS)
              const avcCBuffer = buf.slice(4);
              const u8Avc = new Uint8Array(avcCBuffer);
              let codecStr = 'avc1.640028';
              if (u8Avc.length >= 4) {
                const p = u8Avc[1].toString(16).padStart(2, '0');
                const c = u8Avc[2].toString(16).padStart(2, '0');
                const l = u8Avc[3].toString(16).padStart(2, '0');
                codecStr = 'avc1.' + p + c + l;
              }
              initWebCodecsDecoder(codecStr, avcCBuffer);
              return;
            } else if (channel === 0x02) {
              // Channel 0x02: Raw H.264 Video Frame (NAL units in AVCC format)
              const isKey = (flag & 0x01) !== 0;
              const naluBuffer = buf.slice(4);
              handleVideoChunk(naluBuffer, isKey);
              return;
            } else if (channel === 0x03) {
              // Channel 0x03: Ultra-fast WebSocket Bitmap Frame (Runs in all browsers & HTTP non-secure contexts!)
              const imgBuffer = buf.slice(4);
              handleFastImageFrame(imgBuffer);
              return;
            }
          }
        } else if (typeof event.data === 'string') {
          try {
            const msg = JSON.parse(event.data);
            if (msg.type === 'hello' || msg.type === 'status') {
              updateStatusUi(msg);
            }
          } catch (_) {}
        }
      } catch (err) {
        console.error('WS onmessage error:', err);
      }
    };

    ws.onclose = () => {
      wsConnected = false;
      if (tagNetwork) {
        tagNetwork.textContent = '🔌 WS 重连中';
        tagNetwork.style.borderColor = 'rgba(239, 68, 68, 0.4)';
        tagNetwork.style.color = '#f87171';
      }
      clearTimeout(wsReconnectTimer);
      wsReconnectTimer = setTimeout(initWebSocket, 1500);
    };

    ws.onerror = () => {
      wsConnected = false;
    };
  }
  initWebSocket();

  function updateStatusUi(data) {
    currentWidth = data.width || 0;
    currentHeight = data.height || 0;

    if (data.width && data.height) {
      tagResolution.textContent = data.width + ' × ' + data.height;
    }
    if (data.fps !== undefined && !hasReceivedVideoFrame) {
      tagFpsLatency.textContent = 'FPS: ' + data.fps;
    }

    if (data.active && data.hasSession) {
      statusBadge.classList.remove('idle');
      statusText.textContent = hasReceivedVideoFrame ? 'CarPlay 硬件流' : 'CarPlay 运行中';
      emptyOverlay.classList.add('hidden');
    } else {
      statusBadge.classList.add('idle');
      statusText.textContent = data.hasSession ? '流准备中' : '等待连接';
      if (!data.hasSession) emptyOverlay.classList.remove('hidden');
    }

    // Suggested resolution check without aggressive downscaling:
    if (!hasCheckedAspect && data.active && data.hasSession && data.width && data.height) {
      hasCheckedAspect = true;
      const fit = computeAutoFitDimensions();
      const currentRatio = currentWidth / currentHeight;
      const targetRatio = fit.width / fit.height;
      if (Math.abs(currentRatio - targetRatio) > 0.1 && aspectHint) {
        aspectHint.style.display = 'flex';
      }
    }
  }

  // Periodic HTTP fallback status polling
  async function pollStatus() {
    try {
      const resp = await fetch('/api/status', { cache: 'no-store' });
      if (resp.ok) {
        const data = await resp.json();
        updateStatusUi(data);
      }
    } catch (e) {
      if (!wsConnected) {
        statusBadge.classList.add('idle');
        statusText.textContent = '离线断开';
        emptyOverlay.classList.remove('hidden');
      }
    }
  }
  setInterval(pollStatus, 2500);
  pollStatus();

  screen.onload = () => {
    if (!hasReceivedVideoFrame) {
      emptyOverlay.classList.add('hidden');
      statusBadge.classList.remove('idle');
      statusText.textContent = 'CarPlay 运行中';
    }
  };
  screen.onerror = () => {
    setTimeout(() => { screen.src = '/stream?t=' + Date.now(); }, 2000);
  };

  // ----------------------------------------------------
  // WebCodecs 60 FPS Ultra-HD Video Stream Player & Safari Metal Accelerator
  // ----------------------------------------------------
  const isSafari = /^((?!chrome|android).)*safari/i.test(navigator.userAgent);
  const hasWebCodecs = typeof window.VideoDecoder !== 'undefined';
  const videoCanvas = document.getElementById('carplayVideoCanvas');
  const videoCtx = videoCanvas ? videoCanvas.getContext('2d', isSafari ? { alpha: false } : { alpha: false, desynchronized: true }) : null;
  if (videoCtx) {
    videoCtx.imageSmoothingEnabled = true;
    videoCtx.imageSmoothingQuality = 'high';
  }
  let videoDecoder = null;
  let decoderConfigured = false;
  let hasReceivedVideoFrame = false;
  let hasReceivedKeyframe = false;
  let videoFrameCounter = 0;
  let videoFpsInterval = performance.now();
  let currentVideoFps = 0;
  let cachedCodecStr = 'avc1.640028';
  let cachedDescription = null;
  let lastKeyframeRequestTime = 0;
  let lastVideoTimestamp = 0;
  let lastWebCodecsFrameTime = 0;

  function sendRemoteLog(msg) {
    console.log(msg);
    if (ws && wsConnected) {
      try {
        ws.send(JSON.stringify({ type: 'log', msg: msg }));
      } catch (_) {}
    }
  }

  /**
   * Transforms Annex-B (0x00 00 00 01) into AVCC (4-byte length prefix) IN-PLACE.
   * Apple VideoToolbox / Safari WebKit strictly requires AVCC length-prefixed NAL units
   * when configured with an AVCDecoderConfigurationRecord (avcC).
   */
  function toAvccInPlace(buffer) {
    const u8 = new Uint8Array(buffer);
    if (u8.length < 4) return buffer;

    if (u8[0] === 0 && u8[1] === 0 && u8[2] === 0 && u8[3] === 1) {
      const offsets = [];
      let i = 0;
      const len = u8.length;
      while (i <= len - 4) {
        if (u8[i] === 0 && u8[i+1] === 0 && u8[i+2] === 0 && u8[i+3] === 1) {
          offsets.push(i);
          i += 4;
        } else {
          i++;
        }
      }
      for (let s = 0; s < offsets.length; s++) {
        const start = offsets[s];
        const next = (s + 1 < offsets.length) ? offsets[s + 1] : len;
        const nalLen = next - (start + 4);
        u8[start] = (nalLen >>> 24) & 0xff;
        u8[start + 1] = (nalLen >>> 16) & 0xff;
        u8[start + 2] = (nalLen >>> 8) & 0xff;
        u8[start + 3] = nalLen & 0xff;
      }
    }
    return buffer;
  }

  function initWebCodecsDecoder(codecStr, descriptionData) {
    if (!hasWebCodecs || !videoCanvas) return;
    try {
      if (descriptionData && descriptionData.byteLength > 0) {
        cachedDescription = descriptionData;
      }
      if (codecStr) {
        cachedCodecStr = codecStr;
      }
      if (videoDecoder) {
        try { videoDecoder.close(); } catch (_) {}
      }
      hasReceivedKeyframe = false;

      videoDecoder = new VideoDecoder({
        output: (frame) => {
          lastWebCodecsFrameTime = performance.now();
          if (!hasReceivedVideoFrame) {
            hasReceivedVideoFrame = true;
            screen.style.display = 'none';
            videoCanvas.style.display = 'block';
            emptyOverlay.classList.add('hidden');
            statusBadge.classList.remove('idle');
            statusText.textContent = 'CarPlay 硬件流';
            sendRemoteLog('[WebCodecs] 满帧 60 FPS 硬件直通视频流已点亮！');
          }
          if (videoCanvas.width !== frame.displayWidth || videoCanvas.height !== frame.displayHeight) {
            videoCanvas.width = frame.displayWidth;
            videoCanvas.height = frame.displayHeight;
            currentWidth = frame.displayWidth;
            currentHeight = frame.displayHeight;
            tagResolution.textContent = currentWidth + ' × ' + currentHeight;
            if (videoCtx) {
              videoCtx.imageSmoothingEnabled = true;
              videoCtx.imageSmoothingQuality = 'high';
            }
          }
          if (videoCtx) {
            videoCtx.drawImage(frame, 0, 0, videoCanvas.width, videoCanvas.height);
          }
          frame.close();

          videoFrameCounter++;
          const now = performance.now();
          if (now - videoFpsInterval >= 1000) {
            currentVideoFps = Math.round((videoFrameCounter * 1000) / (now - videoFpsInterval));
            videoFrameCounter = 0;
            videoFpsInterval = now;
            tagFpsLatency.textContent = 'FPS: ' + currentVideoFps + ' (60fps硬件流)';
            tagFpsLatency.style.color = '#34d399';
            sendRemoteLog('[WebCodecs] 实时硬件帧率: ' + currentVideoFps + ' FPS');
          }
        },
        error: (e) => {
          sendRemoteLog('[WebCodecs] Decoder error: ' + (e ? (e.message || e) : 'unknown'));
          const now = Date.now();
          if (now - lastKeyframeRequestTime > 1000) {
            lastKeyframeRequestTime = now;
            if (ws && wsConnected) {
              ws.send(JSON.stringify({ type: 'requestKeyFrame' }));
            }
          }
          setTimeout(() => {
            if (cachedDescription) {
              initWebCodecsDecoder(cachedCodecStr, cachedDescription);
            }
          }, 300);
        }
      });

      const config = {
        codec: cachedCodecStr || 'avc1.640028'
      };
      if (!isSafari) {
        config.optimizeForLatency = true;
      }
      if (cachedDescription && cachedDescription.byteLength > 0) {
        config.description = cachedDescription;
      }
      videoDecoder.configure(config);
      decoderConfigured = true;
      sendRemoteLog('[WebCodecs] Decoder configured with: ' + config.codec + ' (Safari=' + isSafari + ')');
    } catch (err) {
      sendRemoteLog('[WebCodecs] Configuration exception: ' + (err ? (err.message || err) : ''));
    }
  }

  function handleVideoChunk(naluBuffer, isKey) {
    if (!hasWebCodecs) return;

    if (!videoDecoder || !decoderConfigured || videoDecoder.state === 'closed') {
      if (cachedDescription) {
        initWebCodecsDecoder(cachedCodecStr, cachedDescription);
      } else {
        return;
      }
    }

    // Critical: Do NOT submit delta frames until the first keyframe has arrived!
    if (!hasReceivedKeyframe) {
      if (!isKey) {
        const now = Date.now();
        if (now - lastKeyframeRequestTime > 1000) {
          lastKeyframeRequestTime = now;
          if (ws && wsConnected) {
            ws.send(JSON.stringify({ type: 'requestKeyFrame' }));
          }
        }
        return;
      }
      hasReceivedKeyframe = true;
      sendRemoteLog('[WebCodecs] First Keyframe received! Starting hardware render.');
    }

    try {
      let currentTs = Math.round(performance.now() * 1000);
      if (currentTs <= lastVideoTimestamp) {
        currentTs = lastVideoTimestamp + 1000;
      }
      lastVideoTimestamp = currentTs;

      // Transform Annex-B start codes to AVCC 4-byte lengths for Apple VideoToolbox / Safari
      const avccBuffer = toAvccInPlace(naluBuffer);

      const chunk = new EncodedVideoChunk({
        type: isKey ? 'key' : 'delta',
        timestamp: currentTs,
        data: avccBuffer
      });
      videoDecoder.decode(chunk);
    } catch (e) {
      sendRemoteLog('[WebCodecs] Chunk decode exception: ' + (e ? (e.message || e) : ''));
    }
  }

  // ----------------------------------------------------
  // Ultra-Fast WebSocket Bitmap Streaming (GPU createImageBitmap Fallback)
  // ----------------------------------------------------
  let fastFrameCount = 0;
  let fastFpsInterval = performance.now();
  let currentFastFps = 0;

  function handleFastImageFrame(imgBuffer) {
    // If WebCodecs hardware stream has started, NEVER let fallback JPEG overwrite the canvas or distort dimensions!
    if (hasWebCodecs && hasReceivedVideoFrame) {
      return;
    }

    const blob = new Blob([imgBuffer], { type: 'image/jpeg' });
    if (typeof createImageBitmap !== 'undefined') {
      createImageBitmap(blob, { resizeQuality: 'high' }).then(bmp => {
        // Double check WebCodecs hasn't taken over while image was decoding
        if (hasWebCodecs && videoDecoder && (performance.now() - lastWebCodecsFrameTime < 1500)) {
          bmp.close();
          return;
        }
        if (!hasReceivedVideoFrame) {
          hasReceivedVideoFrame = true;
          screen.style.display = 'none';
          videoCanvas.style.display = 'block';
          emptyOverlay.classList.add('hidden');
          statusBadge.classList.remove('idle');
          statusText.textContent = 'CarPlay 极速流';
        }
        if (videoCanvas.width !== bmp.width || videoCanvas.height !== bmp.height) {
          videoCanvas.width = bmp.width;
          videoCanvas.height = bmp.height;
          currentWidth = bmp.width;
          currentHeight = bmp.height;
          tagResolution.textContent = currentWidth + ' × ' + currentHeight;
          if (videoCtx) {
            videoCtx.imageSmoothingEnabled = true;
            videoCtx.imageSmoothingQuality = 'high';
          }
        }
        if (videoCtx) {
          videoCtx.drawImage(bmp, 0, 0, videoCanvas.width, videoCanvas.height);
        }
        bmp.close();

        fastFrameCount++;
        const now = performance.now();
        if (now - fastFpsInterval >= 1000) {
          currentFastFps = Math.round((fastFrameCount * 1000) / (now - fastFpsInterval));
          fastFrameCount = 0;
          fastFpsInterval = now;
          tagFpsLatency.textContent = 'FPS: ' + currentFastFps + ' (GPU极速流)';
          tagFpsLatency.style.color = '#34d399';
        }
      }).catch(() => {});
    }
  }

  function getActiveScreenElement() {
    if (hasReceivedVideoFrame && videoCanvas && videoCanvas.style.display !== 'none') {
      return videoCanvas;
    }
    return screen;
  }

  /**
   * Geometry computation to map coordinates accurately to video content,
   * handling letterboxing/pillarboxing dynamically.
   */
  function getAccurateTouchCoordinates(e) {
    const activeEl = getActiveScreenElement();
    const rect = activeEl.getBoundingClientRect();
    const naturalW = (activeEl === videoCanvas ? videoCanvas.width : (screen.naturalWidth || currentWidth || 16));
    const naturalH = (activeEl === videoCanvas ? videoCanvas.height : (screen.naturalHeight || currentHeight || 9));
    const naturalRatio = naturalW / naturalH;
    const clientRatio = rect.width / rect.height;

    let renderWidth = rect.width;
    let renderHeight = rect.height;
    let offsetX = 0;
    let offsetY = 0;

    if (clientRatio > naturalRatio) {
      renderWidth = rect.height * naturalRatio;
      offsetX = (rect.width - renderWidth) / 2;
    } else {
      renderHeight = rect.width / naturalRatio;
      offsetY = (rect.height - renderHeight) / 2;
    }

    const clickX = e.clientX - rect.left - offsetX;
    const clickY = e.clientY - rect.top - offsetY;

    if (clickX < 0 || clickX > renderWidth || clickY < 0 || clickY > renderHeight) {
      return null;
    }

    return {
      x: Math.max(0, Math.min(1, clickX / renderWidth)),
      y: Math.max(0, Math.min(1, clickY / renderHeight)),
      visualX: e.clientX,
      visualY: e.clientY
    };
  }

  // ----------------------------------------------------
  // Native Multi-Touch Support (Slot 0 & Slot 1)
  // ----------------------------------------------------
  const MAX_SLOTS = 2;
  const activeContacts = new Map(); // pointerId -> { slot, x, y, visualX, visualY, down }

  function getAvailableSlot() {
    const used = new Set();
    for (const c of activeContacts.values()) {
      used.add(c.slot);
    }
    for (let slot = 0; slot < MAX_SLOTS; slot++) {
      if (!used.has(slot)) return slot;
    }
    return -1;
  }

  function updateTouchFeedback(slot, visualX, visualY, visible) {
    const el = slot === 0 ? touchFeedback0 : touchFeedback1;
    if (!el) return;
    if (visible) {
      el.style.left = visualX + 'px';
      el.style.top = visualY + 'px';
      el.style.display = 'block';
      el.style.transform = 'translate(-50%, -50%) scale(1)';
      el.style.opacity = '1';
    } else {
      el.style.transform = 'translate(-50%, -50%) scale(1.6)';
      el.style.opacity = '0';
      setTimeout(() => {
        let isUsed = false;
        for (const c of activeContacts.values()) {
          if (c.slot === slot) { isUsed = true; break; }
        }
        if (!isUsed) el.style.display = 'none';
      }, 150);
    }
  }

  let touchSendScheduled = false;
  function scheduleSendTouchReport() {
    if (touchSendScheduled) return;
    touchSendScheduled = true;
    requestAnimationFrame(() => {
      touchSendScheduled = false;
      dispatchTouchContacts();
    });
  }

  async function dispatchTouchContacts() {
    const contacts = [];
    for (const c of activeContacts.values()) {
      contacts.push({
        id: c.slot,
        x: c.x,
        y: c.y,
        down: c.down
      });
    }

    // Prioritize ultra-low latency WebSocket transmission
    if (wsConnected && ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        type: 'touch',
        contacts: contacts
      }));
      return;
    }

    // Fallback to HTTP POST
    try {
      await fetch('/api/touch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ contacts })
      });
    } catch (_) {}
  }

  canvas.addEventListener('pointerdown', (e) => {
    unlockAudio();
    if (e.target.closest('.floating-header') || e.target.closest('.control-dock') || e.target.closest('.res-popover') || e.target.closest('.aspect-hint')) {
      return;
    }
    const coords = getAccurateTouchCoordinates(e);
    if (!coords) return;

    e.preventDefault();
    canvas.setPointerCapture(e.pointerId);

    const slot = getAvailableSlot();
    if (slot === -1) return; // CarPlay supports up to 2 fingers

    activeContacts.set(e.pointerId, {
      slot: slot,
      x: coords.x,
      y: coords.y,
      visualX: coords.visualX,
      visualY: coords.visualY,
      down: true
    });

    updateTouchFeedback(slot, coords.visualX, coords.visualY, true);
    dispatchTouchContacts();
  });

  canvas.addEventListener('pointermove', (e) => {
    const contact = activeContacts.get(e.pointerId);
    if (!contact) return;

    const coords = getAccurateTouchCoordinates(e);
    if (coords) {
      contact.x = coords.x;
      contact.y = coords.y;
      contact.visualX = coords.visualX;
      contact.visualY = coords.visualY;
    } else {
      contact.visualX = e.clientX;
      contact.visualY = e.clientY;
    }

    updateTouchFeedback(contact.slot, contact.visualX, contact.visualY, true);
    scheduleSendTouchReport();
  });

  function handlePointerRelease(e) {
    const contact = activeContacts.get(e.pointerId);
    if (!contact) return;

    contact.down = false;
    updateTouchFeedback(contact.slot, contact.visualX, contact.visualY, false);

    // Send the lifted state (down: false) to CarPlay
    dispatchTouchContacts();

    activeContacts.delete(e.pointerId);

    if (activeContacts.size === 0) {
      setTimeout(() => {
        if (activeContacts.size === 0) {
          if (wsConnected && ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'touch', contacts: [] }));
          }
        }
      }, 10);
    }
  }

  canvas.addEventListener('pointerup', handlePointerRelease);
  canvas.addEventListener('pointercancel', handlePointerRelease);

  // Quick Action Buttons
  async function triggerAction(actionName) {
    if (wsConnected && ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'action', action: actionName }));
      return;
    }
    try {
      await fetch('/api/action', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: actionName })
      });
    } catch (e) {
      console.error('Action error:', e);
    }
  }

  document.getElementById('btnHome').addEventListener('click', () => triggerAction('home'));
  document.getElementById('btnPlayPause').addEventListener('click', () => triggerAction('play_pause'));
  document.getElementById('btnPrev').addEventListener('click', () => triggerAction('prev'));
  document.getElementById('btnNext').addEventListener('click', () => triggerAction('next'));

  // ----------------------------------------------------
  // Ultra Low-Latency Dedicated Web Audio Stream Player
  // ----------------------------------------------------
  class WebAudioStreamPlayer {
    constructor() {
      this.audioCtx = null;
      this.nextStartTime = 0;
      this.running = false;
      this.audioWs = null;
      this.controller = null;
    }

    async start() {
      if (this.running && this.audioCtx && this.audioCtx.state === 'running') return;
      this.running = true;
      try {
        const AudioCtxClass = window.AudioContext || window.webkitAudioContext;
        if (!AudioCtxClass) {
          throw new Error('当前浏览器不支持 Web Audio API');
        }
        if (!this.audioCtx) {
          this.audioCtx = new AudioCtxClass({ latencyHint: 'interactive' });
        }
        if (this.audioCtx.state === 'suspended') {
          await this.audioCtx.resume();
        }
        this.nextStartTime = this.audioCtx.currentTime + 0.015;

        // Establish dedicated zero-contention audio WebSocket connection
        this.connectAudioWs();

        // If WebSocket is not yet connected, fallback to fetch pull
        if (!wsConnected && !this.controller) {
          this.pullAudioStream();
        }
      } catch (e) {
        if (!this.audioCtx || this.audioCtx.state === 'suspended') {
          this.running = false;
        }
        throw e;
      }
    }

    connectAudioWs() {
      if (this.audioWs && (this.audioWs.readyState === WebSocket.OPEN || this.audioWs.readyState === WebSocket.CONNECTING)) {
        return;
      }
      try {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        const url = proto + '//' + location.host + '/audio_ws';
        const ws = new WebSocket(url);
        ws.binaryType = 'arraybuffer';
        ws.onopen = () => {
          sendRemoteLog('[WebAudio] 独立音频专用 WebSocket 通道已建立 (零视频挤占与抖动)！');
        };
        ws.onmessage = (event) => {
          if (event.data instanceof ArrayBuffer) {
            const buf = event.data;
            if (buf.byteLength >= 4) {
              const u8 = new Uint8Array(buf);
              const channel = u8[0];
              const flag = u8[1];
              if (channel === 0x00) {
                const rateCode = flag & 0x07;
                let sRate = 44100;
                if (rateCode === 1) sRate = 16000;
                else if (rateCode === 2) sRate = 24000;
                else if (rateCode === 3) sRate = 48000;
                const ch = (flag & 0x08) !== 0 ? 1 : 2;
                this.feedPcmChunk(buf.slice(4), sRate, ch);
              }
            }
          }
        };
        ws.onerror = () => {};
        ws.onclose = () => {
          this.audioWs = null;
          if (this.running) {
            setTimeout(() => this.connectAudioWs(), 2000);
          }
        };
        this.audioWs = ws;
      } catch (_) {}
    }

    isDedicatedWsActive() {
      return this.audioWs && this.audioWs.readyState === WebSocket.OPEN;
    }

    feedPcmChunk(arrayBuffer, sampleRate = 44100, channels = 2) {
      if (!this.running) return;
      if (!this.audioCtx) {
        const AudioCtxClass = window.AudioContext || window.webkitAudioContext;
        if (AudioCtxClass) {
          try {
            this.audioCtx = new AudioCtxClass({ latencyHint: 'interactive' });
          } catch (_) {}
        }
      }
      if (!this.audioCtx) return;
      if (this.audioCtx.state === 'suspended') {
        this.audioCtx.resume().catch(() => {});
        return;
      }

      const bytesPerSample = 2; // 16-bit PCM
      const bytesPerFrame = channels * bytesPerSample;
      const sampleCount = Math.floor(arrayBuffer.byteLength / bytesPerFrame);
      if (sampleCount <= 0) return;

      const audioBuffer = this.audioCtx.createBuffer(channels, sampleCount, sampleRate);
      const left = audioBuffer.getChannelData(0);
      const right = channels > 1 ? audioBuffer.getChannelData(1) : left;

      const view = new DataView(arrayBuffer);
      for (let i = 0; i < sampleCount; i++) {
        const off = i * bytesPerFrame;
        left[i] = view.getInt16(off, true) / 32768.0;
        if (channels > 1) {
          right[i] = view.getInt16(off + 2, true) / 32768.0;
        }
      }

      const source = this.audioCtx.createBufferSource();
      source.buffer = audioBuffer;
      source.connect(this.audioCtx.destination);

      const now = this.audioCtx.currentTime;
      // Seamless Jitter-Free Audio Scheduling:
      let startTime = this.nextStartTime;
      if (startTime < now) {
        // Underflow: Resume IMMEDIATELY without inserting massive dead silence!
        startTime = now + 0.003;
      } else if (startTime - now > 0.350) {
        // Drifted beyond 350ms, gently catch up
        startTime = now + 0.040;
      }
      source.start(startTime);
      this.nextStartTime = startTime + audioBuffer.duration;
    }

    async pullAudioStream() {
      try {
        this.controller = new AbortController();
        const resp = await fetch('/audio', { signal: this.controller.signal });
        if (!resp.body) return;
        const reader = resp.body.getReader();
        let remainder = null;

        while (this.running) {
          const { done, value } = await reader.read();
          if (done) break;
          if (!value || value.length === 0) continue;

          let data = value;
          if (remainder) {
            const merged = new Uint8Array(remainder.length + data.length);
            merged.set(remainder, 0);
            merged.set(data, remainder.length);
            data = merged;
            remainder = null;
          }

          const frameSize = 4; // 16-bit stereo = 4 bytes
          const validLen = data.length - (data.length % frameSize);
          if (validLen === 0) {
            remainder = data;
            continue;
          }
          if (validLen < data.length) {
            remainder = data.slice(validLen);
          }

          this.feedPcmChunk(data.buffer.slice(data.byteOffset, data.byteOffset + validLen), 44100, 2);
        }
      } catch (e) {
        if (e.name !== 'AbortError') {
          console.warn('Audio stream read ended:', e);
        }
      }
    }

    stop() {
      this.running = false;
      if (this.audioWs) {
        try { this.audioWs.close(); } catch (_) {}
        this.audioWs = null;
      }
      if (this.controller) {
        try { this.controller.abort(); } catch (_) {}
        this.controller = null;
      }
      if (this.audioCtx) {
        try { this.audioCtx.close(); } catch (_) {}
        this.audioCtx = null;
      }
    }
  }

  audioPlayer = new WebAudioStreamPlayer();
  const btnAudio = document.getElementById('btnAudio');
  const labelAudio = document.getElementById('labelAudio');
  let audioActive = false;

  function setAudioActiveUi(active) {
    audioActive = active;
    if (active) {
      btnAudio.classList.add('active-audio');
      labelAudio.textContent = '网页声音: 播放中';
    } else {
      btnAudio.classList.remove('active-audio');
      labelAudio.textContent = '网页声音: 已静音';
    }
  }

  const unlockAudio = async () => {
    if (audioActive) return;
    try {
      await audioPlayer.start();
      if (audioPlayer.audioCtx && audioPlayer.audioCtx.state === 'running') {
        setAudioActiveUi(true);
      }
    } catch (_) {}
  };

  // 1. 尝试无感自启（部分浏览器支持直接播放）
  unlockAudio();
  window.addEventListener('DOMContentLoaded', unlockAudio);
  window.addEventListener('load', unlockAudio);

  // 2. 捕获阶段全域监听所有手势与输入，只要屏幕有任何触控/点击/按键，立即自动激活出声，完全无需手动点喇叭
  ['pointerdown', 'mousedown', 'touchstart', 'touchend', 'keydown', 'click'].forEach(evt => {
    window.addEventListener(evt, unlockAudio, { capture: true, passive: true });
  });

  async function toggleAudio() {
    if (audioActive) {
      audioPlayer.stop();
      setAudioActiveUi(false);
    } else {
      try {
        await audioPlayer.start();
        setAudioActiveUi(true);
      } catch (e) {
        console.warn('Audio start failed:', e);
      }
    }
  }
  btnAudio.addEventListener('click', (e) => {
    e.stopPropagation();
    toggleAudio();
  });

  // ----------------------------------------------------
  // Web Microphone Input Streaming (Direct WS / HTTP)
  // ----------------------------------------------------
  const btnMic = document.getElementById('btnMic');
  const labelMic = document.getElementById('labelMic');
  let micAudioContext = null;
  let micMediaStream = null;
  let micProcessor = null;
  let isMicActive = false;

  async function getCompatibleUserMedia() {
    if (navigator.mediaDevices && navigator.mediaDevices.getUserMedia) {
      return await navigator.mediaDevices.getUserMedia({
        audio: {
          sampleRate: 16000,
          channelCount: 1,
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true
        }
      });
    }

    const legacyGUM = navigator.getUserMedia ||
                      navigator.webkitGetUserMedia ||
                      navigator.mozGetUserMedia ||
                      navigator.msGetUserMedia;

    if (legacyGUM) {
      return new Promise((resolve, reject) => {
        legacyGUM.call(navigator, { audio: true }, resolve, reject);
      });
    }

    if (window.isSecureContext === false) {
      throw new Error('浏览器安全策略限制：在非 HTTPS 下，麦克风权限仅允许在 http://127.0.0.1:8088 或 http://localhost:8088 下使用。请通过 127.0.0.1 访问本页面！');
    }
    throw new Error('当前浏览器环境不支持麦克风输入');
  }

  async function startWebMic() {
    try {
      const stream = await getCompatibleUserMedia();
      micMediaStream = stream;
      const AudioCtxClass = window.AudioContext || window.webkitAudioContext;
      micAudioContext = new AudioCtxClass({ sampleRate: 16000 });
      const source = micAudioContext.createMediaStreamSource(stream);
      // Buffer size 2048 at 16kHz is ~128ms
      micProcessor = micAudioContext.createScriptProcessor(2048, 1, 1);
      micProcessor.onaudioprocess = (e) => {
        if (!isMicActive) return;
        const inputData = e.inputBuffer.getChannelData(0);
        const pcm16 = new Int16Array(inputData.length);
        for (let i = 0; i < inputData.length; i++) {
          let s = Math.max(-1, Math.min(1, inputData[i]));
          pcm16[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
        }

        // Fast binary send via WebSocket
        if (wsConnected && ws && ws.readyState === WebSocket.OPEN) {
          ws.send(pcm16.buffer);
        } else {
          fetch('/api/mic', {
            method: 'POST',
            headers: { 'Content-Type': 'application/octet-stream' },
            body: pcm16.buffer
          }).catch(() => {});
        }
      };
      source.connect(micProcessor);
      micProcessor.connect(micAudioContext.destination);

      isMicActive = true;
      btnMic.classList.add('active-mic');
      labelMic.textContent = '麦克风: 监听中';
    } catch (e) {
      alert('无法开启网页麦克风: ' + e.message);
    }
  }

  function stopWebMic() {
    isMicActive = false;
    if (micProcessor) {
      try { micProcessor.disconnect(); } catch (_) {}
      micProcessor = null;
    }
    if (micMediaStream) {
      micMediaStream.getTracks().forEach(t => t.stop());
      micMediaStream = null;
    }
    if (micAudioContext) {
      try { micAudioContext.close(); } catch (_) {}
      micAudioContext = null;
    }
    btnMic.classList.remove('active-mic');
    labelMic.textContent = '网页麦克风';
  }

  btnMic.addEventListener('click', () => {
    if (isMicActive) stopWebMic(); else startWebMic();
  });

  document.getElementById('btnSiri').addEventListener('click', async () => {
    await triggerAction('siri');
    if (!isMicActive) {
      startWebMic();
    }
  });

  window.addEventListener('beforeunload', () => {
    stopWebMic();
    audioPlayer.stop();
  });

  // ----------------------------------------------------
  // Resolution Adaptation
  // ----------------------------------------------------
  async function setResolution(w, h) {
    if (wsConnected && ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resolution', width: w, height: h }));
      tagResolution.textContent = w + ' × ' + h;
      aspectHint.style.display = 'none';
      resPopover.classList.remove('show');
      btnToggleRes.classList.remove('active-pop');
      return;
    }
    try {
      const resp = await fetch('/api/resolution', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ width: w, height: h })
      });
      if (resp.ok) {
        tagResolution.textContent = w + ' × ' + h;
        aspectHint.style.display = 'none';
        resPopover.classList.remove('show');
        btnToggleRes.classList.remove('active-pop');
      }
    } catch (e) {
      alert('分辨率切换失败: ' + e.message);
    }
  }

  function computeAutoFitDimensions() {
    const rawW = window.innerWidth;
    const rawH = window.innerHeight;
    const aspect = (rawW && rawH) ? (rawW / rawH) : (16 / 9);

    // Guarantee pristine HD clarity: Minimum 1280x720, default 1920x1080 baseline for flawless text
    let targetW = 1920;
    let targetH = Math.round(1920 / aspect);

    if (targetH > 1080) {
      targetH = 1080;
      targetW = Math.round(1080 * aspect);
    }

    targetW = Math.max(1280, Math.min(2560, (targetW + 1) & ~1));
    targetH = Math.max(720, Math.min(1440, (targetH + 1) & ~1));

    return { width: targetW, height: targetH };
  }

  document.getElementById('btnQuickAdapt').addEventListener('click', () => {
    const fit = computeAutoFitDimensions();
    setResolution(fit.width, fit.height);
  });

  btnToggleRes.addEventListener('click', () => {
    const isOpen = resPopover.classList.contains('show');
    if (isOpen) {
      resPopover.classList.remove('show');
      btnToggleRes.classList.remove('active-pop');
    } else {
      const fit = computeAutoFitDimensions();
      document.getElementById('autoFitDimensions').textContent = fit.width + '×' + fit.height;
      resPopover.classList.add('show');
      btnToggleRes.classList.add('active-pop');
    }
  });

  document.getElementById('btnClosePopover').addEventListener('click', () => {
    resPopover.classList.remove('show');
    btnToggleRes.classList.remove('active-pop');
  });

  document.querySelectorAll('.res-item').forEach(item => {
    item.addEventListener('click', () => {
      const w = item.getAttribute('data-w');
      const h = item.getAttribute('data-h');
      if (w === 'auto') {
        const fit = computeAutoFitDimensions();
        setResolution(fit.width, fit.height);
      } else if (w === 'native') {
        setResolution(1080, 2228);
      } else {
        setResolution(parseInt(w, 10), parseInt(h, 10));
      }
    });
  });

  // ----------------------------------------------------
  // Fluidity & Quality Switcher (Defaults to 90% Visual Lossless Champion)
  // ----------------------------------------------------
  let currentQuality = 90;
  const qualityBtn = document.getElementById('btnQuality');
  const qualityLabel = document.getElementById('qualityLabel');
  qualityBtn.addEventListener('click', async () => {
    if (currentQuality === 90) {
      currentQuality = 75;
      qualityLabel.textContent = '画质: 极速流畅 (75% / 55FPS)';
    } else if (currentQuality === 75) {
      currentQuality = 95;
      qualityLabel.textContent = '画质: 极限保真 (95% 无损)';
    } else {
      currentQuality = 90;
      qualityLabel.textContent = '画质: 原画超清 (90% 推荐)';
    }
    await triggerAction('quality_' + currentQuality);
  });

  // Snapshot
  // High-Resolution Snapshot Capture (supports pixel-perfect Canvas capture)
  document.getElementById('btnSnapshot').addEventListener('click', () => {
    if (hasReceivedVideoFrame && videoCanvas && videoCanvas.width > 0) {
      const a = document.createElement('a');
      a.href = videoCanvas.toDataURL('image/jpeg', 0.98);
      a.download = 'carplay_hd_' + Date.now() + '.jpg';
      a.click();
    } else {
      window.open('/snapshot?t=' + Date.now(), '_blank');
    }
  });

  // Fullscreen
  const btnFullscreen = document.getElementById('btnFullscreen');
  btnFullscreen.addEventListener('click', () => {
    if (!document.fullscreenElement) {
      document.documentElement.requestFullscreen().catch(() => {});
    } else {
      document.exitFullscreen().catch(() => {});
    }
  });
</script>
</body>
</html>
""".trimIndent()
}
