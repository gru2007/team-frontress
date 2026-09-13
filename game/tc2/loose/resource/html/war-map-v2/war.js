(function () {
  'use strict';

  var MAP_W = 1000, MAP_H = 600;
  var params = {};
  location.search.replace(/^\?/, '').split('&').forEach(function (part) {
    if (!part) return;
    var pair = part.split('=');
    params[decodeURIComponent(pair[0])] = decodeURIComponent(pair.slice(1).join('=') || '');
  });

  var isCard = params.view === 'card';
  var demo = params.demo !== '0';
  document.body.className = isCard ? 'card' : '';

  var state = null;
  var selected = null;
  var sectorLayers = {};
  var routeLayers = [];
  var labelLayers = [];
  var battleLayers = [];
  var geometry = null;

  var map = L.map('map', {
    crs: L.CRS.Simple,
    minZoom: -1.5,
    maxZoom: 2.5,
    zoomSnap: 0.25,
    zoomDelta: 0.5,
    zoomControl: !isCard,
    attributionControl: false,
    dragging: !isCard,
    scrollWheelZoom: !isCard,
    doubleClickZoom: !isCard,
    boxZoom: !isCard,
    keyboard: !isCard,
    tap: !isCard
  });

  var bounds = L.latLngBounds(point(0, MAP_H), point(MAP_W, 0));
  map.fitBounds(bounds, { padding: [8, 8] });
  map.setMaxBounds(bounds.pad(0.08));

  function point(x, y) { return L.latLng(MAP_H - y, x); }
  function geoPoint(coords) { return point(coords[0], coords[1]); }
  function byId(id) {
    if (!state || !state.nodes) return null;
    for (var i = 0; i < state.nodes.length; ++i) if (state.nodes[i].id === id) return state.nodes[i];
    return null;
  }
  function frontFor(id) {
    if (!state || !state.fronts) return null;
    for (var i = 0; i < state.fronts.length; ++i) if (state.fronts[i].node === id) return state.fronts[i];
    return null;
  }
  function ownerColor(owner) {
    if (owner === 'RED') return '#b94434';
    if (owner === 'BLU') return '#477da4';
    return '#6b6255';
  }
  function ownerBright(owner) {
    if (owner === 'RED') return '#f06c56';
    if (owner === 'BLU') return '#7abbe5';
    return '#b7aa91';
  }

  function normalize(data) {
    data = data || {};
    if (!Array.isArray(data.nodes)) {
      var nodes = [];
      Object.keys(data.nodes || {}).forEach(function (id) {
        var n = data.nodes[id] || {};
        n.id = n.id || id;
        nodes.push(n);
      });
      data.nodes = nodes;
    }
    data.fronts = Array.isArray(data.fronts) ? data.fronts : [];
    data.edges = Array.isArray(data.edges) ? data.edges : [];
    data.servers = Array.isArray(data.servers) ? data.servers : [];
    return data;
  }

  function sample() {
    return normalize({
      name: 'Example Line', demo: true, deploy: '', lang: 'english',
      nodes: [
        {id:'red_hq',name:'RED HQ',owner:'RED',players:0},
        {id:'yard',name:'Rail Yard',owner:'RED',players:6},
        {id:'depot',name:'Sawmill Depot',owner:'RED',players:0},
        {id:'works',name:'Foundry 17',owner:'RED',players:21},
        {id:'reservoir',name:'Reservoir',owner:'BLU',players:11},
        {id:'quarry',name:'Quarry',owner:'BLU',players:0},
        {id:'junction',name:'Iron Junction',owner:'BLU',players:0},
        {id:'blu_hq',name:'BLU HQ',owner:'BLU',players:0}
      ],
      fronts: [
        {node:'works',attacker:'BLU',stage:2,stages:3,progress:.62,players:21,kind:'assault',map:'cp_gravelpit',server:'eu-1'},
        {node:'reservoir',attacker:'RED',stage:1,stages:3,progress:.28,players:11,kind:'skirmish',map:'koth_viaduct',server:'eu-2'}
      ]
    });
  }

  function loadGeometry() {
    return fetch('./maps/frontline.geojson', {cache:'no-store'})
      .then(function (r) { if (!r.ok) throw new Error('geometry ' + r.status); return r.json(); })
      .then(function (g) { geometry = g; drawGeometry(); });
  }

  function drawGeometry() {
    var sectors = {type:'FeatureCollection',features:[]};
    var routes = {type:'FeatureCollection',features:[]};
    geometry.features.forEach(function (f) {
      if (f.properties.kind === 'sector') sectors.features.push(f);
      if (f.properties.kind === 'route') routes.features.push(f);
    });

    L.geoJSON(routes, {
      coordsToLatLng: geoPoint,
      style: function () { return {color:'#776756',weight:3,opacity:.52,dashArray:'8 7',lineCap:'round'}; },
      onEachFeature: function (feature, layer) { routeLayers.push({feature:feature,layer:layer}); }
    }).addTo(map);

    L.geoJSON(sectors, {
      coordsToLatLng: geoPoint,
      style: function () { return {className:'sector',color:'#312821',weight:2,fillColor:'#62594d',fillOpacity:.48}; },
      onEachFeature: function (feature, layer) {
        var id = feature.properties.node;
        sectorLayers[id] = layer;
        layer.on('click', function () { select(id); });
        var p = point(feature.properties.x, feature.properties.y);
        var label = L.tooltip({permanent:true,direction:'center',className:'node-label',interactive:false})
          .setLatLng(p).setContent(feature.properties.label || id).addTo(map);
        labelLayers.push(label);
      }
    }).addTo(map);

    render();
  }

  function clearBattles() {
    battleLayers.forEach(function (l) { map.removeLayer(l); });
    battleLayers = [];
  }

  function render() {
    if (!state || !geometry) return;

    Object.keys(sectorLayers).forEach(function (id) {
      var n = byId(id);
      var layer = sectorLayers[id];
      var front = frontFor(id);
      var owner = n ? n.owner : 'NEUTRAL';
      layer.setStyle({
        color: front ? '#e7b365' : (selected === id ? '#ded3bb' : '#312821'),
        weight: front ? 5 : (selected === id ? 4 : 2),
        fillColor: ownerColor(owner),
        fillOpacity: front ? .66 : .46
      });
      if (layer._path) layer._path.classList.toggle('sector-contested', !!front);
    });

    routeLayers.forEach(function (item) {
      var a = byId(item.feature.properties.a), b = byId(item.feature.properties.b);
      var divided = a && b && a.owner && b.owner && a.owner !== b.owner;
      item.layer.setStyle(divided ? {color:'#e7b365',weight:5,opacity:.95,dashArray:'12 10'} : {color:'#776756',weight:3,opacity:.48,dashArray:'8 7'});
      if (item.layer._path) item.layer._path.classList.toggle('frontline', !!divided);
    });

    clearBattles();
    state.fronts.forEach(function (f) {
      var feature = null;
      geometry.features.some(function (x) {
        if (x.properties.kind === 'sector' && x.properties.node === f.node) { feature = x; return true; }
        return false;
      });
      if (!feature) return;
      var marker = L.circleMarker(point(feature.properties.x, feature.properties.y), {
        radius:7,color:'#fff0c4',weight:3,fillColor:'#e7b365',fillOpacity:1,className:'battle-marker'
      }).addTo(map);
      marker.on('click', function () { select(f.node); });
      battleLayers.push(marker);
    });

    var livePlayers = 0;
    state.fronts.forEach(function (f) { livePlayers += Number(f.players || 0); });
    document.getElementById('campaignName').textContent = state.name || 'GLOBAL WAR';
    document.getElementById('campaignMeta').textContent = state.demo ? 'DEMO THEATER' : 'LIVE CAMPAIGN';
    document.getElementById('status').textContent = state.fronts.length + ' FRONTS / ' + livePlayers + ' DEPLOYED';

    if (!selected) selected = state.deploy || (state.fronts[0] && state.fronts[0].node) || (state.nodes[0] && state.nodes[0].id);
    fillSidebar();
    fillCard();
  }

  function select(id) {
    selected = id;
    render();
  }

  function fillSidebar() {
    var n = byId(selected);
    if (!n) return;
    var f = frontFor(selected);
    document.getElementById('sectorName').textContent = n.name || n.id;
    var own = document.getElementById('sectorOwner');
    own.textContent = (n.owner || 'NEUTRAL') + ' CONTROL';
    own.className = 'owner ' + ((n.owner || 'neutral').toLowerCase());
    document.getElementById('sectorOperation').textContent = f ? ((f.attacker || '?') + ' ' + (f.kind || 'battle')).toUpperCase() : 'QUIET';
    document.getElementById('sectorStage').textContent = f ? ((f.stage || 1) + ' / ' + (f.stages || 1)) : '—';
    document.getElementById('sectorPlayers').textContent = f ? (f.players || 0) : (n.players || 0);
    document.getElementById('sectorMap').textContent = f && f.map ? f.map : '—';
    var button = document.getElementById('deploy');
    button.textContent = state.deploy === selected ? 'DEPLOYED HERE' : 'DEPLOY HERE';
    button.disabled = !selected;
  }

  function fillCard() {
    var f = state.fronts[0];
    if (!f) {
      document.getElementById('cardTitle').textContent = state.name || 'GLOBAL WAR';
      document.getElementById('cardText').textContent = 'The front is quiet. Open the War Map for sector control.';
      return;
    }
    var n = byId(f.node);
    document.getElementById('cardTitle').textContent = n ? n.name : f.node;
    document.getElementById('cardText').textContent = (f.attacker || '?') + ' attacking · stage ' + (f.stage || 1) + '/' + (f.stages || 1) + ' · ' + (f.players || 0) + ' players';
  }

  function post(line) {
    if (demo && line !== 'close') return;
    try { fetch('/v1/campaign/command', {method:'POST',body:line}).catch(function () {}); } catch (e) {}
  }

  document.getElementById('deploy').addEventListener('click', function () {
    if (!selected) return;
    post('deploy ' + selected);
    state.deploy = selected;
    render();
  });

  function updateLive() {
    if (demo) return Promise.resolve();
    return fetch('/v1/campaign', {cache:'no-store'})
      .then(function (r) { if (!r.ok) throw new Error('campaign ' + r.status); return r.json(); })
      .then(function (doc) { state = normalize(doc); render(); })
      .catch(function () {
        document.getElementById('status').textContent = 'CAMPAIGN OFFLINE';
        if (!state) { state = sample(); state.demo = true; render(); }
      });
  }

  loadGeometry().then(function () {
    if (demo) {
      state = sample();
      render();
    } else {
      updateLive();
      setInterval(updateLive, 2000);
    }
  }).catch(function () {
    location.replace('../campaign-legacy.html' + location.search);
  });

  window.addEventListener('resize', function () { map.invalidateSize(); });
}());
