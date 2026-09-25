// Store screenshots of the demo's menu, straight from the page.
//
//   cd tools/shots && npm install && npx playwright install chromium
//   node ui-shots.mjs                       # every shot, RED and BLU, en + ru
//   node ui-shots.mjs --shots war,dossier --factions RED --langs english --size 2560x1440
//
// Serves game/tc2/loose/resource/html itself, opens each preset from
// frontress/js/dev/shots.js at the store's size, waits for the TF2 fonts and
// for the page to say it is ready, and writes PNGs to tools/shots/out/ui/.
// These are real renders of the real menu -- the same page the game shows.

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname( fileURLToPath( import.meta.url ) );
const HTML = path.resolve( HERE, '../../game/tc2/loose/resource/html' );
const OUT = path.join( HERE, 'out', 'ui' );

const ALL_SHOTS = [
	'title', 'faction', 'contract', 'war', 'dossier', 'support', 'front-moved', 'coordinator',
	'threat', 'defense', 'debrief-advance', 'debrief-win', 'debrief-saved', 'debrief-loss',
	'journal', 'briefing', 'ingame', 'final',
];

function arg( name, fallback ) {
	const i = process.argv.indexOf( `--${ name }` );
	return i > 0 ? process.argv[ i + 1 ] : fallback;
}

const shots = arg( 'shots', ALL_SHOTS.join( ',' ) ).split( ',' );
const factions = arg( 'factions', 'RED,BLU' ).split( ',' );
const langs = arg( 'langs', 'english,russian' ).split( ',' );
const [ width, height ] = arg( 'size', '1920x1080' ).split( 'x' ).map( Number );

let chromium;
try {
	( { chromium } = await import( 'playwright' ) );
} catch {
	console.error( 'Playwright is not installed. Once:\n  cd tools/shots && npm install && npx playwright install chromium' );
	process.exit( 1 );
}

// A plain static server: ES modules will not load from file://.
const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png', '.woff2': 'font/woff2' };
const server = http.createServer( ( req, res ) => {
	const file = path.normalize( path.join( HTML, decodeURIComponent( new URL( req.url, 'http://x' ).pathname ) ) );
	if ( !file.startsWith( HTML ) || !fs.existsSync( file ) || fs.statSync( file ).isDirectory() ) {
		res.writeHead( 404 );
		return res.end();
	}
	res.writeHead( 200, { 'Content-Type': TYPES[ path.extname( file ) ] || 'application/octet-stream' } );
	fs.createReadStream( file ).pipe( res );
} );
await new Promise( r => server.listen( 0, '127.0.0.1', r ) );
const base = `http://127.0.0.1:${ server.address().port }/frontress/index.html`;

fs.mkdirSync( OUT, { recursive: true } );
const browser = await chromium.launch();
const page = await browser.newPage( { viewport: { width, height }, deviceScaleFactor: 1 } );
page.on( 'pageerror', e => console.error( `  page error: ${ e.message }` ) );

let n = 0;
for ( const lang of langs ) {
	for ( const faction of factions ) {
		for ( const shot of shots ) {
			// The faction screen has no side yet; one copy per language is enough.
			if ( ( shot === 'faction' ) && faction !== factions[ 0 ] )
				continue;
			const url = `${ base }?bridge=browser&shot=${ shot }&faction=${ faction }&lang=${ lang }`;
			await page.goto( url );
			await page.waitForFunction( () => document.documentElement.dataset.shotReady === '1', null, { timeout: 10000 } );
			await page.evaluate( () => document.fonts.ready );
			// Label plates on the map are sized after layout; give it a frame.
			await page.waitForTimeout( 400 );
			const file = path.join( OUT, `${ lang.slice( 0, 2 ) }-${ faction.toLowerCase() }-${ shot }.png` );
			await page.screenshot( { path: file } );
			console.log( `  ${ path.relative( HERE, file ) }` );
			n++;
		}
	}
}

await browser.close();
server.close();
console.log( `${ n } screenshots in ${ path.relative( process.cwd(), OUT ) || OUT }` );
