// A stand-in for the game's side of the Team Frontress demo, for testing the
// page end to end without building the client.
//
//   node tools/frontress-demo-mock-game.mjs [port]
//   open http://127.0.0.1:58270/ui/frontress/index.html
//
// It speaks what the game speaks (src/game/shared/gamestate/gamestate.cpp and
// src/game/client/tf/frontress/tf_frontress_demo.cpp):
//
//   /ui/*              the html directory, like the Crow blueprint
//   GET/PUT /v1/demo/state   the campaign file (kept in a temp file here)
//   GET /v1/demo/battle      the result document
//   /ws                RPC {i,m,p} -> {i,r}; events {e,d}
//
// and plays the battle: frontress_demo_deploy "loads" the map, the player is
// "in game" for a few seconds, the round is decided, the result published,
// and the player "disconnects" back to the menu -- the same order of events
// the client produces, compressed.
//
// Control it while it runs:
//   GET /mock/outcome?next=ally|enemy|none|leave   how the next battle ends
//   GET /mock/esc                                  open the pause menu in game
//   GET /mock/log                                  every console line the page sent

import http from 'node:http';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const PORT = +( process.argv[ 2 ] || 58270 );
const HTML = fileURLToPath( new URL( '../game/tc2/loose/resource/html/', import.meta.url ) );
const STATE = path.join( os.tmpdir(), `frontress_demo_state.mock-${ PORT }.json` );
const BATTLE_MS = +( process.env.MOCK_BATTLE_MS || 4000 );
const RETURN_MS = +( process.env.MOCK_RETURN_MS || 1500 );

const SAFE = /^[A-Za-z0-9_-]+$/;
const CLASSES = [ 'scout', 'soldier', 'pyro', 'demoman', 'heavyweapons', 'engineer', 'medic', 'sniper', 'spy' ];
const BOT_MAPS = new Set( fs.readFileSync( new URL( '../game/tc2/cfg/mapcycle_quickplay_bots.txt', import.meta.url ), 'utf8' ).split( /\s+/ ).filter( Boolean ) );

const game = { inGame: false, battle: {}, uniformSwap: 0, next: 'ally', log: [], timers: [] };
const clients = new Set();

function say( line ) {
	const stamp = new Date().toISOString().slice( 11, 19 );
	game.log.push( `${ stamp } ${ line }` );
	console.log( `[mock ${ stamp }] ${ line }` );
}

function emit( event, data = '' ) {
	for ( const c of clients ) wsSend( c, JSON.stringify( data ? { e: event, d: data } : { e: event } ) );
}

//-----------------------------------------------------------------------------
// The console: what the page can make the game do.
//-----------------------------------------------------------------------------
function command( line ) {
	// The real "cmd" RPC refuses more than one command.
	if ( line.includes( ';' ) ) {
		say( `REFUSED (semicolon): ${ line }` );
		return;
	}
	say( `cmd: ${ line }` );
	const args = line.trim().split( /\s+/ );
	switch ( args[ 0 ] ) {
	case 'frontress_demo_deploy': return deploy( args );
	case 'frontress_demo_clear':
		game.battle = {};
		if ( !game.inGame ) game.uniformSwap = 0;
		return;
	case 'disconnect': return disconnect( 'player left' );
	case 'gameui_hide': emit( 'closedmenu' ); return;
	default: return;
	}
}

function deploy( args ) {
	const [ , ticket, map, team, players, cls = 'any', swap = '0' ] = args;
	if ( args.length < 5 || !SAFE.test( ticket || '' ) || !SAFE.test( map || '' ) ) return say( 'deploy: bad ticket or map' );
	if ( team !== 'red' && team !== 'blue' ) return say( 'deploy: team must be red or blue' );

	for ( const t of game.timers ) clearTimeout( t );
	game.timers = [];
	game.battle = { ticket, map, team, players: Math.min( 24, Math.max( 2, +players || 0 ) ), swap: swap === '1' };
	const joinclass = CLASSES.includes( cls ) ? cls : null;

	if ( !BOT_MAPS.has( map ) ) {
		game.battle.error = 'missing_map';
		return say( `deploy: maps/${ map }.bsp is not installed` );
	}

	disconnect( 'new battle', true );
	say( `loading ${ map }; setup: exec frontress_demo.cfg; greyline_uniform_swap ${ game.battle.swap ? 1 : 0 }; ` +
		`jointeam ${ team }${ joinclass ? `; joinclass ${ joinclass }` : '' }; tf_bot_quota ${ game.battle.players }` );

	const outcome = game.next;
	game.timers.push( setTimeout( () => {
		game.inGame = true;
		game.uniformSwap = game.battle.swap ? 1 : 0;
		emit( 'closedmenu' );
		say( `in game on ${ map } as ${ team }${ game.uniformSwap ? ' (uniforms swapped)' : '' }` );
	}, 800 ) );

	game.timers.push( setTimeout( () => {
		if ( outcome === 'leave' ) return disconnect( 'player left mid-battle' );
		const enemy = team === 'red' ? 'blue' : 'red';
		game.battle.winner = outcome === 'ally' ? team : outcome === 'enemy' ? enemy : 'none';
		say( `full round won by ${ game.battle.winner }` );
		game.timers.push( setTimeout( () => disconnect( 'return to war map' ), RETURN_MS ) );
	}, 800 + BATTLE_MS ) );
}

function disconnect( why, quiet = false ) {
	const was = game.inGame;
	game.inGame = false;
	if ( game.battle.winner ) game.uniformSwap = 0;
	if ( was ) {
		say( `disconnect (${ why })` );
		emit( 'openedmenu' );
	} else if ( !quiet ) {
		say( `disconnect (${ why }), not in game` );
	}
}

//-----------------------------------------------------------------------------
// RPC
//-----------------------------------------------------------------------------
function rpc( msg ) {
	const p = msg.p ?? '';
	switch ( msg.m ) {
	case 'cmd': command( p ); return '';
	case 'getcvar':
		if ( p === 'cl_language' ) return process.env.MOCK_LANGUAGE || 'english';
		if ( p === 'greyline_uniform_swap' ) return String( game.uniformSwap );
		return '';
	case 'getingame': return game.inGame ? '1' : '0';
	case 'getlanguage': return process.env.MOCK_LANGUAGE || 'english';
	case 'playsound': return '';
	default: return '';
	}
}

//-----------------------------------------------------------------------------
// A minimal WebSocket server: text frames, one message per frame, which is all
// the page sends.
//-----------------------------------------------------------------------------
function wsSend( socket, text ) {
	const data = Buffer.from( text );
	const head = data.length < 126 ? Buffer.from( [ 0x81, data.length ] )
		: Buffer.from( [ 0x81, 126, data.length >> 8, data.length & 255 ] );
	socket.write( Buffer.concat( [ head, data ] ) );
}

function wsAccept( req, socket ) {
	const key = crypto.createHash( 'sha1' ).update( req.headers[ 'sec-websocket-key' ] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11' ).digest( 'base64' );
	socket.write( `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${ key }\r\n\r\n` );
	clients.add( socket );
	let buf = Buffer.alloc( 0 );
	socket.on( 'data', chunk => {
		buf = Buffer.concat( [ buf, chunk ] );
		while ( buf.length >= 2 ) {
			const op = buf[ 0 ] & 15;
			let len = buf[ 1 ] & 127, off = 2;
			if ( len === 126 ) { len = buf.readUInt16BE( 2 ); off = 4; }
			else if ( len === 127 ) { len = Number( buf.readBigUInt64BE( 2 ) ); off = 10; }
			if ( buf.length < off + 4 + len ) return;
			const mask = buf.subarray( off, off + 4 );
			const body = Buffer.from( buf.subarray( off + 4, off + 4 + len ) ).map( ( b, i ) => b ^ mask[ i % 4 ] );
			buf = buf.subarray( off + 4 + len );
			if ( op === 8 ) { socket.end(); return; }
			if ( op !== 1 ) continue;
			let msg;
			try { msg = JSON.parse( body.toString() ); } catch { continue; }
			if ( typeof msg.i !== 'number' || typeof msg.m !== 'string' ) continue;
			const r = rpc( msg );
			wsSend( socket, JSON.stringify( r ? { i: msg.i, r } : { i: msg.i } ) );
		}
	} );
	const drop = () => clients.delete( socket );
	socket.on( 'close', drop );
	socket.on( 'error', drop );
}

//-----------------------------------------------------------------------------
// HTTP
//-----------------------------------------------------------------------------
const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png', '.woff2': 'font/woff2', '.json': 'application/json', '.md': 'text/plain' };

function json( res, code, body ) {
	res.writeHead( code, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' } );
	res.end( body );
}

const server = http.createServer( ( req, res ) => {
	const url = new URL( req.url, 'http://x' );

	if ( url.pathname === '/v1/demo/state' ) {
		if ( req.method === 'PUT' ) {
			let body = '';
			req.on( 'data', c => { body += c; } );
			req.on( 'end', () => {
				try {
					if ( body[ 0 ] !== '{' ) throw new Error( 'not an object' );
					JSON.parse( body );
				} catch { return json( res, 400, '' ); }
				fs.writeFileSync( STATE + '.tmp', body );
				fs.renameSync( STATE + '.tmp', STATE );
				res.writeHead( 204 ); res.end();
			} );
			return;
		}
		const text = fs.existsSync( STATE ) ? fs.readFileSync( STATE, 'utf8' ) : '';
		return json( res, 200, text.startsWith( '{' ) ? text : '{}' );
	}
	if ( url.pathname === '/v1/demo/battle' )
		return json( res, 200, game.battle.ticket ? JSON.stringify( game.battle ) : '{}' );

	if ( url.pathname === '/mock/outcome' ) {
		game.next = url.searchParams.get( 'next' ) || 'ally';
		say( `next battle ends: ${ game.next }` );
		return json( res, 200, JSON.stringify( { next: game.next } ) );
	}
	if ( url.pathname === '/mock/esc' ) {
		if ( game.inGame ) emit( 'openedmenu' );
		return json( res, 200, JSON.stringify( { inGame: game.inGame } ) );
	}
	if ( url.pathname === '/mock/log' ) return json( res, 200, JSON.stringify( { game: { ...game, timers: undefined, log: undefined }, log: game.log }, null, 1 ) );
	if ( url.pathname === '/mock/reset' ) {
		fs.rmSync( STATE, { force: true } );
		game.battle = {}; game.log = []; game.inGame = false; game.next = 'ally';
		return json( res, 200, '{}' );
	}

	if ( url.pathname.startsWith( '/ui/' ) ) {
		const file = path.normalize( path.join( HTML, decodeURIComponent( url.pathname.slice( 4 ) ) ) );
		if ( !file.startsWith( HTML ) || !fs.existsSync( file ) || fs.statSync( file ).isDirectory() ) {
			res.writeHead( 404 ); return res.end();
		}
		res.writeHead( 200, { 'Content-Type': TYPES[ path.extname( file ) ] || 'application/octet-stream', 'Cache-Control': 'no-store' } );
		return fs.createReadStream( file ).pipe( res );
	}
	res.writeHead( 404 ); res.end();
} );

server.on( 'upgrade', ( req, socket ) => {
	if ( new URL( req.url, 'http://x' ).pathname === '/ws' ) wsAccept( req, socket );
	else socket.destroy();
} );

server.listen( PORT, '127.0.0.1', () => {
	console.log( `Frontress demo mock game on http://127.0.0.1:${ PORT }/ui/frontress/index.html` );
	console.log( `campaign file: ${ STATE }` );
} );
