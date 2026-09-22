// ==========================================================================
// OME Institutional Trading Terminal Controller
// ==========================================================================

const state = {
  activeSymbol: 'AAPL',
  side: 0, // 0 = Buy, 1 = Sell
  type: 0, // 0 = Limit, 1 = Market
  myOrders: [],
  ws: null,
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

// --------------------------------------------------------------------------
// 1. WebSocket Connection with Auto-Reconnect
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
    fetchOrderBook(); // Refresh depth
    updateOpenOrdersAfterMatch(msg.data);
  } else if (msg.type === 'order_cancelled' && msg.symbol === state.activeSymbol) {
    fetchOrderBook();
  }
}

// --------------------------------------------------------------------------
// 2. Fetch & Render L2 Order Book
// --------------------------------------------------------------------------
async function fetchOrderBook() {
  try {
    const res = await fetch(`/orderbook?symbol=${state.activeSymbol}`);
    if (!res.ok) return;
    const data = await res.json();
    renderOrderBook(data);
  } catch (err) {
    console.error('Failed to fetch order book:', err);
  }
}

function renderOrderBook(data) {
  const bids = data.bids || [];
  const asks = data.asks || [];

  // Calculate cumulative volumes for depth bars
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

  // Render Asks (reversed so lowest ask is at the bottom, near the spread)
  if (asksWithCum.length === 0) {
    asksContainer.innerHTML = '<div class="empty-state">No asks resting</div>';
  } else {
    asksContainer.innerHTML = asksWithCum.slice(0, 15).reverse().map(a => {
      const pct = (a.cum / maxTotal) * 100;
      const priceFormatted = (a.price / 100).toFixed(2);
      return `
        <div class="book-row ask-row" onclick="setPrice('${priceFormatted}')">
          <div class="book-depth-bar" style="width: ${pct}%"></div>
          <span class="book-price">${priceFormatted}</span>
          <span>${a.volume}</span>
          <span>${a.cum}</span>
        </div>
      `;
    }).join('');
  }

  // Render Bids (highest bid at the top, near the spread)
  if (bidsWithCum.length === 0) {
    bidsContainer.innerHTML = '<div class="empty-state">No bids resting</div>';
  } else {
    bidsContainer.innerHTML = bidsWithCum.slice(0, 15).map(b => {
      const pct = (b.cum / maxTotal) * 100;
      const priceFormatted = (b.price / 100).toFixed(2);
      return `
        <div class="book-row bid-row" onclick="setPrice('${priceFormatted}')">
          <div class="book-depth-bar" style="width: ${pct}%"></div>
          <span class="book-price">${priceFormatted}</span>
          <span>${b.volume}</span>
          <span>${b.cum}</span>
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

// Click-to-fill: Clicking any price in the book fills the order ticket!
window.setPrice = function(price) {
  if (state.type === 0) {
    inputPrice.value = price;
    updateTotal();
  }
};

// --------------------------------------------------------------------------
// 3. Trade Stream Rendering
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

  // Remove empty state if present
  const empty = tradesStream.querySelector('.empty-state');
  if (empty) empty.remove();

  tradesStream.insertBefore(row, tradesStream.firstChild);

  // Keep stream bounded to 50 items
  if (tradesStream.children.length > 50) {
    tradesStream.removeChild(tradesStream.lastChild);
  }

  // Update Last Traded Price in spread indicator and active tab
  lastTradedPrice.textContent = `$${price}`;
  const activeTabPrice = document.getElementById(`tabPrice-${state.activeSymbol}`);
  if (activeTabPrice) activeTabPrice.textContent = `$${price}`;
}

// --------------------------------------------------------------------------
// 4. Order Submission & State
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

    // If order was a Limit order with remaining resting quantity, track it
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
// 5. Open Orders Table & 1-Click Cancel
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
      ? '<span style="color:var(--green); font-weight:600;">BUY</span>'
      : '<span style="color:var(--red); font-weight:600;">SELL</span>';
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
// 6. UI Controls & Event Listeners
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

// Side selection (BUY / SELL)
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

// Type selection (LIMIT / MARKET)
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
  priceGroup.style.display = 'none'; // Hide price input for market orders
  btnSubmitOrder.textContent = `SUBMIT MARKET ${state.side === 0 ? 'BUY' : 'SELL'} ORDER`;
  updateTotal();
});

// Amount / Price change listeners
inputPrice.addEventListener('input', updateTotal);
inputAmount.addEventListener('input', updateTotal);

// Quick amount shortcut buttons
document.querySelectorAll('.quick-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    inputAmount.value = btn.dataset.qty;
    updateTotal();
  });
});

// Symbol tabs switcher
symbolTabs.addEventListener('click', (e) => {
  const btn = e.target.closest('.tab-btn');
  if (!btn) return;

  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  btn.classList.add('active');

  state.activeSymbol = btn.dataset.symbol;
  activeSymbolTag.textContent = state.activeSymbol;

  // Set standard starting prices
  if (state.activeSymbol === 'AAPL') inputPrice.value = '150.00';
  if (state.activeSymbol === 'TSLA') inputPrice.value = '240.00';
  if (state.activeSymbol === 'BTC-USD') inputPrice.value = '64000.00';

  updateTotal();
  fetchOrderBook();
  renderOpenOrders();
});

// Submit button
btnSubmitOrder.addEventListener('click', submitOrder);

// --------------------------------------------------------------------------
// Initialization
// --------------------------------------------------------------------------
connectWebSocket();
updateTotal();
