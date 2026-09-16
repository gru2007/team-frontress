(function () {
  'use strict';

  var qs = new URLSearchParams(location.search);
  var view = qs.get('view') === 'full' ? 'full' : 'card';
  var isFull = view === 'full';
  // VGUI always supplies this explicitly. Standalone previews remain safe and
  // offline by default; -frontressdemo selects ?demo=1 in the native client.
  var isDemo = qs.get('demo') !== '0';
  var lang = qs.get('lang') === 'ru' ? 'ru' : 'en';

  document.body.classList.add(isFull ? 'view-full' : 'view-card');

  var STR = {
    en: { sector:'SECTOR', owner:'OWNER', players:'PLAYERS', operation:'OPERATION', active:'ACTIVE OPERATION', deploy:'DEPLOY', deploying:'STARTING LOCAL BATTLE…', noBattle:'No active operation', mapHint:'Wheel to zoom · drag to pan · click a sector', demo:'OFFLINE DEMO', live:'LIVE CAMPAIGN', legacy:'LEGACY MAP', reset:'RESET VIEW', open:'OPEN WAR MAP', stage:'Stage %a% / %b%', waiting:'Waiting for campaign data…', stale:'Live campaign unavailable — showing last known data', demoReady:'Deployment reason: your faction is committed to this %kind% operation. Force size: %size%v%size%.', simulatedFront:'This neighboring front is a transparent local simulation and is not deployable.', selectFront:'Select an active operation to deploy.' },
    ru: { sector:'СЕКТОР', owner:'СТОРОНА', players:'ИГРОКИ', operation:'ОПЕРАЦИЯ', active:'АКТИВНАЯ ОПЕРАЦИЯ', deploy:'ВЫСАДИТЬСЯ', deploying:'ЗАПУСК ЛОКАЛЬНОГО БОЯ…', noBattle:'Активной операции нет', mapHint:'Колесо — масштаб · перетаскивание — карта · клик — сектор', demo:'ОФЛАЙН-ДЕМО', live:'ЖИВАЯ КАМПАНИЯ', legacy:'СТАРАЯ КАРТА', reset:'СБРОСИТЬ ВИД', open:'ОТКРЫТЬ КАРТУ ВОЙНЫ', stage:'Этап %a% / %b%', waiting:'Ожидание данных кампании…', stale:'Нет связи с кампанией — показаны последние данные', demoReady:'Причина высадки: ваша фракция участвует в операции «%kind%». Силы: %size% на %size%.', simulatedFront:'Это соседний фронт прозрачно симулируется локально; высадка на него недоступна.', selectFront:'Выберите активную операцию.' }
  };
  function T(k){ return (STR[lang] && STR[lang][k]) || STR.en[k] || k; }

  var FLOW = {
    en: {
      eyebrow:'THE SECOND GRAVEL WAR', chooseTitle:'CHOOSE YOUR SIDE',
      chooseBody:'Pick a faction for this local three-stage operation. Your choice and every battle result are saved on this machine.',
      deployTitle:'DEPLOYMENT FOUND', deployBody:'The local coordinator selected this battle because your faction is committed to the active operation.',
      pendingTitle:'BATTLE TICKET RECOVERED', pendingBody:'An unfinished local deployment was found. Rejoin it, or abandon the ticket without changing the war.',
      debriefTitle:'AFTER-ACTION REPORT', debriefBody:'The local coordinator has applied the battle result to the war map.',
      finalTitle:'SECTOR CAPTURED', finalBody:'You completed the directed demo campaign. This local simulation uses the same battle-ticket and war-resolution boundaries intended for the persistent online war. In the Playtest, real players and Companies fight over one shared campaign.',
      battles:'BATTLES', victories:'VICTORIES', defeats:'DEFEATS', rejoin:'REJOIN BATTLE', abandon:'ABANDON',
      returnMap:'RETURN TO WAR MAP', followPlaytest:'FOLLOW THE PLAYTEST', continueOffline:'CONTINUE OFFLINE', restart:'NEW CAMPAIGN', warLog:'WAR LOG', resetCampaign:'RESET CAMPAIGN',
      pendingDeploy:'BATTLE IN PROGRESS', joinBattle:'JOIN BATTLE', back:'BACK', front:'FRONT', objective:'OBJECTIVE', mode:'MAP', forceSize:'FORCE SIZE',
      breakthrough:'BREAKTHROUGH', advance:'ADVANCE', assault:'ASSAULT', simulatedFrontKind:'SIMULATED FRONT', newFrontKind:'NEW FRONT', activeKind:'ACTIVE', factionRed:'RED', factionBlu:'BLU',
      factionRedHint:'Defend the industrial network', factionBluHint:'Take Mann Co. infrastructure',
      ifWin:'IF YOUR FACTION WINS', ifLoss:'IF YOUR FACTION LOSES', capture:'Capture the sector and open the next front',
      advanceTo:'Advance to %stage%', fallBackTo:'Fall back to %stage%', holdLine:'The offensive remains at Breakthrough',
      offlineLabel:'OFFLINE DEMO', offlineFeatures:'Local campaign · AI soldiers · simulated fronts',
      playtestLabel:'STEAM PLAYTEST', playtestFeatures:'Shared war · real players · persistent fronts · companies',
      campaignName:'IRON FRONTIER', commandKind:'COMMAND', railKind:'RAIL', supplyKind:'SUPPLY', industrialKind:'INDUSTRIAL', waterKind:'RESERVOIR', miningKind:'MINING',
      journeyLabel:'YOUR CAMPAIGN', journeySide:'CHOOSE SIDE', journeyFront:'CHOOSE FRONT', journeyBattle:'FIGHT WITH BOTS', journeyResult:'CHANGE THE WAR',
      resultCapturedTitle:'VICTORY — SECTOR CAPTURED', resultCapturedBody:'The operation succeeded. Your side now owns the sector, and a new front has opened.',
      resultAdvancedTitle:'VICTORY — OPERATION ADVANCED', resultAdvancedBody:'Your victory moved the operation to the next tactical stage. The next battle uses a new map and larger forces.',
      resultRepulsedTitle:'DEFEAT — OFFENSIVE REPULSED', resultRepulsedBody:'The enemy held the sector. The operation remains active, but it fell back one stage and can be attempted again.',
      resultStalemateTitle:'STALEMATE — OFFENSIVE REPULSED', resultStalemateBody:'Time expired without a winner. Strategically the defenders held, so the operation fell back one stage.',
      resultTimedOutTitle:'CAMPAIGN REPORT', resultTimedOutBody:'The operation ended after four battles. Every result still changed the front; start a new local campaign or join the shared Playtest war.',
      eventWhileAwayTitle:'WHILE YOU WERE AWAY', eventWhileAwayBody:'The opposing armies contested a neighboring sector. Your operation remains active.',
      eventRecoveredTitle:'CAMPAIGN RECOVERED', eventRecoveredBody:'An invalid local save was replaced with a clean campaign.',
      eventRedTitle:'RED MOBILIZED', eventBluTitle:'BLU MOBILIZED', eventFactionBody:'Your side committed to the Iron Track operation.',
      eventDeployedTitle:'BATTLE TICKET ISSUED', eventDeployedBody:'Deployment confirmed for %node%. The selected map will load locally and AI soldiers will fill both teams.',
      eventAbandonedTitle:'DEPLOYMENT CANCELLED', eventAbandonedBody:'The interrupted battle was abandoned. The score and war state did not change.',
      eventRedVictoryTitle:'RED VICTORY', eventBluVictoryTitle:'BLU VICTORY', eventStalemateTitle:'STALEMATE', eventBattleBody:'The full-round result was recorded and applied to the campaign score.',
      eventCapturedTitle:'TERRITORY CAPTURED', eventCapturedBody:'Your side captured %node%.', eventFrontTitle:'NEW FRONT OPENED', eventFrontBody:'A route into %node% is now open. Continue the shared war in the Playtest.',
      eventAdvancedTitle:'OPERATION ADVANCED', eventAdvancedBody:'Victory moved the operation to its next tactical stage.',
      eventRepulsedTitle:'OFFENSIVE REPULSED', eventRepulsedBody:'The defenders held. The operation remains active at its updated stage.',
      eventFailedTitle:'OPERATION ENDED', eventFailedBody:'The four-battle limit was reached before the sector was captured.',
      eventElsewhereTitle:'WAR CONTINUES ELSEWHERE', eventElsewhereGainedBody:'A simulated neighboring offensive gained momentum while you fought.', eventElsewhereStalledBody:'Local defenders stalled a neighboring offensive and shifted the supply line.',
      eventResetTitle:'SECOND GRAVEL WAR', eventResetBody:'A new local campaign has begun.', eventDebugTitle:'OPERATION STAGE CHANGED', eventDebugBody:'The campaign stage was changed by a test command.'
    },
    ru: {
      eyebrow:'ВТОРАЯ ГРАВИЙНАЯ ВОЙНА', chooseTitle:'ВЫБЕРИТЕ СТОРОНУ',
      chooseBody:'Выберите фракцию для локальной операции из трёх этапов. Выбор и результаты боёв сохраняются на этом компьютере.',
      deployTitle:'ВЫСАДКА НАЙДЕНА', deployBody:'Локальный координатор выбрал этот бой, потому что ваша фракция участвует в активной операции.',
      pendingTitle:'НАЙДЕН БОЕВОЙ БИЛЕТ', pendingBody:'Обнаружена незавершённая локальная высадка. Вернитесь в бой или отмените билет без изменения карты войны.',
      debriefTitle:'ПОСЛЕБОЕВОЙ ОТЧЁТ', debriefBody:'Локальный координатор применил результат боя к карте войны.',
      finalTitle:'СЕКТОР ЗАХВАЧЕН', finalBody:'Демонстрационная кампания завершена. Локальная симуляция использует те же границы боевого билета и разрешения войны, что запланированы для постоянной онлайн-войны. В плейтесте реальные игроки и роты сражаются в одной общей кампании.',
      battles:'БОИ', victories:'ПОБЕДЫ', defeats:'ПОРАЖЕНИЯ', rejoin:'ВЕРНУТЬСЯ В БОЙ', abandon:'ОТМЕНИТЬ',
      returnMap:'ВЕРНУТЬСЯ НА КАРТУ', followPlaytest:'СЛЕДИТЬ ЗА ПЛЕЙТЕСТОМ', continueOffline:'ПРОДОЛЖИТЬ ОФЛАЙН', restart:'НОВАЯ КАМПАНИЯ', warLog:'ЖУРНАЛ ВОЙНЫ', resetCampaign:'СБРОСИТЬ КАМПАНИЮ',
      pendingDeploy:'БОЙ УЖЕ ИДЁТ', joinBattle:'ВСТУПИТЬ В БОЙ', back:'НАЗАД', front:'ФРОНТ', objective:'ЗАДАЧА', mode:'КАРТА', forceSize:'СИЛЫ',
      breakthrough:'ПРОРЫВ', advance:'ПРОДВИЖЕНИЕ', assault:'ШТУРМ', simulatedFrontKind:'СИМУЛИРУЕМЫЙ ФРОНТ', newFrontKind:'НОВЫЙ ФРОНТ', activeKind:'АКТИВНА', factionRed:'RED', factionBlu:'BLU',
      factionRedHint:'Защитить промышленную сеть', factionBluHint:'Захватить инфраструктуру Mann Co.',
      ifWin:'ЕСЛИ ВАША СТОРОНА ПОБЕДИТ', ifLoss:'ЕСЛИ ВАША СТОРОНА ПРОИГРАЕТ', capture:'Захват сектора и открытие нового фронта',
      advanceTo:'Переход к этапу «%stage%»', fallBackTo:'Откат к этапу «%stage%»', holdLine:'Наступление остаётся на этапе «Прорыв»',
      offlineLabel:'ОФЛАЙН-ДЕМО', offlineFeatures:'Локальная кампания · бойцы ИИ · симуляция фронтов',
      playtestLabel:'STEAM PLAYTEST', playtestFeatures:'Общая война · реальные игроки · постоянные фронты · роты',
      campaignName:'ЖЕЛЕЗНЫЙ ФРОНТИР', commandKind:'КОМАНДОВАНИЕ', railKind:'ЖЕЛЕЗНАЯ ДОРОГА', supplyKind:'СНАБЖЕНИЕ', industrialKind:'ПРОМЫШЛЕННОСТЬ', waterKind:'ВОДОХРАНИЛИЩЕ', miningKind:'ДОБЫЧА',
      journeyLabel:'ВАША КАМПАНИЯ', journeySide:'ВЫБРАТЬ СТОРОНУ', journeyFront:'ВЫБРАТЬ ФРОНТ', journeyBattle:'БОЙ С БОТАМИ', journeyResult:'ИЗМЕНИТЬ ФРОНТ',
      resultCapturedTitle:'ПОБЕДА — СЕКТОР ЗАХВАЧЕН', resultCapturedBody:'Операция успешна. Сектор перешёл к вашей стороне, и открылся новый фронт.',
      resultAdvancedTitle:'ПОБЕДА — ОПЕРАЦИЯ ПРОДВИНУЛАСЬ', resultAdvancedBody:'Победа перевела операцию на следующий тактический этап. Следующий бой пройдёт на новой карте с более крупными силами.',
      resultRepulsedTitle:'ПОРАЖЕНИЕ — НАСТУПЛЕНИЕ ОТБИТО', resultRepulsedBody:'Противник удержал сектор. Операция продолжается, но откатилась на один этап; бой можно повторить.',
      resultStalemateTitle:'НИЧЬЯ — НАСТУПЛЕНИЕ ОТБИТО', resultStalemateBody:'Время истекло без победителя. Стратегически защитники удержали сектор, поэтому операция откатилась на один этап.',
      resultTimedOutTitle:'ИТОГИ КАМПАНИИ', resultTimedOutBody:'Операция завершилась после четырёх боёв. Каждый результат влиял на фронт; начните новую локальную кампанию или перейдите в общую войну плейтеста.',
      eventWhileAwayTitle:'ПОКА ВАС НЕ БЫЛО', eventWhileAwayBody:'Противоборствующие армии сражались за соседний сектор. Ваша операция всё ещё активна.',
      eventRecoveredTitle:'КАМПАНИЯ ВОССТАНОВЛЕНА', eventRecoveredBody:'Повреждённое локальное сохранение заменено новой кампанией.',
      eventRedTitle:'RED МОБИЛИЗОВАНЫ', eventBluTitle:'BLU МОБИЛИЗОВАНЫ', eventFactionBody:'Ваша сторона вступила в операцию «Железный путь».',
      eventDeployedTitle:'БОЕВОЙ БИЛЕТ ВЫДАН', eventDeployedBody:'Высадка в сектор %node% подтверждена. Выбранная карта запустится локально, а обе команды заполнят боты.',
      eventAbandonedTitle:'ВЫСАДКА ОТМЕНЕНА', eventAbandonedBody:'Прерванный бой отменён. Счёт и состояние войны не изменились.',
      eventRedVictoryTitle:'ПОБЕДА RED', eventBluVictoryTitle:'ПОБЕДА BLU', eventStalemateTitle:'НИЧЬЯ', eventBattleBody:'Результат полного раунда записан и применён к счёту кампании.',
      eventCapturedTitle:'СЕКТОР ЗАХВАЧЕН', eventCapturedBody:'Ваша сторона захватила сектор %node%.', eventFrontTitle:'ОТКРЫТ НОВЫЙ ФРОНТ', eventFrontBody:'Открыт путь в сектор %node%. Продолжите общую войну в плейтесте.',
      eventAdvancedTitle:'ОПЕРАЦИЯ ПРОДВИНУЛАСЬ', eventAdvancedBody:'Победа перевела операцию на следующий тактический этап.',
      eventRepulsedTitle:'НАСТУПЛЕНИЕ ОТБИТО', eventRepulsedBody:'Защитники удержали рубеж. Операция продолжается на обновлённом этапе.',
      eventFailedTitle:'ОПЕРАЦИЯ ЗАВЕРШЕНА', eventFailedBody:'Лимит в четыре боя исчерпан до захвата сектора.',
      eventElsewhereTitle:'ВОЙНА ПРОДОЛЖАЕТСЯ', eventElsewhereGainedBody:'Пока вы сражались, соседнее симулируемое наступление набрало силу.', eventElsewhereStalledBody:'Защитники соседнего фронта остановили наступление и сместили линию снабжения.',
      eventResetTitle:'ВТОРАЯ ГРАВИЙНАЯ ВОЙНА', eventResetBody:'Началась новая локальная кампания.', eventDebugTitle:'ЭТАП ОПЕРАЦИИ ИЗМЕНЁН', eventDebugBody:'Этап кампании изменён тестовой командой.'
    }
  };
  function F(k){ return (FLOW[lang] && FLOW[lang][k]) || FLOW.en[k] || k; }

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
    fronts:[], status:{checked:true,valid:true,serversKnown:true,name:'Local War Coordinator',online:1,matches:0,servers:1}, queue:{state:'idle'},
    demoState:{schemaVersion:1,faction:'NEUTRAL',needsFaction:true,stage:1,stages:3,teamSize:4,battlesPlayed:0,victories:0,defeats:0,completed:false,debriefUnread:false,target:'',pendingBattleId:'',pendingNode:'',pendingMap:'',lastTitle:'',lastBody:'',events:[]}
  };

  var NS = 'http://www.w3.org/2000/svg';
  var svg = document.getElementById('map');
  var viewport = document.getElementById('viewport');
  var territories = document.getElementById('territories');
  var routes = document.getElementById('routes');
  var markers = document.getElementById('markers');
  var data = null, selected = null, geo = null, stale = false;
  var lastFaction = 'NEUTRAL';
  var finaleDismissed = false;
  var deploymentPreview = null;
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
  function operationKind(kind){
    var key={'breakthrough':'breakthrough','advance':'advance','assault':'assault','simulated front':'simulatedFrontKind','new front':'newFrontKind','active':'activeKind'}[String(kind||'active').toLowerCase()];
    return F(key||'activeKind');
  }
  function sectorKind(kind){
    var key={command:'commandKind',rail:'railKind',supply:'supplyKind',industrial:'industrialKind',water:'waterKind',mining:'miningKind'}[String(kind||'').toLowerCase()];
    return key?F(key):(kind||'');
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
    var currentFaction=data.demoState&&data.demoState.faction||'NEUTRAL';
    if(isDemo&&data.demoState&&!data.demoState.needsFaction&&lastFaction==='NEUTRAL'&&data.demoState.target) selected=data.demoState.target;
    lastFaction=currentFaction;
    clear(territories); clear(routes); clear(markers);

    (data.nodes||[]).forEach(function(n){
      var f=geoFeature(n.id), coords;
      if(f && f.geometry && f.geometry.type==='Polygon') coords=f.geometry.coordinates[0];
      else coords=fallbackPolygon(n);
      var captured=data.demoState&&data.demoState.completed&&data.demoState.territoryCaptured&&data.demoState.target===n.id;
      var cls='sector sector-'+cssOwner(n.owner)+(frontFor(n.id)?' front':'')+(selected===n.id?' selected':'')+(captured?' captured':'');
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
    renderWarLog();
    renderDemoFlow();
    document.getElementById('warName').textContent=isDemo?F('campaignName'):(data.name||'GLOBAL WAR');
    document.getElementById('warStatus').textContent=(isDemo?T('demo'):T('live'))+(stale&&!isDemo?' · '+T('stale'):'');
  }

  function fillSidebar(){
    if(!isFull) return;
    var n=nodeById(selected)||nodeById(data.deploy)||(data.fronts&&data.fronts.length?nodeById(data.fronts[0].node):null)||data.nodes[0];
    if(!n) return;
    selected=n.id;
    var f=frontFor(n.id);
    document.getElementById('sectorName').textContent=n.name||n.id;
    document.getElementById('sectorMeta').textContent=[sectorKind(n.kind),n.region||''].filter(Boolean).join(' · ');
    document.getElementById('owner').textContent=n.owner||'—';
    document.getElementById('players').textContent=String((f&&f.players!=null)?f.players:(n.players||0));
    document.getElementById('battle').textContent=f?operationKind(f.kind):T('noBattle');
    var op=document.getElementById('operation'); op.hidden=!f;
    if(f){
      document.getElementById('attacker').textContent=(f.attacker||'—')+' → '+(n.owner||'—');
      document.getElementById('stage').textContent=T('stage').replace('%a%',f.stage||1).replace('%b%',f.stages||1);
      document.getElementById('progressFill').style.width=Math.round(Math.max(0,Math.min(1,Number(f.progress)||0))*100)+'%';
      document.getElementById('operationMeta').textContent=[f.map||'',f.server||''].filter(Boolean).join(' · ');
      Array.prototype.forEach.call(document.querySelectorAll('#stageRail [data-stage]'),function(item){
        var itemStage=Number(item.getAttribute('data-stage')), current=Number(f.stage)||1;
        item.className=itemStage<current?'done':(itemStage===current?'current':'');
      });
    }
    var demoState=data.demoState||{};
    var deploy=document.getElementById('deploy');
    deploy.textContent=demoState.pendingBattleId?F('pendingDeploy'):T('deploy'); deploy.classList.remove('here'); deploy.disabled=!f||!!f.locked||!!demoState.pendingBattleId||!!demoState.completed;
    var teamSize=(data.demoState&&data.demoState.teamSize)||Math.max(1,Math.round((f&&f.players||8)/2));
    document.getElementById('note').textContent=f&&f.locked?T('simulatedFront'):(f&&isDemo?T('demoReady').replace('%kind%',operationKind(f.kind).toLocaleLowerCase(lang)).replace(/%size%/g,teamSize):(!f?T('selectFront'):''));
  }

  function setText(id, text){ var e=document.getElementById(id); if(e) e.textContent=text||''; }
  function show(id, visible){ var e=document.getElementById(id); if(e) e.hidden=!visible; }
  function eventText(event){
    var node=nodeById(event.node), nodeName=(node&&node.name)||event.node||'—';
    var type=event.type;
    // Migrate the semantic meaning of events written by earlier demo builds.
    if(type==='FACTION_CHOSEN') type=/BLU/i.test(event.title||'')?'FACTION_CHOSEN_BLU':'FACTION_CHOSEN_RED';
    if(type==='BATTLE_RESULT') type=/STALEMATE/i.test(event.title||'')?'BATTLE_RESULT_STALEMATE':(/BLU/i.test(event.title||'')?'BATTLE_RESULT_BLU':'BATTLE_RESULT_RED');
    var pair={
      WHILE_AWAY:['eventWhileAwayTitle','eventWhileAwayBody'], SAVE_RECOVERED:['eventRecoveredTitle','eventRecoveredBody'],
      FACTION_CHOSEN_RED:['eventRedTitle','eventFactionBody'], FACTION_CHOSEN_BLU:['eventBluTitle','eventFactionBody'],
      BATTLE_DEPLOYED:['eventDeployedTitle','eventDeployedBody'], BATTLE_ABANDONED:['eventAbandonedTitle','eventAbandonedBody'],
      BATTLE_RESULT_RED:['eventRedVictoryTitle','eventBattleBody'], BATTLE_RESULT_BLU:['eventBluVictoryTitle','eventBattleBody'], BATTLE_RESULT_STALEMATE:['eventStalemateTitle','eventBattleBody'],
      TERRITORY_CAPTURED:['eventCapturedTitle','eventCapturedBody'], FRONT_OPENED:['eventFrontTitle','eventFrontBody'],
      OPERATION_ADVANCED:['eventAdvancedTitle','eventAdvancedBody'], OPERATION_REPULSED:['eventRepulsedTitle','eventRepulsedBody'], OPERATION_FAILED:['eventFailedTitle','eventFailedBody'],
      BACKGROUND_FRONT_GAINED:['eventElsewhereTitle','eventElsewhereGainedBody'], BACKGROUND_FRONT_STALLED:['eventElsewhereTitle','eventElsewhereStalledBody'],
      BACKGROUND_FRONT:['eventElsewhereTitle','eventElsewhereGainedBody'], CAMPAIGN_RESET:['eventResetTitle','eventResetBody'], DEBUG_STAGE:['eventDebugTitle','eventDebugBody']
    }[type];
    if(!pair) return {title:event.title||event.type||'',body:event.body||''};
    return {title:F(pair[0]),body:F(pair[1]).replace(/%node%/g,nodeName)};
  }
  function resultText(state){
    var keys={
      CAPTURED:['resultCapturedTitle','resultCapturedBody'], ADVANCED:['resultAdvancedTitle','resultAdvancedBody'],
      REPULSED:['resultRepulsedTitle','resultRepulsedBody'], STALEMATE:['resultStalemateTitle','resultStalemateBody'],
      TIMED_OUT:['resultTimedOutTitle','resultTimedOutBody']
    }[state.lastResult];
    // Migration for saves created before lastResult was persisted.
    if(!keys&&state.lastTitle){
      if(/TERRITORY CAPTURED/i.test(state.lastTitle)) keys=['resultCapturedTitle','resultCapturedBody'];
      else if(/OPERATION ADVANCED/i.test(state.lastTitle)) keys=['resultAdvancedTitle','resultAdvancedBody'];
      else if(/STALEMATE/i.test(state.lastTitle)) keys=['resultStalemateTitle','resultStalemateBody'];
      else if(/DEFEAT|REPULSED/i.test(state.lastTitle)) keys=['resultRepulsedTitle','resultRepulsedBody'];
      else if(/WAR REPORT/i.test(state.lastTitle)) keys=['resultTimedOutTitle','resultTimedOutBody'];
    }
    return keys?{title:F(keys[0]),body:F(keys[1])}:{title:state.lastTitle||'',body:state.lastBody||''};
  }
  function renderJourney(state){
    var root=document.getElementById('playerJourney');
    if(!isDemo||!state){ root.hidden=true; return; }
    root.hidden=false;
    var current=state.needsFaction?1:(state.debriefUnread||state.completed?4:(state.pendingBattleId?3:2));
    Array.prototype.forEach.call(root.querySelectorAll('[data-journey]'),function(item){
      var step=Number(item.getAttribute('data-journey'));
      item.className=step<current?'done':(step===current?'current':'');
      if(step===current)item.setAttribute('aria-current','step');else item.removeAttribute('aria-current');
    });
  }
  function renderWarLog(){
    if(!isFull) return;
    var list=document.getElementById('warEvents'); clear(list);
    var events=data&&data.demoState&&Array.isArray(data.demoState.events)?data.demoState.events:[];
    events.slice(-6).forEach(function(event){
      var item=document.createElement('li'), title=document.createElement('strong'), body=document.createElement('span'), localized=eventText(event);
      title.textContent=localized.title; body.textContent=localized.body;
      item.appendChild(title); item.appendChild(body); list.appendChild(item);
    });
    show('warLog', !!(isDemo&&events.length));
    show('resetDemo', !!(isDemo&&data&&data.demoState&&!data.demoState.needsFaction));
  }
  function fillFlowStats(state){
    var stats=document.getElementById('flowStats'); clear(stats);
    [['battlesPlayed','battles'],['victories','victories'],['defeats','defeats']].forEach(function(pair){
      var cell=document.createElement('div'), value=document.createElement('b'), label=document.createElement('span');
      value.textContent=String(Number(state[pair[0]])||0); label.textContent=F(pair[1]);
      cell.appendChild(value); cell.appendChild(label); stats.appendChild(cell);
    });
  }
  function fillBriefing(preview){
    var details=document.getElementById('briefingDetails'); clear(details);
    [[F('front'),preview.name],[F('objective'),operationKind(preview.front.kind)],[F('mode'),preview.front.map||'—'],[F('forceSize'),preview.teamSize+'v'+preview.teamSize]].forEach(function(pair){
      var cell=document.createElement('div'), label=document.createElement('small'), value=document.createElement('b');
      label.textContent=pair[0]; value.textContent=pair[1]; cell.appendChild(label); cell.appendChild(value); details.appendChild(cell);
    });
    var stakes=document.getElementById('briefingStakes'); clear(stakes);
    var stage=Math.max(1,Math.min(3,Number(preview.front.stage)||1));
    var labels=[F('breakthrough'),F('advance'),F('assault')];
    var win=stage===3?F('capture'):F('advanceTo').replace('%stage%',labels[stage]);
    var loss=stage===1?F('holdLine'):F('fallBackTo').replace('%stage%',labels[stage-2]);
    [[F('ifWin'),win],[F('ifLoss'),loss]].forEach(function(pair){
      var cell=document.createElement('div'), label=document.createElement('small'), value=document.createElement('b');
      label.textContent=pair[0]; value.textContent=pair[1]; cell.appendChild(label); cell.appendChild(value); stakes.appendChild(cell);
    });
  }
  function hideFlowActions(){ ['chooseFaction','pendingActions','deploymentActions','debriefActions','finalActions','flowStats','briefingDetails','briefingStakes','finalComparison'].forEach(function(id){show(id,false);}); }
  function renderDemoFlow(){
    if(!isFull) return;
    var root=document.getElementById('demoFlow'), state=data&&data.demoState;
    hideFlowActions();
    renderJourney(state);
    if(!isDemo||!state){ root.hidden=true; return; }
    setText('flowEyebrow',F('eyebrow'));
    if(state.needsFaction){
      setText('flowTitle',F('chooseTitle')); setText('flowBody',F('chooseBody')); show('chooseFaction',true); root.hidden=false; return;
    }
    if(deploymentPreview){
      setText('flowTitle',F('deployTitle')); setText('flowBody',F('deployBody')); fillBriefing(deploymentPreview);
      show('briefingDetails',true); show('briefingStakes',true); show('deploymentActions',true); root.hidden=false; return;
    }
    if(state.pendingBattleId){
      setText('flowTitle',F('pendingTitle')); setText('flowBody',(state.pendingMap?state.pendingMap+' — ':'')+F('pendingBody')); show('pendingActions',true); root.hidden=false; return;
    }
    if(state.completed){
      if(finaleDismissed){ root.hidden=true; return; }
      var finalResult=resultText(state);
      setText('flowTitle',finalResult.title||F('finalTitle')); setText('flowBody',(finalResult.body?finalResult.body+' ':'')+F('finalBody')); fillFlowStats(state); show('flowStats',true); show('finalComparison',true); show('finalActions',true); root.hidden=false; return;
    }
    if(state.debriefUnread){
      var debriefResult=resultText(state);
      setText('flowTitle',debriefResult.title||F('debriefTitle')); setText('flowBody',debriefResult.body||F('debriefBody')); fillFlowStats(state); show('flowStats',true); show('debriefActions',true); root.hidden=false; return;
    }
    root.hidden=true;
  }

  function post(line){
    try{ return fetch('/v1/campaign/command',{method:'POST',body:line}).then(function(){setTimeout(loadLive,80);}).catch(function(){}); }catch(e){ return Promise.resolve(); }
  }
  document.getElementById('deploy').addEventListener('click',function(){
    if(!selected||!data||!frontFor(selected)||frontFor(selected).locked) return;
    if(isDemo){
      deploymentPreview={node:selected,name:(nodeById(selected)||{}).name||selected,front:frontFor(selected),teamSize:(data.demoState&&data.demoState.teamSize)||4};
      renderDemoFlow(); return;
    }
    post('deploy '+selected); data.deploy=selected; render();
  });
  Array.prototype.forEach.call(document.querySelectorAll('[data-faction]'),function(button){
    button.addEventListener('click',function(){ post('select_faction '+button.getAttribute('data-faction')); });
  });
  document.getElementById('rejoinBattle').addEventListener('click',function(){post('rejoin');});
  document.getElementById('abandonBattle').addEventListener('click',function(){post('abandon');});
  document.getElementById('joinDeployment').addEventListener('click',function(){
    if(!deploymentPreview) return; var node=deploymentPreview.node; deploymentPreview=null;
    document.getElementById('demoFlow').hidden=true; document.getElementById('deploy').textContent=T('deploying'); document.getElementById('deploy').disabled=true;
    post('deploy '+node);
  });
  document.getElementById('cancelDeployment').addEventListener('click',function(){deploymentPreview=null; renderDemoFlow();});
  document.getElementById('continueCampaign').addEventListener('click',function(){post('ack_debrief');});
  document.getElementById('openPlaytest').addEventListener('click',function(){post('open_playtest');});
  document.getElementById('continueOffline').addEventListener('click',function(){finaleDismissed=true; document.getElementById('demoFlow').hidden=true;});
  document.getElementById('restartCampaign').addEventListener('click',function(){finaleDismissed=false; post('reset_demo');});
  document.getElementById('resetDemo').addEventListener('click',function(){finaleDismissed=false; post('reset_demo');});

  function applyLabels(){
    document.getElementById('sectorLabel').textContent=T('sector'); document.getElementById('ownerLabel').textContent=T('owner');
    document.getElementById('playersLabel').textContent=T('players'); document.getElementById('battleLabel').textContent=T('operation');
    document.getElementById('operationLabel').textContent=T('active'); document.getElementById('mapHint').textContent=T('mapHint');
    document.getElementById('resetView').textContent=T('reset'); document.getElementById('legacy').textContent=T('legacy'); document.getElementById('openFull').textContent=T('open');
    setText('warLogLabel',F('warLog')); setText('resetDemo',F('resetCampaign'));
    setText('rejoinBattle',F('rejoin')); setText('abandonBattle',F('abandon')); setText('continueCampaign',F('returnMap'));
    setText('joinDeployment',F('joinBattle')); setText('cancelDeployment',F('back'));
    setText('openPlaytest',F('followPlaytest')); setText('continueOffline',F('continueOffline')); setText('restartCampaign',F('restart'));
    setText('factionRedLabel',F('factionRed')); setText('factionBluLabel',F('factionBlu'));
    setText('factionRedHint',F('factionRedHint')); setText('factionBluHint',F('factionBluHint'));
    setText('offlineLabel',F('offlineLabel')); setText('offlineFeatures',F('offlineFeatures'));
    setText('playtestLabel',F('playtestLabel')); setText('playtestFeatures',F('playtestFeatures'));
    setText('journeyLabel',F('journeyLabel')); setText('journeySide',F('journeySide')); setText('journeyFront',F('journeyFront'));
    setText('journeyBattle',F('journeyBattle')); setText('journeyResult',F('journeyResult'));
    document.documentElement.lang=lang; document.getElementById('playerJourney').setAttribute('aria-label',F('journeyLabel'));
    var stageLabels=[F('breakthrough'),F('advance'),F('assault')];
    Array.prototype.forEach.call(document.querySelectorAll('#stageRail [data-stage] b'),function(item,index){item.textContent=stageLabels[index];});
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

  window.addEventListener('keydown',function(e){
    if(e.key!=='Escape') return;
    if(qs.get('embedded')==='1') post('close');
    else location.href='?'+new URLSearchParams({view:'card',demo:isDemo?'1':'0',lang:lang}).toString();
  });

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
    // Both providers use the game's loopback document. In demo mode it is the
    // local campaign model, not a remote coordinator; SAMPLE is only the
    // standalone/error fallback. This keeps the visible ticket and the native
    // map chosen by DEPLOY on the same authored data.
    document.getElementById('warStatus').textContent=T('waiting');
    loadLive();
    setInterval(loadLive,isDemo?750:3000);
  });
}());
