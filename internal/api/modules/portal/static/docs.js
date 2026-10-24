const base = location.pathname.replace(/\/ui\/docs\.html$/, '');
fetch(base + '/public').then(r => { if (!r.ok) throw Error(); return r.json(); }).then(site => {
  if (site.public_api_base) document.getElementById('api-base').textContent = site.public_api_base;
}).catch(() => {});
