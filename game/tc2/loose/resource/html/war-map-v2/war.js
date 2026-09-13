(function () {
  'use strict';

  var qs = new URLSearchParams(location.search);
  var view = qs.get('view') === 'full' ? 'full' : 'card';
  var isFull = view === 'full';
  // Preserve the legacy page contract: demo is the safe default; ?demo=0 uses the live game feed.
  var isDemo = qs.get('demo') !== '0';
  var lang = qs.get('lang') === 'ru' ? 'ru' : 'en';

  document.body.classList.add(isFull ? 'view-full' : 'view-card');

  var STR = {
    en: { sector:'SECTOR', owner:'OWNER', players:'PLAYERS', operation:'OPERATION', active:'ACTIVE OPERATION', deploy:'DEPLOY HERE', deployed:'DEPLOYED HERE', noBattle:'No active operation', mapHint:'Wheel to zoom · drag to pan · click a sector', demo:'DEMO / SIMULATION', live:'LIVE CAMPAIGN', legacy:'LEGACY MAP', reset:'RESET VIEW', open:'OPEN WAR MAP', stage:'Stage %a% / %b%', waiting:'Waiting for campaign data…', stale:'Live campaign unavailable — showing last known data', selected:'Sector selected in demo; matchmaking is disabled.' },
    ru: { sector:'СЕКТОР', owner:'СТОРОНА', players:'ИГРОКИ', operation:'ОПЕРАЦИЯ', active:'АКТИВНАЯ ОПЕРАЦИЯ', deploy:'ВЫСАДИТЬСЯ', deployed:'ВЫБРАНО', noBattle:'Активной операции нет', mapHint:'Колесо — масштаб · перетаскивание — карта · клик — сектор', demo:'ДЕМО / СИМУЛЯЦИЯ', live:'ЖИВАЯ КАМПАНИЯ', legacy:'СТАРАЯ КАРТА', reset:'СБРОСИТЬ ВИД', open:'ОТКРЫТЬ КАРТУ ВОЙНЫ', stage:'Этап %a% / %b%', waiting:'Ожидание данных кампании…', stale:'Нет связи с кампанией — показаны последние данные', selected:'Сектор отмечен в демо; поиск матча отключён.' }
  };
  function T(k){ return (STR[lang] && STR[lang][k]) || STR.en[k] || k; }

  var SAMPLE = {
    version:1, lang:'english', demo:true, name:'Iron Frontier', deploy:'',
    nodes:[
      {id:'red_hq',name:'RED HQ',owner:'RED',x:.07,y:.50,hq:true,kind:'command',region:'EU',players:0,battles:0},
      {id:'yard',name:'Rail Yard',owner:'RED',x:.24,y:.22,hq:false,kind:'rail',region:'EU',players:6,battles:0},
      {id:'depot',name:'Sawmill Depot',owner:'RED',x:.22,y:.79,hq:false,kind:'supply',region:'EU',players:0,battles:0},
      {id:'works',name:'Foundry 17',owner:'RED',x:.45,y:.55,hq:false,kind:'industrial',region:'EU',players:21,battles:1},
      {id:'reservoir',name:'Reservoir',owner:'BLU',x:.53,y:.18,hq:false,kind:'water',region:'EU',players:11,battles:1},
      {id:'quarry',name:'Quarry',owner:'BLU',x:.62,y:.83,hq:false,kind:'mining',region:'RU',players:0,battles:0},
      {id:'junction',name:'Iron Junction',owner:'BLU',x:.78,y:.47,hq:false,kind:'rail',region:'RU',players:0,battles:0},
      {id:'blu_hq',name:'BLU HQ',owner:'BLU',x:.94,y:.52,hq:true,kind:'command',region:'RU',players:0,battles:0}
    ],
    edges:[{a:'red_hq',b:'yard'},{a:'red_hq',b:'depot'},{a:'yard',b:'works'},{a:'depot',b:'works'},{a:'yard',b:'reservoir'},{a:'works',b:'reservoir'},{a:'works',b:'quarry'},{a:'depot',b:'quarry'},{a:'reservoir',b:'junction'},{a:'quarry',b:'junction'},{a:'junction',b:'blu_hq'}],
    fronts:[{node:'works',attacker:'BLU',stage:2,stages:3,progress:.62,players:21,kind:'assault',map:'cp_gravelpit',server:'eu-1'},{node:'reservoir',attacker:'RED',stage:1,stages:3,progress:.28,players:11,kind:'skirmish',map:'koth_viaduct',server:'eu-2'}],
    status:{checked:true,valid:true,serversKnown:true,name:'Frontress',online:47,matches:2,servers:3}, queue:{state:'idle'}
  };

  var NS = 'http://www.w3.org/2000/svg';
  var svg = document.getElementById('map');
  var viewport = document.getElementById('viewport');
  var territories = document.getElementById('territories');
  var routes = document.getElementById('routes');
  var markers = document.getElementById('markers');
  var data = null, selected = null, geo = null, stale = false;
  var viewBox = {x:0,y:0,w:1000,h:620};
  var dragging = false, last = {x:0,y:0};

  function nodeById(id){
    if(!data || !data.nodes) return null;
    for(var i=0;i<data.nodes.length;i++) if(data.nodes[i].id===id) return data.nodes[i];
    return null;
  }
  function frontFor(id){
    var a = data && data.fronts || [];
    for(var i=0;i<a.length;i++) if(a[i].node===id) return a[i];
    return null;
  }
  function xy(n){ return {x:55 + (Number(n.x)||0)*890, y:45 + (Number(n.y)||0)*530}; }
  function el(name, attrs, parent){
    var e=document.createElementNS(NS,name), k;
    for(k in (attrs||{})) e.setAttribute(k,attrs[k]);
    (parent||viewport).appendChild(e); return e;
  }
  function clear(e){ while(e.firstChild) e.removeChild(e.firstChild); }
  function cssOwner(owner){ return owner==='RED'?'red':owner==='BLU'?'blu':'neutral'; }
  function polyPath(coords){
    if(!coords || !coords.length) return '';
    return coords.map(function(p,i){ return (i?'L':'M')+Number(p[0]).toFixed(1)+' '+Number(p[1]).toFixed(1); }).join(' ')+' Z';
  }
  function fallbackPolygon(n){
    var p=xy(n), r=n.hq?80:64;
    return [[p.x-r,p.y],[p.x-r*.45,p.y-r*.72],[p.x+r*.45,p.y-r*.72],[p.x+r,p.y],[p.x+r*.45,p.y+r*.72],[p.x-r*.45,p.y+r*.72],[p.x-r,p.y]];
  }
  function geoFeature(id){
    var f=geo && geo.features || [];
    for(var i=0;i<f.length;i++) if(f[i].properties && f[i].properties.id===id) return f[i];
    return null;
  }
  function edgeIsFront(e){
    var a=nodeById(e.a),b=nodeById(e.b);
    return a&&b&&a.owner&&b.owner&&a.owner!==b.owner&&a.owner!=='NEUTRAL'&&b.owner!=='NEUTRAL';
  }
  function setSelected(id){ selected=id; render(); }

  function render(){
    if(!data || !data.nodes) return;
    clear(territories); clear(routes); clear(markers);

    (data.nodes||[]).forEach(function(n){
      var f=geoFeature(n.id), coords;
      if(f && f.geometry && f.geometry.type==='Polygon') coords=f.geometry.coordinates[0];
      else coords=fallbackPolygon(n);
      var cls='sector sector-'+cssOwner(n.owner)+(frontFor(n.id)?' front':'')+(selected===n.id?' selected':'');
      var p=el('path',{d:polyPath(coords),'class':cls,'data-id':n.id},territories);
      p.addEventListener('click',function(ev){ ev.stopPropagation(); setSelected(n.id); });
    });

    (data.edges||[]).forEach(function(e){
      var a=nodeById(e.a), b=nodeById(e.b); if(!a||!b) return;
      var pa=xy(a), pb=xy(b);
      el('path',{d:'M'+pa.x+' '+pa.y+' L'+pb.x+' '+pb.y,'class':'route'+(edgeIsFront(e)?' front':'')},routes);
    });

    (data.nodes||[]).forEach(function(n){
      var p=xy(n), owner=cssOwner(n.owner), f=frontFor(n.id);
      if(f) el('circle',{cx:p.x,cy:p.y,r:25,'class':'battle-ring'},markers);
      if(data.deploy===n.id) el('circle',{cx:p.x,cy:p.y,r:34,'class':'deploy-ring'},markers);
      el('circle',{cx:p.x,cy:p.y,r:n.hq?15:11,'class':'node-dot node-'+owner},markers);
      var text=el('text',{x:p.x,y:p.y+(n.hq?33:30),'class':'node-label'},markers); text.textContent=n.name||n.id;
    });

    fillSidebar();
    document.getElementById('warName').textContent=data.name||'GLOBAL WAR';
    document.getElementById('warStatus').textContent=(isDemo?T('demo'):T('live'))+(stale?' · '+T('stale'):'');
  }

  function fillSidebar(){
    if(!isFull) return;
    var n=nodeById(selected)||nodeById(data.deploy)||(data.fronts&&data.fronts.length?nodeById(data.fronts[0].node):null)||data.nodes[0];
    if(!n) return;
    selected=n.id;
    var f=frontFor(n.id);
    document.getElementById('sectorName').textContent=n.name||n.id;
    document.getElementById('sectorMeta').textContent=[n.kind||'',n.region||''].filter(Boolean).join(' · ');
    document.getElementById('owner').textContent=n.owner||'—';
    document.getElementById('players').textContent=String((f&&f.players!=null)?f.players:(n.players||0));
    document.getElementById('battle').textContent=f?(f.kind||'ACTIVE'):T('noBattle');
    var op=document.getElementById('operation'); op.hidden=!f;
    if(f){
      document.getElementById('attacker').textContent=(f.attacker||'—')+' → '+(n.owner||'—');
      document.getElementById('stage').textContent=T('stage').replace('%a%',f.stage||1).replace('%b%',f.stages||1);
      document.getElementById('progressFill').style.width=Math.round(Math.max(0,Math.min(1,Number(f.progress)||0))*100)+'%';
      document.getElementById('operationMeta').textContent=[f.map||'',f.server||''].filter(Boolean).join(' · ');
    }
    var deploy=document.getElementById('deploy'), here=data.deploy===n.id;
    deploy.textContent=here?T('deployed'):T('deploy'); deploy.classList.toggle('here',here); deploy.disabled=!n.id;
    document.getElementById('note').textContent=isDemo?T('selected'):'';
  }

  function post(line){
    if(isDemo && line!=='close') return;
    try{ fetch('/v1/campaign/command',{method:'POST',body:line}).catch(function(){}); }catch(e){}
  }
  document.getElementById('deploy').addEventListener('click',function(){
    if(!selected||!data) return; post('deploy '+selected); data.deploy=selected; render();
  });

  function applyLabels(){
    document.getElementById('sectorLabel').textContent=T('sector'); document.getElementById('ownerLabel').textContent=T('owner');
    document.getElementById('playersLabel').textContent=T('players'); document.getElementById('battleLabel').textContent=T('operation');
    document.getElementById('operationLabel').textContent=T('active'); document.getElementById('mapHint').textContent=T('mapHint');
    document.getElementById('resetView').textContent=T('reset'); document.getElementById('legacy').textContent=T('legacy'); document.getElementById('openFull').textContent=T('open');
  }

  function setViewBox(){ svg.setAttribute('viewBox',[viewBox.x,viewBox.y,viewBox.w,viewBox.h].join(' ')); }
  function resetView(){ viewBox={x:0,y:0,w:1000,h:620}; setViewBox(); }
  document.getElementById('resetView').addEventListener('click',resetView);
  svg.addEventListener('wheel',function(e){
    if(!isFull) return; e.preventDefault();
    var rect=svg.getBoundingClientRect(), mx=viewBox.x+(e.clientX-rect.left)/rect.width*viewBox.w, my=viewBox.y+(e.clientY-rect.top)/rect.height*viewBox.h;
    var factor=e.deltaY>0?1.12:.89, nw=Math.max(420,Math.min(1400,viewBox.w*factor)), nh=nw*.62;
    var rx=(mx-viewBox.x)/viewBox.w, ry=(my-viewBox.y)/viewBox.h;
    viewBox.x=mx-rx*nw; viewBox.y=my-ry*nh; viewBox.w=nw; viewBox.h=nh; setViewBox();
  },{passive:false});
  svg.addEventListener('mousedown',function(e){ if(!isFull||e.button!==0)return; dragging=true; last={x:e.clientX,y:e.clientY}; document.body.classList.add('dragging'); });
  window.addEventListener('mousemove',function(e){
    if(!dragging)return; var r=svg.getBoundingClientRect(); viewBox.x-=(e.clientX-last.x)/r.width*viewBox.w; viewBox.y-=(e.clientY-last.y)/r.height*viewBox.h; last={x:e.clientX,y:e.clientY}; setViewBox();
  });
  window.addEventListener('mouseup',function(){ dragging=false; document.body.classList.remove('dragging'); });

  var fullQuery=new URLSearchParams(location.search); fullQuery.set('view','full'); document.getElementById('openFull').href='?'+fullQuery.toString();
  var legacyQuery=new URLSearchParams(location.search); document.getElementById('legacy').href='../campaign-legacy.html?'+legacyQuery.toString();

  function loadGeo(){
    return fetch('maps/frontline.geojson',{cache:'no-store'}).then(function(r){ if(!r.ok) throw new Error('geojson '+r.status); return r.json(); }).then(function(g){geo=g;}).catch(function(){geo={type:'FeatureCollection',features:[]};});
  }
  function loadLive(){
    fetch('/v1/campaign',{cache:'no-store'}).then(function(r){if(!r.ok)throw new Error('campaign '+r.status);return r.json();}).then(function(d){
      if(!d||!Array.isArray(d.nodes))throw new Error('invalid campaign'); data=d; stale=false; if(d.lang) lang=String(d.lang).toLowerCase().indexOf('russian')>=0?'ru':'en'; applyLabels(); render();
    }).catch(function(){ stale=!!data; if(!data){ data=JSON.parse(JSON.stringify(SAMPLE)); stale=true; } render(); });
  }

  applyLabels(); resetView();
  loadGeo().then(function(){
    if(isDemo){ data=JSON.parse(JSON.stringify(SAMPLE)); render(); }
    else{ document.getElementById('warStatus').textContent=T('waiting'); loadLive(); setInterval(loadLive,3000); }
  });
}());
