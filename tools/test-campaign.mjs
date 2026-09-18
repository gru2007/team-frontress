// Run with Node 20+ and Playwright installed (see docs/frontress-demo.md).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = new URL('../game/tc2/loose/resource/html/', import.meta.url);
const repoRoot = new URL('../', import.meta.url);

const nodes = [
	{ id:'red_hq', name:'RED HQ', owner:'RED', x:.07, y:.5, hq:true, kind:'command', region:'EU' },
	{ id:'yard', name:'Rail Yard', owner:'RED', x:.24, y:.22, kind:'rail', region:'EU' },
	{ id:'works', name:'Foundry 17', owner:'RED', x:.45, y:.55, kind:'industrial', region:'EU' },
	{ id:'reservoir', name:'Reservoir', owner:'BLU', x:.53, y:.18, kind:'water', region:'EU' },
	{ id:'junction', name:'Iron Junction', owner:'BLU', x:.78, y:.47, kind:'rail', region:'EU' },
	{ id:'blu_hq', name:'BLU HQ', owner:'BLU', x:.94, y:.52, hq:true, kind:'command', region:'EU' }
];
const edges = [{a:'red_hq',b:'yard'},{a:'yard',b:'works'},{a:'works',b:'reservoir'},{a:'reservoir',b:'junction'},{a:'junction',b:'blu_hq'}];

const liveCampaign = {
	version:1, lang:'english', demo:false, name:'Live fixture', deploy:'', nodes, edges,
	fronts:[{node:'works',attacker:'BLU',stage:2,stages:3,progress:.62,players:7,kind:'assault',map:'cp_gravelpit'}],
	status:{checked:true,valid:true,serversKnown:true,name:'Frontress',online:7,matches:1,servers:1}, queue:{state:'idle'}
};

function demoCampaign(overrides = {}) {
	const state = Object.assign({
		schemaVersion:1, faction:'NEUTRAL', needsFaction:true, stage:1, stages:3, teamSize:4,
		battlesPlayed:0, victories:0, defeats:0, completed:false, territoryCaptured:false,
		debriefUnread:false, target:'', pendingBattleId:'', pendingNode:'', pendingMap:'', lastResult:'', lastTitle:'', lastBody:'', events:[]
	}, overrides);
	if (!Object.prototype.hasOwnProperty.call(overrides,'teamSize')) state.teamSize=[4,6,9][state.stage-1] || 4;
	const selected = !state.needsFaction && state.faction !== 'NEUTRAL';
	const target = state.faction === 'RED' ? 'reservoir' : 'works';
	const front = state.completed && state.territoryCaptured
		? [{node:state.faction === 'RED'?'junction':'yard',attacker:state.faction,stage:1,stages:3,progress:.08,players:0,kind:'new front',map:'Playtest',locked:true}]
		: selected && !state.completed
			? [{node:target,attacker:state.faction,stage:state.stage,stages:3,progress:.15,players:state.pendingBattleId?state.teamSize*2:0,kind:['breakthrough','advance','assault'][state.stage-1],map:(state.faction==='RED'?['koth_viaduct','cp_badlands','cp_foundry']:['cp_gorge','pl_badwater','cp_dustbowl'])[state.stage-1],server:'local'}]
			: [];
	if (selected && !state.completed) front.push({node:state.faction === 'RED'?'yard':'junction',attacker:state.faction === 'RED'?'BLU':'RED',stage:1,stages:3,progress:.2,players:0,kind:'simulated front',map:'world tick',server:'local simulation',locked:true});
	state.target = selected ? target : '';
	return {version:1,lang:'english',demo:true,name:'Iron Frontier',deploy:'',nodes:structuredClone(nodes),edges:structuredClone(edges),fronts:front,status:{checked:true,valid:true,serversKnown:true,name:'Local War Coordinator',online:1,matches:state.pendingBattleId?1:0,servers:1},queue:{state:'idle'},demoState:state};
}

let browser, server, origin, campaignResponse, campaignStatus, commands;

function applyCommand(body) {
	if (!campaignResponse?.demoState) return;
	const [verb, arg] = body.trim().split(/\s+/, 2);
	const old = campaignResponse.demoState;
	if (verb === 'select_faction' && (arg === 'RED' || arg === 'BLU')) campaignResponse = demoCampaign({...old,faction:arg,needsFaction:false});
	if (verb === 'deploy' && campaignResponse.fronts.some(front => front.node === arg && !front.locked)) campaignResponse = demoCampaign({...old,pendingBattleId:'demo_00001',pendingNode:arg,pendingMap:campaignResponse.fronts[0].map});
	if (verb === 'abandon') campaignResponse = demoCampaign({...old,pendingBattleId:'',pendingNode:'',pendingMap:''});
	if (verb === 'ack_debrief') campaignResponse = demoCampaign({...old,debriefUnread:false});
	if (verb === 'reset_demo') campaignResponse = demoCampaign();
}

before(async () => {
	campaignResponse = liveCampaign; campaignStatus = 200; commands = [];
	server = createServer(async (req, res) => {
		const url = new URL(req.url, 'http://localhost');
		if (url.pathname === '/v1/campaign') {
			res.writeHead(campaignStatus, {'Content-Type':'application/json','Cache-Control':'no-store'});
			res.end(JSON.stringify(campaignResponse)); return;
		}
		if (url.pathname === '/v1/campaign/command' && req.method === 'POST') {
			let body=''; for await (const chunk of req) body += chunk;
			commands.push(body); applyCommand(body); res.writeHead(204).end(); return;
		}
		const relative=decodeURIComponent(url.pathname).replace(/^\/+/, '');
		if (!relative || relative.includes('..')) { res.writeHead(404).end(); return; }
		try {
			const body=await readFile(new URL(relative,root));
			const type=relative.endsWith('.html')?'text/html; charset=utf-8':relative.endsWith('.js')?'text/javascript; charset=utf-8':relative.endsWith('.css')?'text/css; charset=utf-8':relative.endsWith('.geojson')?'application/geo+json':'application/octet-stream';
			res.writeHead(200,{'Content-Type':type}).end(body);
		} catch { res.writeHead(404).end(); }
	});
	await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
	origin=`http://127.0.0.1:${server.address().port}`;
	browser=await chromium.launch({headless:true});
});

after(async () => {
	await browser?.close();
	await new Promise(resolve => server ? server.close(resolve) : resolve());
});

async function open(context, query) {
	const page=await context.newPage(), errors=[];
	page.on('pageerror',error => errors.push(error.message)); page.errors=errors;
	await page.goto(`${origin}/campaign.html?${query}`); await page.waitForSelector('.sector'); return page;
}

test('offline battle config fills around the human and uses bot-supported maps', async () => {
	const [source, config, botCycle] = await Promise.all([
		readFile(new URL('src/game/client/tf/frontress/tf_campaign_map.cpp',repoRoot),'utf8'),
		readFile(new URL('game/tc2/cfg/frontress_demo.cfg',repoRoot),'utf8'),
		readFile(new URL('game/tc2/cfg/mapcycle_quickplay_bots.txt',repoRoot),'utf8')
	]);
	const mapBlock=source.match(/s_pszBluMaps[\s\S]*?s_pszRedMaps[^;]+;/)?.[0] || '';
	const authoredMaps=Array.from(mapBlock.matchAll(/"((?:cp|pl|koth)_[a-z0-9_]+)"/g),match=>match[1]);
	const supported=new Set(botCycle.split(/\r?\n/).map(line=>line.trim()).filter(Boolean));
	assert.deepEqual(authoredMaps,['cp_gorge','pl_badwater','cp_dustbowl','koth_viaduct','cp_badlands','cp_foundry']);
	for (const map of authoredMaps) assert.equal(supported.has(map),true,`${map} must remain in the bot mapcycle`);
	assert.match(config,/^tf_bot_quota_mode fill$/m); assert.match(config,/^tf_bot_quota 0$/m);
	assert.match(config,/^tf_bot_auto_vacate 0$/m); assert.match(config,/^tf_bot_offline_practice 1$/m);
	assert.match(config,/^mp_waitingforplayers_cancel 1$/m);
	assert.match(config,/^mp_stalemate_enable 0$/m);
	assert.match(config,/^mp_winlimit 1$/m); assert.match(config,/^mp_maxrounds 1$/m);
	assert.match(source,/exec frontress_demo\.cfg\\njointeam %s\\ntf_bot_quota %d/);
	assert.match(source,/nWinningTeam == TEAM_UNASSIGNED/);
	assert.match(source,/strLastResultType = bStalemate \? "STALEMATE" : "REPULSED"/);
	assert.match(source,/DemoLocalizedResultText/);
});

test('campaign redirect preserves demo mode and standalone fallback opens faction choice', async () => {
	const context=await browser.newContext({viewport:{width:1440,height:900}}); campaignStatus=503;
	try {
		const page=await open(context,'view=full&demo=1&lang=ru&embedded=1'); const url=new URL(page.url());
		assert.equal(url.pathname,'/war-map-v2/index.html'); assert.equal(url.searchParams.get('demo'),'1');
		assert.equal(await page.locator('.sector').count(),8); assert.equal(await page.locator('#warStatus').textContent(),'ОФЛАЙН-ДЕМО');
		assert.equal(await page.locator('#demoFlow').isVisible(),true); assert.match(await page.locator('#flowTitle').textContent(),/ВЫБЕРИТЕ/);
		assert.equal(await page.locator('#chooseFaction').isVisible(),true);
		assert.deepEqual(await page.locator('#playerJourney li').allTextContents(),['1ВЫБРАТЬ СТОРОНУ','2ВЫБРАТЬ ФРОНТ','3БОЙ С БОТАМИ','4ИЗМЕНИТЬ ФРОНТ']);
		assert.equal(await page.locator('#playerJourney li.current').getAttribute('data-journey'),'1');
		for (const id of ['pendingActions','deploymentActions','debriefActions','finalActions','flowStats','briefingDetails','briefingStakes','finalComparison'])
			assert.equal(await page.locator(`#${id}`).isVisible(),false,`${id} must stay hidden on faction choice`);
		assert.deepEqual(page.errors,[]);
	} finally { campaignStatus=200; await context.close(); }
});

test('faction selection creates the authored front and DEPLOY creates one battle ticket', async () => {
	const context=await browser.newContext(); commands=[]; campaignResponse=demoCampaign();
	try {
		const page=await open(context,'view=full&demo=1&embedded=1');
		await page.locator('[data-faction="BLU"]').click(); await page.waitForFunction(() => !document.querySelector('#demoFlow').offsetParent);
		assert.equal(await page.locator('#playerJourney li.current').getAttribute('data-journey'),'2');
		assert.equal((await page.locator('.sector[data-id="works"]').getAttribute('class')).includes('front'),true);
		await page.locator('.sector[data-id="yard"]').click(); assert.equal(await page.locator('#deploy').isDisabled(),true);
		await page.locator('.sector[data-id="works"]').click(); assert.equal(await page.locator('#deploy').isDisabled(),false);
		await page.locator('#deploy').click(); await page.waitForFunction(() => document.querySelector('#deploymentActions').offsetParent !== null);
		assert.match(await page.locator('#briefingDetails').textContent(),/4v4/);
		assert.match(await page.locator('#briefingDetails').textContent(),/cp_gorge/);
		assert.match(await page.locator('#briefingStakes').textContent(),/Advance/);
		assert.match(await page.locator('#briefingStakes').textContent(),/remains at Breakthrough/);
		assert.deepEqual(commands,['select_faction BLU']);
		await page.locator('#joinDeployment').click(); await page.waitForFunction(() => document.querySelector('#pendingActions').offsetParent !== null);
		assert.equal(await page.locator('#playerJourney li.current').getAttribute('data-journey'),'3');
		assert.deepEqual(commands,['select_faction BLU','deploy works']); assert.deepEqual(page.errors,[]);
	} finally { await context.close(); }
});

test('pending ticket can be abandoned without advancing campaign', async () => {
	const context=await browser.newContext(); commands=[];
	campaignResponse=demoCampaign({faction:'RED',needsFaction:false,pendingBattleId:'demo_00007',pendingNode:'reservoir',pendingMap:'koth_viaduct'});
	try {
		const page=await open(context,'view=full&demo=1'); assert.equal(await page.locator('#pendingActions').isVisible(),true);
		await page.locator('#abandonBattle').click(); await page.waitForFunction(() => !document.querySelector('#demoFlow').offsetParent);
		assert.deepEqual(commands,['abandon']); assert.equal(campaignResponse.demoState.stage,1); assert.equal(campaignResponse.demoState.battlesPlayed,0);
	} finally { await context.close(); }
});

test('debrief and war history render coordinator events as text and acknowledge once', async () => {
	const context=await browser.newContext(); commands=[];
	campaignResponse=demoCampaign({faction:'BLU',needsFaction:false,stage:2,battlesPlayed:1,victories:1,debriefUnread:true,lastResult:'ADVANCED',lastTitle:'OPERATION ADVANCED',lastBody:'Victory opened the next stage.',events:[{sequence:1,type:'OPERATION_ADVANCED',title:'<b>SAFE TITLE</b>',body:'The line moved.',node:'works'}]});
	try {
		const page=await open(context,'view=full&demo=1'); assert.equal(await page.locator('#debriefActions').isVisible(),true);
		assert.match(await page.locator('#note').textContent(),/6v6/);
		assert.equal(await page.locator('#warEvents b').count(),0); assert.equal(await page.locator('#warEvents strong').textContent(),'OPERATION ADVANCED');
		assert.equal(await page.locator('#playerJourney li.current').getAttribute('data-journey'),'4');
		await page.locator('#continueCampaign').click(); await page.waitForFunction(() => !document.querySelector('#demoFlow').offsetParent);
		assert.deepEqual(commands,['ack_debrief']); assert.deepEqual(page.errors,[]);
	} finally { await context.close(); }
});

test('Russian localization covers saved results, legacy events and battle meaning', async () => {
	const context=await browser.newContext(); commands=[];
	campaignResponse=demoCampaign({faction:'RED',needsFaction:false,stage:1,battlesPlayed:1,defeats:1,debriefUnread:true,lastResult:'STALEMATE',lastTitle:'STALEMATE - OFFENSIVE REPULSED',lastBody:'English legacy fallback.',events:[
		{sequence:1,type:'FACTION_CHOSEN',title:'RED MOBILIZED',body:'English legacy fallback.',node:'reservoir'},
		{sequence:2,type:'BATTLE_RESULT',title:'STALEMATE',body:'English legacy fallback.',node:'reservoir'},
		{sequence:3,type:'OPERATION_REPULSED',title:'OFFENSIVE REPULSED',body:'English legacy fallback.',node:'reservoir'}
	]});
	campaignResponse.lang='russian';
	try {
		const page=await open(context,'view=full&demo=1&lang=en');
		assert.equal(await page.locator('html').getAttribute('lang'),'ru');
		assert.equal(await page.locator('#flowTitle').textContent(),'НИЧЬЯ — НАСТУПЛЕНИЕ ОТБИТО');
		assert.match(await page.locator('#flowBody').textContent(),/защитники удержали сектор/i);
		assert.match(await page.locator('#battle').textContent(),/ПРОРЫВ/);
		assert.deepEqual(await page.locator('#warEvents strong').allTextContents(),['RED МОБИЛИЗОВАНЫ','НИЧЬЯ','НАСТУПЛЕНИЕ ОТБИТО']);
		assert.doesNotMatch(await page.locator('#warEvents').textContent(),/English legacy fallback|OFFENSIVE REPULSED/);
		assert.equal(await page.locator('#playerJourney li.current').getAttribute('data-journey'),'4');
		assert.deepEqual(page.errors,[]);
	} finally { await context.close(); }
});

test('captured territory opens a locked next front and finale links to the Playtest', async () => {
	const context=await browser.newContext(); commands=[];
	campaignResponse=demoCampaign({faction:'BLU',needsFaction:false,stage:3,battlesPlayed:3,victories:3,completed:true,territoryCaptured:true,lastTitle:'TERRITORY CAPTURED',lastBody:'Foundry 17 now belongs to BLU.',events:[{sequence:4,type:'FRONT_OPENED',title:'NEW FRONT OPENED',body:'The route continues.',node:'yard'}]});
	campaignResponse.nodes.find(node => node.id === 'works').owner='BLU';
	try {
		const page=await open(context,'view=full&demo=1'); assert.equal(await page.locator('#finalActions').isVisible(),true);
		assert.equal(await page.locator('#finalComparison').isVisible(),true); assert.match(await page.locator('#finalComparison').textContent(),/real players/);
		await page.locator('#openPlaytest').click(); await page.waitForTimeout(100); assert.deepEqual(commands,['open_playtest']);
		await page.locator('#continueOffline').click(); await page.waitForTimeout(900); assert.equal(await page.locator('#demoFlow').isVisible(),false);
		await page.locator('.sector[data-id="yard"]').click();
		assert.equal(await page.locator('#deploy').isDisabled(),true); assert.equal((await page.locator('.sector[data-id="works"]').getAttribute('class')).includes('sector-blu'),true);
	} finally { await context.close(); }
});

test('normal mode uses the live campaign feed and retains its parameters', async () => {
	const context=await browser.newContext(); campaignResponse=liveCampaign; campaignStatus=200;
	try {
		const page=await open(context,'view=full&demo=0'); await page.waitForFunction(() => document.querySelector('#warName').textContent === 'Live fixture');
		assert.equal(await page.locator('.sector').count(),6); assert.equal(await page.locator('#warStatus').textContent(),'LIVE CAMPAIGN');
		assert.equal(new URL(await page.locator('#openFull').getAttribute('href'),page.url()).searchParams.get('demo'),'0');
		campaignStatus=503; await page.waitForFunction(() => document.querySelector('#warStatus').textContent.includes('unavailable'),null,{timeout:4500});
		assert.equal(await page.locator('.sector').count(),6); assert.deepEqual(page.errors,[]);
	} finally { campaignStatus=200; await context.close(); }
});

test('embedded Escape posts close and legacy routing remains available', async () => {
	const context=await browser.newContext(); commands=[]; campaignResponse=demoCampaign({faction:'BLU',needsFaction:false});
	try {
		const page=await open(context,'view=full&demo=1&embedded=1'); const legacyURL=new URL(await page.locator('#legacy').getAttribute('href'),page.url());
		assert.equal(legacyURL.pathname,'/campaign-legacy.html'); assert.equal(legacyURL.searchParams.get('demo'),'1');
		await page.keyboard.press('Escape'); await page.waitForTimeout(50); assert.deepEqual(commands,['close']);
	} finally { await context.close(); }
});
