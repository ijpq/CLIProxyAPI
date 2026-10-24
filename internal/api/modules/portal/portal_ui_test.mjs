import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import vm from 'node:vm';

// Run the actual application script with a small deterministic DOM adapter.
// This covers serialization and async UI state; it is not a browser layout test.
function app(pathname = '/private-prefix/portal/ui/', options = {}) {
  const selectors = new Map(), alerts = [], events = {}, timers = [], stored = new Map();
  if (options.token) stored.set('portal_token', options.token);
  const nodes = new Map();
  const element = () => {
    const classes = new Set();
    return {
      value: '', textContent: '', innerHTML: '', style: {}, dataset: {}, focus() {}, querySelector() {return null;}, remove() {}, insertAdjacentHTML(position, html) {this.innerHTML += html;},
      classList: {
        add: value => classes.add(value), remove: value => classes.delete(value),
        contains: value => classes.has(value),
        toggle: (value, on) => on ? classes.add(value) : classes.delete(value),
      },
    };
  };
  const document = {
    getElementById(id) { if (!nodes.has(id)) nodes.set(id, element()); return nodes.get(id); },
    querySelectorAll(selector) { return selectors.get(selector) || []; },
    querySelector(selector) { return (selectors.get(selector) || [])[0] || null; },
    createElement() {
      return {
        textContent: '',
        get innerHTML() {
          return String(this.textContent).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
        },
      };
    },
  };
  const location = {pathname, hash:options.hash || '', reload() {location.reloaded = true;}};
  const context = vm.createContext({document, window: {addEventListener(name,fn) {events[name]=fn;}}, location,
    localStorage: {getItem(key) {return stored.get(key) || null;}, removeItem(key) {stored.delete(key);}, setItem(key,value) {stored.set(key,value);}},
    fetch: options.fetch || (async () => ({ok: true, status: 200, json: async () => ({})})),
    alert: message => alerts.push(message), confirm:()=>true,
    navigator:{clipboard:{writeText:async text=>{context.copied=text;}}},
    setTimeout:fn=>timers.push(fn), clearTimeout(){}, console, URL, TextEncoder});
  const scriptURL = new URL('./static/app.js', import.meta.url);
  vm.runInContext(readFileSync(scriptURL, 'utf8'), context, {filename: fileURLToPath(scriptURL)});
  return {document, context, selectors, alerts, events, timers, stored, location, run: source => vm.runInContext(source, context)};
}

test('HTML escaping is safe in quoted attributes and text', () => {
  const {run} = app();
  const escaped = run(`esc(${JSON.stringify('x" onfocus="bad()\'')})`);
  assert.ok(!escaped.includes('"'), 'attribute delimiter remains unescaped');
  assert.ok(!escaped.includes("'"), 'single quote remains unescaped');
  assert.equal(run(`esc('<tag>&')`), '&lt;tag&gt;&amp;');
});

test('creating an order leaves payment details visible after refreshing the list', async () => {
  const {document, run} = app();
  document.getElementById('topup-amount').value = '10';
  await run(`
    selectedMethod = {method: 'usdt', network: 'TRC20'};
    api = async () => ({id: 'test-order', amount: '10', currency: 'USDT', method: 'usdt', wallet_address: 'TTest', status: 'pending'});
    loadTopup = async () => {
      await Promise.resolve();
      document.getElementById('topup-order-detail').classList.add('hidden');
    };
    createTopup();
  `);
  await Promise.resolve();
  assert.equal(document.getElementById('topup-order-detail').classList.contains('hidden'), false);
  assert.match(document.getElementById('topup-order-detail').innerHTML, /TTest/);
});

test('private portal prefix is preserved for API requests', () => {
  const {run} = app();
  assert.equal(run('BASE'), '/private-prefix/portal');
});

test('index.html and the canonical UI URL use the same private API prefix', () => {
  assert.equal(app('/private-prefix/portal/ui/index.html').run('BASE'), '/private-prefix/portal');
});

async function initialized() {
  const instance = app();
  for (let i = 0; i < 10; i++) await Promise.resolve();
  return instance;
}

test('anonymous visitors see the public page before authentication', async () => {
  const {document} = await initialized();
  assert.equal(document.getElementById('page-home').classList.contains('hidden'), false);
  assert.equal(document.getElementById('page-auth').classList.contains('hidden'), true);
  assert.equal(document.getElementById('page-app').classList.contains('hidden'), true);
});

test('login and returning home are explicit navigation actions', async () => {
  const {document, run} = await initialized();
  run(`showAuth('register')`);
  assert.equal(document.getElementById('page-home').classList.contains('hidden'), true);
  assert.equal(document.getElementById('form-register').classList.contains('hidden'), false);
  run('showHome()');
  assert.equal(document.getElementById('page-auth').classList.contains('hidden'), true);
});

test('public model prices escape markup and unsafe document links fall back locally', async () => {
  const {document, run} = await initialized();
  await run(`api = async () => ({site_name:'Test Site', docs_url:'javascript:alert(1)', cny_points_per_unit:'100', usdt_points_per_unit:'700', prices:[{model:'<img src=x>',input_per_million:'12.5',output_per_million:'25',cache_read_per_million:'0',cache_write_per_million:'0'}]}); loadPublicSite()`);
  assert.equal(document.getElementById('site-name').textContent, 'Test Site');
  assert.match(document.getElementById('home-prices').innerHTML, /&lt;img/);
  assert.ok(!document.getElementById('home-prices').innerHTML.includes('<img'));
  assert.equal(document.getElementById('home-docs').href, '/private-prefix/portal/ui/docs.html');
  assert.match(document.getElementById('home-rates').textContent, /100.*700/);
});

test('public pricing outage is visible and does not fabricate prices', async () => {
  const {document, run} = await initialized();
  await run(`api = async () => {throw {status:503,message:'unavailable'}}; loadPublicSite()`);
  assert.match(document.getElementById('home-prices').innerHTML, /无法读取/);
  assert.match(document.getElementById('home-site-error').textContent, /不可用/);
});

test('payment details distinguish payment currency from the credited points snapshot', async () => {
  const {document, run} = await initialized();
  run(`showOrderDetail({id:'test-order',amount:'2.31',currency:'CNY',points_amount:'231.000000',exchange_rate:'100.000000',method:'wechat',status:'pending'})`);
  const html = document.getElementById('topup-order-detail').innerHTML;
  assert.match(html, /2.31.*CNY/);
  assert.match(html, /231.000000/);
  assert.match(html, /100.000000/);
});

test('assistant never sends a client-selected model or recognizable raw keys', async () => {
  const {document, run} = await initialized();
  document.getElementById('assistant-input').value = 'Configure cpk_syntheticsecretkey123456789';
  await run(`assistantReady=true; globalThis.captured=null; api=async (path,opts)=>{captured={path,body:JSON.parse(opts.body)};return {reply:'<img src=x onerror=alert(1)> sk-syntheticsecretkey123456789'}}; sendAssistant({preventDefault(){}})`);
  const captured = JSON.parse(run('JSON.stringify(captured)'));
  assert.equal(captured.path, '/assistant');
  assert.deepEqual(Object.keys(captured.body), ['messages']);
  assert.equal(captured.body.messages[0].content, 'Configure YOUR_API_KEY');
  const rendered = document.getElementById('assistant-chat').innerHTML;
  assert.match(rendered, /&lt;img/);
  assert.ok(!rendered.includes('<img'));
  assert.ok(!rendered.includes('syntheticsecretkey'));
  assert.equal(document.getElementById('assistant-send').disabled, false);
});

test('assistant prevents simultaneous sends and retains the question on failure', async () => {
  const {document, run} = await initialized();
  document.getElementById('assistant-input').value = 'Help with my client';
  const pending = run(`assistantReady=true; globalThis.calls=0; globalThis.finish=null; api=()=>{calls++;return new Promise(resolve=>finish=resolve)}; sendAssistant({preventDefault(){}})`);
  await run('sendAssistant({preventDefault(){}})');
  assert.equal(run('calls'), 1);
  assert.equal(document.getElementById('assistant-send').disabled, true);
  run(`finish({reply:'Use the documented base URL'})`);
  await pending;
  document.getElementById('assistant-input').value = 'Second question';
  await run(`api=async()=>{throw {status:429,message:'Daily limit reached'}};sendAssistant({preventDefault(){}})`);
  assert.equal(document.getElementById('assistant-input').value, 'Second question');
  assert.equal(document.getElementById('assistant-error').textContent, 'Daily limit reached');
});

test('assistant history stays inside server message and byte limits', async () => {
  const {document, run} = await initialized();
  document.getElementById('assistant-input').value = 'More help';
  await run(`assistantReady=true; assistantMessages=Array.from({length:12},(_,i)=>({role:i%2?'assistant':'user',content:'界'.repeat(2500)}));api=async(path,opts)=>{globalThis.sent=JSON.parse(opts.body).messages;return {reply:'Bounded answer'}};sendAssistant({preventDefault(){}})`);
  const sent = JSON.parse(run('JSON.stringify(sent)'));
  assert.ok(sent.length <= 8);
  assert.ok(sent.every(m => Array.from(m.content).length <= 2000));
  assert.ok(sent.reduce((sum, m) => sum + Buffer.byteLength(m.content), 0) <= 12000);
});

test('disabled assistant links to documentation and keeps send disabled', async () => {
  const {document, run} = await initialized();
  await run(`api=async()=>({helper_enabled:false,docs_url:'https://docs.example.test'});loadAssistant()`);
  assert.equal(document.getElementById('assistant-send').disabled, true);
  assert.equal(document.getElementById('assistant-docs').href, 'https://docs.example.test/');
  assert.match(document.getElementById('assistant-info').textContent, /暂未开放/);
});

test('price editor preserves decimal strings and sends its loaded version', async () => {
  const {document, run} = await initialized();
  run(`pointPrices=[{model:'priced-model',input_per_million:'0.000001',output_per_million:'20',cache_read_per_million:'0',cache_write_per_million:'0',version:7,enabled:true}];editPointPrice(0)`);
  assert.equal(document.getElementById('price-input').value, '0.000001');
  await run(`globalThis.savedPrice=null;api=async(path,opts)=>{if(opts){savedPrice=JSON.parse(opts.body);return {...savedPrice,version:8}}return {prices:[]}};savePointPrice({preventDefault(){}})`);
  const price = JSON.parse(run('JSON.stringify(savedPrice)'));
  assert.equal(price.version, 7);
  assert.equal(price.input_per_million, '0.000001');
  assert.equal(price.cache_read_per_million, '0');
  assert.match(document.getElementById('price-msg').textContent, /已保存/);
});

test('stale administrator price writes require reloading instead of silent overwrite', async () => {
  const {document, run} = await initialized();
  await run(`api=async()=>{throw {status:409,message:'conflict'}};savePointPrice({preventDefault(){}})`);
  assert.match(document.getElementById('price-msg').textContent, /重新载入/);
});


test('key management displays the active-key cap, revoked history and escaped policies', async () => {
 const {document,run}=await initialized();
 await run(`api=async path=>path==='/api-key-models'?{models:['model-a']}:{max_active_keys:10,keys:[...Array.from({length:10},(_,i)=>({id:'key-'+i,key_prefix:'cpk_demo',name:'<img src=x>',model_scope:'selected',allowed_models:['model-a']})),{id:'revoked',model_scope:'none',revoked_at:'2026-01-01'}]};loadKeys()`);
 assert.equal(document.getElementById('keys-count').textContent,'10 / 10 个有效 Key');
 assert.equal(document.getElementById('key-create').disabled,true);
 assert.match(document.getElementById('keys-list').innerHTML,/&lt;img/);
 assert.match(document.getElementById('keys-list').innerHTML,/已吊销/);
 assert.match(document.getElementById('keys-list').innerHTML,/model-a/);
 assert.match(document.getElementById('keys-list').innerHTML,/已禁止所有模型/);
});

test('key creation sends an explicit policy and refuses an empty selected list',async()=>{
 const {document,run}=await initialized();
 document.getElementById('key-scope').value='selected';
 await run(`globalThis.sent=[];api=async(path,opts)=>{sent.push({path,opts});return {}};createKey()`);
 assert.equal(run('sent.length'),0);
 assert.match(document.getElementById('new-key-msg').innerHTML,/至少选择/);
 document.getElementById('key-scope').value='none';
 await run(`api=async(path,opts)=>{if(opts){sent.push(JSON.parse(opts.body));return {key:'cpk_synthetic'}}return {keys:[],models:[]}};createKey()`);
 assert.deepEqual(JSON.parse(run('JSON.stringify(sent[0])')),{name:'',model_scope:'none',allowed_models:[]});
 assert.match(document.getElementById('new-key-msg').innerHTML,/仅|无法再次查看/);
});

test('creating a key twice while pending issues only one mutation and retains the name on failure',async()=>{
 const {document,run}=await initialized();document.getElementById('key-name').value='WorkBuddy';
 const pending=run(`globalThis.calls=0;globalThis.reject=null;api=()=>{calls++;return new Promise((resolve,fail)=>reject=fail)};createKey()`);
 await run('createKey()');assert.equal(run('calls'),1);assert.equal(document.getElementById('key-create').disabled,true);
 run(`reject({status:409,message:'最多 10 个 Key'})`);await pending;
 assert.equal(document.getElementById('key-name').value,'WorkBuddy');assert.match(document.getElementById('new-key-msg').innerHTML,/10/);
});

test('editing a key preserves unpublished selections and saves the target key only',async()=>{
 const {document,run}=await initialized();
 run(`keyRows=[{id:'key-a',name:'client-a',model_scope:'selected',allowed_models:['old-model']}];keyModelOptions=['model-a'];editKeyPolicy(0)`);
 assert.match(document.getElementById('edit-key-models').innerHTML,/old-model.*当前未上架/);
 assert.equal(document.getElementById('key-edit').classList.contains('hidden'),false);
 document.getElementById('edit-key-scope').value='none';
 await run(`globalThis.saved=null;api=async(path,opts)=>{if(opts){saved={path,body:JSON.parse(opts.body)};return {}}return {keys:[],models:[]}};saveKeyPolicy()`);
 const saved=JSON.parse(run('JSON.stringify(saved)'));assert.equal(saved.path,'/api-keys/key-a/models');assert.deepEqual(saved.body,{model_scope:'none',allowed_models:[]});
 assert.match(document.getElementById('key-edit-msg').textContent,/已保存/);
 run('closeKeyPolicy()');assert.equal(document.getElementById('key-edit').classList.contains('hidden'),true);
});

test('failed key policy changes remain editable and surface the error',async()=>{
 const {document,run}=await initialized();
 run(`keyRows=[{id:'key-a',model_scope:'all'}];editKeyPolicy(0)`);
 await run(`api=async()=>{throw {message:'Unable to save'}};saveKeyPolicy()`);
 assert.equal(document.getElementById('key-edit-msg').textContent,'Unable to save');assert.equal(document.getElementById('key-policy-save').disabled,false);
 assert.equal(document.getElementById('key-edit').classList.contains('hidden'),false);
});

test('the customer dashboard separates credits from upstream accounts and shows failures',async()=>{
 const {document,run}=await initialized();
 await run(`api=async path=>({'/me':{user:{email:'customer@example.test',unbilled:false}},'/wallet':{balance:'123.45'},'/usage?limit=10':{records:[{created_at:'2026-10-24T00:00:00Z',model:'test-model',auth_id:'private-upstream-id',cost:'1.25',status:'success'}]},'/api-keys':{keys:[{}, {revoked_at:'2026-10-24'}]}}[path]);loadDashboard()`);
 assert.equal(document.getElementById('dash-balance').textContent,'123.450000');assert.equal(document.getElementById('dash-keys').textContent,1);
 assert.equal(document.getElementById('dash-plan').textContent,'按量计费');assert.ok(!document.getElementById('dash-usage').innerHTML.includes('private-upstream-id'));
 await run(`api=async()=>{throw {status:503}};loadDashboard()`);
 assert.equal(document.getElementById('dashboard-error').classList.contains('hidden'),false);
});

test('customer navigation cannot open administrator or upstream quota panels',async()=>{
 const {document,run}=await initialized();
 await run(`globalThis.dest='';loadDashboard=()=>{dest='dashboard'};loadQuota=()=>{dest='quota'};loadAdmin=()=>{dest='admin'};isAdmin=false;navigate('quota')`);
 assert.equal(run('dest'),'dashboard');run(`navigate('admin')`);assert.equal(run('dest'),'dashboard');
 run(`isAdmin=true;navigate('quota')`);assert.equal(run('dest'),'quota');
});

const submitEvent = `({preventDefault(){}})`;
async function flush() { for (let i=0;i<25;i++) await Promise.resolve(); }

test('API wrapper preserves the private prefix, bearer credentials and server errors',async()=>{
 const {run,context}=await initialized();
 run(`token='synthetic-session'`);
 context.fetch=async(path,opts)=>{context.request={path,opts};return {ok:true,status:204};};
 assert.deepEqual(JSON.parse(JSON.stringify(await run(`api('/api-keys/key-a',{method:'DELETE'})`))),{});
 assert.equal(context.request.path,'/private-prefix/portal/api-keys/key-a');assert.equal(context.request.opts.headers.Authorization,'Bearer synthetic-session');
 for (const [json,message] of [[async()=>({error:'expired'}),'expired'],[async()=>{throw Error('invalid JSON');},'request failed']]) {
  context.fetch=async()=>({ok:false,status:401,json});
  await assert.rejects(run(`api('/me')`),err=>err.status===401 && err.message===message);
 }
});

test('password login, registration and errors keep credentials out of rendered messages',async()=>{
 const {document,run,stored}=await initialized();
 document.getElementById('login-email').value='user@example.test';document.getElementById('login-pass').value='test-only-password';
 await run(`globalThis.entered=0;globalThis.sent=null;enterApp=()=>{entered++};api=async(path,opts)=>{sent={path,body:JSON.parse(opts.body)};return {token:'session-a'}};doLogin(${submitEvent})`);
 assert.equal(run('sent.path'),'/login');assert.equal(stored.get('portal_token'),'session-a');assert.equal(run('entered'),1);
 document.getElementById('reg-email').value='new@example.test';document.getElementById('reg-pass').value='another-test-password';document.getElementById('reg-name').value='New User';
 await run(`doRegister(${submitEvent})`);assert.equal(run('sent.path'),'/register');assert.equal(run('sent.body.display_name'),'New User');
 for (const fn of ['doLogin','doRegister']) {
  await run(`api=async()=>{throw {message:'<rejected>'}};${fn}(${submitEvent})`);
  assert.match(document.getElementById('auth-msg').innerHTML,/&lt;rejected&gt;/);assert.ok(!document.getElementById('auth-msg').innerHTML.includes('test-password'));
 }
});

test('email login requests a code before verification and reset discards the old code',async()=>{
 const {document,run,stored}=await initialized();document.getElementById('code-email').value=' user@example.test ';
 await run(`globalThis.sent=[];enterApp=()=>{};api=async(path,opts)=>{sent.push({path,body:JSON.parse(opts.body)});return {token:'code-session'}};switchAuthTab('code');doCode(${submitEvent})`);
 assert.equal(run('sent[0].path'),'/login/code/request');assert.equal(run('sent[0].body.email'),'user@example.test');assert.equal(document.getElementById('code-step2').classList.contains('hidden'),false);
 document.getElementById('code-input').value='123456';await run(`doCode(${submitEvent})`);
 assert.equal(run('sent[1].body.code'),'123456');assert.equal(stored.get('portal_token'),'code-session');
 run('resetCode()');assert.equal(document.getElementById('code-input').value,'');assert.equal(run('codeSent'),false);
 await run(`api=async()=>{throw {message:'try later'}};doCode(${submitEvent})`);assert.match(document.getElementById('auth-msg').innerHTML,/try later/);
});

test('session startup and logout do not keep a previous account active',async()=>{
 for (const valid of [true,false]) {
  const instance=app('/private-prefix/portal/ui/',{token:'old-token',fetch:async path=>({ok:valid||!path.endsWith('/me'),status:valid?200:401,json:async()=>path.endsWith('/me')?{user:{email:'owner@example.test',is_admin:true}}:{email_login:true}})});
  await flush();
  assert.equal(instance.document.getElementById('tab-code').classList.contains('hidden'),false);
  if (valid) {
   assert.equal(instance.document.getElementById('side-email').textContent,'owner@example.test');assert.equal(instance.document.getElementById('nav-admin').classList.contains('hidden'),false);
   instance.run('doLogout()');assert.equal(instance.location.reloaded,true);
  }
  assert.equal(instance.stored.has('portal_token'),false);
 }
 const failed=app('/portal/ui/',{fetch:async()=>{throw Error('offline')}});await flush();assert.equal(failed.document.getElementById('page-home').classList.contains('hidden'),false);
});

test('navigation activates each section and invokes its data loader',async()=>{
 const {document,run,selectors,events,location}=await initialized();
 const pages=['dashboard','keys','topup','usage','quota','admin','assistant','settings'];
 const sections=pages.map(page=>document.getElementById('sec-'+page));selectors.set('[id^="sec-"]',sections);
 const links=pages.map(page=>{const node=document.getElementById('link-'+page);node.dataset.page=page;return node;});selectors.set('.sidebar nav a',links);
 run(`isAdmin=true;globalThis.loaded=[];loadDashboard=()=>loaded.push('dashboard');loadKeys=()=>loaded.push('keys');loadTopup=()=>loaded.push('topup');loadUsageFilterOptions=()=>{};loadUsageStats=()=>{};loadUsage=()=>loaded.push('usage');loadQuota=()=>loaded.push('quota');loadAdmin=()=>loaded.push('admin');loadAssistant=()=>loaded.push('assistant')`);
 for (const page of pages) {run(`navigate('${page}')`);assert.equal(document.getElementById('sec-'+page).classList.contains('hidden'),false);assert.equal(links[pages.indexOf(page)].classList.contains('active'),true);}
 location.hash='#keys';events.hashchange();assert.equal(run('loaded.at(-1)'),'keys');run(`navigate('')`);assert.equal(run('loaded.at(-1)'),'dashboard');
});

test('account shell loads roles before opening the requested route',async()=>{
 const {document,run,location}=await initialized();location.hash='#keys';
 await run(`globalThis.destination='';navigate=page=>destination=page;api=async()=>({user:{email:'user@example.test',is_admin:false}});enterApp()`);
 assert.equal(run('destination'),'keys');assert.equal(document.getElementById('nav-quota').classList.contains('hidden'),true);
 location.hash='';await run(`api=async()=>{throw {status:503}};enterApp()`);assert.equal(run('destination'),'dashboard');
});

test('quota cards escape provider data and communicate unavailable or bounded capacity',async()=>{
 const {run}=await initialized();
 const full=run(`renderQuotaAccount({handle:'opaque-id',label:'<account>',provider:'codex',can_reset:true,reset_credits:2,details:[{label:'Balance',value:'<value>'}],windows:[{label:'5h',remaining_percent:20,resets_at:'2026-10-24T10:00:00Z'}],note:'Test only'})`);
 assert.match(full,/&lt;account&gt;/);assert.match(full,/&lt;value&gt;/);assert.match(full,/20%/);assert.match(full,/重置额度 \(2\)/);
 assert.match(run(`renderQuotaAccount({provider:'<other>',error:'<error>'})`),/&lt;error&gt;/);
 assert.match(run(`renderQuotaAccount({can_reset:true})`),/暂无数据/);
 for (const [remaining,expected] of [[null,'--'],[-2,'0%'],[5,'danger'],[50,'50%'],[125,'100%']]) assert.ok(run(`renderQuotaMeter({remaining_percent:${remaining}})`).includes(expected));
 assert.match(run(`providerBadge('claude')`),/Claude/);assert.match(run(`providerBadge('antigravity')`),/Antigravity/);
});

test('quota loaders handle populated, empty and failed results without stale busy state',async()=>{
 const {document,run,location}=await initialized();
 for (const state of [{unbilled:true,allowed_models:['model-a'],allowed_auth_ids:['opaque']},{allowed_auth_ids:['opaque']},{}]) {
  run(`globalThis.user=${JSON.stringify(state)}`);
  await run(`api=async path=>path==='/me'?{user}:path==='/wallet'?{balance:'5'}:path==='/account-labels'?{labels:{opaque:'Example'}}:{accounts:[{label:'Example',provider:'codex'}]};loadQuota()`);await flush();
  assert.match(document.getElementById('q-quota').innerHTML,/Example/);assert.equal(document.getElementById('q-refresh').disabled,false);
 }
 await run(`api=async()=>({accounts:[]});loadAccountQuota()`);assert.match(document.getElementById('q-quota').innerHTML,/暂无/);
 await run(`api=async()=>{throw {status:503}};loadAccountQuota()`);assert.match(document.getElementById('q-quota').innerHTML,/失败/);
 await run(`api=async()=>{throw {status:401}};loadQuota();loadAccountQuota()`);await flush();assert.equal(location.reloaded,true);
});

test('quota refresh and explicit reset update only the selected account',async()=>{
 const {document,run,selectors,alerts,location}=await initialized();const card=document.getElementById('quota-card');selectors.set('.qacct[data-handle="one"]',[card]);
 await run(`globalThis.sent=[];api=async(path,opts)=>{sent.push({path,opts});return {label:'Updated'}};refreshAccountQuota('one')`);
 assert.match(card.outerHTML,/Updated/);assert.equal(run('sent[0].path'),'/quota/one');
 await run(`resetAccountCredit('one')`);assert.equal(run('sent[1].path'),'/quota/one/reset');assert.match(card.innerHTML,/已重置/);
 await run(`api=async()=>{throw {message:'<rejected>',status:503}};refreshAccountQuota('one');resetAccountCredit('one')`);await flush();
 assert.match(card.innerHTML,/&lt;rejected&gt;/);assert.equal(card.classList.contains('busy'),false);
 run(`confirm=()=>false`);const before=run('sent.length');await run(`resetAccountCredit('one')`);assert.equal(run('sent.length'),before);
 run(`confirm=()=>true`);await run(`api=async()=>{throw {status:401}};refreshAccountQuota('one');resetAccountCredit('one')`);await flush();assert.equal(location.reloaded,true);
});

test('topup selection renders available payment methods and locks order snapshots',async()=>{
 const {document,run,selectors}=await initialized();
 await run(`api=async path=>path==='/topup/methods'?{methods:[{method:'usdt',network:'TESTNET',currency:'USDT'},{method:'wechat',currency:'CNY'},{method:'alipay',currency:'CNY'}]}:{orders:[{id:'paid',status:'confirmed',amount:'5',currency:'USDT'},{id:'waiting',status:'pending',network:'TESTNET',points_amount:'5000000'}]};loadTopup()`);
 assert.match(document.getElementById('topup-methods').innerHTML,/USDT TESTNET/);assert.match(document.getElementById('topup-list').innerHTML,/5000000/);
 const method=document.getElementById('method-test');method.dataset={method:'usdt',network:'TESTNET'};selectors.set('.method-card',[method]);
 run(`selectMethod(document.getElementById('method-test'))`);assert.equal(run('selectedMethod.network'),'TESTNET');assert.equal(document.getElementById('topup-form').classList.contains('hidden'),false);
 await run(`api=async path=>path==='/topup/methods'?{methods:[]}:{orders:[]};loadTopup()`);assert.match(document.getElementById('topup-methods').innerHTML,/未配置/);assert.equal(run('selectedMethod'),null);
});

test('topup mutations require valid input and cancellation confirmation',async()=>{
 const {document,run,alerts}=await initialized();
 await run(`globalThis.calls=[];api=async(path,opts)=>{calls.push({path,body:opts?.body});return {id:'test-order',amount:'5',currency:'USDT',method:'usdt',status:'pending'}};loadTopup=()=>{};createTopup()`);assert.equal(run('calls.length'),0);
 run(`selectedMethod={method:'usdt',network:'TESTNET'}`);document.getElementById('topup-amount').value='0';await run('createTopup()');assert.match(alerts.at(-1),/有效金额/);
 document.getElementById('topup-amount').value='5';await run('createTopup()');assert.equal(run('calls[0].path'),'/topup');
 await run(`submitTxHash('test-order')`);assert.match(alerts.at(-1),/输入 TX Hash/);
 document.getElementById('submit-txhash').value=' synthetic-hash ';await run(`submitTxHash('test-order')`);assert.equal(JSON.parse(run('calls.at(-1).body')).tx_hash,'synthetic-hash');
 run('confirm=()=>false');const count=run('calls.length');await run(`cancelOrder('test-order')`);assert.equal(run('calls.length'),count);
 run('confirm=()=>true');await run(`cancelOrder('test-order')`);assert.equal(run('calls.at(-1).path'),'/topup/test-order/cancel');
 await run(`viewOrder('test-order')`);assert.match(document.getElementById('topup-order-detail').innerHTML,/USDT/);
 for (const expr of ['createTopup()',"submitTxHash('x')","cancelOrder('x')","viewOrder('x')"]) {await run(`api=async()=>{throw {message:'payment unavailable'}};${expr}`);assert.equal(alerts.at(-1),'payment unavailable');}
});

test('order details show payment instructions safely for fiat and token transfers',async()=>{
 const {document,run}=await initialized();
 run(`showOrderDetail({id:'test',amount:'36.36',currency:'CNY',wallet_address:'https://example.test',qr_code_url:'https://example.test/qr.png',notes:'<note>',status:'submitted'})`);
 const html=document.getElementById('topup-order-detail').innerHTML;assert.match(html,/qr.png/);assert.match(html,/&lt;note&gt;/);assert.ok(!html.includes('id="order-addr"'));
 run(`renderTopupOrders([{id:'submitted',status:'submitted'},{id:'expired',status:'expired'}])`);assert.match(document.getElementById('topup-list').innerHTML,/查看/);
});

test('usage separates customer and administrator columns, filters and pagination',async()=>{
 const {document,run}=await initialized();
 for (const scope of ['self','all']) {
  await run(`api=async()=>({scope:'${scope}',models:['<model>'],accounts:[{id:'opaque',label:'Private account'}],users:[{id:'u',email:'operator@example.test'}]});loadUsageFilterOptions()`);
  assert.equal(document.getElementById('uf-account-box').classList.contains('hidden'),scope==='self');assert.match(document.getElementById('uf-model').innerHTML,/&lt;model&gt;/);
  await run(`api=async()=>({scope:'${scope}',records:[{model:'visible-model',auth_label:'Private account',created_at:'2026-10-24T10:00:00Z',unit:'POINTS',cost:'3',input_tokens:2,output_tokens:4}]});loadUsage(true)`);
  assert.equal(document.getElementById('usage-list').innerHTML.includes('Private account'),scope==='all');assert.equal(document.getElementById('uf-count').textContent,'已显示 1 条');
 }
 document.getElementById('uf-model').value='a/b';document.getElementById('uf-account').value='opaque';document.getElementById('uf-user').value='u';
 await run(`globalThis.url='';api=async path=>{url=path;return {records:Array.from({length:50},()=>({created_at:'2026-10-24',model:'m',unit:'LEGACY'}))}};applyUsageFilters()`);await flush();
 assert.match(run('url'),/model=a%2Fb/);assert.match(run('url'),/auth_id=opaque/);assert.match(document.getElementById('usage-list').innerHTML,/待对账/);assert.equal(document.getElementById('usage-more').classList.contains('hidden'),false);
 run('loadMoreUsage()');await flush();assert.match(run('url'),/before=/);
 run('resetUsageFilters()');await flush();assert.ok(!run('url').includes('&model='));
 await run(`api=async()=>({records:[]});loadUsage(true)`);assert.match(document.getElementById('usage-list').innerHTML,/暂无记录/);
 await run(`api=async()=>{throw {message:'<failed>',status:503}};loadUsage(true)`);assert.match(document.getElementById('usage-list').innerHTML,/&lt;failed&gt;/);
});

test('usage charts show real date and model totals and tolerate missing statistics',async()=>{
 const {document,run}=await initialized();
 await run(`api=async()=>({daily:[{date:'2026-10-23',cost:'0'},{date:'2026-10-24',cost:'2'}],by_model:[{model:'<model-a>',cost:'2'}]});loadUsageStats()`);
 const html=document.getElementById('usage-charts').innerHTML;assert.match(html,/2026-10-24/);assert.match(html,/100.0%/);assert.match(html,/&lt;model-a&gt;/);
 await run(`api=async()=>({});loadUsageStats()`);assert.equal(document.getElementById('usage-charts').innerHTML,'');
 await run(`api=async()=>{throw Error('offline')};loadUsageStats()`);assert.equal(document.getElementById('usage-charts').innerHTML,'');
});

test('password change clears successful inputs and preserves inputs on error',async()=>{
 const {document,run}=await initialized();document.getElementById('pw-old').value='old-test';document.getElementById('pw-new').value='new-test';
 await run(`globalThis.saved=null;api=async(path,opts)=>{saved={path,body:JSON.parse(opts.body)};return {}};doChangePassword(${submitEvent})`);
 assert.equal(run('saved.body.new_password'),'new-test');assert.equal(document.getElementById('pw-new').value,'');
 document.getElementById('pw-new').value='retry-test';await run(`api=async()=>{throw {message:'wrong password'}};doChangePassword(${submitEvent})`);
 assert.equal(document.getElementById('pw-new').value,'retry-test');assert.match(document.getElementById('pw-msg').innerHTML,/wrong password/);
});

test('administrator lists expose pending orders and distinguish legacy balances',async()=>{
 const {document,run}=await initialized();
 await run(`isAdmin=false;globalThis.calls=0;api=async()=>{calls++;return {}};loadAdmin()`);assert.equal(run('calls'),0);
 await run(`isAdmin=true;api=async path=>path.startsWith('/admin/topup')?{orders:[{id:'pending',status:'pending',user_id:'u',method:'usdt',network:'TESTNET',amount:'5',currency:'USDT',tx_hash:'test-hash'},{id:'submitted',status:'submitted',user_id:'u',method:'wechat'},{id:'done',status:'confirmed'}]}:path==='/admin/users'?{users:[{id:'u',email:'<user>',is_admin:true,unbilled:true,balance_unit:'LEGACY',allowed_models:['one'],allowed_auth_ids:['opaque']},{id:'v',email:'other@example.test'}]}:path==='/admin/accounts'?{accounts:[{id:'opaque',provider:'codex',label:'<account>',status:'suspended',alias:'test-alias'},{id:'active',status:'active'}]}:{prices:[]};loadAdmin()`);await flush();
 assert.match(document.getElementById('admin-orders').innerHTML,/test-hash/);assert.ok(!document.getElementById('admin-orders').innerHTML.includes("adminConfirm('done')"));
 assert.match(document.getElementById('admin-users').innerHTML,/待对账/);assert.match(document.getElementById('admin-users').innerHTML,/&lt;user&gt;/);assert.match(document.getElementById('admin-accounts').innerHTML,/&lt;account&gt;/);
 await run(`api=async path=>path==='/admin/users'?{users:[]}:path.startsWith('/admin/topup')?{orders:[]}:{};loadAdmin()`);await flush();assert.match(document.getElementById('admin-orders').innerHTML,/无待确认/);assert.match(document.getElementById('admin-accounts').innerHTML,/暂无/);
 await run(`api=async()=>{throw {status:403,message:'forbidden'}};loadAdmin();loadAdminAccounts()`);await flush();assert.equal(run('isAdmin'),false);
});

test('administrator mutations require explicit confirmation and preserve their target',async()=>{
 const {document,run,alerts,timers}=await initialized();
 run(`globalThis.sent=[];globalThis.reloads=0;loadAdmin=()=>reloads++;api=async(path,opts)=>{sent.push({path,body:JSON.parse(opts.body)});return {balance:'15.000000'}};confirm=()=>false`);
 await run(`adminConfirm('order-a')`);assert.equal(run('sent.length'),0);run('confirm=()=>true');await run(`adminConfirm('order-a')`);assert.equal(run('sent[0].path'),'/admin/topup/order-a/confirm');
 await run('doAdminCredit()');assert.match(alerts.at(-1),/请填写/);document.getElementById('credit-uid').value='user-a';document.getElementById('credit-amount').value='15';document.getElementById('credit-note').value='test credit';
 await run('doAdminCredit()');assert.equal(run('sent.at(-1).body.user_id'),'user-a');assert.match(document.getElementById('credit-msg').innerHTML,/15.000000/);
 const input=document.getElementById('alias-input');input.dataset.id='opaque';input.value=' Friendly name ';
 const button=document.getElementById('alias-button');button.textContent='保存';button.closest=()=>({querySelector:()=>input});
 await run(`saveAlias(document.getElementById('alias-button'))`);assert.equal(run('sent.at(-1).body.label'),'Friendly name');assert.equal(button.textContent,'已保存');timers.forEach(fn=>fn());assert.equal(button.textContent,'保存');
 await run(`api=async()=>{throw {message:'storage unavailable'}};adminConfirm('order-a');doAdminCredit();saveAlias(document.getElementById('alias-button'))`);await flush();assert.equal(alerts.at(-1),'storage unavailable');assert.match(document.getElementById('credit-msg').innerHTML,/storage unavailable/);
});

test('user permission editor retains selections, searches models, saves and handles failure',async()=>{
 const {document,run,selectors}=await initialized();
 await run(`openUserModal('missing')`);assert.equal(run('editingUserId'),null);
 await run(`adminUsers=[{id:'u',email:'user@example.test',unbilled:true,allow_reset:true,allowed_models:['Model A'],allowed_auth_ids:['one']}];api=async path=>path==='/admin/models'?{models:['Model A','Model B']}:{accounts:[{id:'one',provider:'codex',status:'suspended'},{id:'two',provider:'test',label:'Two',status:'active'}]};openUserModal('u')`);
 assert.equal(document.getElementById('um-email').textContent,'user@example.test');assert.match(document.getElementById('um-model-list').innerHTML,/value="Model A" checked/);assert.match(document.getElementById('um-account-list').innerHTML,/value="one" checked/);
 const first=document.getElementById('picker-a'),second=document.getElementById('picker-b');first.dataset.name='model a';second.dataset.name='model b';selectors.set('#um-model-list .picker-item',[first,second]);
 run(`filterModels(' A ')`);assert.equal(first.style.display,'');assert.equal(second.style.display,'none');run(`filterModels('')`);assert.equal(second.style.display,'');
 selectors.set('#um-model-list input:checked',[{value:'Model A'}]);selectors.set('#um-account-list input:checked',[{value:'one'}]);
 await run(`globalThis.saved=null;loadAdmin=()=>{};api=async(path,opts)=>{saved={path,body:JSON.parse(opts.body)};return {}};saveUserLimits()`);
 assert.equal(run('saved.path'),'/admin/users/u/limits');assert.deepEqual(JSON.parse(run('JSON.stringify(saved.body.allowed_models)')),['Model A']);assert.equal(run('editingUserId'),null);
 await run(`saveUserLimits()`);
 await run(`allModels=[];allAccounts=[];api=async()=>{throw {message:'offline'}};openUserModal('u');saveUserLimits()`);await flush();assert.match(document.getElementById('um-model-list').innerHTML,/暂无模型/);assert.match(document.getElementById('um-msg').innerHTML,/offline/);
});

test('commerce settings preserve loaded versions and warn on stale saves',async()=>{
 const {document,run}=await initialized();
 await run(`api=async path=>path==='/admin/settings'?{version:3,site_name:'TrueCore AI',helper_enabled:true,helper_daily_limit:20,helper_global_daily_limit:100,helper_max_output_tokens:768}:{prices:[{model:'model-a',enabled:true,version:1},{model:'model-b',enabled:false,version:2}]};loadCommerceAdmin()`);
 assert.equal(document.getElementById('cs-name').value,'TrueCore AI');assert.match(document.getElementById('admin-prices').innerHTML,/停用/);
 await run(`globalThis.saved=null;api=async(path,opts)=>{if(opts){saved=JSON.parse(opts.body);return {...saved,version:4}}return {}};saveCommerceSettings(${submitEvent})`);
 assert.equal(run('saved.version'),3);assert.equal(run('saved.helper_enabled'),true);assert.match(document.getElementById('commerce-msg').textContent,/已保存/);
 for (const status of [409,503]) {await run(`api=async()=>{throw {status:${status},message:'unavailable'}};saveCommerceSettings(${submitEvent})`);assert.match(document.getElementById('commerce-msg').textContent,status===409?/刷新/:/unavailable/);}
 run(`pointPrices=[{model:'MODEL-A',version:8}];document.getElementById('price-model').value='model-a';selectPriceVersion()`);assert.equal(run('priceVersion'),8);
 run(`document.getElementById('price-model').value='new-model';selectPriceVersion();editPointPrice(100)`);assert.equal(run('priceVersion'),0);
 await run(`api=async()=>{throw {status:503,message:'offline'}};savePointPrice(${submitEvent})`);assert.equal(document.getElementById('price-msg').textContent,'offline');
 await run(`commerceSettings=null;saveCommerceSettings(${submitEvent})`);
});

test('key catalog failures and revocation never silently alter another key',async()=>{
 const {document,run,alerts,location}=await initialized();
 await run(`api=async path=>{if(path==='/api-key-models')throw {status:503};return {keys:[{id:'key-a',model_scope:'all',name:'A'}]}};loadKeys()`);assert.match(document.getElementById('keys-error').textContent,/模型列表/);
 await run(`api=async()=>{throw {status:503}};loadKeys()`);assert.match(document.getElementById('keys-error').textContent,/API Key/);
 await run(`api=async()=>{throw {status:401}};loadKeys()`);assert.equal(location.reloaded,true);
 run(`keyRows=[{id:'key-a',model_scope:'all'}];editKeyPolicy(0);globalThis.sent=[];loadKeys=async()=>{};api=async(path,opts)=>{sent.push({path,opts});return {}};confirm=()=>false`);
 await run(`revokeKeyAt(0)`);assert.equal(run('sent.length'),0);run('confirm=()=>true');await run(`revokeKeyAt(0)`);assert.equal(run('sent[0].path'),'/api-keys/key-a');assert.equal(run('editingKeyId'),null);
 await run(`api=async()=>{throw {message:'cannot revoke'}};revokeKey('key-b')`);assert.equal(alerts.at(-1),'cannot revoke');
});

test('copy, password reveal and helper clearing provide deterministic feedback',async()=>{
 const {document,run,context,timers}=await initialized();
 const input=document.getElementById('password-input');input.type='password';const toggle=document.getElementById('password-toggle');toggle.parentElement={querySelector:()=>input};
 run(`togglePw(document.getElementById('password-toggle'))`);assert.equal(input.type,'text');run(`togglePw(document.getElementById('password-toggle'))`);assert.equal(input.type,'password');
 const value=document.getElementById('copy-value');value.textContent='synthetic-copy';const button=document.getElementById('copy-button');button.textContent='复制';value.parentElement={querySelector:()=>button};
 run(`copyText('copy-value')`);await flush();assert.equal(context.copied,'synthetic-copy');assert.equal(button.textContent,'已复制');timers.forEach(fn=>fn());assert.equal(button.textContent,'复制');
 run(`assistantMessages=[{role:'user',content:'test'}];assistantBusy=true;clearAssistant()`);assert.equal(run('assistantMessages.length'),1);run(`assistantBusy=false;clearAssistant()`);assert.equal(run('assistantMessages.length'),0);
});

test('expiry during customer data loading resets the session',async()=>{
 const {run,location}=await initialized();
 for (const call of ['loadDashboard()','loadTopup()','loadUsageFilterOptions()','loadUsage(true)']) {
  location.reloaded=false;await run(`api=async()=>{throw {status:401}};${call}`);assert.equal(location.reloaded,true);
 }
});

test('empty and legacy dashboard states never invent a usable balance or consumption',async()=>{
 const {document,run}=await initialized();
 await run(`api=async path=>path==='/me'?{user:{email:'admin@example.test',unbilled:true}}:path==='/wallet'?{balance:'0'}:path==='/api-keys'?{keys:[]}:{records:[]};loadDashboard()`);
 assert.equal(document.getElementById('dash-plan').textContent,'免余额账户');assert.equal(document.getElementById('dash-balance').textContent,'0.000000');assert.match(document.getElementById('dash-usage').innerHTML,/还没有消费/);
 await run(`api=async path=>path==='/me'?{user:{email:'admin@example.test'}}:path==='/wallet'?{balance:'0'}:path==='/api-keys'?{keys:[]}:{records:[{model:'legacy',unit:'LEGACY'}]};loadDashboard()`);assert.match(document.getElementById('dash-usage').innerHTML,/待对账/);
});

test('key permission saves prevent overlapping writes and protect an open editor',async()=>{
 const {document,run,selectors}=await initialized();
 await run(`saveKeyPolicy()`);run(`editKeyPolicy(100);keyRows=[{id:'revoked',revoked_at:'2026-10-24'}];editKeyPolicy(0)`);assert.equal(run('editingKeyId'),null);
 run(`keyRows=[{id:'one',name:'One'}];keyModelOptions=['model-a'];editKeyPolicy(0)`);
 document.getElementById('edit-key-scope').value='selected';selectors.set('#edit-key-models input:checked',[{value:'model-a'}]);
 const pending=run(`globalThis.count=0;globalThis.finish=null;api=()=>{count++;return new Promise(resolve=>finish=resolve)};saveKeyPolicy()`);
 run(`closeKeyPolicy();editKeyPolicy(0)`);await run('saveKeyPolicy()');assert.equal(run('count'),1);assert.equal(run('editingKeyId'),'one');assert.equal(document.getElementById('key-policy-save').disabled,true);
 run(`loadKeys=async()=>{};finish({})`);await pending;assert.equal(document.getElementById('key-policy-save').disabled,false);
 document.getElementById('key-scope').value='selected';selectors.set('#key-models input:checked',[{value:'model-a'}]);
 await run(`api=async(path,opts)=>{globalThis.policy=JSON.parse(opts.body);return {key:'cpk_test'}};createKey()`);assert.deepEqual(JSON.parse(run('JSON.stringify(policy.allowed_models)')),['model-a']);
 assert.equal(run(`keyPolicyLabel({model_scope:'selected',allowed_models:[]})`),'无可用模型');
});

test('model pickers and usage handle an empty published catalog without broadening permissions',async()=>{
 const {document,run}=await initialized();
 await run(`api=async()=>({});loadKeys();loadUsageFilterOptions();loadUsage(true)`);await flush();assert.match(document.getElementById('key-models').innerHTML,/暂时没有/);
 await run(`api=async()=>({scope:'all',users:[{id:'user-only'}],records:[{model:'test',unit:'POINTS',cost:'0'}]});loadUsageFilterOptions();loadUsage(true)`);await flush();assert.match(document.getElementById('uf-user').innerHTML,/user-only/);
 await run(`api=async()=>{throw {status:503}};loadUsage(true)`);assert.match(document.getElementById('usage-list').innerHTML,/未知错误/);
 await run(`api=async()=>({by_model:[{model:'zero-cost',cost:'0'}]});loadUsageStats()`);assert.ok(!document.getElementById('usage-charts').innerHTML.includes('NaN'));
 await run(`adminUsers=[{id:'u',email:'user@example.test'}];allModels=[];allAccounts=[];api=async()=>({});openUserModal('u')`);assert.match(document.getElementById('um-account-list').innerHTML,/暂无/);
});

test('assistant opening, empty input and clearing respect request state',async()=>{
 const {document,run}=await initialized();
 await run(`api=async()=>({helper_enabled:true});loadAssistant()`);assert.equal(document.getElementById('assistant-send').disabled,false);assert.match(document.getElementById('assistant-info').textContent,/每日次数/);
 await run(`globalThis.sent=0;api=async()=>{sent++;return {reply:'test'}};sendAssistant(${submitEvent})`);assert.equal(run('sent'),0);
 run(`assistantBusy=true`);await run(`loadAssistant()`);assert.equal(document.getElementById('assistant-send').disabled,true);
});

test('clipboard copies input values and auth tab feedback remains accessible',async()=>{
 const {document,run,selectors,context}=await initialized();
 const tab=document.getElementById('login-tab');selectors.set('.seg .tab',[tab]);selectors.set(`.seg .tab[onclick*="'login'"]`,[tab]);run(`switchAuthTab('login')`);assert.equal(tab.classList.contains('active'),true);
 const value=document.getElementById('input-copy');value.value='input-value';value.parentElement={querySelector:()=>null};run(`copyText('input-copy')`);await flush();assert.equal(context.copied,'input-value');assert.equal(run('esc(0)'),'0');
});

test('the documentation page fetches the same private-prefix public configuration',async()=>{
 for (const kind of ['configured','empty','http-error','offline']) {
  const node={textContent:'pending'};let requested='';
  const context=vm.createContext({location:{pathname:'/private-prefix/portal/ui/docs.html'},document:{getElementById:()=>node},
   fetch:async path=>{requested=path;if(kind==='offline')throw Error('offline');return {ok:kind!=='http-error',json:async()=>kind==='configured'?{public_api_base:'https://api.example.test/v1'}:{}};}});
  const scriptURL=new URL('./static/docs.js',import.meta.url);
  vm.runInContext(readFileSync(scriptURL,'utf8'),context,{filename:fileURLToPath(scriptURL)});await flush();
  assert.equal(requested,'/private-prefix/portal/public');assert.equal(node.textContent,kind==='configured'?'https://api.example.test/v1':'pending');
 }
});

test('leaving key management clears the one-time plaintext and clipboard failures are visible',async()=>{
 const {document,run,context,alerts}=await initialized();
 document.getElementById('new-key-msg').innerHTML='synthetic-key';
 run(`loadDashboard=()=>{};navigate('dashboard')`);assert.equal(document.getElementById('new-key-msg').innerHTML,'');
 context.navigator.clipboard.writeText=async()=>{throw Error('permission denied');};
 await run(`copyText('test-key')`);assert.match(alerts.at(-1),/手动选择并复制/);
});
