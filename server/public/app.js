// ==========================================================================
// OME // Institutional Trading Terminal Controller (v2.0)
// ==========================================================================

const state = {
  activeSymbol: 'AAPL',
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
  }
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
const depthCanvas = document.getElementById('depthChart');
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
// 1. Web Audio Synthesizer (Trade Execution Chime)
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
    // Handled gracefully if browser policy blocks audio before gesture
  }
}

// --------------------------------------------------------------------------
// 2. WebSocket Connection & Auto-Reconnect
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
    msg.data.forEach(appendTrade);
    fetchOrderBook();
    updateOpenOrdersAfterMatch(msg.data);
    playTradeSound();
  } else if (msg.type === 'order_cancelled' && msg.symbol === state.activeSymbol) {
    fetchOrderBook();
  }
}

// --------------------------------------------------------------------------
// 3. Fetch & Render L2 Order Book & Canvas Depth Chart
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

// --------------------------------------------------------------------------
// 4. HTML5 Canvas Depth Chart Visualizer
// --------------------------------------------------------------------------
function drawDepthChart(bids, asks) {
  if (!depthCanvas) return;
  const ctx = depthCanvas.getContext('2d');
  const w = depthCanvas.width;
  const h = depthCanvas.height;

  ctx.clearRect(0, 0, w, h);

  if (bids.length === 0 && asks.length === 0) return;

  let cumBids = [];
  let totalBid = 0;
  bids.slice(0, 20).forEach(b => {
    totalBid += b.volume;
    cumBids.push({ price: b.price, cum: totalBid });
  });

  let cumAsks = [];
  let totalAsk = 0;
  asks.slice(0, 20).forEach(a => {
    totalAsk += a.volume;
    cumAsks.push({ price: a.price, cum: totalAsk });
  });

  const maxVol = Math.max(totalBid, totalAsk, 1);
  const midX = w / 2;

  // Draw Bids (Left side, Green)
  if (cumBids.length > 0) {
    ctx.beginPath();
    ctx.moveTo(0, h);

    cumBids.forEach((b, i) => {
      const x = midX - (i / cumBids.length) * midX;
      const y = h - (b.cum / maxVol) * (h - 10);
      ctx.lineTo(x, y);
    });

    ctx.lineTo(midX, h - (cumBids[0].cum / maxVol) * (h - 10));
    ctx.lineTo(midX, h);
    ctx.closePath();

    const bidGrad = ctx.createLinearGradient(0, 0, 0, h);
    bidGrad.addColorStop(0, 'rgba(0, 240, 144, 0.3)');
    bidGrad.addColorStop(1, 'rgba(0, 240, 144, 0.02)');
    ctx.fillStyle = bidGrad;
    ctx.fill();

    ctx.strokeStyle = '#00f090';
    ctx.lineWidth = 1.5;
    ctx.stroke();
  }

  // Draw Asks (Right side, Red)
  if (cumAsks.length > 0) {
    ctx.beginPath();
    ctx.moveTo(midX, h);

    cumAsks.forEach((a, i) => {
      const x = midX + (i / cumAsks.length) * midX;
      const y = h - (a.cum / maxVol) * (h - 10);
      ctx.lineTo(x, y);
    });

    ctx.lineTo(w, h - (cumAsks[cumAsks.length - 1].cum / maxVol) * (h - 10));
    ctx.lineTo(w, h);
    ctx.closePath();

    const askGrad = ctx.createLinearGradient(0, 0, 0, h);
    askGrad.addColorStop(0, 'rgba(255, 51, 88, 0.3)');
    askGrad.addColorStop(1, 'rgba(255, 51, 88, 0.02)');
    ctx.fillStyle = askGrad;
    ctx.fill();

    ctx.strokeStyle = '#ff3358';
    ctx.lineWidth = 1.5;
    ctx.stroke();
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
// 5. Trade Stream & 24h Ticker Updates
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
// 6. Order Submission & State
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
// 7. Open Orders Table & 1-Click Cancel
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
// 8. UI Controls & Event Listeners
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
// Initialization
// --------------------------------------------------------------------------
connectWebSocket();
updateTotal();
