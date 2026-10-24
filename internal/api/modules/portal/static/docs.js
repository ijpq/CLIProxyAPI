const legacy = /\/ui\/docs\.html$/.test(location.pathname);
const base = legacy ? location.pathname.replace(/\/ui\/docs\.html$/, '') : '/api/portal';
document.getElementById('docs-home').href = legacy ? base + '/ui/' : '/';
document.getElementById('docs-console').href = legacy ? base + '/ui/#keys' : '/console/keys';
fetch(base + '/public').then(r => { if (!r.ok) throw Error(); return r.json(); }).then(site => {
  if (site.public_api_base) document.getElementById('api-base').textContent = site.public_api_base;
}).catch(() => {});
