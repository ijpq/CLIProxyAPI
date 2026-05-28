package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// serveRequestLogsPage returns a self-contained HTML page that lists all
// per-request log files via the /v0/management/request-logs API and lets the
// user inspect each file's contents via /v0/management/request-log-by-id/:id.
//
// The page is gated identically to /management.html: disabled when the Home
// mode is active or the control panel has been turned off, and only served
// when a config is loaded.
func (s *Server) serveRequestLogsPage(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(requestLogsPageHTML))
}

const requestLogsPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>CLI Proxy API - Request Logs</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body { font: 14px/1.4 system-ui, -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 16px; }
header { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 12px; }
header h1 { font-size: 18px; margin: 0 12px 0 0; }
input, button, select { font: inherit; padding: 6px 10px; }
input[type=password], input[type=text] { min-width: 220px; }
button { cursor: pointer; }
.main { display: grid; grid-template-columns: minmax(380px, 1fr) 2fr; gap: 12px; align-items: stretch; height: calc(100vh - 100px); }
.list-pane, .detail-pane { border: 1px solid #8884; border-radius: 4px; overflow: auto; min-height: 0; }
.list-pane table { width: 100%; border-collapse: collapse; }
.list-pane th, .list-pane td { padding: 6px 8px; border-bottom: 1px solid #8882; text-align: left; vertical-align: top; }
.list-pane th { position: sticky; top: 0; background: Canvas; cursor: default; }
.list-pane tr.row { cursor: pointer; }
.list-pane tr.row:hover { background: #8881; }
.list-pane tr.row.active { background: #8883; }
.badge { display: inline-block; padding: 1px 6px; border-radius: 10px; font-size: 11px; }
.badge.err { background: #d95; color: #000; }
.badge.ok { background: #6c9; color: #000; }
.detail-pane pre { margin: 0; padding: 12px; white-space: pre-wrap; word-break: break-all; }
.muted { color: #888; }
.empty { padding: 24px; text-align: center; color: #888; }
.error { color: #c33; padding: 8px 0; }
.toolbar { display: flex; gap: 6px; align-items: center; padding: 6px 8px; border-bottom: 1px solid #8882; background: Canvas; position: sticky; top: 0; }
.toolbar .title { font-weight: 600; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
</head>
<body>
<header>
  <h1>Request Logs</h1>
  <input id="key" type="password" placeholder="Management key" autocomplete="off">
  <button id="login">Load</button>
  <button id="refresh">Refresh</button>
  <label class="muted"><input id="errOnly" type="checkbox"> errors only</label>
  <span id="status" class="muted"></span>
</header>
<div id="err" class="error"></div>
<div class="main">
  <div class="list-pane">
    <table>
      <thead><tr><th>When</th><th>Endpoint</th><th>ID</th><th>Size</th></tr></thead>
      <tbody id="rows"></tbody>
    </table>
    <div id="listEmpty" class="empty" hidden>No request logs found.</div>
  </div>
  <div class="detail-pane">
    <div class="toolbar"><span class="title" id="detailTitle">Select a row to view contents</span></div>
    <pre id="detail" class="muted">(nothing selected)</pre>
  </div>
</div>
<script>
(function(){
  const $ = (id) => document.getElementById(id);
  const KEY_STORE = 'cpa.mgmt.key';
  const keyInput = $('key');
  keyInput.value = sessionStorage.getItem(KEY_STORE) || '';

  let allFiles = [];
  let activeID = null;

  function setStatus(msg) { $('status').textContent = msg || ''; }
  function setError(msg) { $('err').textContent = msg || ''; }
  function authHeader() {
    const k = keyInput.value.trim();
    return k ? { 'Authorization': 'Bearer ' + k } : {};
  }
  function fmtSize(n) {
    if (n < 1024) return n + ' B';
    if (n < 1024*1024) return (n/1024).toFixed(1) + ' KB';
    return (n/1024/1024).toFixed(2) + ' MB';
  }
  function fmtTime(ts) {
    const d = new Date(ts * 1000);
    return d.toISOString().replace('T', ' ').replace(/\.\d+Z$/, '');
  }
  function endpointOf(name) {
    // strip "error-" prefix, trailing "-{ts}-{id}.log"
    let s = name.replace(/^error-/, '');
    s = s.replace(/-\d{4}-\d{2}-\d{2}T\d{6}-[0-9a-fA-F]+\.log$/, '');
    return s.replace(/-/g, '/');
  }

  async function fetchJSON(path) {
    const r = await fetch(path, { headers: authHeader() });
    if (!r.ok) {
      const t = await r.text().catch(() => '');
      throw new Error(r.status + ' ' + r.statusText + (t ? ': ' + t : ''));
    }
    return r.json();
  }

  async function load() {
    setError('');
    setStatus('loading...');
    try {
      const data = await fetchJSON('/v0/management/request-logs');
      allFiles = data.files || [];
      render();
      setStatus(allFiles.length + ' file(s)');
      sessionStorage.setItem(KEY_STORE, keyInput.value.trim());
    } catch (e) {
      setStatus('');
      setError(e.message);
    }
  }

  function render() {
    const rows = $('rows');
    rows.innerHTML = '';
    const errOnly = $('errOnly').checked;
    const list = errOnly ? allFiles.filter(f => f.is_error) : allFiles;
    if (list.length === 0) {
      $('listEmpty').hidden = false;
      return;
    }
    $('listEmpty').hidden = true;
    const frag = document.createDocumentFragment();
    list.forEach(f => {
      const tr = document.createElement('tr');
      tr.className = 'row' + (f.request_id === activeID ? ' active' : '');
      tr.dataset.id = f.request_id;
      const badge = f.is_error ? '<span class="badge err">err</span> ' : '<span class="badge ok">ok</span> ';
      tr.innerHTML =
        '<td>' + fmtTime(f.modified) + '</td>' +
        '<td>' + badge + '<code>' + endpointOf(f.name) + '</code></td>' +
        '<td><code>' + f.request_id + '</code></td>' +
        '<td>' + fmtSize(f.size) + '</td>';
      tr.addEventListener('click', () => openLog(f));
      frag.appendChild(tr);
    });
    rows.appendChild(frag);
  }

  async function openLog(f) {
    setError('');
    activeID = f.request_id;
    document.querySelectorAll('tr.row').forEach(r => r.classList.toggle('active', r.dataset.id === activeID));
    $('detailTitle').textContent = f.name;
    $('detail').textContent = 'loading...';
    $('detail').className = 'muted';
    try {
      const r = await fetch('/v0/management/request-log-by-id/' + encodeURIComponent(f.request_id), { headers: authHeader() });
      if (!r.ok) {
        const t = await r.text().catch(() => '');
        throw new Error(r.status + ' ' + r.statusText + (t ? ': ' + t : ''));
      }
      const text = await r.text();
      $('detail').textContent = text;
      $('detail').className = '';
    } catch (e) {
      $('detail').textContent = e.message;
      $('detail').className = 'error';
    }
  }

  $('login').addEventListener('click', load);
  $('refresh').addEventListener('click', load);
  $('errOnly').addEventListener('change', render);
  keyInput.addEventListener('keydown', e => { if (e.key === 'Enter') load(); });

  if (keyInput.value) load();
})();
</script>
</body>
</html>
`
