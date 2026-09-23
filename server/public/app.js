// ==========================================================================
// OME // Institutional Trading Terminal Controller & Dual-Chart Engine (v2.0)
// ==========================================================================

const SYMBOLS = ['AAPL', 'TSLA', 'BTC-USD'];

function emptyStats() {
  return { high: null, low: null, volume: 0, tradesCount: 0, openPrice: null, lastPrice: null };
}

const state = {
  activeSymbol: 'AAPL',
  activeChartTab: 'price', // 'price' | 'depth'
  side: 0, // 0 = Buy, 1 = Sell
  type: 0, // 0 = Limit, 1 = Market
  myOrders: [],
  ws: null,
  audioEnabled: true,
  botRunning: false, // synced from /simulator/status on load
  // Session statistics, built only from trades received in this browser session.
  statsBySymbol: Object.fromEntries(SYMBOLS.map(s => [s, emptyStats()])),
  recentTrades: Object.fromEntries(SYMBOLS.map(s => [s, []])),
  // Maker fills seen over WS for orders not yet in myOrders (POST response still in flight).
  earlyMakerFills: new Map(),
  pendingSubmissions: 0,
  bookRequestId: 0,
  lastBookBySymbol: Object.fromEntries(SYMBOLS.map(s => [s, { bids: [], asks: [] }])),
  hoverPriceChart: null,
  hoverDepthChart: null,
};

const REFERENCE_PRICES = {
  'AAPL': '150.00',
  'TSLA': '240.00',
  'BTC-USD': '64000.00',
};

// DOM Elements
const symbolTabs = document.getElementById('symbolTabs');
const activeSymbolTag = document.getElementById('activeSymbolTag');
const btnSideBuy = document.getElementById('btnSideBuy');
const btnSideSell = document.getElementById('btnSideSell');
const btnTypeLimit = document.getElementById('btnTypeLimit');
const btnTypeMarket = document.getElementById('btnTypeMarket');
const priceGroup = document.getElementById('priceGroup');
const inputPrice = document.getElementById('inputPrice');
const inputAmount = document.getElementById('inputAmount');
const orderTotal = document.getElementById('orderTotal');
const btnSubmitOrder = document.getElementById('btnSubmitOrder');
const asksContainer = document.getElementById('asksContainer');
const bidsContainer = document.getElementById('bidsContainer');
const spreadValue = document.getElementById('spreadValue');
const spreadBps = document.getElementById('spreadBps');
const lastTradedPrice = document.getElementById('lastTradedPrice');
const tradesStream = document.getElementById('tradesStream');
const ordersTableBody = document.getElementById('ordersTableBody');
const openOrdersCount = document.getElementById('openOrdersCount');
const wsStatus = document.getElementById('wsStatus');

// Chart Elements
const tabChartPrice = document.getElementById('tabChartPrice');
const tabChartDepth = document.getElementById('tabChartDepth');
const priceCanvas = document.getElementById('priceChartCanvas');
const depthCanvas = document.getElementById('depthChartCanvas');
const ohlcOpen = document.getElementById('ohlcOpen');
const ohlcHigh = document.getElementById('ohlcHigh');
const ohlcLow = document.getElementById('ohlcLow');
const ohlcClose = document.getElementById('ohlcClose');
const ohlcVol = document.getElementById('ohlcVol');

// Bot & Audio Elements
const btnToggleBot = document.getElementById('btnToggleBot');
const botLabel = document.getElementById('botLabel');
const btnToggleAudio = document.getElementById('btnToggleAudio');

// Ticker DOM Elements
const tickerLastPrice = document.getElementById('tickerLastPrice');
const tickerChange = document.getElementById('tickerChange');
const tickerHigh = document.getElementById('tickerHigh');
const tickerLow = document.getElementById('tickerLow');
const tickerVolume = document.getElementById('tickerVolume');
const tickerTradesCount = document.getElementById('tickerTradesCount');

// --------------------------------------------------------------------------
// 1. Candlestick Data Store & Live Time-Bucketed Updates
// --------------------------------------------------------------------------
const CANDLE_PERIOD_MS = 15000; // 15-second candles
const MAX_CANDLES = 80;
const candles = Object.fromEntries(SYMBOLS.map(s => [s, []]));

function updateCandleOnTrade(symbol, price, amount, tsMs) {
  const list = candles[symbol];
  if (!list) return;

  const bucket = Math.floor(tsMs / CANDLE_PERIOD_MS) * CANDLE_PERIOD_MS;
  const last = list[list.length - 1];

  if (!last || bucket > last.time) {
    // Fill quiet periods with flat candles so the x-axis stays linear in time.
    if (last) {
      const firstGap = Math.max(last.time + CANDLE_PERIOD_MS, bucket - MAX_CANDLES * CANDLE_PERIOD_MS);
      for (let t = firstGap; t < bucket; t += CANDLE_PERIOD_MS) {
        list.push({ time: t, open: last.close, high: last.close, low: last.close, close: last.close, volume: 0 });
      }
    }
    list.push({ time: bucket, open: price, high: price, low: price, close: price, volume: amount });
    while (list.length > MAX_CANDLES) list.shift();
  } else {
    const c = list.find(x => x.time === bucket);
    if (!c) return; // older than the retained window
    c.high = Math.max(c.high, price);
    c.low = Math.min(c.low, price);
    c.volume += amount;
    if (c === last) c.close = price;
  }

  if (symbol === state.activeSymbol) {
    updateOHLCHeader();
    if (state.activeChartTab === 'price') renderPriceChart();
  }
}

function updateOHLCHeader(customCandle = null) {
  const list = candles[state.activeSymbol];
  const c = customCandle || (list && list[list.length - 1]);
  if (!c) {
    [ohlcOpen, ohlcHigh, ohlcLow, ohlcClose, ohlcVol].forEach(el => { if (el) el.textContent = '—'; });
    if (ohlcClose) ohlcClose.className = '';
    return;
  }

  ohlcOpen.textContent = c.open.toFixed(2);
  ohlcHigh.textContent = c.high.toFixed(2);
  ohlcLow.textContent = c.low.toFixed(2);
  ohlcClose.textContent = c.close.toFixed(2);
  ohlcClose.className = c.close >= c.open ? 'text-green' : 'text-red';

  if (ohlcVol) {
    ohlcVol.textContent = c.volume >= 1000 ? (c.volume / 1000).toFixed(1) + 'K' : c.volume.toString();
  }
}

// --------------------------------------------------------------------------
// 2. High-DPI Canvas Helper
// --------------------------------------------------------------------------
function setupCanvasDPI(canvas) {
  const rect = canvas.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  const w = Math.floor(rect.width);
  const h = Math.floor(rect.height);

  if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
    canvas.width = w * dpr;
    canvas.height = h * dpr;
  }

  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { ctx, width: w, height: h };
}

// --------------------------------------------------------------------------
// 3. Japanese Candlestick & Volume Chart Engine
// --------------------------------------------------------------------------
function renderPriceChart() {
  if (!priceCanvas || state.activeChartTab !== 'price') return;
  const { ctx, width, height } = setupCanvasDPI(priceCanvas);

  ctx.clearRect(0, 0, width, height);

  const list = candles[state.activeSymbol];
  if (!list || list.length === 0) {
    ctx.fillStyle = '#475569';
    ctx.font = '11px "Plus Jakarta Sans", sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Waiting for trades...', width / 2, height / 2);
    return;
  }

  const rightMargin = 70;
  const bottomMargin = 22;
  const chartW = width - rightMargin;
  const chartH = height - bottomMargin;

  // Price & Volume domain calculations
  let minPrice = Infinity;
  let maxPrice = -Infinity;
  let maxVol = 1;

  list.forEach(c => {
    if (c.low < minPrice) minPrice = c.low;
    if (c.high > maxPrice) maxPrice = c.high;
    if (c.volume > maxVol) maxVol = c.volume;
  });

  // Add 8% padding to price bounds
  const pricePadding = (maxPrice - minPrice) * 0.08 || 1;
  const pMin = minPrice - pricePadding;
  const pMax = maxPrice + pricePadding;
  const pRange = pMax - pMin;

  const volH = chartH * 0.22;
  const candleAreaH = chartH * 0.78;

  function priceToY(p) {
    return candleAreaH - ((p - pMin) / pRange) * candleAreaH;
  }

  function yToPrice(y) {
    return pMax - (y / candleAreaH) * pRange;
  }

  // 1. Draw Subtle Grid Lines & Price Labels
  const gridCount = 5;
  ctx.lineWidth = 1;
  ctx.font = '10px "JetBrains Mono", monospace';

  for (let i = 0; i <= gridCount; i++) {
    const y = (candleAreaH / gridCount) * i;
    const priceAtY = yToPrice(y);

    // Horizontal grid line
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.035)';
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(chartW, y);
    ctx.stroke();

    // Price label on right axis
    ctx.fillStyle = '#64748b';
    ctx.textAlign = 'left';
    ctx.fillText(`$${priceAtY.toFixed(2)}`, chartW + 8, y + 3);
  }

  // Vertical Separator for Axis
  ctx.setLineDash([]);
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.06)';
  ctx.beginPath();
  ctx.moveTo(chartW, 0);
  ctx.lineTo(chartW, height);
  ctx.stroke();

  // Bottom Timeline Separator
  ctx.beginPath();
  ctx.moveTo(0, chartH);
  ctx.lineTo(width, chartH);
  ctx.stroke();

  // 2. Draw Candlesticks & Volume Bars
  const totalSlots = Math.max(list.length, 40);
  const slotW = chartW / totalSlots; // fixed minimum slot count keeps candle width sane
  const candleW = Math.max(3, slotW * 0.68);
  const startSlot = totalSlots - list.length; // right-align candles

  list.forEach((c, idx) => {
    const x = (startSlot + idx) * slotW + slotW / 2;
    const isBull = c.close >= c.open;
    const color = isBull ? '#00f090' : '#ff3358';
    const volColor = isBull ? 'rgba(0, 240, 144, 0.22)' : 'rgba(255, 51, 88, 0.22)';

    // Volume Bar
    const vBarH = Math.max(1, (c.volume / maxVol) * volH);
    const vY = chartH - vBarH;
    ctx.fillStyle = volColor;
    ctx.fillRect(x - candleW / 2, vY, candleW, vBarH);

    // Candle Wick
    const yHigh = priceToY(c.high);
    const yLow = priceToY(c.low);
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.2;
    ctx.beginPath();
    ctx.moveTo(x, yHigh);
    ctx.lineTo(x, yLow);
    ctx.stroke();

    // Candle Body
    const yOpen = priceToY(c.open);
    const yClose = priceToY(c.close);
    const bodyTop = Math.min(yOpen, yClose);
    const bodyH = Math.max(Math.abs(yClose - yOpen), 1.5);

    ctx.fillStyle = color;
    ctx.fillRect(x - candleW / 2, bodyTop, candleW, bodyH);

    // Bottom time label every 8 candles
    if (idx % 8 === 0) {
      const timeStr = new Date(c.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
      ctx.fillStyle = '#64748b';
      ctx.font = '9px "JetBrains Mono", monospace';
      ctx.textAlign = 'center';
      ctx.fillText(timeStr, x, chartH + 14);
    }
  });

  // 3. Live Price Line & Glowing Badge
  const lastCandle = list[list.length - 1];
  const lastY = priceToY(lastCandle.close);
  const liveColor = lastCandle.close >= lastCandle.open ? '#00f090' : '#ff3358';

  ctx.strokeStyle = liveColor;
  ctx.setLineDash([4, 4]);
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(0, lastY);
  ctx.lineTo(chartW, lastY);
  ctx.stroke();
  ctx.setLineDash([]);

  // Live Price Badge on Axis
  const badgeW = 62;
  const badgeH = 18;
  ctx.fillStyle = liveColor;
  ctx.beginPath();
  ctx.roundRect(chartW + 4, lastY - badgeH / 2, badgeW, badgeH, 3);
  ctx.fill();

  ctx.fillStyle = '#000000';
  ctx.font = 'bold 10px "JetBrains Mono", monospace';
  ctx.textAlign = 'center';
  ctx.fillText(`$${lastCandle.close.toFixed(2)}`, chartW + 4 + badgeW / 2, lastY + 3.5);

  // 4. Interactive Crosshair & Hover Tooltip
  if (state.hoverPriceChart && state.hoverPriceChart.x <= chartW && state.hoverPriceChart.y <= chartH) {
    const { x, y } = state.hoverPriceChart;
    const hoveredSlot = Math.floor(x / slotW);
    const hoveredIdx = hoveredSlot - startSlot;

    if (hoveredIdx >= 0 && hoveredIdx < list.length) {
      const c = list[hoveredIdx];
      updateOHLCHeader(c);

      // Draw Crosshair Lines
      ctx.strokeStyle = 'rgba(255, 255, 255, 0.25)';
      ctx.lineWidth = 1;
      ctx.setLineDash([2, 2]);

      // Vertical line
      ctx.beginPath();
      ctx.moveTo(x, 0);
      ctx.lineTo(x, chartH);
      ctx.stroke();

      // Horizontal line
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(chartW, y);
      ctx.stroke();
      ctx.setLineDash([]);

      // Floating Price Badge on Right Axis
      const hoverPrice = yToPrice(y);
      ctx.fillStyle = '#2563eb';
      ctx.fillRect(chartW, y - 9, rightMargin, 18);
      ctx.fillStyle = '#ffffff';
      ctx.font = '10px "JetBrains Mono", monospace';
      ctx.textAlign = 'left';
      ctx.fillText(`$${hoverPrice.toFixed(2)}`, chartW + 8, y + 4);

      // Floating Time Badge on Bottom Axis
      const hoverTime = new Date(c.time).toLocaleTimeString();
      const timeBoxW = 60;
      ctx.fillStyle = '#1e293b';
      ctx.fillRect(x - timeBoxW / 2, chartH, timeBoxW, bottomMargin);
      ctx.strokeStyle = 'rgba(255, 255, 255, 0.1)';
      ctx.strokeRect(x - timeBoxW / 2, chartH, timeBoxW, bottomMargin);
      ctx.fillStyle = '#94a3b8';
      ctx.textAlign = 'center';
      ctx.fillText(hoverTime, x, chartH + 14);
    }
  }
}

// --------------------------------------------------------------------------
// 4. Step-Staircase Market Depth Chart Engine
// --------------------------------------------------------------------------
function drawDepthChart(bids, asks) {
  if (bids !== undefined && asks !== undefined) {
    state.lastBookBySymbol[state.activeSymbol] = { bids: bids || [], asks: asks || [] };
  }
  if (!depthCanvas || state.activeChartTab !== 'depth') return;

  const { ctx, width, height } = setupCanvasDPI(depthCanvas);
  ctx.clearRect(0, 0, width, height);

  const currentBook = state.lastBookBySymbol[state.activeSymbol] || { bids: [], asks: [] };
  const b = currentBook.bids.slice(0, 24); // best (highest) first
  const a = currentBook.asks.slice(0, 24); // best (lowest) first

  if (b.length === 0 && a.length === 0) {
    ctx.fillStyle = '#475569';
    ctx.font = '11px "Plus Jakarta Sans", sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Waiting for Order Book Depth...', width / 2, height / 2);
    return;
  }

  let running = 0;
  const cumBids = b.map(l => ({ price: l.price / 100, cum: (running += l.volume) }));
  running = 0;
  const cumAsks = a.map(l => ({ price: l.price / 100, cum: (running += l.volume) }));

  // Linear price domain covering both sides.
  const prices = cumBids.concat(cumAsks).map(l => l.price);
  let lo = Math.min(...prices);
  let hi = Math.max(...prices);
  const pad = (hi - lo) * 0.05 || Math.max(hi * 0.001, 0.01);
  lo -= pad;
  hi += pad;

  const paddingBottom = 24;
  const topPad = 14;
  const drawH = height - paddingBottom;
  const maxCum = Math.max(
    cumBids.length ? cumBids[cumBids.length - 1].cum : 0,
    cumAsks.length ? cumAsks[cumAsks.length - 1].cum : 0,
    1
  );
  const xOf = p => ((p - lo) / (hi - lo)) * width;
  const yOf = c => drawH - (c / maxCum) * (drawH - topPad);
  const xToPrice = x => lo + (x / width) * (hi - lo);
  const mid = cumBids.length && cumAsks.length ? (cumBids[0].price + cumAsks[0].price) / 2 : null;

  // 1. Grid
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.035)';
  ctx.setLineDash([3, 3]);
  ctx.lineWidth = 1;
  ctx.font = '10px "JetBrains Mono", monospace';
  ctx.fillStyle = '#64748b';
  for (let i = 1; i <= 4; i++) {
    const y = drawH - (i / 4) * (drawH - topPad);
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(width, y);
    ctx.stroke();
    ctx.textAlign = 'left';
    ctx.fillText(Math.round((i / 4) * maxCum).toLocaleString(), 8, y - 3);
  }
  ctx.setLineDash([]);

  // 2. Bids: depth at price p = total bid volume priced >= p
  if (cumBids.length) {
    ctx.beginPath();
    ctx.moveTo(xOf(cumBids[0].price), drawH);
    ctx.lineTo(xOf(cumBids[0].price), yOf(cumBids[0].cum));
    for (let i = 1; i < cumBids.length; i++) {
      const x = xOf(cumBids[i].price);
      ctx.lineTo(x, yOf(cumBids[i - 1].cum));
      ctx.lineTo(x, yOf(cumBids[i].cum));
    }
    ctx.lineTo(0, yOf(cumBids[cumBids.length - 1].cum));
    ctx.lineTo(0, drawH);
    ctx.closePath();
    const bidGrad = ctx.createLinearGradient(0, 0, 0, drawH);
    bidGrad.addColorStop(0, 'rgba(0, 240, 144, 0.28)');
    bidGrad.addColorStop(1, 'rgba(0, 240, 144, 0.01)');
    ctx.fillStyle = bidGrad;
    ctx.fill();
    ctx.strokeStyle = '#00f090';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  // 3. Asks: depth at price p = total ask volume priced <= p
  if (cumAsks.length) {
    ctx.beginPath();
    ctx.moveTo(xOf(cumAsks[0].price), drawH);
    ctx.lineTo(xOf(cumAsks[0].price), yOf(cumAsks[0].cum));
    for (let i = 1; i < cumAsks.length; i++) {
      const x = xOf(cumAsks[i].price);
      ctx.lineTo(x, yOf(cumAsks[i - 1].cum));
      ctx.lineTo(x, yOf(cumAsks[i].cum));
    }
    ctx.lineTo(width, yOf(cumAsks[cumAsks.length - 1].cum));
    ctx.lineTo(width, drawH);
    ctx.closePath();
    const askGrad = ctx.createLinearGradient(0, 0, 0, drawH);
    askGrad.addColorStop(0, 'rgba(255, 51, 88, 0.28)');
    askGrad.addColorStop(1, 'rgba(255, 51, 88, 0.01)');
    ctx.fillStyle = askGrad;
    ctx.fill();
    ctx.strokeStyle = '#ff3358';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  // 4. Mid-market line at its true price position
  if (mid !== null) {
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.2)';
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    ctx.moveTo(xOf(mid), 0);
    ctx.lineTo(xOf(mid), drawH);
    ctx.stroke();
    ctx.setLineDash([]);
  }

  // 5. Price axis: edges and mid, at their real positions
  ctx.font = '10px "JetBrains Mono", monospace';
  ctx.fillStyle = '#64748b';
  ctx.textAlign = 'left';
  ctx.fillText(`$${xToPrice(0).toFixed(2)}`, 4, height - 8);
  ctx.textAlign = 'right';
  ctx.fillText(`$${xToPrice(width).toFixed(2)}`, width - 4, height - 8);
  if (mid !== null) {
    ctx.fillStyle = '#94a3b8';
    ctx.textAlign = 'center';
    ctx.fillText(`MID $${mid.toFixed(2)}`, xOf(mid), height - 8);
  }

  // 6. Hover tooltip: cumulative depth available up to the hovered price
  if (state.hoverDepthChart && state.hoverDepthChart.y <= drawH) {
    const { x, y } = state.hoverDepthChart;
    const p = xToPrice(x);
    const onBidSide = mid !== null ? p <= mid : cumBids.length > 0;
    let text = '';
    if (onBidSide) {
      const lv = cumBids.filter(l => l.price >= p);
      if (lv.length) {
        text = `BIDS ≥ $${p.toFixed(2)} | DEPTH: ${lv[lv.length - 1].cum.toLocaleString()}`;
      } else if (cumBids.length) {
        text = `SPREAD | BEST BID: $${cumBids[0].price.toFixed(2)}`;
      }
    } else {
      const lv = cumAsks.filter(l => l.price <= p);
      if (lv.length) {
        text = `ASKS ≤ $${p.toFixed(2)} | DEPTH: ${lv[lv.length - 1].cum.toLocaleString()}`;
      } else if (cumAsks.length) {
        text = `SPREAD | BEST ASK: $${cumAsks[0].price.toFixed(2)}`;
      }
    }

    ctx.strokeStyle = 'rgba(255, 255, 255, 0.3)';
    ctx.setLineDash([2, 2]);
    ctx.beginPath();
    ctx.moveTo(x, 0);
    ctx.lineTo(x, drawH);
    ctx.stroke();
    ctx.setLineDash([]);

    if (text) {
      const txtW = ctx.measureText(text).width + 16;
      const boxX = Math.max(10, Math.min(width - txtW - 10, x - txtW / 2));
      const boxY = Math.max(10, y - 28);
      ctx.fillStyle = '#0f172a';
      ctx.strokeStyle = onBidSide ? '#00f090' : '#ff3358';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.roundRect(boxX, boxY, txtW, 20, 4);
      ctx.fill();
      ctx.stroke();
      ctx.fillStyle = '#f8fafc';
      ctx.textAlign = 'center';
      ctx.fillText(text, boxX + txtW / 2, boxY + 13.5);
    }
  }
}

// --------------------------------------------------------------------------
// 5. Chart Interaction Listeners (Hover & Tab Switching)
// --------------------------------------------------------------------------
priceCanvas.addEventListener('mousemove', (e) => {
  const rect = priceCanvas.getBoundingClientRect();
  state.hoverPriceChart = {
    x: e.clientX - rect.left,
    y: e.clientY - rect.top,
  };
  renderPriceChart();
});

priceCanvas.addEventListener('mouseleave', () => {
  state.hoverPriceChart = null;
  updateOHLCHeader();
  renderPriceChart();
});

depthCanvas.addEventListener('mousemove', (e) => {
  const rect = depthCanvas.getBoundingClientRect();
  state.hoverDepthChart = {
    x: e.clientX - rect.left,
    y: e.clientY - rect.top,
  };
  drawDepthChart();
});

depthCanvas.addEventListener('mouseleave', () => {
  state.hoverDepthChart = null;
  drawDepthChart();
});

tabChartPrice.addEventListener('click', () => {
  state.activeChartTab = 'price';
  tabChartPrice.classList.add('active');
  tabChartDepth.classList.remove('active');
  priceCanvas.style.display = 'block';
  depthCanvas.style.display = 'none';
  renderPriceChart();
});

tabChartDepth.addEventListener('click', () => {
  state.activeChartTab = 'depth';
  tabChartDepth.classList.add('active');
  tabChartPrice.classList.remove('active');
  priceCanvas.style.display = 'none';
  depthCanvas.style.display = 'block';
  drawDepthChart();
});

window.addEventListener('resize', () => {
  if (state.activeChartTab === 'price') {
    renderPriceChart();
  } else {
    drawDepthChart();
  }
});

// --------------------------------------------------------------------------
// 6. Web Audio Synthesizer (Trade Execution Chime)
// --------------------------------------------------------------------------
let audioCtx = null;

function ensureAudioContext() {
  try {
    if (!audioCtx) {
      audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    }
    if (audioCtx && audioCtx.state === 'suspended') {
      audioCtx.resume();
    }
  } catch (e) {
    // AudioContext may be restricted before user gesture
  }
}

// Browser autoplay policy requires AudioContext creation/resumption within a direct user gesture.
['pointerdown', 'keydown'].forEach(evt => {
  document.addEventListener(evt, () => {
    if (state.audioEnabled) ensureAudioContext();
  }, { once: true });
});

function playTradeSound() {
  if (!state.audioEnabled) return;
  try {
    ensureAudioContext();
    if (!audioCtx || audioCtx.state !== 'running') return;

    const osc = audioCtx.createOscillator();
    const gain = audioCtx.createGain();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(880, audioCtx.currentTime);
    osc.frequency.exponentialRampToValueAtTime(1760, audioCtx.currentTime + 0.08);

    gain.gain.setValueAtTime(0.04, audioCtx.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, audioCtx.currentTime + 0.12);

    osc.connect(gain);
    gain.connect(audioCtx.destination);
    osc.start();
    osc.stop(audioCtx.currentTime + 0.12);
  } catch (e) {
    // Audio context may be restricted before user gesture
  }
}

// --------------------------------------------------------------------------
// 7. WebSocket Connection & Auto-Reconnect
// --------------------------------------------------------------------------
function connectWebSocket() {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${window.location.host}/ws`;

  state.ws = new WebSocket(wsUrl);

  state.ws.onopen = () => {
    wsStatus.innerHTML = '<span class="dot-pulse"></span><span class="telemetry-val">WS LIVE</span>';
    fetchOrderBook();
    syncBotStatus();
    SYMBOLS.forEach(sym => hydrateTradeHistory(sym));
  };

  state.ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      handleServerEvent(msg);
    } catch (e) {
      console.error('WS parse error:', e);
    }
  };

  state.ws.onclose = () => {
    wsStatus.innerHTML = '<span class="dot" style="background:#ff3358"></span><span class="telemetry-val">RECONNECTING...</span>';
    setTimeout(connectWebSocket, 2000);
  };
}

function handleServerEvent(msg) {
  if (msg.type === 'trades') {
    updateOpenOrdersAfterMatch(msg.data);
    // Record every trade for its own symbol, not just the one on screen.
    msg.data.forEach(trade => recordTrade(msg.symbol, trade));
    if (msg.symbol === state.activeSymbol) {
      renderTradesStream();
      renderTickerForActiveSymbol();
      scheduleBookRefresh();
      playTradeSound();
    }
  } else if (msg.type === 'book_update') {
    if (msg.symbol === state.activeSymbol) scheduleBookRefresh();
  } else if (msg.type === 'order_cancelled') {
    state.myOrders = state.myOrders.filter(o => o.id !== msg.order_id);
    renderOpenOrders();
    if (msg.symbol === state.activeSymbol) scheduleBookRefresh();
  }
}

let bookRefreshTimer = null;
function scheduleBookRefresh() {
  if (bookRefreshTimer) return;
  bookRefreshTimer = setTimeout(() => {
    bookRefreshTimer = null;
    fetchOrderBook();
  }, 100);
}

// --------------------------------------------------------------------------
// 8. Fetch & Render L2 Order Book
// --------------------------------------------------------------------------
async function fetchOrderBook() {
  const symbol = state.activeSymbol;
  const requestId = ++state.bookRequestId;
  try {
    const res = await fetch(`/orderbook?symbol=${encodeURIComponent(symbol)}`);
    if (!res.ok) return;
    const data = await res.json();
    state.lastBookBySymbol[symbol] = { bids: data.bids || [], asks: data.asks || [] };
    // Drop responses superseded by a newer request or a tab switch.
    if (requestId !== state.bookRequestId || data.symbol !== state.activeSymbol) return;
    renderOrderBook(data);
    drawDepthChart(data.bids || [], data.asks || []);
  } catch (err) {
    console.error('Failed to fetch order book:', err);
  }
}

function renderOrderBook(data) {
  const bids = data.bids || [];
  const asks = data.asks || [];

  let cumAsk = 0;
  const asksWithCum = asks.map(a => {
    cumAsk += a.volume;
    return { ...a, cum: cumAsk };
  });

  let cumBid = 0;
  const bidsWithCum = bids.map(b => {
    cumBid += b.volume;
    return { ...b, cum: cumBid };
  });

  const maxTotal = Math.max(cumAsk, cumBid, 1);

  // Render Asks (reversed so lowest ask is near spread)
  if (asksWithCum.length === 0) {
    asksContainer.innerHTML = '<div class="empty-state">No asks resting</div>';
  } else {
    asksContainer.innerHTML = asksWithCum.slice(0, 15).reverse().map(a => {
      const pct = Math.min((a.cum / maxTotal) * 100, 100);
      const priceFormatted = (a.price / 100).toFixed(2);
      return `
        <div class="book-row ask-row" onclick="setPrice('${priceFormatted}')">
          <div class="book-depth-bar" style="width: ${pct}%"></div>
          <span class="book-price">${priceFormatted}</span>
          <span>${a.volume.toLocaleString()}</span>
          <span>${a.cum.toLocaleString()}</span>
        </div>
      `;
    }).join('');
  }

  // Render Bids (highest bid near spread)
  if (bidsWithCum.length === 0) {
    bidsContainer.innerHTML = '<div class="empty-state">No bids resting</div>';
  } else {
    bidsContainer.innerHTML = bidsWithCum.slice(0, 15).map(b => {
      const pct = Math.min((b.cum / maxTotal) * 100, 100);
      const priceFormatted = (b.price / 100).toFixed(2);
      return `
        <div class="book-row bid-row" onclick="setPrice('${priceFormatted}')">
          <div class="book-depth-bar" style="width: ${pct}%"></div>
          <span class="book-price">${priceFormatted}</span>
          <span>${b.volume.toLocaleString()}</span>
          <span>${b.cum.toLocaleString()}</span>
        </div>
      `;
    }).join('');
  }

  // Update Spread Indicator
  if (bids.length > 0 && asks.length > 0) {
    const bestBid = bids[0].price;
    const bestAsk = asks[0].price;
    const spreadCents = bestAsk - bestBid;
    const spreadDollars = (spreadCents / 100).toFixed(2);
    const midPrice = (bestAsk + bestBid) / 2;
    const bps = ((spreadCents / midPrice) * 10000).toFixed(1);

    spreadValue.textContent = `$${spreadDollars}`;
    spreadBps.textContent = `(${bps} bps)`;
  } else {
    spreadValue.textContent = '—';
    spreadBps.textContent = '';
  }
}

window.setPrice = function(price) {
  if (state.type === 0) {
    inputPrice.value = price;
    updateTotal();
  }
};

// --------------------------------------------------------------------------
// 9. Trade Stream, Per-Symbol Stats & Ticker Updates
// --------------------------------------------------------------------------
function formatUSD(p) {
  return '$' + p.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// Returns display text + class for a percentage. Never produces "+-0.00%".
function formatPct(pct) {
  if (pct === null || !isFinite(pct)) return { text: '—', cls: 'ticker-val' };
  const rounded = Math.round(pct * 100) / 100;
  if (rounded === 0) return { text: '0.00%', cls: 'ticker-val' };
  return {
    text: `${rounded > 0 ? '+' : ''}${rounded.toFixed(2)}%`,
    cls: `ticker-val ${rounded > 0 ? 'text-green' : 'text-red'}`,
  };
}

const seenTradeKeys = new Set();

function getTradeKey(symbol, trade) {
  return `${symbol}-${trade.maker_order_id}-${trade.taker_order_id}-${trade.timestamp}-${trade.price}-${trade.amount}`;
}

// Updates stats, trade history and candles for ANY symbol; DOM only for the active one.
function recordTrade(symbol, trade) {
  const stats = state.statsBySymbol[symbol];
  if (!stats) return;

  const key = getTradeKey(symbol, trade);
  if (seenTradeKeys.has(key)) return;
  seenTradeKeys.add(key);
  if (seenTradeKeys.size > 5000) {
    const oldest = seenTradeKeys.values().next().value;
    seenTradeKeys.delete(oldest);
  }

  const price = trade.price / 100;

  if (stats.openPrice === null) stats.openPrice = price;
  stats.lastPrice = price;
  stats.high = stats.high === null ? price : Math.max(stats.high, price);
  stats.low = stats.low === null ? price : Math.min(stats.low, price);
  stats.volume += trade.amount;
  stats.tradesCount++;

  const list = state.recentTrades[symbol];
  list.unshift(trade);
  if (list.length > 50) list.pop();

  updateCandleOnTrade(symbol, price, trade.amount, trade.timestamp);

  const tabPrice = document.getElementById(`tabPrice-${symbol}`);
  if (tabPrice) tabPrice.textContent = formatUSD(price);
}

function renderTradesStream() {
  const list = state.recentTrades[state.activeSymbol] || [];
  if (list.length === 0) {
    tradesStream.innerHTML = '<div class="empty-state">Waiting for executions...</div>';
    return;
  }
  tradesStream.innerHTML = list.map(t => {
    const sideClass = t.side === 0 ? 'buy' : 'sell';
    const time = new Date(t.timestamp).toLocaleTimeString();
    return `
      <div class="trade-row">
        <span class="trade-price ${sideClass}">$${(t.price / 100).toFixed(2)}</span>
        <span>${t.amount}</span>
        <span>${time}</span>
      </div>`;
  }).join('');
}

function renderTickerForActiveSymbol() {
  const s = state.statsBySymbol[state.activeSymbol];
  const fmt = v => (v === null ? '—' : formatUSD(v));

  tickerLastPrice.textContent = fmt(s.lastPrice);
  lastTradedPrice.textContent = fmt(s.lastPrice);
  tickerHigh.textContent = fmt(s.high);
  tickerLow.textContent = fmt(s.low);
  tickerVolume.textContent = s.volume.toLocaleString();
  tickerTradesCount.textContent = s.tradesCount.toLocaleString();

  const pct = s.openPrice === null || s.lastPrice === null
    ? null
    : ((s.lastPrice - s.openPrice) / s.openPrice) * 100;
  const { text, cls } = formatPct(pct);
  tickerChange.textContent = text;
  tickerChange.className = cls;
}

// --------------------------------------------------------------------------
// 10. Order Submission & State
// --------------------------------------------------------------------------
async function submitOrder() {
  const rawAmount = inputAmount.value.trim();
  const amount = Number(rawAmount);
  if (!rawAmount || !Number.isInteger(amount) || amount <= 0) {
    alert('Please enter a valid positive integer quantity');
    return;
  }

  let price = 0;
  if (state.type === 0) {
    const rawPrice = inputPrice.value.trim();
    const numericPrice = parseFloat(rawPrice);
    if (!rawPrice || isNaN(numericPrice) || numericPrice <= 0) {
      alert('Please enter a valid limit price');
      return;
    }
    // Convert dollars to integer cents via string parsing to avoid
    // IEEE 754 floating-point rounding errors (e.g. 1.255 * 100 → 125.49...)
    const parts = rawPrice.split('.');
    const dollars = parseInt(parts[0] || '0', 10);
    const decStr = (parts[1] || '00').padEnd(2, '0').slice(0, 2);
    price = dollars * 100 + parseInt(decStr, 10);
  }

  const payload = {
    symbol: state.activeSymbol,
    side: state.side,
    type: state.type,
    price: price,
    amount: amount,
  };

  btnSubmitOrder.disabled = true;
  btnSubmitOrder.style.opacity = '0.6';
  state.pendingSubmissions++;
  try {
    const res = await fetch('/order', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    if (!res.ok) {
      const errText = await res.text();
      alert('Order rejected: ' + errText);
      return;
    }

    const data = await res.json();

    if (data.status === 'RESTING' || data.status === 'PARTIALLY_FILLED_RESTING') {
      // Subtract maker fills that arrived over WS before this response did.
      const remaining = data.remaining_amount - takeEarlyFills(data.order.id);
      if (remaining > 0) {
        state.myOrders.push({ ...data.order, amount: remaining });
        renderOpenOrders();
      }
    } else {
      takeEarlyFills(data.order.id);
    }

    scheduleBookRefresh();
  } catch (err) {
    console.error('Submit order error:', err);
  } finally {
    btnSubmitOrder.disabled = false;
    btnSubmitOrder.style.opacity = '1';
    state.pendingSubmissions = Math.max(0, state.pendingSubmissions - 1);
  }
}

// --------------------------------------------------------------------------
// 11. Open Orders Table & 1-Click Cancel
// --------------------------------------------------------------------------
function renderOpenOrders() {
  const currentSymbolOrders = state.myOrders.filter(o => o.symbol === state.activeSymbol);
  openOrdersCount.textContent = currentSymbolOrders.length;

  if (currentSymbolOrders.length === 0) {
    ordersTableBody.innerHTML = '<tr class="empty-row"><td colspan="5">No active resting orders</td></tr>';
    return;
  }

  ordersTableBody.innerHTML = currentSymbolOrders.map(o => {
    const sideBadge = o.side === 0 
      ? '<span style="color:var(--green); font-weight:700;">BUY</span>'
      : '<span style="color:var(--red); font-weight:700;">SELL</span>';
    const priceFormatted = (o.price / 100).toFixed(2);

    return `
      <tr>
        <td>#${o.id}</td>
        <td>${sideBadge}</td>
        <td>$${priceFormatted}</td>
        <td>${o.amount}</td>
        <td>
          <button class="btn-cancel" onclick="cancelOrder(${o.id}, '${o.symbol}')">CANCEL</button>
        </td>
      </tr>
    `;
  }).join('');
}

window.cancelOrder = async function(id, symbol) {
  try {
    const res = await fetch(`/order?symbol=${encodeURIComponent(symbol)}&id=${id}`, { method: 'DELETE' });
    if (res.ok || res.status === 404) {
      // 404 means it was already filled or cancelled; either way it is no longer resting.
      state.myOrders = state.myOrders.filter(o => o.id !== id);
      renderOpenOrders();
      scheduleBookRefresh();
    } else {
      alert('Cancel failed: ' + (await res.text()));
    }
  } catch (err) {
    console.error('Cancel order error:', err);
  }
};

function updateOpenOrdersAfterMatch(trades) {
  trades.forEach(t => {
    const idx = state.myOrders.findIndex(o => o.id === t.maker_order_id);
    if (idx === -1) {
      // Record early maker fills only if we have an order submission in-flight
      if (state.pendingSubmissions > 0) {
        state.earlyMakerFills.set(t.maker_order_id, (state.earlyMakerFills.get(t.maker_order_id) || 0) + t.amount);
      }
      return;
    }
    const o = state.myOrders[idx];
    const remaining = o.amount - t.amount;
    if (remaining > 0) {
      state.myOrders[idx] = { ...o, amount: remaining };
    } else {
      state.myOrders.splice(idx, 1);
    }
  });

  // Bound memory: keep only the most recent entries
  while (state.earlyMakerFills.size > 100) {
    state.earlyMakerFills.delete(state.earlyMakerFills.keys().next().value);
  }
  renderOpenOrders();
}

function takeEarlyFills(id) {
  const filled = state.earlyMakerFills.get(id) || 0;
  state.earlyMakerFills.delete(id);
  return filled;
}

// --------------------------------------------------------------------------
// 12. UI Controls & Event Listeners
// --------------------------------------------------------------------------
function updateTotal() {
  const price = parseFloat(inputPrice.value) || 0;
  const amount = parseInt(inputAmount.value, 10) || 0;
  if (state.type === 1) {
    orderTotal.textContent = 'At Market Price';
  } else {
    orderTotal.textContent = `$${(price * amount).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  }
}

btnSideBuy.addEventListener('click', () => {
  state.side = 0;
  btnSideBuy.classList.add('active');
  btnSideSell.classList.remove('active');
  btnSubmitOrder.className = 'btn-submit btn-submit-buy';
  btnSubmitOrder.textContent = `SUBMIT ${state.type === 0 ? 'LIMIT' : 'MARKET'} BUY ORDER`;
});

btnSideSell.addEventListener('click', () => {
  state.side = 1;
  btnSideSell.classList.add('active');
  btnSideBuy.classList.remove('active');
  btnSubmitOrder.className = 'btn-submit btn-submit-sell';
  btnSubmitOrder.textContent = `SUBMIT ${state.type === 0 ? 'LIMIT' : 'MARKET'} SELL ORDER`;
});

btnTypeLimit.addEventListener('click', () => {
  state.type = 0;
  btnTypeLimit.classList.add('active');
  btnTypeMarket.classList.remove('active');
  priceGroup.style.display = 'flex';
  btnSubmitOrder.textContent = `SUBMIT LIMIT ${state.side === 0 ? 'BUY' : 'SELL'} ORDER`;
  updateTotal();
});

btnTypeMarket.addEventListener('click', () => {
  state.type = 1;
  btnTypeMarket.classList.add('active');
  btnTypeLimit.classList.remove('active');
  priceGroup.style.display = 'none';
  btnSubmitOrder.textContent = `SUBMIT MARKET ${state.side === 0 ? 'BUY' : 'SELL'} ORDER`;
  updateTotal();
});

inputPrice.addEventListener('input', updateTotal);
inputAmount.addEventListener('input', updateTotal);

document.querySelectorAll('.quick-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    inputAmount.value = btn.dataset.qty;
    updateTotal();
  });
});

symbolTabs.addEventListener('click', (e) => {
  const btn = e.target.closest('.tab-btn');
  if (!btn || btn.dataset.symbol === state.activeSymbol) return;

  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  btn.classList.add('active');

  state.activeSymbol = btn.dataset.symbol;
  activeSymbolTag.textContent = state.activeSymbol;

  const symStats = state.statsBySymbol[state.activeSymbol];
  if (symStats && symStats.lastPrice !== null) {
    inputPrice.value = symStats.lastPrice.toFixed(2);
  } else {
    inputPrice.value = REFERENCE_PRICES[state.activeSymbol] || '100.00';
  }
  renderTickerForActiveSymbol();
  renderTradesStream();

  // Clear order book and depth chart immediately to prevent flashing stale data
  asksContainer.innerHTML = '<div class="empty-state">Loading order book...</div>';
  bidsContainer.innerHTML = '<div class="empty-state">Loading order book...</div>';
  spreadValue.textContent = '—';
  spreadBps.textContent = '';
  if (state.activeChartTab === 'depth') {
    drawDepthChart();
  }

  updateTotal();
  updateOHLCHeader();
  if (state.activeChartTab === 'price') {
    renderPriceChart();
  }
  fetchOrderBook();
  renderOpenOrders();
  hydrateTradeHistory(state.activeSymbol);
});

function renderBotButton() {
  btnToggleBot.classList.toggle('active', state.botRunning);
  botLabel.textContent = state.botRunning ? 'BOT: ACTIVE' : 'BOT: PAUSED';
}

async function syncBotStatus() {
  try {
    const res = await fetch('/simulator/status');
    if (!res.ok) return;
    state.botRunning = (await res.json()).running;
    renderBotButton();
  } catch (err) {
    console.error('Bot status error:', err);
  }
}

btnToggleBot.addEventListener('click', async () => {
  try {
    const res = await fetch('/simulator/toggle', { method: 'POST' });
    if (!res.ok) {
      alert('Toggle failed: ' + (await res.text()));
      return;
    }
    state.botRunning = (await res.json()).running;
    renderBotButton();
  } catch (err) {
    console.error('Toggle bot error:', err);
  }
});

btnToggleAudio.addEventListener('click', () => {
  state.audioEnabled = !state.audioEnabled;
  btnToggleAudio.textContent = state.audioEnabled ? '🔊 AUDIO: ON' : '🔇 AUDIO: OFF';
  if (state.audioEnabled) {
    ensureAudioContext();
  }
});

btnSubmitOrder.addEventListener('click', submitOrder);

// --------------------------------------------------------------------------
// 13. Hydration & Initial State Synchronization
// --------------------------------------------------------------------------
async function hydrateTradeHistory(symbol) {
  try {
    const res = await fetch(`/trades?symbol=${encodeURIComponent(symbol)}`);
    if (!res.ok) return;
    const data = await res.json();
    if (data.trades && Array.isArray(data.trades)) {
      data.trades.forEach(t => recordTrade(symbol, t));
      if (symbol === state.activeSymbol) {
        renderTradesStream();
        renderTickerForActiveSymbol();
        updateOHLCHeader();
        if (state.activeChartTab === 'price') renderPriceChart();
      }
    }
  } catch (err) {
    console.error('Failed to hydrate trade history:', err);
  }
}



connectWebSocket();
updateTotal();
updateOHLCHeader();
renderTickerForActiveSymbol();
renderTradesStream();
renderPriceChart();

