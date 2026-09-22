// Run with Node 20+ and Playwright installed (see docs/CAMPAIGN_DEMO.md).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { join } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = new URL('../game/tc2/loose/resource/html/', import.meta.url);
let browser, server, origin;

before(async () => {
	server = createServer(async (req, res) => {
		const path = new URL(req.url, 'http://localhost').pathname;
		if (path !== '/campaign.html' && !/^\/fonts\/[\w.-]+\.woff2$/.test(path)) {
			res.writeHead(404).end();
			return;
		}
		try {
			const body = await readFile(new URL(path.slice(1), root));
			res.writeHead(200, { 'Content-Type': path.endsWith('.html') ? 'text/html; charset=utf-8' : 'font/woff2' }).end(body);
		} catch { res.writeHead(404).end(); }
	});
	await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
	origin = `http://127.0.0.1:${server.address().port}`;
	browser = await chromium.launch({ headless: true });
});

after(async () => {
	await browser?.close();
	await new Promise(resolve => server ? server.close(resolve) : resolve());
});

async function open(context, view = 'full', extra = '&lang=ru') {
	const page = await context.newPage();
	const errors = [];
	page.on('pageerror', error => errors.push(error.message));
	page.errors = errors;
	await page.goto(`${origin}/campaign.html?view=${view}${extra}`);
	await page.waitForFunction(() => document.querySelectorAll('.nodeHit').length === 8);
	await page.evaluate(() => document.fonts.ready);
	return page;
}

test('demo is offline, localized, and cannot send deploy commands', async () => {
	const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	try {
		const page = await open(context);
		const commands = [];
		page.on('request', req => { if (req.method() === 'POST') commands.push(req.postData()); });
		await page.locator('.nodeHit[data-id="quarry"]').click();
		await page.locator('#deploy').click();
		assert.equal(await page.locator('#selName').textContent(), 'Карьер');
		assert.equal(await page.locator('#deploy').textContent(), 'Сектор отмечен');
		assert.equal(await page.locator('#queuePanel').isVisible(), false);
		assert.equal(await page.locator('#demoBadge').isVisible(), true);
		assert.deepEqual(commands, []);
		assert.deepEqual(page.errors, []);
	} finally { await context.close(); }
});

test('three complete cycles are periodic, bounded, and preserve SVG nodes', async () => {
	const context = await browser.newContext();
	try {
		const page = await open(context);
		const result = await page.evaluate(() => {
			const element = document.querySelector('.frontLine');
			const original = demoState(0);
			let maxSeam = 0;
			for (let cycle = 0; cycle < 3; cycle++) {
				for (let i = 0; i <= 64; i++) {
					const time = cycle * DEMO_DURATION + i * 1000;
					const state = demoState(time);
					if (state.blu < 0 || state.blu > 1 || state.red < 0 || state.red > 1) throw new Error('unbounded');
					demoClock.pausedAt = time;
					updateDemo();
				}
				const a = demoState((cycle + 1) * DEMO_DURATION - 1);
				const b = demoState((cycle + 1) * DEMO_DURATION + 1);
				maxSeam = Math.max(maxSeam, Math.abs(a.blu - b.blu), Math.abs(a.red - b.red));
			}
			const before = element.getAttribute('d');
			demoClock.pausedAt = DEMO_DURATION * .4;
			updateDemo();
			return { maxSeam, original, repeated:demoState(DEMO_DURATION * 3),
				persistent:element === document.querySelector('.frontLine'),
				territoryMoves:before !== element.getAttribute('d'),
				invalid: /NaN|Infinity/.test(document.querySelector('#map').innerHTML) };
		});
		assert.deepEqual(result.original, result.repeated);
		assert.ok(result.maxSeam < .00001);
		assert.equal(result.persistent, true);
		assert.equal(result.territoryMoves, true);
		assert.equal(result.invalid, false);
		assert.deepEqual(page.errors, []);
	} finally { await context.close(); }
});

test('card and theater share time, geometry, and pause/resume', async () => {
	const context = await browser.newContext();
	try {
		const card = await open(context, 'card');
		const full = await open(context);
		await full.locator('#pauseDemo').click();
		await card.waitForFunction(() => demoClock.pausedAt !== null);
		const snapshot = page => page.evaluate(() => ({ time:demoTime(), state:demoState(demoTime()),
			nodes:data.nodes.map(n => [n.id, mapX(n), mapY(n)]), field:field(500, 200) }));
		assert.deepEqual(await snapshot(card), await snapshot(full));
		const path = await full.locator('.frontLine').getAttribute('d');
		await full.waitForTimeout(350);
		assert.equal(await full.locator('.frontLine').getAttribute('d'), path);
		await full.locator('#pauseDemo').click();
		await card.waitForFunction(() => demoClock.pausedAt === null);
		assert.ok(Math.abs((await snapshot(card)).time - (await snapshot(full)).time) < 100);
		assert.equal(await card.locator('.labelName').count(), 2);
		assert.equal(await full.locator('.labelName').count(), 8);
	} finally { await context.close(); }
});

test('four captures change ownership, rings, defense and supply without replacing nodes', async () => {
	const context = await browser.newContext({ viewport:{width:1440, height:900} });
	try {
		const page = await open(context);
		const result = await page.evaluate(() => {
			demoClock.offset = 0;
			const nodes = [...document.querySelectorAll('.nodeHit')];
			const snapshots = [];
			for (const time of [0, 11999, 12000, 15000, 24000, 29999, 30000, 33000, 43999, 44000, 47000, 55999, 56000, 59000, 63999, 64000]) {
				demoClock.pausedAt = time;
				updateDemo();
				snapshots.push({time, works:nodeById('works').owner, reservoir:nodeById('reservoir').owner,
					worksProgress:data.fronts[0].progress, reservoirProgress:data.fronts[1].progress,
					worksActive:data.fronts[0].active,
					fill:document.querySelector('[data-id="works"] .nodeFace').getAttribute('fill'),
					ownerText:document.querySelector('#selOwner').textContent,
					front:territoryPaths[2].getAttribute('d'),
					road:demoEdges.find(e => e.a.id === 'works' && e.b.id === 'quarry').road.getAttribute('stroke-dasharray')});
			}
			return {snapshots, persistent:nodes.every((n, i) => n === document.querySelectorAll('.nodeHit')[i]),
				stand:[demoState(23000).red, demoState(24000).red, demoState(25000).red]};
		});
		const at = time => result.snapshots.find(s => s.time === time);
		assert.equal(at(11999).works, 'RED');
		assert.equal(at(12000).works, 'BLU');
		assert.equal(at(12000).worksProgress, 1);
		assert.equal(at(15000).worksActive, false);
		assert.equal(at(15000).fill, 'rgb(74,127,168)');
		assert.ok(at(15000).ownerText.includes('BLU'));
		assert.equal(at(15000).road, 'none');
		assert.equal(at(29999).reservoir, 'BLU');
		assert.equal(at(30000).reservoir, 'RED');
		assert.equal(at(30000).reservoirProgress, 1);
		assert.equal(at(43999).works, 'BLU');
		assert.equal(at(44000).works, 'RED');
		assert.equal(at(44000).worksProgress, 1);
		assert.equal(at(47000).fill, 'rgb(194,59,44)');
		assert.equal(at(47000).road, '9 8');
		assert.equal(at(55999).reservoir, 'RED');
		assert.equal(at(56000).reservoir, 'BLU');
		assert.equal(at(56000).reservoirProgress, 1);
		assert.equal(at(63999).front, at(64000).front);
		assert.equal(at(63999).works, at(64000).works);
		assert.equal(at(63999).reservoir, at(64000).reservoir);
		assert.deepEqual(result.stand, [.42, .42, .42]);
		assert.equal(result.persistent, true);
		if (process.env.CAMPAIGN_SCREENSHOTS) {
			for (const time of [13000, 31000, 45000, 57000]) {
				await page.evaluate(time => { demoClock.pausedAt = time; updateDemo(); }, time);
				assert.ok(Number(await page.locator('#captureNotice').evaluate(n => getComputedStyle(n).opacity)) > .9);
				await page.screenshot({path:join(process.env.CAMPAIGN_SCREENSHOTS, `capture-${time / 1000}.png`)});
			}
		}
		assert.deepEqual(page.errors, []);
	} finally { await context.close(); }
});

test('demo front advances every frame with stable dash phase and no raster work', async t => {
	const context = await browser.newContext();
	try {
		const page = await open(context);
		const result = await page.evaluate(() => {
			paintTerrain = () => { throw new Error('Raster work during playback'); };
			frontRuns = () => { throw new Error('Contour rebuild during playback'); };
			demoClock.offset = 0;
			const frames = [];
			const start = performance.now();
			for (let i = 0; i < 180; i++) {
				demoClock.pausedAt = 11000 + i * 1000 / 60;
				updateDemo();
				frames.push({dash:parseFloat(territoryPaths[2].style.strokeDashoffset),
					path:territoryPaths[2].getAttribute('d')});
			}
			return {frames, elapsed:performance.now() - start,
				length:territoryPaths[2].getAttribute('pathLength'),
				cssAnimation:getComputedStyle(territoryPaths[2]).animationName};
		});
		assert.equal(result.length, '520');
		assert.equal(result.cssAnimation, 'none');
		let changed = 0;
		for (let i = 1; i < result.frames.length; i++) {
			const previous = result.frames[i - 1], frame = result.frames[i];
			const movement = ((previous.dash - frame.dash) % 26 + 26) % 26;
			assert.ok(Math.abs(movement - 1 / 6) < .001, `dash phase at frame ${i}`);
			assert.equal(frame.path.match(/Q/g).length, previous.path.match(/Q/g).length);
			if (frame.path !== previous.path) changed++;
		}
		assert.ok(changed > 170, 'the contour must not update at a 4 Hz cadence');
		t.diagnostic(`180 demo updates: ${result.elapsed.toFixed(1)} ms of scripting (not GPU/frame timing)`);
		const cadence = await page.evaluate(() => new Promise(resolve => {
			demoClock.pausedAt = null;
			demoClock.offset = Date.now() - 11000;
			const intervals = [];
			let previous;
			function sample(now) {
				if (previous !== undefined) intervals.push(now - previous);
				previous = now;
				if (intervals.length < 120) requestAnimationFrame(sample);
				else {
					intervals.sort((a, b) => a - b);
					resolve({median:intervals[60], p95:intervals[114], max:intervals[119]});
				}
			}
			requestAnimationFrame(sample);
		}));
		t.diagnostic(`Browser RAF during capture: median ${cadence.median.toFixed(1)} ms, p95 ${cadence.p95.toFixed(1)} ms, max ${cadence.max.toFixed(1)} ms (not a native VGUI benchmark)`);
		assert.deepEqual(page.errors, []);
	} finally { await context.close(); }
});

test('live polling and keyboard selection do not replace animated elements', async () => {
	const context = await browser.newContext();
	try {
		const fixturePage = await open(context);
		const fixture = await fixturePage.evaluate(() => JSON.parse(JSON.stringify(SAMPLE)));
		fixture.demo = false;
		let fail = false;
		await context.route('**/v1/campaign', route => route.fulfill({ status:fail ? 503 : 200,
			contentType:'application/json', body:JSON.stringify(fixture) }));
		const page = await open(context, 'full', '&demo=0');
		assert.equal(new URL(await page.locator('#cardOpen').getAttribute('href'), origin).searchParams.get('demo'), '0');
		await page.evaluate(() => {
			window.originalFront = document.querySelector('.frontLine');
			window.originalAnimation = originalFront.getAnimations()[0];
		});
		const quarry = page.locator('.nodeHit[data-id="quarry"]');
		await quarry.focus();
		await page.keyboard.press('Enter');
		await page.waitForTimeout(5700);
		assert.equal(await quarry.getAttribute('aria-pressed'), 'true');
		assert.equal(await page.evaluate(() => originalFront === document.querySelector('.frontLine')), true);
		assert.equal(await page.evaluate(() => originalAnimation === originalFront.getAnimations()[0]), true);
		assert.ok(await page.evaluate(() => originalAnimation.currentTime > 5200));
		assert.equal(await page.evaluate(() => document.activeElement.getAttribute('data-id')), 'quarry');
		fail = true;
		await page.evaluate(() => poll());
		await page.waitForFunction(() => document.querySelector('#subtitle').textContent.includes('Connection lost'));
		fail = false;
		await page.evaluate(() => poll());
		await page.waitForFunction(() => !document.querySelector('#subtitle').textContent.includes('Connection lost'));
		assert.deepEqual(page.errors, []);
	} finally { await context.close(); }
});

test('standalone open/close, embedded Escape, and game language', async () => {
	const context = await browser.newContext();
	try {
		const card = await open(context, 'card');
		await card.locator('#cardOpen').click();
		assert.equal(new URL(card.url()).searchParams.get('view'), 'full');
		await card.keyboard.press('Escape');
		await card.waitForURL('**/*view=card*');
		const commands = [];
		await context.route('**/v1/campaign/command', route => {
			commands.push(route.request().postData());
			return route.fulfill({ status:204 });
		});
		await context.route('**/v1/campaign', route => route.fulfill({
			json:{ lang:'russian', status:{ valid:false } } }));
		const full = await open(context, 'full', '&embedded=1');
		await full.waitForFunction(() => document.documentElement.lang === 'ru');
		await full.locator('.nodeHit[data-id="yard"]').focus();
		await full.keyboard.press('Escape');
		await full.waitForTimeout(100);
		assert.deepEqual(commands, ['close']);
		assert.equal(await full.locator('#cardPop').textContent(), '');
	} finally { await context.close(); }
});

test('desktop, compact card and mobile layouts fit; capture screenshots', async () => {
	for (const [name, view, width, height] of [
		['card', 'card', 734, 230], ['card-small', 'card', 360, 180],
		['full', 'full', 1440, 900], ['mobile', 'full', 390, 844]
	]) {
		const context = await browser.newContext({ viewport:{ width, height } });
		try {
			const page = await open(context, view);
			await page.evaluate(() => { demoClock.pausedAt = DEMO_DURATION * .4; updateDemo(); });
			const bounds = await page.evaluate(() => ({
				overflow:document.documentElement.scrollWidth > innerWidth,
				mapHeight:document.querySelector('#mapWrap').getBoundingClientRect().height,
				buttons:[...document.querySelectorAll('.view-full #deploy, .view-full #closeMap, .view-card #cardOpen')].map(n => {
					const r = n.getBoundingClientRect(); return r.left >= 0 && r.top >= 0 && r.right <= innerWidth && r.bottom <= innerHeight;
				})
			}));
			assert.equal(bounds.overflow, false, name);
			assert.ok(bounds.mapHeight > 100, name);
			assert.ok(bounds.buttons.every(Boolean), name);
			assert.deepEqual(page.errors, [], name);
			if (process.env.CAMPAIGN_SCREENSHOTS) await page.screenshot({ path:join(process.env.CAMPAIGN_SCREENSHOTS, `${name}.png`) });
		} finally { await context.close(); }
	}
});

test('reduced motion produces a stable, usable demo', async () => {
	const context = await browser.newContext({ reducedMotion:'reduce' });
	try {
		const page = await open(context);
		const path = await page.locator('.frontLine').getAttribute('d');
		await page.waitForTimeout(400);
		assert.equal(await page.locator('.frontLine').getAttribute('d'), path);
		assert.equal(await page.locator('#pauseDemo').isDisabled(), true);
		assert.equal(await page.locator('.frontLine').evaluate(n => getComputedStyle(n).animationName), 'none');
		await page.locator('.nodeHit[data-id="quarry"]').click();
		assert.equal(await page.locator('#selName').textContent(), 'Карьер');
	} finally { await context.close(); }
});

test('live loading, empty feed, recovery and disappearance disable stale actions', async () => {
	const context = await browser.newContext();
	try {
		const fixturePage = await open(context);
		const fixture = await fixturePage.evaluate(() => JSON.parse(JSON.stringify(SAMPLE)));
		fixture.demo = false;
		let response = null;
		await context.route('**/v1/campaign', route => route.fulfill({ json:response }));
		const page = await context.newPage();
		await page.goto(`${origin}/campaign.html?view=full&demo=0`);
		await page.waitForFunction(() => document.body.classList.contains('offline'));
		assert.equal(await page.locator('#offline').textContent(), 'Waiting for the game...');
		response = fixture;
		await page.evaluate(() => poll());
		await page.waitForFunction(() => document.querySelectorAll('.nodeHit').length === 8);
		assert.equal(await page.locator('#deploy').isDisabled(), false);
		response = { ...fixture, nodes:[], fronts:[], edges:[] };
		await page.evaluate(() => poll());
		await page.waitForFunction(() => document.querySelector('#deploy').disabled);
		assert.equal(await page.locator('.nodeHit').count(), 0);
		assert.equal(await page.locator('#offline').textContent(), 'No campaign is running');
		assert.equal(await page.evaluate(() => selected), null);
	} finally { await context.close(); }
});
