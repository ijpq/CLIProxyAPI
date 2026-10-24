const CLEAN_ROUTES = !/\/ui(?:\/(?:index|docs)\.html)?\/?$/.test(location.pathname);
const BASE = CLEAN_ROUTES ? '/api/portal' : location.pathname.replace(/\/ui(?:\/(?:index|docs)\.html)?\/?$/, '');
const CONSOLE_PAGES = ['dashboard','keys','topup','usage','assistant','settings','admin','quota'];
let siteMode = document.documentElement.dataset.portalSite || 'legacy';
let registrationEnabled = siteMode !== 'privileged';
function pageFromLocation() {
  if (!CLEAN_ROUTES) return location.hash.slice(1) || 'dashboard';
  const page = location.pathname.replace(/^\/console\/?/, '');
  return CONSOLE_PAGES.includes(page) ? page : 'dashboard';
}
let requestedConsolePage = pageFromLocation();
function updateRoute(path) {
  if (CLEAN_ROUTES && location.pathname !== path) history.pushState(null, '', path);
}
function documentationLink(value) {
  return CLEAN_ROUTES ? '/docs' : safeLink(value, BASE + '/ui/docs.html');
}
function updateConsoleLinks() {
  if (!CLEAN_ROUTES) return;
  for (const link of document.querySelectorAll('a[href^="#"]')) {
    const page = link.getAttribute('href').slice(1);
    if (CONSOLE_PAGES.includes(page)) link.href = page === 'dashboard' ? '/console' : '/console/' + page;
  }
}
let token = localStorage.getItem('portal_token');
let isAdmin = false;
let selectedMethod = null;
let usageCursor = '';
let adminUsers = [], allModels = [], allAccounts = [], editingUserId = null;
let publicSite = null, commerceSettings = null, pointPrices = [], priceVersion = 0;
let assistantMessages = [], assistantBusy = false, assistantReady = false;

function showHome() {
  if (siteMode === 'privileged') { showAuth('login'); return; }
  updateRoute('/');
  document.getElementById('page-home').classList.remove('hidden');
  document.getElementById('page-auth').classList.add('hidden');
  document.getElementById('page-app').classList.add('hidden');
}
function showAuth(tab) {
  if (tab === 'register' && !registrationEnabled) tab = 'login';
  document.getElementById('page-home').classList.add('hidden');
  document.getElementById('page-app').classList.add('hidden');
  document.getElementById('page-auth').classList.remove('hidden');
  switchAuthTab(tab);
}
function safeLink(value, fallback) {
  try { const u = new URL(value); if ((u.protocol === 'http:' || u.protocol === 'https:') && !u.username && !u.password) return u.href; } catch {}
  return fallback;
}
async function loadPublicSite() {
  try {
    publicSite = await api('/public');
    document.getElementById('site-name').textContent = publicSite.site_name || 'TrueCore AI';
    for (const id of ['home-docs','workspace-docs','dashboard-docs']) document.getElementById(id).href = documentationLink(publicSite.docs_url);
    document.getElementById('home-prices').innerHTML = (publicSite.prices || []).map(p => `<tr><td>${esc(p.model)}</td><td>${esc(p.input_per_million)}</td><td>${esc(p.output_per_million)}</td><td>${esc(p.cache_read_per_million)}</td><td>${esc(p.cache_write_per_million)}</td></tr>`).join('') || '<tr><td colspan="5">模型价格尚未公布，暂未开放付费调用。</td></tr>';
    document.getElementById('home-rates').textContent = `1 CNY = ${publicSite.cny_points_per_unit || '未配置'} 积分 · 1 USDT = ${publicSite.usdt_points_per_unit || '未配置'} 积分。订单到账积分以创建时快照为准。`;
    document.getElementById('home-site-error').textContent = '';
    return publicSite;
  } catch(err) {
    document.getElementById('home-prices').innerHTML = '<tr><td colspan="5">价格暂时无法读取，请稍后重试。</td></tr>';
    document.getElementById('home-site-error').textContent = '站点配置暂时不可用。';
    return null;
  }
}

function api(path, opts = {}) {
  const headers = {'Content-Type': 'application/json'};
  if (token) headers['Authorization'] = 'Bearer ' + token;
  return fetch(BASE + path, {...opts, headers}).then(async r => {
    const data = r.status === 204 ? {} : await r.json().catch(() => ({}));
    if (!r.ok) throw {status: r.status, message: data.error || 'request failed'};
    return data;
  });
}

// ---- Auth ----
function switchAuthTab(tab) {
  if (tab === 'register' && !registrationEnabled) tab = 'login';
  updateRoute(tab === 'register' ? '/register' : '/login');
  document.querySelectorAll('.seg .tab').forEach(t => t.classList.remove('active'));
  const btn = document.querySelector(`.seg .tab[onclick*="'${tab}'"]`);
  if (btn) btn.classList.add('active');
  document.getElementById('form-login').classList.toggle('hidden', tab !== 'login');
  document.getElementById('form-register').classList.toggle('hidden', tab !== 'register');
  document.getElementById('form-code').classList.toggle('hidden', tab !== 'code');
  document.getElementById('auth-msg').innerHTML = '';
  if (tab === 'code') resetCode();
}

// Email-code login (two phases: request code, then verify).
let codeSent = false;
function resetCode() {
  codeSent = false;
  document.getElementById('code-step2').classList.add('hidden');
  document.getElementById('code-resend').classList.add('hidden');
  document.getElementById('code-input').value = '';
  document.getElementById('code-submit').textContent = '发送验证码';
}
async function doCode(e) {
  e.preventDefault();
  const email = document.getElementById('code-email').value.trim();
  try {
    if (!codeSent) {
      await api('/login/code/request', {method:'POST', body: JSON.stringify({email})});
      codeSent = true;
      document.getElementById('code-step2').classList.remove('hidden');
      document.getElementById('code-resend').classList.remove('hidden');
      document.getElementById('code-submit').textContent = '登录';
      document.getElementById('code-input').focus();
      showAuthMsg('验证码已发送，请查收邮箱', true);
    } else {
      const code = document.getElementById('code-input').value.trim();
      const r = await api('/login/code/verify', {method:'POST', body: JSON.stringify({email, code})});
      token = r.token; localStorage.setItem('portal_token', token);
      enterApp();
    }
  } catch(err) { showAuthMsg(err.message); }
}
function showAuthMsg(msg, ok) {
  document.getElementById('auth-msg').innerHTML = `<div class="msg ${ok ? 'msg-ok' : 'msg-error'}">${esc(msg)}</div>`;
}
function togglePw(btn) {
  const inp = btn.parentElement.querySelector('input');
  const show = inp.type === 'password';
  inp.type = show ? 'text' : 'password';
  btn.textContent = show ? '隐藏' : '显示';
  btn.setAttribute('aria-pressed', String(show));
  btn.setAttribute('aria-label', show ? '隐藏密码' : '显示密码');
}

async function doLogin(e) {
  e.preventDefault();
  try {
    const r = await api('/login', {method:'POST', body: JSON.stringify({
      email: document.getElementById('login-email').value,
      password: document.getElementById('login-pass').value
    })});
    token = r.token; localStorage.setItem('portal_token', token);
    enterApp();
  } catch(err) { showAuthMsg(err.message); }
}
async function doRegister(e) {
  e.preventDefault();
  try {
    const r = await api('/register', {method:'POST', body: JSON.stringify({
      email: document.getElementById('reg-email').value,
      password: document.getElementById('reg-pass').value,
      display_name: document.getElementById('reg-name').value
    })});
    token = r.token; localStorage.setItem('portal_token', token);
    enterApp();
  } catch(err) { showAuthMsg(err.message); }
}
// Hard reset on logout so no previous-account data can linger in memory/DOM.
function doLogout() {
  token = null; localStorage.removeItem('portal_token');
  if (CLEAN_ROUTES) location.assign('/login'); else location.reload();
}

// ---- App shell ----
async function enterApp() {
  document.getElementById('page-home').classList.add('hidden');
  document.getElementById('page-auth').classList.add('hidden');
  document.getElementById('page-app').classList.remove('hidden');
  try {
    const me = await api('/me');
    isAdmin = !!me.user.is_admin && !CLEAN_ROUTES;
    document.getElementById('nav-admin').classList.toggle('hidden', !isAdmin);
    document.getElementById('nav-quota').classList.toggle('hidden', !isAdmin);
    document.getElementById('side-email').textContent = me.user.email;
    document.getElementById('side-avatar').textContent = (me.user.email||'U').charAt(0).toUpperCase();
  } catch(e) {}
  navigate(CLEAN_ROUTES ? requestedConsolePage : (location.hash.slice(1) || 'dashboard'));
}
function navigate(page) {
  if (page !== 'keys') closeKeyPolicy(true);
  if (page !== 'keys') document.getElementById('new-key-msg').innerHTML = '';
  if (!page || !document.getElementById('sec-' + page)) page = 'dashboard';
  if ((page === 'admin' || page === 'quota') && !isAdmin) page = 'dashboard';
  updateRoute(page === 'dashboard' ? '/console' : '/console/' + page);
  document.querySelectorAll('[id^="sec-"]').forEach(s => s.classList.add('hidden'));
  document.getElementById('sec-' + page).classList.remove('hidden');
  document.querySelectorAll('.sidebar nav a').forEach(a => a.classList.toggle('active', a.dataset.page === page));
  if (page === 'dashboard') loadDashboard();
  else if (page === 'keys') loadKeys();
  else if (page === 'topup') loadTopup();
  else if (page === 'usage') { usageCursor = ''; loadUsageFilterOptions(); loadUsage(true); loadUsageStats(); }
  else if (page === 'quota') loadQuota();
  else if (page === 'admin') loadAdmin();
  else if (page === 'assistant') loadAssistant();
}

// My entitlements: balance, billing status, allowed models/accounts.
async function loadQuota() {
  try {
    const [me, wallet, lbl] = await Promise.all([api('/me'), api('/wallet'), api('/account-labels')]);
    const u = me.user, labels = (lbl && lbl.labels) || {};
    document.getElementById('q-balance').textContent = parseFloat(wallet.balance).toFixed(6);
    document.getElementById('q-plan').innerHTML = u.unbilled
      ? '<span class="badge badge-violet">免余额</span>' : '<span class="badge badge-gray">计费</span>';
    const models = u.allowed_models || [], accts = u.allowed_auth_ids || [];
    document.getElementById('q-models').innerHTML = models.length
      ? models.map(m => `<span class="chip"><span class="dot"></span>${esc(m)}</span>`).join('')
      : accts.length
        ? '<span class="chip all">由所选认证文件支持的模型决定</span>'
        : '<span class="chip all">全部模型</span>';
    document.getElementById('q-accounts').innerHTML = accts.length
      ? accts.map(a => `<span class="chip"><span class="dot"></span>${esc(labels[a] || '账号')}</span>`).join('')
      : '<span class="chip all">全部账号</span>';
    loadAccountQuota();
  } catch(err) { if (err.status === 401) doLogout(); }
}

// Live upstream quota/balance for the user's allowed accounts.
async function loadAccountQuota() {
  const box = document.getElementById('q-quota');
  const btn = document.getElementById('q-refresh');
  box.innerHTML = '<p class="muted" style="font-size:.85rem">正在查询上游余额…</p>';
  if (btn) btn.disabled = true;
  try {
    const res = await api('/quota');
    const accts = (res && res.accounts) || [];
    if (!accts.length) { box.innerHTML = '<p class="empty">暂无可查询余额的账号</p>'; return; }
    box.innerHTML = accts.map(renderQuotaAccount).join('');
  } catch(err) {
    if (err.status === 401) { doLogout(); return; }
    box.innerHTML = '<p class="qacct-err">余额查询失败</p>';
  } finally { if (btn) btn.disabled = false; }
}

function renderQuotaAccount(a) {
  const badge = providerBadge(a.provider);
  const h = esc(a.handle || '');
  const actions = `<div class="qacct-actions">
    <button class="qbtn" title="刷新额度" onclick="refreshAccountQuota('${h}')">↻ 刷新</button>
    ${a.can_reset ? `<button class="qbtn qbtn-reset" title="消耗一个重置额度以立即重置限流" onclick="resetAccountCredit('${h}')">重置额度${a.reset_credits ? ' ('+a.reset_credits+')' : ''}</button>` : ''}
  </div>`;
  let inner = '';
  if (a.error) {
    inner = `<div class="qacct-err">${esc(a.error)}</div>`;
  } else {
    if (a.details && a.details.length) {
      inner += '<div class="qdetails">' + a.details.map(d =>
        `<div class="qrow"><span>${esc(d.label)}</span><b>${esc(d.value)}</b></div>`).join('') + '</div>';
    }
    if (a.windows && a.windows.length) {
      inner += '<div class="qsub">用量窗口</div>' + a.windows.map(renderQuotaMeter).join('');
    }
    if (a.note) inner += `<div class="qacct-note">${esc(a.note)}</div>`;
    if (!inner) inner = '<div class="qacct-note">暂无数据</div>';
  }
  return `<div class="qacct" data-handle="${h}"><div class="qacct-head">${badge}<span class="name">${esc(a.label || '账号')}</span>${actions}</div>${inner}</div>`;
}

// Refresh a single account's live quota in place.
async function refreshAccountQuota(handle) {
  const card = document.querySelector(`.qacct[data-handle="${handle}"]`);
  if (card) card.classList.add('busy');
  try {
    const a = await api('/quota/' + encodeURIComponent(handle));
    if (card) card.outerHTML = renderQuotaAccount(a);
  } catch(err) {
    if (err.status === 401) { doLogout(); return; }
    if (card) card.classList.remove('busy');
  }
}

// Consume one Codex rate-limit reset credit for an account, then refresh it.
async function resetAccountCredit(handle) {
  if (!confirm('确认消耗一个「重置额度」来立即重置该账号的 5 小时限流？此操作不可撤销。')) return;
  const card = document.querySelector(`.qacct[data-handle="${handle}"]`);
  if (card) card.classList.add('busy');
  try {
    const a = await api('/quota/' + encodeURIComponent(handle) + '/reset', {method:'POST'});
    if (card) {
      card.outerHTML = renderQuotaAccount(a);
      const fresh = document.querySelector(`.qacct[data-handle="${handle}"]`);
      if (fresh) fresh.insertAdjacentHTML('beforeend', '<div class="qacct-msg ok">已重置</div>');
    }
  } catch(err) {
    if (err.status === 401) { doLogout(); return; }
    if (card) {
      card.classList.remove('busy');
      const old = card.querySelector('.qacct-msg'); if (old) old.remove();
      card.insertAdjacentHTML('beforeend', `<div class="qacct-msg err">${esc(err.message || '重置失败')}</div>`);
    }
  }
}

function renderQuotaMeter(w) {
  const rem = (w.remaining_percent == null) ? null : Math.max(0, Math.min(100, w.remaining_percent));
  const pct = rem == null ? '--' : Math.round(rem) + '%';
  const cls = rem == null ? '' : (rem <= 10 ? 'danger' : (rem <= 30 ? 'warn' : ''));
  const width = rem == null ? 0 : rem;
  const reset = w.resets_at ? `重置于 ${fmtTime(w.resets_at)}` : '';
  return `<div class="qmeter">
    <div class="qmeter-top"><span class="qmeter-label">${esc(w.label)}</span>
      <span><span class="qmeter-pct">${pct}</span>${reset ? ` <span class="qmeter-reset">· ${esc(reset)}</span>` : ''}</span></div>
    <div class="qbar ${cls}"><span style="width:${width}%"></span></div>
  </div>`;
}

function providerBadge(p) {
  const map = {claude:['Claude','badge-yellow'], codex:['Codex','badge-violet'], antigravity:['Antigravity','badge-blue']};
  const m = map[(p||'').toLowerCase()] || [p || '账号','badge-gray'];
  return `<span class="badge ${m[1]}">${esc(m[0])}</span>`;
}
window.addEventListener('hashchange', () => {
  const page = location.hash.slice(1);
  if (CONSOLE_PAGES.includes(page)) navigate(page);
});
window.addEventListener('popstate', () => {
  if (!CLEAN_ROUTES) return;
  requestedConsolePage = pageFromLocation();
  if (location.pathname.startsWith('/console') && token) enterApp();
  else if (location.pathname === '/login' || location.pathname.startsWith('/console')) showAuth('login');
  else if (location.pathname === '/register') showAuth('register');
  else showHome();
});

// ---- Dashboard ----
async function loadDashboard() {
  try {
    const [me, wallet, usage, keys] = await Promise.all([
      api('/me'), api('/wallet'), api('/usage?limit=10'), api('/api-keys')
    ]);
    document.getElementById('dash-email').textContent = me.user.email;
    document.getElementById('dash-balance').textContent = parseFloat(wallet.balance).toFixed(6);
    document.getElementById('dash-keys').textContent = keys.keys.filter(k => !k.revoked_at).length;
    document.getElementById('dash-plan').textContent = me.user.unbilled ? '免余额账户' : '按量计费';
    document.getElementById('dashboard-error').classList.add('hidden');
    const tbody = document.getElementById('dash-usage');
    tbody.innerHTML = usage.records.length ? usage.records.map(r => `<tr>
      <td>${fmtTime(r.created_at)}</td><td>${esc(r.model)}</td>
      <td>${r.input_tokens}</td><td>${r.output_tokens}</td>
      <td>${r.unit === 'LEGACY' ? '历史货币 · 待对账' : parseFloat(r.cost).toFixed(6)}</td><td>${statusBadge(r.status)}</td></tr>`).join('')
      : '<tr><td colspan="6" class="empty">还没有消费记录。完成充值和客户端配置后，你的每次调用会显示在这里。</td></tr>';
  } catch(err) { if (err.status === 401) { doLogout(); return; } const node=document.getElementById('dashboard-error');node.textContent='暂时无法读取账户数据，请刷新重试。';node.classList.remove('hidden'); }
}

// ---- Keys ----
let keyRows = [], keyModelOptions = [], editingKeyId = null, keyCreateBusy = false, keyPolicyBusy = false;
let keyPrices = [], keyGroups = {key:'all', 'edit-key':'all'}, keyDialogOpener = null;
let exampleLanguage = 'curl';
function keyPolicyLabel(key) {
  if (key.model_scope === 'none') return '已禁止所有模型';
  if (key.model_scope === 'selected') return (key.allowed_models || []).join('、') || '无可用模型';
  return '全部可用模型';
}
function selectedKeyModels(prefix) {
  return [...document.querySelectorAll('#' + prefix + '-models input:checked')].map(input => input.value);
}
function renderKeyModels(prefix, selected = []) {
  const selectedSet = new Set(selected), published = new Set(keyModelOptions);
  const models = [...new Set([...keyModelOptions, ...selected])];
  const prices = new Map(keyPrices.map(p => [p.model, p]));
  const discounts = [...new Set([10, 20, ...keyPrices.map(p => p.discount_percent || 0).filter(Boolean)])].sort((a,b) => a-b);
  const groups = ['all', ...discounts.map(String)];
  const group = groups.includes(keyGroups[prefix]) ? keyGroups[prefix] : 'all';
  keyGroups[prefix] = group;
  const query = document.getElementById(prefix + '-search').value.trim().toLowerCase();
  const scope = document.getElementById(prefix + '-scope').value || 'all';
  document.getElementById(prefix + '-groups').innerHTML = groups.map(value => {
    const count = value === 'all' ? keyModelOptions.length : keyPrices.filter(p => String(p.discount_percent) === value).length;
    return `<button type="button" class="model-group ${group === value ? 'active' : ''}" aria-pressed="${group === value}" onclick="setKeyGroup('${prefix}','${value}')">${value === 'all' ? '全部模型' : '便宜 ' + value + '%'} <span>${count}</span></button>`;
  }).join('');
  let visible = 0;
  const rows = models.map((model, index) => {
    const p = prices.get(model);
    const matches = model.toLowerCase().includes(query) && (group === 'all' || String(p?.discount_percent) === group);
    if (matches) visible++;
    const badge = p?.discount_percent ? `<span class="badge badge-green">便宜 ${esc(p.discount_percent)}% · 按 ${esc(100-p.discount_percent)}% 计费</span>` : '';
    const cells = ['input_per_million','output_per_million','cache_read_per_million','cache_write_per_million'].map(field => `<td>${modelPriceCell(p, field)}</td>`).join('');
    return `<tr ${matches ? '' : 'hidden'}><td><label class="key-choice" for="${prefix}-model-${index}"><input id="${prefix}-model-${index}" type="checkbox" value="${esc(model)}" ${selectedSet.has(model) ? 'checked' : ''} ${scope === 'selected' ? '' : 'disabled'} onchange="updateKeySelectionSummary('${prefix}')"><span><code>${esc(model)}</code>${published.has(model) ? '' : '（当前未上架）'}${badge}</span></label></td>${cells}</tr>`;
  }).join('');
  document.getElementById(prefix + '-models').innerHTML = `<table class="model-prices"><thead><tr><th>模型</th><th>输入</th><th>输出</th><th>缓存读</th><th>缓存写</th></tr></thead><tbody>${rows}</tbody></table>` + (visible ? '' : '<p class="empty">没有匹配的模型，可切换分组或修改搜索词。</p>');
  document.getElementById(prefix + '-model-help').textContent = scope === 'all' ? '此 Key 可调用全部可用模型。分组与搜索仅用于查看价格；如需限制权限，请选择“指定模型”。' : scope === 'none' ? '此 Key 已禁止调用模型；下面的价格仅供查看。' : '可跨组勾选模型；搜索和切换分组不会清除已选项。分组不改变同一模型的计费价格。';
  updateKeySelectionSummary(prefix);
}
function modelPriceCell(price, field) {
  if (!price || price[field] === undefined) return '<span class="muted">未公布</span>';
  const standard = price.standard_price?.[field];
  return `${price.discount_percent && standard !== undefined ? '<del>' + esc(standard) + '</del>' : ''}<span>${esc(price[field])}</span>`;
}
function setKeyGroup(prefix, group) {
  keyGroups[prefix] = group;
  renderKeyModels(prefix, selectedKeyModels(prefix));
}
function searchKeyModels(prefix) { renderKeyModels(prefix, selectedKeyModels(prefix)); }
function updateKeySelectionSummary(prefix) {
  const scope = document.getElementById(prefix + '-scope').value || 'all';
  document.getElementById(prefix + '-selection').textContent = scope === 'selected' ? `已选 ${selectedKeyModels(prefix).length} 个模型` : scope === 'none' ? '已禁止调用' : '全部可用模型';
}
function toggleKeyModels(prefix) {
  renderKeyModels(prefix, selectedKeyModels(prefix));
}
function keyPolicyInput(prefix) {
  const scope = document.getElementById(prefix + '-scope').value || 'all';
  const models = scope === 'selected' ? selectedKeyModels(prefix) : [];
  if (scope === 'selected' && models.length === 0) throw Error('请至少选择一个模型，或选择“禁止调用所有模型”。');
  return {model_scope: scope, allowed_models: models};
}
async function loadKeys() {
  const errorNode = document.getElementById('keys-error');
  errorNode.classList.add('hidden');
  try {
    const r = await api('/api-keys');
    keyRows = r.keys || [];
    const active = keyRows.filter(k => !k.revoked_at).length;
    document.getElementById('keys-count').textContent = `${active} / ${r.max_active_keys || 10} 个有效 Key`;
    document.getElementById('key-create').disabled = keyCreateBusy || active >= (r.max_active_keys || 10);
    document.getElementById('keys-list').innerHTML = keyRows.length ? keyRows.map((k, index) => `<tr>
      <td><code>${esc(k.key_prefix)}…</code></td><td>${esc(k.name || '未命名 Key')}</td><td class="key-models-cell">${esc(keyPolicyLabel(k))}</td>
      <td>${fmtTime(k.created_at)}</td><td>${k.last_used_at ? fmtTime(k.last_used_at) : '尚未使用'}</td>
      <td>${k.revoked_at ? '<span class="badge badge-red">已吊销</span>' : '<span class="badge badge-green">有效</span>'}</td>
      <td style="text-align:right">${k.revoked_at ? '' : `<button class="btn btn-sm" onclick="editKeyPolicy(${index})">配置</button> <button class="btn btn-danger btn-sm" onclick="revokeKeyAt(${index})">吊销</button>`}</td>
    </tr>`).join('') : '<tr><td colspan="7" class="empty">还没有 API Key。为你的第一个客户端创建密钥，开始接入。</td></tr>';
    try {
      const catalog = await api('/api-key-models');
      keyModelOptions = catalog.models || [];
      keyPrices = catalog.prices || [];
      renderKeyModels('key', selectedKeyModels('key'));
      if (editingKeyId) renderKeyModels('edit-key', selectedKeyModels('edit-key'));
    } catch(err) {
      keyPrices = []; keyModelOptions = [];
      renderKeyModels('key', selectedKeyModels('key'));
      if (editingKeyId) renderKeyModels('edit-key', selectedKeyModels('edit-key'));
      errorNode.textContent = '模型列表暂时无法读取，指定模型时请稍后重试。';
      errorNode.classList.remove('hidden');
    }
    if (!publicSite) await loadPublicSite();
    renderKeyExamples();
  } catch(err) {
    if (err.status === 401) { doLogout(); return; }
    errorNode.textContent = '暂时无法读取 API Key，请刷新重试。';
    errorNode.classList.remove('hidden');
  }
}
async function createKey() {
  if (keyCreateBusy) return;
  const button = document.getElementById('key-create');
  try {
    const policy = keyPolicyInput('key');
    keyCreateBusy = true; button.disabled = true;
    const name = document.getElementById('key-name').value;
    const r = await api('/api-keys', {method:'POST', body: JSON.stringify({name, ...policy})});
    document.getElementById('new-key-msg').innerHTML = `<div class="msg msg-ok">
      Key 已创建，请立即复制保存。离开此页面后无法再次查看明文。
      <div class="copy-wrap"><code id="new-key-val">${esc(r.key)}</code>
      <button class="btn btn-sm btn-primary" onclick="copyText('new-key-val')">复制</button></div></div>`;
    document.getElementById('key-name').value = '';
    await loadKeys();
  } catch(err) { document.getElementById('new-key-msg').innerHTML = `<div class="msg msg-error">${esc(err.message)}</div>`; }
  finally { keyCreateBusy = false; button.disabled = keyRows.filter(k => !k.revoked_at).length >= 10; }
}
function editKeyPolicy(index) {
  const key = keyRows[index];
  if (!key || key.revoked_at || keyPolicyBusy) return;
  editingKeyId = key.id;
  keyDialogOpener = document.activeElement;
  keyGroups['edit-key'] = 'all';
  document.getElementById('edit-key-search').value = '';
  document.getElementById('key-edit-title').textContent = '配置 Key · ' + (key.name || key.key_prefix);
  document.getElementById('edit-key-scope').value = key.model_scope || 'all';
  document.getElementById('key-edit-msg').textContent = '';
  renderKeyModels('edit-key', key.allowed_models || []);
  const dialog = document.getElementById('key-edit');
  dialog.classList.remove('hidden');
  if (!dialog.open) dialog.showModal();
  document.getElementById('edit-key-scope').focus();
}
function closeKeyPolicy(forNavigation = false) {
  if (keyPolicyBusy && !forNavigation) return;
  editingKeyId = null;
  const dialog = document.getElementById('key-edit');
  dialog.close();
  dialog.classList.add('hidden');
  keyDialogOpener?.focus();
  keyDialogOpener = null;
}
function cancelKeyPolicy(event) { event.preventDefault(); closeKeyPolicy(); }
async function saveKeyPolicy() {
  if (!editingKeyId || keyPolicyBusy) return;
  const button = document.getElementById('key-policy-save');
  try {
    const policy = keyPolicyInput('edit-key');
    keyPolicyBusy = true; button.disabled = true;
    document.getElementById('key-policy-fields').disabled = true;
    await api('/api-keys/' + encodeURIComponent(editingKeyId) + '/models', {method:'PUT', body:JSON.stringify(policy)});
    document.getElementById('key-edit-msg').textContent = '已保存，新请求将使用这些权限。';
    await loadKeys();
    if (editingKeyId) renderKeyModels('edit-key', policy.allowed_models);
  } catch(err) { document.getElementById('key-edit-msg').textContent = err.message; }
  finally { keyPolicyBusy = false; button.disabled = false; document.getElementById('key-policy-fields').disabled = false; }
}
function revokeKeyAt(index) {
  const key = keyRows[index];
  if (key && !key.revoked_at) return revokeKey(key.id);
}
async function revokeKey(id) {
  if (!confirm('吊销后，使用此 Key 的客户端将无法继续调用。确认吊销？')) return;
  try {
    await api('/api-keys/' + encodeURIComponent(id), {method:'DELETE'});
    if (editingKeyId === id) closeKeyPolicy();
    await loadKeys();
  } catch(err) { alert(err.message); }
}

// Examples deliberately contain placeholders, never the newly created key.
function exampleAPIBase() {
  try {
    const url = new URL(publicSite?.public_api_base);
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) return '';
    return url.href.replace(/\/+$/, '').replace(/(?:\/v1)+$/, '') + '/v1';
  } catch { return ''; }
}
function shellQuote(value) { return "'" + value.replaceAll("'", "'\\''") + "'"; }
function renderKeyExamples(language = exampleLanguage) {
  exampleLanguage = ['curl','python','nodejs','shell'].includes(language) ? language : 'curl';
  const base = exampleAPIBase() || 'https://YOUR_API_HOST/YOUR_API_PATH/v1';
  const chosen = document.getElementById('key-example-model').value;
  const model = keyModelOptions.includes(chosen) ? chosen : keyModelOptions[0] || 'MODEL_FROM_PRICE_LIST';
  document.getElementById('key-example-model').innerHTML = (keyModelOptions.length ? keyModelOptions : [model]).map(value => `<option value="${esc(value)}">${esc(value)}</option>`).join('');
  document.getElementById('key-example-model').value = model;
  document.getElementById('key-api-base').textContent = exampleAPIBase() || '运营方尚未配置 API Base，请先查看接入文档。';
  document.getElementById('key-examples-note').textContent = '使用有权调用所选模型的 Key；执行示例会按实际用量扣除 credits。密钥通过本机环境变量 CPA_API_KEY 读取。';
  document.querySelectorAll('[data-example-language]').forEach(button => {
    button.classList.toggle('active', button.dataset.exampleLanguage === exampleLanguage);
    button.setAttribute('aria-pressed', String(button.dataset.exampleLanguage === exampleLanguage));
  });
  const body = {model, messages:[{role:'user', content:'你好'}], max_tokens:64};
  const endpoint = base + '/chat/completions';
  const snippets = {
    curl: `# 先在本机设置 CPA_API_KEY 环境变量\ncurl --fail-with-body ${shellQuote(endpoint)} \\\n  -H "Authorization: Bearer \${CPA_API_KEY:?请先设置 CPA_API_KEY}" \\\n  -H 'Content-Type: application/json' \\\n  --data-raw ${shellQuote(JSON.stringify(body))}`,
    python: `# Python 3 · 使用标准库，无需安装依赖\nimport json\nimport os\nimport urllib.request\n\nbody = ${JSON.stringify(body,null,2)}\nrequest = urllib.request.Request(\n    ${JSON.stringify(endpoint)},\n    data=json.dumps(body).encode("utf-8"),\n    headers={\n        "Authorization": "Bearer " + os.environ["CPA_API_KEY"],\n        "Content-Type": "application/json",\n    },\n    method="POST",\n)\nwith urllib.request.urlopen(request) as response:\n    print(json.loads(response.read())["choices"][0]["message"]["content"])`,
    nodejs: `// Node.js 18+ · 保存为 request.mjs 后运行 node request.mjs\nconst key = process.env.CPA_API_KEY;\nif (!key) throw new Error("请先设置 CPA_API_KEY");\nconst response = await fetch(${JSON.stringify(endpoint)}, {\n  method: "POST",\n  headers: {\n    "Authorization": "Bearer " + key,\n    "Content-Type": "application/json",\n  },\n  body: JSON.stringify(${JSON.stringify(body,null,2)}),\n});\nif (!response.ok) throw new Error("HTTP " + response.status);\nconst result = await response.json();\nconsole.log(result.choices[0].message.content);`,
    shell: `#!/usr/bin/env bash\n# Bash · 依赖 curl 和 Python 3；交互输入不会显示密钥\nset -euo pipefail\nread -r -s -p 'API Key: ' CPA_API_KEY\nprintf '\\n'\nexport CPA_API_KEY\ntrap 'unset CPA_API_KEY' EXIT\nexport CPA_API_BASE=${shellQuote(base)}\nexport CPA_MODEL=${shellQuote(model)}\npython3 - <<'PY' | curl --fail-with-body "\${CPA_API_BASE}/chat/completions" \\\n  -H "Authorization: Bearer \${CPA_API_KEY}" \\\n  -H 'Content-Type: application/json' --data-binary @-\nimport json, os\nprint(json.dumps({"model": os.environ["CPA_MODEL"], "messages": [{"role": "user", "content": "你好"}], "max_tokens": 64}))\nPY`
  };
  document.getElementById('key-example-code').textContent = snippets[exampleLanguage];
  document.getElementById('key-example-copy-status').textContent = '';
}
async function copyKeyExample() {
  try {
    await navigator.clipboard.writeText(document.getElementById('key-example-code').textContent);
    document.getElementById('key-example-copy-status').textContent = '已复制';
  } catch { document.getElementById('key-example-copy-status').textContent = '复制失败，请手动选择代码复制。'; }
}

// ---- Topup ----
async function loadTopup() {
  try {
    const [methods, orders] = await Promise.all([api('/topup/methods'), api('/topup?limit=50')]);
    const grid = document.getElementById('topup-methods');
    if (!methods.methods.length) {
      grid.innerHTML = '<div class="msg msg-error">运营方未配置充值方式</div>';
    } else {
      grid.innerHTML = methods.methods.map(m => {
        const key = m.method + ':' + (m.network || '');
        const label = {'usdt':'USDT','wechat':'微信','alipay':'支付宝'}[m.method] || m.method;
        return `<div class="method-card" data-key="${esc(key)}" data-method="${esc(m.method)}" data-network="${esc(m.network||'')}" onclick="selectMethod(this)">
          <div class="name">${label}${m.network ? ' ' + esc(m.network) : ''}</div><div class="currency">${esc(m.currency)}</div></div>`;
      }).join('');
    }
    selectedMethod = null;
    document.getElementById('topup-form').classList.add('hidden');
    document.getElementById('topup-order-detail').classList.add('hidden');
    renderTopupOrders(orders.orders);
  } catch(err) { if (err.status === 401) doLogout(); }
}
function selectMethod(el) {
  document.querySelectorAll('.method-card').forEach(c => c.classList.remove('selected'));
  el.classList.add('selected');
  selectedMethod = {method: el.dataset.method, network: el.dataset.network};
  document.getElementById('topup-form').classList.remove('hidden');
  document.getElementById('topup-order-detail').classList.add('hidden');
}
async function createTopup() {
  if (!selectedMethod) return;
  const amount = parseFloat(document.getElementById('topup-amount').value);
  if (!amount || amount <= 0) return alert('请输入有效金额');
  try {
    const r = await api('/topup', {method:'POST', body: JSON.stringify({amount, method: selectedMethod.method, network: selectedMethod.network})});
    await loadTopup();
    showOrderDetail(r);
  } catch(err) { alert(err.message); }
}
function showOrderDetail(o) {
  const detail = document.getElementById('topup-order-detail');
  detail.classList.remove('hidden');
  let html = `<h3>订单 ${esc(o.id).slice(0,8)}…</h3>
    <p><strong>请支付：</strong><span style="font-size:1.3rem;font-weight:800;color:var(--accent-2)">${esc(o.amount)} ${esc(o.currency)}</span></p>
    <p style="color:var(--danger);font-size:.85rem;margin:.5rem 0">请务必按此精确金额支付</p>`;
  html += `<p>到账积分：<strong>${esc(o.points_amount || '待对账')}</strong> · 创建时兑换率：1 ${esc(o.currency)} = ${esc(o.exchange_rate || '待对账')} 积分</p>`;
  if (o.wallet_address && !o.wallet_address.startsWith('http'))
    html += `<div class="copy-wrap"><code id="order-addr">${esc(o.wallet_address)}</code><button class="btn btn-sm btn-primary" onclick="copyText('order-addr')">复制</button></div>`;
  if (o.qr_code_url) html += `<img class="qr-img" src="${esc(o.qr_code_url)}" alt="收款码">`;
  if (o.notes) html += `<p class="hint">${esc(o.notes)}</p>`;
  if (o.method === 'usdt' && o.status === 'pending')
    html += `<div style="margin-top:1rem"><label class="field-label">提交 TX Hash</label>
      <div class="copy-wrap"><input id="submit-txhash" placeholder="粘贴交易哈希">
      <button class="btn btn-sm btn-success" onclick="submitTxHash('${o.id}')">提交</button></div></div>`;
  html += `<p style="margin-top:.6rem;font-size:.85rem">状态：${statusBadge(o.status)}</p>`;
  detail.innerHTML = html;
}
async function submitTxHash(id) {
  const hash = document.getElementById('submit-txhash').value.trim();
  if (!hash) return alert('请输入 TX Hash');
  try { await api('/topup/' + id + '/submit', {method:'POST', body:JSON.stringify({tx_hash:hash})}); alert('已提交，等待确认'); loadTopup(); }
  catch(err) { alert(err.message); }
}
async function cancelOrder(id) {
  if (!confirm('确认取消此订单？')) return;
  try { await api('/topup/' + id + '/cancel', {method:'POST'}); loadTopup(); } catch(err) { alert(err.message); }
}
function renderTopupOrders(orders) {
  const tbody = document.getElementById('topup-list');
  tbody.innerHTML = orders.length ? orders.map(o => `<tr>
    <td>${fmtTime(o.created_at)}</td><td>${esc(o.method)}${o.network ? ' ' + esc(o.network) : ''}</td>
    <td>${esc(o.amount)} ${esc(o.currency)}<div class="hint">${esc(o.points_amount || '待对账')} 积分</div></td><td>${statusBadge(o.status)}</td>
    <td style="text-align:right">${o.status === 'pending' || o.status === 'submitted' ?
      `<button class="btn btn-sm btn-ghost" onclick="viewOrder('${o.id}')">查看</button>
       <button class="btn btn-danger btn-sm" onclick="cancelOrder('${o.id}')">取消</button>` : ''}</td>
  </tr>`).join('') : '<tr><td colspan="5" class="empty">暂无记录</td></tr>';
}
async function viewOrder(id) { try { showOrderDetail(await api('/topup/' + id)); } catch(err) { alert(err.message); } }

// ---- Usage ----
let usageShowUser = false;
let usageFilters = {model:'', auth_id:'', user_id:''};
let usageLoadedCount = 0;

// Populate the filter dropdowns with the distinct values present in the caller's
// visible usage. Shows the user filter only for admins (scope=all).
async function loadUsageFilterOptions() {
  try {
    const o = await api('/usage/filters');
    const modelSel = document.getElementById('uf-model');
    const acctSel = document.getElementById('uf-account');
    modelSel.innerHTML = '<option value="">全部模型</option>' +
      (o.models || []).map(m => `<option value="${esc(m)}">${esc(m)}</option>`).join('');
    acctSel.innerHTML = '<option value="">全部账号</option>' +
      (o.accounts || []).map(a => `<option value="${esc(a.id)}">${esc(a.label)}</option>`).join('');
    document.getElementById('uf-account-box').classList.toggle('hidden', o.scope !== 'all');
    const userBox = document.getElementById('uf-user-box');
    if (o.scope === 'all') {
      userBox.style.display = '';
      document.getElementById('uf-user').innerHTML = '<option value="">全部用户</option>' +
        (o.users || []).map(u => `<option value="${esc(u.id)}">${esc(u.email || u.id)}</option>`).join('');
    } else {
      userBox.style.display = 'none';
    }
    // Restore any active selections after repopulating.
    modelSel.value = usageFilters.model;
    acctSel.value = usageFilters.auth_id;
    const us = document.getElementById('uf-user');
    if (us) us.value = usageFilters.user_id;
  } catch(err) { if (err.status === 401) doLogout(); }
}

function applyUsageFilters() {
  usageFilters.model = document.getElementById('uf-model').value;
  usageFilters.auth_id = document.getElementById('uf-account').value;
  const us = document.getElementById('uf-user');
  usageFilters.user_id = us ? us.value : '';
  loadUsage(true);
}

function resetUsageFilters() {
  usageFilters = {model:'', auth_id:'', user_id:''};
  document.getElementById('uf-model').value = '';
  document.getElementById('uf-account').value = '';
  const us = document.getElementById('uf-user'); if (us) us.value = '';
  loadUsage(true);
}

async function loadUsage(reset) {
  const tbody = document.getElementById('usage-list');
  if (reset) { usageCursor = ''; usageLoadedCount = 0; tbody.innerHTML = ''; }
  try {
    let url = '/usage?limit=50';
    if (usageCursor) url += '&before=' + encodeURIComponent(usageCursor);
    if (usageFilters.model) url += '&model=' + encodeURIComponent(usageFilters.model);
    if (usageFilters.auth_id) url += '&auth_id=' + encodeURIComponent(usageFilters.auth_id);
    if (usageFilters.user_id) url += '&user_id=' + encodeURIComponent(usageFilters.user_id);
    const r = await api(url);
    const records = (r && r.records) || [];
    usageShowUser = !!(r && r.scope === 'all');
    const cols = usageShowUser ? 11 : 9;
    if (reset) {
      document.getElementById('usage-thead').innerHTML =
        `<th>时间</th>${usageShowUser ? '<th>用户</th>' : ''}<th>提供商</th><th>模型</th>${usageShowUser ? '<th>上游账号</th>' : ''}<th>输入</th><th>输出</th><th>缓存读</th><th>缓存写</th><th>费用（积分）</th><th>状态</th>`;
    }
    const countEl = document.getElementById('uf-count');
    if (!records.length) {
      if (reset) tbody.innerHTML = `<tr><td colspan="${cols}" class="empty">暂无记录</td></tr>`;
      if (countEl && reset) countEl.textContent = '共 0 条';
      document.getElementById('usage-more').classList.add('hidden'); return;
    }
    document.getElementById('usage-more').classList.remove('hidden');
    tbody.innerHTML += records.map(u => `<tr>
      <td>${fmtTime(u.created_at)}</td>${usageShowUser ? `<td>${esc(u.user_email || '-')}</td>` : ''}<td>${esc(u.provider)}</td><td>${esc(u.model)}</td>
      ${usageShowUser ? `<td>${esc(u.auth_label || u.auth_id || '-')}</td>` : ''}
      <td>${u.input_tokens}</td><td>${u.output_tokens}</td><td>${u.cache_read_tokens}</td><td>${u.cache_write_tokens}</td>
      <td>${u.unit === 'LEGACY' ? '历史货币 · 待对账' : parseFloat(u.cost).toFixed(6)}</td><td>${statusBadge(u.status)}</td></tr>`).join('');
    usageCursor = records[records.length - 1].created_at;
    usageLoadedCount += records.length;
    if (countEl) countEl.textContent = `已显示 ${usageLoadedCount} 条${records.length < 50 ? '' : '+'}`;
    if (records.length < 50) document.getElementById('usage-more').classList.add('hidden');
  } catch(err) {
    if (err.status === 401) { doLogout(); return; }
    if (reset) tbody.innerHTML = `<tr><td colspan="11" class="empty">加载失败：${esc(err.message || '未知错误')}</td></tr>`;
    document.getElementById('usage-more').classList.add('hidden');
  }
}
function loadMoreUsage() { loadUsage(false); }

async function loadUsageStats() {
  try {
    const r = await api('/usage/stats?days=30');
    const container = document.getElementById('usage-charts');
    container.innerHTML = '';
    if (r.daily && r.daily.length > 0) {
      const maxCost = Math.max(...r.daily.map(d => parseFloat(d.cost)), 0.001);
      const barW = Math.max(4, Math.floor(500 / r.daily.length) - 2), chartH = 120;
      const bars = r.daily.map((d, i) => {
        const h = Math.max(1, (parseFloat(d.cost) / maxCost) * (chartH - 20));
        return `<rect x="${i*(barW+2)}" y="${chartH-h}" width="${barW}" height="${h}" fill="var(--faint)" rx="2"><title>${d.date}: ${parseFloat(d.cost).toFixed(4)}</title></rect>`;
      }).join('');
      container.innerHTML += `<div class="stat-card usage-chart">
        <div class="label">近 30 天日消费</div>
        <div style="overflow-x:auto;margin-top:.5rem"><svg width="${r.daily.length*(barW+2)}" height="${chartH}" style="display:block">
          ${bars}</svg></div>
        <div style="display:flex;justify-content:space-between;font-size:.7rem;color:var(--muted);margin-top:.25rem">
          <span>${r.daily[0]?.date?.slice(5)||''}</span><span>${r.daily[r.daily.length-1]?.date?.slice(5)||''}</span></div></div>`;
    }
    if (r.by_model && r.by_model.length > 0) {
      const total = r.by_model.reduce((s, m) => s + parseFloat(m.cost), 0) || 1;
      const colors = ['#525252','#737373','#8f8f8f','#a3a3a3','#b8b8b8','#6b7280','#9198a1','#c2c6cc'];
      let html = '<div class="label">模型分布 (近 30 天)</div><div style="margin-top:.5rem">';
      r.by_model.forEach((m, i) => {
        const pct = (parseFloat(m.cost) / total * 100).toFixed(1);
        html += `<div style="display:flex;align-items:center;gap:.5rem;margin:.35rem 0;font-size:.82rem">
          <span style="width:9px;height:9px;border-radius:50%;background:${colors[i%colors.length]};flex-shrink:0"></span>
          <span style="flex:1;overflow:hidden;text-overflow:ellipsis">${esc(m.model)}</span>
          <span class="muted">${pct}%</span><span style="font-weight:700">${parseFloat(m.cost).toFixed(4)}</span></div>`;
      });
      container.innerHTML += `<div class="stat-card">${html}</div></div>`;
    }
  } catch(err) { /* non-critical */ }
}

// ---- Settings ----
async function doChangePassword(e) {
  e.preventDefault();
  try {
    await api('/change-password', {method:'POST', body: JSON.stringify({
      old_password: document.getElementById('pw-old').value,
      new_password: document.getElementById('pw-new').value
    })});
    document.getElementById('pw-msg').innerHTML = '<div class="msg msg-ok">密码已修改</div>';
    document.getElementById('pw-old').value = ''; document.getElementById('pw-new').value = '';
  } catch(err) { document.getElementById('pw-msg').innerHTML = `<div class="msg msg-error">${esc(err.message)}</div>`; }
}

// ---- Admin ----
async function loadAdmin() {
  if (!isAdmin) return;
  loadCommerceAdmin();
  try {
    const [ordersR, usersR] = await Promise.all([api('/admin/topup?limit=100'), api('/admin/users')]);
    const active = ordersR.orders.filter(o => o.status === 'pending' || o.status === 'submitted');
    document.getElementById('admin-orders').innerHTML = active.length ? active.map(o => `<tr>
      <td>${fmtTime(o.created_at)}</td><td><code>${esc(o.user_id).slice(0,8)}…</code></td>
      <td>${esc(o.method)}${o.network ? ' '+esc(o.network) : ''}</td><td>${esc(o.amount)} ${esc(o.currency)}</td>
      <td>${o.tx_hash ? '<code>'+esc(o.tx_hash).slice(0,16)+'…</code>' : '-'}</td><td>${statusBadge(o.status)}</td>
      <td style="text-align:right"><button class="btn btn-success btn-sm" onclick="adminConfirm('${o.id}')">确认</button></td>
    </tr>`).join('') : '<tr><td colspan="7" class="empty">无待确认订单</td></tr>';

    adminUsers = usersR.users;
    document.getElementById('admin-users').innerHTML = adminUsers.map(u => `<tr>
      <td>${esc(u.email)}${u.is_admin?' <span class="badge badge-violet">admin</span>':''}</td>
      <td>${u.balance_unit === 'LEGACY' ? '待对账' : parseFloat(u.balance||'0').toFixed(6)}</td>
      <td>${u.unbilled ? '<span class="badge badge-violet">免余额</span>' : '<span class="badge badge-gray">计费</span>'}</td>
      <td>${limitSummary(u)}</td>
      <td>${fmtTime(u.created_at)}</td>
      <td style="text-align:right">
        <button class="btn btn-sm btn-ghost" onclick="openUserModal('${u.id}')">管理</button>
        <button class="btn btn-sm" onclick="document.getElementById('credit-uid').value='${u.id}'">选中</button></td>
    </tr>`).join('');
    loadAdminAccounts();
  } catch(err) { if (err.status === 403) isAdmin = false; }
}
async function loadAdminAccounts() {
  try {
    const accounts = (await api('/admin/accounts')).accounts || [];
    document.getElementById('admin-accounts').innerHTML = accounts.length ? accounts.map(a => `<tr>
      <td>${esc(a.label || a.id)}</td><td>${esc(a.provider)}</td>
      <td>${a.status && a.status!=='active' ? '<span class="badge badge-yellow">'+esc(a.status)+'</span>' : '<span class="badge badge-green">active</span>'}</td>
      <td><input value="${esc(a.alias||'').replace(/"/g,'&quot;')}" data-id="${esc(a.id)}" placeholder="不填=Provider·短ID" style="min-width:150px"></td>
      <td style="text-align:right"><button class="btn btn-sm btn-primary" onclick="saveAlias(this)">保存</button></td>
    </tr>`).join('') : '<tr><td colspan="5" class="empty">暂无上游账号</td></tr>';
  } catch(e) {}
}
async function saveAlias(btn) {
  const input = btn.closest('tr').querySelector('input[data-id]');
  try {
    await api('/admin/accounts/alias', {method:'POST', body: JSON.stringify({id: input.dataset.id, label: input.value.trim()})});
    const o = btn.textContent; btn.textContent = '已保存'; setTimeout(() => btn.textContent = o, 1500);
  } catch(err) { alert(err.message); }
}
function limitSummary(u) {
  const m = (u.allowed_models||[]).length, a = (u.allowed_auth_ids||[]).length;
  const parts = [];
  if (m) parts.push(`<span class="badge badge-blue">${m} 个模型</span>`);
  if (a) parts.push(`<span class="badge badge-violet">${a} 个认证文件</span>`);
  if (parts.length) return parts.join(' ');
  return '<span class="muted">全部</span>';
}
async function adminConfirm(id) {
  if (!confirm('请先核对实际到账金额、付款人及订单归属，不能只凭 TX Hash。确认后立即按订单快照增加积分。')) return;
  try { await api('/admin/topup/' + id + '/confirm', {method:'POST', body:'{}'}); loadAdmin(); } catch(err) { alert(err.message); }
}
async function doAdminCredit() {
  const uid = document.getElementById('credit-uid').value.trim();
  const amount = parseFloat(document.getElementById('credit-amount').value);
  const note = document.getElementById('credit-note').value.trim();
  if (!uid || !amount) return alert('请填写用户 ID 和金额');
  try {
    const r = await api('/admin/credit', {method:'POST', body:JSON.stringify({user_id:uid, amount, note})});
    document.getElementById('credit-msg').innerHTML = `<div class="msg msg-ok">已更新，新余额：${r.balance}</div>`;
    loadAdmin();
  } catch(err) { document.getElementById('credit-msg').innerHTML = `<div class="msg msg-error">${esc(err.message)}</div>`; }
}

// ---- Admin: per-user limits modal ----
async function openUserModal(id) {
  const u = adminUsers.find(x => x.id === id);
  if (!u) return;
  editingUserId = id;
  document.getElementById('um-email').textContent = u.email;
  document.getElementById('um-unbilled').checked = !!u.unbilled;
  document.getElementById('um-allowreset').checked = !!u.allow_reset;
  document.getElementById('um-msg').innerHTML = '';
  umTab('models');
  // Load the pickers once (models + accounts), then check the user's current sets.
  try {
    if (!allModels.length) allModels = (await api('/admin/models')).models || [];
    if (!allAccounts.length) allAccounts = (await api('/admin/accounts')).accounts || [];
  } catch(e) {}
  const selM = new Set(u.allowed_models || []), selA = new Set(u.allowed_auth_ids || []);
  document.getElementById('um-model-search').value = '';
  document.getElementById('um-model-list').innerHTML = allModels.length ? allModels.map(m =>
    `<label class="picker-item" data-name="${esc(m.toLowerCase())}"><input type="checkbox" value="${esc(m)}" ${selM.has(m)?'checked':''}><span>${esc(m)}</span></label>`
  ).join('') : '<div class="empty">暂无模型（先在管理控制台添加上游账号）</div>';
  document.getElementById('um-account-list').innerHTML = allAccounts.length ? allAccounts.map(a =>
    `<label class="picker-item"><input type="checkbox" value="${esc(a.id)}" ${selA.has(a.id)?'checked':''}>
     <span>${esc(a.label||a.id)} <span class="muted">· ${esc(a.provider)}${a.status&&a.status!=='active'?' ('+esc(a.status)+')':''}</span></span></label>`
  ).join('') : '<div class="empty">暂无上游账号</div>';
  document.getElementById('user-modal').classList.remove('hidden');
}
function closeUserModal() { document.getElementById('user-modal').classList.add('hidden'); editingUserId = null; }
function umTab(which) {
  document.querySelectorAll('.tab-sm').forEach(t => t.classList.toggle('active', t.dataset.tab === which));
  document.getElementById('um-pane-models').classList.toggle('hidden', which !== 'models');
  document.getElementById('um-pane-accounts').classList.toggle('hidden', which !== 'accounts');
}
function filterModels(q) {
  q = q.trim().toLowerCase();
  document.querySelectorAll('#um-model-list .picker-item').forEach(el =>
    el.style.display = (!q || el.dataset.name.includes(q)) ? '' : 'none');
}
async function saveUserLimits() {
  if (!editingUserId) return;
  const models = [...document.querySelectorAll('#um-model-list input:checked')].map(i => i.value);
  const accounts = [...document.querySelectorAll('#um-account-list input:checked')].map(i => i.value);
  const unbilled = document.getElementById('um-unbilled').checked;
  const allow_reset = document.getElementById('um-allowreset').checked;
  try {
    await api('/admin/users/' + editingUserId + '/limits', {method:'POST',
      body: JSON.stringify({unbilled, allowed_models: models, allowed_auth_ids: accounts, allow_reset})});
    closeUserModal(); loadAdmin();
  } catch(err) { document.getElementById('um-msg').innerHTML = `<div class="msg msg-error">${esc(err.message)}</div>`; }
}

// ---- Helpers ----
async function loadCommerceAdmin() {
  try {
    const [settings, pricing] = await Promise.all([api('/admin/settings'), api('/admin/pricing')]);
    commerceSettings = settings;
    pointPrices = pricing.prices || [];
    for (const [id, key] of Object.entries({'cs-name':'site_name','cs-base':'public_api_base','cs-docs':'docs_url','cs-cny':'cny_points_per_unit','cs-usdt':'usdt_points_per_unit','cs-model':'helper_model','cs-limit':'helper_daily_limit','cs-global-limit':'helper_global_daily_limit','cs-output':'helper_max_output_tokens'})) document.getElementById(id).value = settings[key] ?? '';
    document.getElementById('cs-helper').checked = !!settings.helper_enabled;
    document.getElementById('admin-prices').innerHTML = pointPrices.map((p, i) => `<tr><td>${esc(p.model)}</td><td>${esc(p.input_per_million)}</td><td>${esc(p.output_per_million)}</td><td>${esc(p.cache_read_per_million)}</td><td>${esc(p.cache_write_per_million)}</td><td>${p.enabled ? '上架' : '停用'} · v${p.version}</td><td><button class="btn btn-sm" onclick="editPointPrice(${i})">编辑</button></td></tr>`).join('') || '<tr><td colspan="7">尚未定价</td></tr>';
    document.getElementById('commerce-msg').textContent = '';
  } catch(err) { document.getElementById('commerce-msg').textContent = err.message; }
}
async function saveCommerceSettings(e) {
  e.preventDefault();
  if (!commerceSettings) return;
  const settings = {...commerceSettings};
  for (const [id, key] of Object.entries({'cs-name':'site_name','cs-base':'public_api_base','cs-docs':'docs_url','cs-cny':'cny_points_per_unit','cs-usdt':'usdt_points_per_unit','cs-model':'helper_model'})) settings[key] = document.getElementById(id).value.trim();
  settings.helper_daily_limit = Number(document.getElementById('cs-limit').value);
  settings.helper_global_daily_limit = Number(document.getElementById('cs-global-limit').value);
  settings.helper_max_output_tokens = Number(document.getElementById('cs-output').value);
  settings.helper_enabled = document.getElementById('cs-helper').checked;
  try {
    commerceSettings = await api('/admin/settings', {method:'PUT', body:JSON.stringify(settings)});
    document.getElementById('commerce-msg').textContent = '站点配置已保存。';
    await loadPublicSite();
  } catch(err) { document.getElementById('commerce-msg').textContent = err.status === 409 ? '配置已被其他管理员更新，请刷新后重试。' : err.message; }
}
function selectPriceVersion() {
  const model = document.getElementById('price-model').value.trim().toLowerCase();
  priceVersion = pointPrices.find(p => p.model.toLowerCase() === model)?.version || 0;
}
function editPointPrice(i) {
  const p = pointPrices[i];
  if (!p) return;
  document.getElementById('price-model').value = p.model;
  const rates = p.standard_price || p;
  for (const [id, key] of Object.entries({'price-input':'input_per_million','price-output':'output_per_million','price-read':'cache_read_per_million','price-write':'cache_write_per_million'})) document.getElementById(id).value = rates[key];
  document.getElementById('price-discount').value = p.discount_percent || 0;
  document.getElementById('price-enabled').checked = p.enabled;
  priceVersion = p.version;
}
async function savePointPrice(e) {
  e.preventDefault();
  const p = {model:document.getElementById('price-model').value.trim(), version:priceVersion, enabled:document.getElementById('price-enabled').checked};
  for (const [id, key] of Object.entries({'price-input':'input_per_million','price-output':'output_per_million','price-read':'cache_read_per_million','price-write':'cache_write_per_million'})) p[key] = document.getElementById(id).value.trim();
  p.discount_percent = Number(document.getElementById('price-discount').value);
  if (p.discount_percent > 0) p.standard_price = {input_per_million:p.input_per_million,output_per_million:p.output_per_million,cache_read_per_million:p.cache_read_per_million,cache_write_per_million:p.cache_write_per_million};
  try {
    const saved = await api('/admin/pricing', {method:'PUT', body:JSON.stringify(p)});
    priceVersion = saved.version;
    await loadCommerceAdmin();
    await loadPublicSite();
    document.getElementById('price-msg').textContent = '积分价格已保存。';
  } catch(err) { document.getElementById('price-msg').textContent = err.status === 409 ? '价格版本已变化，请重新载入并编辑。' : err.message; }
}

const credentialRE = /(?:cpk_[a-z0-9_-]{12,}|sk-[a-z0-9_-]{12,}|bearer\s+[a-z0-9._-]{12,})/gi;
function redactCredentials(text) { return text.replace(credentialRE, 'YOUR_API_KEY'); }
function clearAssistant() {
  if (assistantBusy) return;
  assistantMessages = [];
  document.getElementById('assistant-chat').innerHTML = '';
  document.getElementById('assistant-error').textContent = '';
}
async function loadAssistant() {
  assistantReady = false;
  document.getElementById('assistant-send').disabled = true;
  const site = await loadPublicSite();
  document.getElementById('assistant-docs').href = documentationLink(site?.docs_url);
  assistantReady = !!site?.helper_enabled;
  document.getElementById('assistant-info').textContent = assistantReady ? '配置助手由运营方提供，每日次数和输出长度有限。回答仅供参考，请核对客户端文档。' : '配置助手暂未开放，你可以先查阅接入文档。';
  document.getElementById('assistant-send').disabled = !assistantReady || assistantBusy;
}
function renderAssistant() {
  document.getElementById('assistant-chat').innerHTML = assistantMessages.map(m => `<div class="chat-line ${m.role === 'user' ? 'user' : 'assistant'}"><strong>${m.role === 'user' ? '你' : '配置助手'}</strong>\n${esc(m.content)}</div>`).join('');
}
async function sendAssistant(e) {
  e.preventDefault();
  if (!assistantReady || assistantBusy) return;
  const input = document.getElementById('assistant-input');
  const question = redactCredentials(input.value.trim());
  if (!question) return;
  const messages = [...assistantMessages.slice(-6).map(m => ({...m, content:Array.from(m.content).slice(0, 2000).join('')})), {role:'user', content:question}];
  while (messages.length > 1 && messages.reduce((n, m) => n + new TextEncoder().encode(m.content).length, 0) > 11000) messages.splice(0, 2);
  assistantBusy = true;
  document.getElementById('assistant-send').disabled = true;
  document.getElementById('assistant-error').textContent = '正在生成配置建议…';
  try {
    const response = await api('/assistant', {method:'POST', body:JSON.stringify({messages})});
    assistantMessages = [...messages, {role:'assistant', content:redactCredentials(response.reply)}];
    input.value = '';
    renderAssistant();
    document.getElementById('assistant-error').textContent = '';
  } catch(err) { document.getElementById('assistant-error').textContent = err.message; }
  finally { assistantBusy = false; document.getElementById('assistant-send').disabled = !assistantReady; }
}

function fmtTime(s) {
  if (!s) return '-';
  const d = new Date(s), pad = n => String(n).padStart(2,'0');
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function esc(s) { if (s === 0) return '0'; if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML.replaceAll('"', '&quot;').replaceAll("'", '&#39;'); }
function statusBadge(s) {
  const m = {success:'badge-green',active:'badge-green',pending:'badge-yellow',submitted:'badge-blue',confirmed:'badge-green',error:'badge-red',cancelled:'badge-gray',expired:'badge-gray',revoked:'badge-red'};
  return `<span class="badge ${m[s]||'badge-gray'}">${esc(s)}</span>`;
}
async function copyText(id) {
  const el = document.getElementById(id), text = el.textContent || el.value;
  try {
    await navigator.clipboard.writeText(text);
    const btn = el.parentElement.querySelector('.btn');
    if (btn) { const original = btn.textContent; btn.textContent = '已复制'; setTimeout(() => btn.textContent = original, 1500); }
  } catch { alert('复制失败，请手动选择并复制。'); }
}

// ---- Init ----
// Reveal email-code login only when the server has SMTP configured.
api('/config').then(cfg => {
  siteMode = cfg?.site_mode || siteMode;
  registrationEnabled = cfg?.registration_enabled ?? (siteMode !== 'privileged');
  document.getElementById('tab-register').classList.toggle('hidden', !registrationEnabled);
  document.getElementById('auth-home').classList.toggle('hidden', siteMode === 'privileged');
  document.getElementById('auth-title').textContent = siteMode === 'privileged' ? '特权账户登录' : 'TrueCore AI';
  if (siteMode === 'privileged' && !token) showAuth('login');
  if (cfg && cfg.email_login) {
    document.getElementById('tab-code').classList.remove('hidden');
    document.getElementById('alt-code').classList.remove('hidden');
  }
}).catch(() => {});

updateConsoleLinks();
if (siteMode !== 'privileged') loadPublicSite();
function showInitialPage() {
  if (siteMode === 'privileged') showAuth('login');
  else if (CLEAN_ROUTES && location.pathname === '/register') showAuth('register');
  else if (CLEAN_ROUTES && (location.pathname === '/login' || location.pathname.startsWith('/console'))) showAuth('login');
  else showHome();
}
if (token) api('/me').then(() => {
  if (CLEAN_ROUTES && location.pathname === '/') showHome(); else enterApp();
}).catch(() => { token = null; localStorage.removeItem('portal_token'); showInitialPage(); });
else showInitialPage();
