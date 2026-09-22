// ==========================================================================
// OME // Institutional Trading Terminal Controller & Dual-Chart Engine (v2.0)
// ==========================================================================

const state = {
  activeSymbol: 'AAPL',
  activeChartTab: 'price', // 'price' | 'depth'
  side: 0, // 0 = Buy, 1 = Sell
  type: 0, // 0 = Limit, 1 = Market
  myOrders: [],
  ws: null,
  audioEnabled: true,
  botRunning: true,
  stats: {
    high: 152.80,
    low: 148.50,
    volume: 1420580,
    tradesCount: 0,
    openPrice: 147.85,
  },
  lastBook: { bids: [], asks: [] },
  hoverPriceChart: null, // { x, y } when mouse is over price canvas
  hoverDepthChart: null, // { x, y } when mouse is over depth canvas
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
// 1. Candlestick Data Store & Historical Seeder
// --------------------------------------------------------------------------
const CANDLE_PERIOD_MS = 15000; // 15-second candle resolution for live action
const candles = {
  'AAPL': [],
  'TSLA': [],
  'BTC-USD': [],
};

function seedHistoricalCandles(symbol, basePrice, volatility, count = 48) {
  const list = [];
  let currentPrice = basePrice;
  const now = Date.now();
  const startTime = now - count * CANDLE_PERIOD_MS;

  for (let i = 0; i < count; i++) {
    const time = startTime + i * CANDLE_PERIOD_MS;
    const change = (Math.random() - 0.49) * volatility;
    const open = currentPrice;
    const close = Math.max(open + change, basePrice * 0.5);
    const high = Math.max(open, close) + Math.random() * (volatility * 0.7);
    const low = Math.min(open, close) - Math.random() * (volatility * 0.7);
    const volume = Math.floor(Math.random() * 8000) + 1200;

    list.push({
      time,
      open: parseFloat(open.toFixed(2)),
      high: parseFloat(high.toFixed(2)),
      low: parseFloat(low.toFixed(2)),
      close: parseFloat(close.toFixed(2)),
      volume,
    });

    currentPrice = close;
  }
  return list;
}

// Initialize seed data
candles['AAPL'] = seedHistoricalCandles('AAPL', 150.00, 0.45);
candles['TSLA'] = seedHistoricalCandles('TSLA', 240.00, 1.20);
candles['BTC-USD'] = seedHistoricalCandles('BTC-USD', 64000.00, 150.00);

function updateCandleOnTrade(symbol, price, amount) {
  const list = candles[symbol];
  if (!list || list.length === 0) return;

  const now = Date.now();
  const last = list[list.length - 1];

  if (now - last.time > CANDLE_PERIOD_MS) {
    // Roll into new candle
    list.push({
      time: now,
      open: last.close,
      high: Math.max(last.close, price),
      low: Math.min(last.close, price),
      close: price,
      volume: amount,
    });
    if (list.length > 80) list.shift();
  } else {
    // Update active candle
    last.high = Math.max(last.high, price);
    last.low = Math.min(last.low, price);
    last.close = price;
    last.volume += amount;
  }

  updateOHLCHeader();
  if (state.activeChartTab === 'price') {
    renderPriceChart();
  }
}

function updateOHLCHeader(customCandle = null) {
  const list = candles[state.activeSymbol];
  if (!list || list.length === 0) return;
  const c = customCandle || list[list.length - 1];

  ohlcOpen.textContent = c.open.toFixed(2);
  ohlcHigh.textContent = c.high.toFixed(2);
  ohlcLow.textContent = c.low.toFixed(2);
  ohlcClose.textContent = c.close.toFixed(2);

  const isBull = c.close >= c.open;
  ohlcClose.className = isBull ? 'text-green' : 'text-red';

  if (ohlcVol) {
    ohlcVol.textContent = c.volume >= 1000 
      ? (c.volume / 1000).toFixed(1) + 'K' 
      : c.volume.toString();
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
  if (!list || list.length === 0) return;

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

  // Add 4% padding to price bounds
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
  const numCandles = list.length;
  const slotW = chartW / numCandles;
  const candleW = Math.max(3, slotW * 0.68);

  list.forEach((c, idx) => {
    const x = idx * slotW + slotW / 2;
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

    // Time Label (Every ~10 candles)
    if (idx % 10 === 0) {
      const timeStr = new Date(c.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
      ctx.fillStyle = '#475569';
      ctx.textAlign = 'center';
      ctx.fillText(timeStr, x, chartH + 15);
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

  // 4. Interactive Hover Crosshair
  if (state.hoverPriceChart) {
    const { x, y } = state.hoverPriceChart;

    if (x <= chartW && y <= chartH) {
      ctx.strokeStyle = 'rgba(255, 255, 255, 0.25)';
      ctx.setLineDash([3, 3]);
      ctx.lineWidth = 1;

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

      // Price Tag on Right Axis
      const hoverPrice = yToPrice(y);
      ctx.fillStyle = '#1e293b';
      ctx.strokeStyle = '#475569';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.roundRect(chartW + 4, y - badgeH / 2, badgeW, badgeH, 3);
      ctx.fill();
      ctx.stroke();

      ctx.fillStyle = '#e2e8f0';
      ctx.font = '10px "JetBrains Mono", monospace';
      ctx.textAlign = 'center';
      ctx.fillText(`$${hoverPrice.toFixed(2)}`, chartW + 4 + badgeW / 2, y + 3.5);

      // Find hovered candle and update OHLC
      const hoveredIdx = Math.floor(x / slotW);
      if (hoveredIdx >= 0 && hoveredIdx < list.length) {
        updateOHLCHeader(list[hoveredIdx]);
      }
    }
  }
}

// --------------------------------------------------------------------------
// 4. Step-Staircase Market Depth Chart Engine
// --------------------------------------------------------------------------
function drawDepthChart(bids, asks) {
  state.lastBook = { bids: bids || [], asks: asks || [] };
  if (!depthCanvas || state.activeChartTab !== 'depth') return;

  const { ctx, width, height } = setupCanvasDPI(depthCanvas);
  ctx.clearRect(0, 0, width, height);

  if (bids.length === 0 && asks.length === 0) {
    ctx.fillStyle = '#475569';
    ctx.font = '11px "Plus Jakarta Sans", sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Waiting for Order Book Depth...', width / 2, height / 2);
    return;
  }

  // Cumulative depth preparation
  let cumBids = [];
  let totalBid = 0;
  bids.slice(0, 24).forEach(b => {
    totalBid += b.volume;
    cumBids.push({ price: b.price / 100, cum: totalBid, volume: b.volume });
  });

  let cumAsks = [];
  let totalAsk = 0;
  asks.slice(0, 24).forEach(a => {
    totalAsk += a.volume;
    cumAsks.push({ price: a.price / 100, cum: totalAsk, volume: a.volume });
  });

  const maxCum = Math.max(totalBid, totalAsk, 100);
  const midX = width / 2;
  const paddingBottom = 24;
  const drawH = height - paddingBottom;

  // 1. Draw Subtle Depth Grid
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.035)';
  ctx.setLineDash([3, 3]);
  ctx.lineWidth = 1;
  ctx.font = '10px "JetBrains Mono", monospace';
  ctx.fillStyle = '#64748b';

  const volSteps = 4;
  for (let i = 1; i <= volSteps; i++) {
    const y = drawH - (i / volSteps) * (drawH - 12);
    const volVal = Math.round((i / volSteps) * maxCum);

    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(width, y);
    ctx.stroke();

    ctx.textAlign = 'left';
    ctx.fillText(`${volVal.toLocaleString()}`, 8, y - 3);
  }
  ctx.setLineDash([]);

  // 2. Draw Bids Step-Staircase Curve (Green, Left)
  if (cumBids.length > 0) {
    ctx.beginPath();
    ctx.moveTo(midX, drawH);

    // Initial step at best bid
    const bestBidY = drawH - (cumBids[0].cum / maxCum) * (drawH - 14);
    ctx.lineTo(midX, bestBidY);

    for (let i = 0; i < cumBids.length; i++) {
      const nextX = midX - ((i + 1) / cumBids.length) * midX;
      const currentY = drawH - (cumBids[i].cum / maxCum) * (drawH - 14);

      // Horizontal step to price level
      ctx.lineTo(nextX, currentY);

      // Vertical step to next cumulative depth if available
      if (i < cumBids.length - 1) {
        const nextY = drawH - (cumBids[i + 1].cum / maxCum) * (drawH - 14);
        ctx.lineTo(nextX, nextY);
      }
    }

    ctx.lineTo(0, drawH);
    ctx.closePath();

    // Gradient fill
    const bidGrad = ctx.createLinearGradient(0, 0, 0, drawH);
    bidGrad.addColorStop(0, 'rgba(0, 240, 144, 0.28)');
    bidGrad.addColorStop(1, 'rgba(0, 240, 144, 0.01)');
    ctx.fillStyle = bidGrad;
    ctx.fill();

    // Step border stroke
    ctx.strokeStyle = '#00f090';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  // 3. Draw Asks Step-Staircase Curve (Red, Right)
  if (cumAsks.length > 0) {
    ctx.beginPath();
    ctx.moveTo(midX, drawH);

    // Initial step at best ask
    const bestAskY = drawH - (cumAsks[0].cum / maxCum) * (drawH - 14);
    ctx.lineTo(midX, bestAskY);

    for (let i = 0; i < cumAsks.length; i++) {
      const nextX = midX + ((i + 1) / cumAsks.length) * midX;
      const currentY = drawH - (cumAsks[i].cum / maxCum) * (drawH - 14);

      // Horizontal step to price level
      ctx.lineTo(nextX, currentY);

      // Vertical step to next cumulative depth if available
      if (i < cumAsks.length - 1) {
        const nextY = drawH - (cumAsks[i + 1].cum / maxCum) * (drawH - 14);
        ctx.lineTo(nextX, nextY);
      }
    }

    ctx.lineTo(width, drawH);
    ctx.closePath();

    // Gradient fill
    const askGrad = ctx.createLinearGradient(0, 0, 0, drawH);
    askGrad.addColorStop(0, 'rgba(255, 51, 88, 0.28)');
    askGrad.addColorStop(1, 'rgba(255, 51, 88, 0.01)');
    ctx.fillStyle = askGrad;
    ctx.fill();

    // Step border stroke
    ctx.strokeStyle = '#ff3358';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  // 4. Center Mid-Market Line
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.2)';
  ctx.setLineDash([3, 3]);
  ctx.beginPath();
  ctx.moveTo(midX, 0);
  ctx.lineTo(midX, drawH);
  ctx.stroke();
  ctx.setLineDash([]);

  // 5. Price Axis at Bottom
  ctx.fillStyle = '#64748b';
  ctx.font = '10px "JetBrains Mono", monospace';
  ctx.textAlign = 'center';

  if (cumBids.length > 0) {
    const deepestBid = cumBids[cumBids.length - 1].price;
    const bestBid = cumBids[0].price;
    ctx.fillText(`$${deepestBid.toFixed(2)}`, 30, height - 8);
    ctx.fillText(`$${bestBid.toFixed(2)}`, midX - 45, height - 8);
  }

  ctx.fillStyle = '#94a3b8';
  ctx.fillText('MID SPREAD', midX, height - 8);

  if (cumAsks.length > 0) {
    const bestAsk = cumAsks[0].price;
    const deepestAsk = cumAsks[cumAsks.length - 1].price;
    ctx.fillStyle = '#64748b';
    ctx.fillText(`$${bestAsk.toFixed(2)}`, midX + 45, height - 8);
    ctx.fillText(`$${deepestAsk.toFixed(2)}`, width - 35, height - 8);
  }

  // 6. Interactive Depth Hover Tooltip
  if (state.hoverDepthChart) {
    const { x, y } = state.hoverDepthChart;
    if (y <= drawH) {
      ctx.strokeStyle = 'rgba(255, 255, 255, 0.3)';
      ctx.setLineDash([2, 2]);
      ctx.beginPath();
      ctx.moveTo(x, 0);
      ctx.lineTo(x, drawH);
      ctx.stroke();
      ctx.setLineDash([]);

      const isBid = x < midX;
      let depthText = '';
      if (isBid && cumBids.length > 0) {
        const idx = Math.min(Math.floor(((midX - x) / midX) * cumBids.length), cumBids.length - 1);
        const item = cumBids[idx];
        depthText = `BID: $${item.price.toFixed(2)} | DEPTH: ${item.cum.toLocaleString()}`;
      } else if (!isBid && cumAsks.length > 0) {
        const idx = Math.min(Math.floor(((x - midX) / midX) * cumAsks.length), cumAsks.length - 1);
        const item = cumAsks[idx];
        depthText = `ASK: $${item.price.toFixed(2)} | DEPTH: ${item.cum.toLocaleString()}`;
      }

      if (depthText) {
        ctx.font = '10px "JetBrains Mono", monospace';
        const txtW = ctx.measureText(depthText).width + 16;
        const boxX = Math.max(10, Math.min(width - txtW - 10, x - txtW / 2));
        const boxY = Math.max(10, y - 28);

        ctx.fillStyle = '#0f172a';
        ctx.strokeStyle = isBid ? '#00f090' : '#ff3358';
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.roundRect(boxX, boxY, txtW, 20, 4);
        ctx.fill();
        ctx.stroke();

        ctx.fillStyle = '#f8fafc';
        ctx.textAlign = 'center';
        ctx.fillText(depthText, boxX + txtW / 2, boxY + 13.5);
      }
    }
  }
}

// --------------------------------------------------------------------------
// 5. Chart Interaction & Tab Switchers
// --------------------------------------------------------------------------
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
  drawDepthChart(state.lastBook.bids, state.lastBook.asks);
});

// Price Canvas Mouse Events
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

// Depth Canvas Mouse Events
depthCanvas.addEventListener('mousemove', (e) => {
  const rect = depthCanvas.getBoundingClientRect();
  state.hoverDepthChart = {
    x: e.clientX - rect.left,
    y: e.clientY - rect.top,
  };
  drawDepthChart(state.lastBook.bids, state.lastBook.asks);
});

depthCanvas.addEventListener('mouseleave', () => {
  state.hoverDepthChart = null;
  drawDepthChart(state.lastBook.bids, state.lastBook.asks);
});

// Responsive resize listener
window.addEventListener('resize', () => {
  if (state.activeChartTab === 'price') {
    renderPriceChart();
  } else {
    drawDepthChart(state.lastBook.bids, state.lastBook.asks);
  }
});

// --------------------------------------------------------------------------
// 6. Web Audio Synthesizer (Trade Execution Chime)
// --------------------------------------------------------------------------
let audioCtx = null;
function playTradeSound() {
  if (!state.audioEnabled) return;
  try {
    if (!audioCtx) audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    if (audioCtx.state === 'suspended') audioCtx.resume();

    const osc = audioCtx.createOscillator();
    const gain = audioCtx.createGain();

    osc.type = 'sine';
    osc.frequency.setValueAtTime(880, audioCtx.currentTime); // A5 note
    osc.frequency.exponentialRampToValueAtTime(1320, audioCtx.currentTime + 0.04);

    gain.gain.setValueAtTime(0.04, audioCtx.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.001, audioCtx.currentTime + 0.05);

    osc.connect(gain);
    gain.connect(audioCtx.destination);

    osc.start();
    osc.stop(audioCtx.currentTime + 0.05);
  } catch (e) {
    // Handled gracefully if browser policy blocks audio before user interaction
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
  if (msg.type === 'trades' && msg.symbol === state.activeSymbol) {
    msg.data.forEach(trade => {
      appendTrade(trade);
      const p = trade.price / 100;
      updateCandleOnTrade(state.activeSymbol, p, trade.amount);
    });
    fetchOrderBook();
    updateOpenOrdersAfterMatch(msg.data);
    playTradeSound();
  } else if (msg.type === 'order_cancelled' && msg.symbol === state.activeSymbol) {
    fetchOrderBook();
  }
}

// --------------------------------------------------------------------------
// 8. Fetch & Render L2 Order Book
// --------------------------------------------------------------------------
async function fetchOrderBook() {
  try {
    const res = await fetch(`/orderbook?symbol=${state.activeSymbol}`);
    if (!res.ok) return;
    const data = await res.json();
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

  // Render Asks
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

  // Render Bids
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

  // Calculate Spread
  if (bids.length > 0 && asks.length > 0) {
    const bestBid = bids[0].price;
    const bestAsk = asks[0].price;
    const spread = bestAsk - bestBid;
    const spreadDollars = (spread / 100).toFixed(2);
    const bps = ((spread / bestAsk) * 10000).toFixed(1);

    spreadValue.textContent = `$${spreadDollars}`;
    spreadBps.textContent = `(${bps} bps)`;
  } else {
    spreadValue.textContent = '---';
    spreadBps.textContent = '';
  }
}

// Click-to-fill
window.setPrice = function(price) {
  if (state.type === 0) {
    inputPrice.value = price;
    updateTotal();
  }
};

// --------------------------------------------------------------------------
// 9. Trade Stream & 24h Ticker Updates
// --------------------------------------------------------------------------
function appendTrade(trade) {
  const price = (trade.price / 100).toFixed(2);
  const time = new Date(trade.timestamp / 1000000).toLocaleTimeString();
  const sideClass = state.side === 0 ? 'buy' : 'sell';

  const row = document.createElement('div');
  row.className = 'trade-row';
  row.innerHTML = `
    <span class="trade-price ${sideClass}">$${price}</span>
    <span>${trade.amount}</span>
    <span>${time}</span>
  `;

  const empty = tradesStream.querySelector('.empty-state');
  if (empty) empty.remove();

  tradesStream.insertBefore(row, tradesStream.firstChild);

  if (tradesStream.children.length > 50) {
    tradesStream.removeChild(tradesStream.lastChild);
  }

  // Update Tickers
  lastTradedPrice.textContent = `$${price}`;
  tickerLastPrice.textContent = `$${price}`;

  const numPrice = parseFloat(price);
  if (numPrice > state.stats.high) state.stats.high = numPrice;
  if (numPrice < state.stats.low) state.stats.low = numPrice;
  state.stats.volume += trade.amount;
  state.stats.tradesCount++;

  const pctChange = (((numPrice - state.stats.openPrice) / state.stats.openPrice) * 100).toFixed(2);
  tickerChange.textContent = `${pctChange >= 0 ? '+' : ''}${pctChange}%`;
  tickerChange.className = `ticker-val ${pctChange >= 0 ? 'text-green' : 'text-red'}`;

  tickerHigh.textContent = `$${state.stats.high.toFixed(2)}`;
  tickerLow.textContent = `$${state.stats.low.toFixed(2)}`;
  tickerVolume.textContent = state.stats.volume.toLocaleString();
  tickerTradesCount.textContent = state.stats.tradesCount.toLocaleString();

  const activeTabPrice = document.getElementById(`tabPrice-${state.activeSymbol}`);
  if (activeTabPrice) activeTabPrice.textContent = `$${price}`;
}

// --------------------------------------------------------------------------
// 10. Order Submission & State
// --------------------------------------------------------------------------
async function submitOrder() {
  const amount = parseInt(inputAmount.value, 10);
  if (!amount || amount <= 0) {
    alert('Please enter a valid amount');
    return;
  }

  let price = 0;
  if (state.type === 0) {
    const rawPrice = parseFloat(inputPrice.value);
    if (!rawPrice || rawPrice <= 0) {
      alert('Please enter a valid limit price');
      return;
    }
    price = Math.round(rawPrice * 100);
  }

  const orderId = Date.now() + Math.floor(Math.random() * 1000);

  const payload = {
    id: orderId,
    symbol: state.activeSymbol,
    side: state.side,
    type: state.type,
    price: price,
    amount: amount,
  };

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

    if (state.type === 0 && data.order && data.order.amount > 0) {
      state.myOrders.push(data.order);
      renderOpenOrders();
    }

    fetchOrderBook();
  } catch (err) {
    console.error('Submit order error:', err);
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
          <button class="btn-cancel" onclick="cancelOrder(${o.id})">CANCEL</button>
        </td>
      </tr>
    `;
  }).join('');
}

window.cancelOrder = async function(id) {
  try {
    const res = await fetch(`/order?symbol=${state.activeSymbol}&id=${id}`, {
      method: 'DELETE',
    });
    if (res.ok) {
      state.myOrders = state.myOrders.filter(o => o.id !== id);
      renderOpenOrders();
      fetchOrderBook();
    }
  } catch (err) {
    console.error('Cancel order error:', err);
  }
};

function updateOpenOrdersAfterMatch(trades) {
  trades.forEach(t => {
    state.myOrders = state.myOrders.map(o => {
      if (o.id === t.maker_order_id) {
        return { ...o, amount: o.amount - t.amount };
      }
      return o;
    }).filter(o => o.amount > 0);
  });
  renderOpenOrders();
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
  if (!btn) return;

  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  btn.classList.add('active');

  state.activeSymbol = btn.dataset.symbol;
  activeSymbolTag.textContent = state.activeSymbol;

  if (state.activeSymbol === 'AAPL') {
    inputPrice.value = '150.00';
    state.stats.openPrice = 147.85;
    state.stats.high = 152.80;
    state.stats.low = 148.50;
  } else if (state.activeSymbol === 'TSLA') {
    inputPrice.value = '240.00';
    state.stats.openPrice = 242.00;
    state.stats.high = 245.50;
    state.stats.low = 238.10;
  } else if (state.activeSymbol === 'BTC-USD') {
    inputPrice.value = '64000.00';
    state.stats.openPrice = 63200.00;
    state.stats.high = 64800.00;
    state.stats.low = 62900.00;
  }

  updateTotal();
  updateOHLCHeader();
  if (state.activeChartTab === 'price') {
    renderPriceChart();
  }
  fetchOrderBook();
  renderOpenOrders();
});

btnToggleBot.addEventListener('click', async () => {
  try {
    const res = await fetch('/simulator/toggle', { method: 'POST' });
    const data = await res.json();
    state.botRunning = data.running;
    if (state.botRunning) {
      btnToggleBot.classList.add('active');
      botLabel.textContent = 'BOT: ACTIVE';
    } else {
      btnToggleBot.classList.remove('active');
      botLabel.textContent = 'BOT: PAUSED';
    }
  } catch (err) {
    console.error('Toggle bot error:', err);
  }
});

btnToggleAudio.addEventListener('click', () => {
  state.audioEnabled = !state.audioEnabled;
  btnToggleAudio.textContent = state.audioEnabled ? '🔊 AUDIO: ON' : '🔇 AUDIO: OFF';
});

btnSubmitOrder.addEventListener('click', submitOrder);

// --------------------------------------------------------------------------
// 13. Initialization
// --------------------------------------------------------------------------
connectWebSocket();
updateTotal();
updateOHLCHeader();
renderPriceChart();
