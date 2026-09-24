// Everything the page needs from the outside world, behind one interface.
//
//   GameBridge     the page is running inside the game: it was served by the
//                  game's local server (src/game/shared/gamestate), talks to
//                  it over /ws for console commands, and keeps the campaign
//                  in cfg/frontress_demo_state.json via /v1/demo/state.
//   BrowserBridge  the page was opened in an ordinary browser for design work
//                  or QA: state lives in localStorage and battles are
//                  simulated by the page itself.
//
// Views never talk to either directly; they go through main.js.

const STATE_URL = '/v1/demo/state';
const BATTLE_URL = '/v1/demo/battle';
const LOCAL_KEY = 'frontress.demo.state.v1';

async function fetchJSON( url, opts = {}, timeout = 1500 ) {
	const ctl = new AbortController();
	const timer = setTimeout( () => ctl.abort(), timeout );
	try {
		const res = await fetch( url, { cache: 'no-store', ...opts, signal: ctl.signal } );
		if ( !res.ok )
			return null;
		const text = await res.text();
		return text ? JSON.parse( text ) : {};
	} catch {
		return null;
	} finally {
		clearTimeout( timer );
	}
}

//-----------------------------------------------------------------------------
class GameBridge {
	constructor() {
		this.kind = 'game';
		this.ws = null;
		this.nextId = 1;
		this.calls = new Map();
		this.listeners = new Map();
		this.queue = [];
		this.retries = 0;
		this.saveTimer = null;
		this.connect();
	}

	connect() {
		if ( this.ws )
			return;
		const ws = new WebSocket( `ws://${ location.host }/ws` );
		this.ws = ws;
		const drop = () => {
			if ( this.ws !== ws )
				return;
			this.ws = null;
			setTimeout( () => this.connect(), Math.min( 500 * 2 ** this.retries++, 8000 ) );
		};
		ws.addEventListener( 'error', drop );
		ws.addEventListener( 'close', drop );
		ws.addEventListener( 'open', () => {
			this.retries = 0;
			while ( this.queue.length )
				ws.send( JSON.stringify( this.queue.shift() ) );
		} );
		ws.addEventListener( 'message', e => {
			let msg;
			try { msg = JSON.parse( e.data ); } catch { return; }
			if ( msg.i !== undefined && this.calls.has( msg.i ) ) {
				this.calls.get( msg.i )( msg.r ?? null );
				this.calls.delete( msg.i );
			}
			if ( msg.e !== undefined )
				for ( const fn of this.listeners.get( msg.e ) || [] ) fn( msg.d );
		} );
	}

	rpc( method, params = null ) {
		const i = this.nextId++;
		const msg = params === null ? { i, m: method } : { i, m: method, p: String( params ) };
		return new Promise( resolve => {
			this.calls.set( i, resolve );
			// A game that is busy loading a map answers late or never; nothing
			// on the page may wait on it forever.
			setTimeout( () => { if ( this.calls.delete( i ) ) resolve( null ); }, 4000 );
			if ( this.ws?.readyState === WebSocket.OPEN )
				this.ws.send( JSON.stringify( msg ) );
			else
				this.queue.push( msg );
		} );
	}

	on( event, fn ) {
		if ( !this.listeners.has( event ) )
			this.listeners.set( event, new Set() );
		this.listeners.get( event ).add( fn );
	}

	// One console command. The game refuses ';' so there is never more than one.
	cmd( line ) { return this.rpc( 'cmd', line ); }

	async getCvar( name ) { return this.rpc( 'getcvar', name ); }

	// The running game's UI language (engine->GetUILanguage). cl_language is
	// the fallback for a client built before getlanguage existed.
	async gameLanguage() {
		return ( await this.rpc( 'getlanguage' ) ) || ( await this.getCvar( 'cl_language' ) ) || null;
	}
	playSound( path ) { this.rpc( 'playsound', path ); }
	async inGame() { return ( await this.rpc( 'getingame' ) ) === '1'; }

	async loadState() { return fetchJSON( STATE_URL ); }

	// Debounced, except right before the game is told to load a map: then the
	// caller waits for the write, so a crash on the loading screen still finds
	// the ticket on disk.
	saveState( state, now = false ) {
		clearTimeout( this.saveTimer );
		const write = () => fetchJSON( STATE_URL, { method: 'PUT', body: JSON.stringify( state ),
			headers: { 'Content-Type': 'application/json' } }, 3000 );
		if ( now )
			return write();
		this.saveTimer = setTimeout( write, 150 );
		return Promise.resolve();
	}

	async battle() { return ( await fetchJSON( BATTLE_URL ) ) || {}; }

	// The game team, not the war side: on a swapped-uniform battle a RED
	// offensive plays BLU and the last argument tells the game to redraw it.
	async deploy( p ) {
		const team = p.team === 'RED' ? 'red' : 'blue';
		await this.cmd( `frontress_demo_deploy ${ p.ticket } ${ p.map } ${ team } ${ p.players } ${ p.cls || 'any' } ${ p.swap ? 1 : 0 }` );
		return true;
	}

	clearBattle() { return this.cmd( 'frontress_demo_clear' ); }
	resume() { return this.cmd( 'gameui_hide' ); }
	leaveBattle() { return this.cmd( 'disconnect' ); }
	openOptions() { return this.cmd( 'gamemenucommand OpenOptionsDialog' ); }
	quit() { return this.cmd( 'quit' ); }
}

//-----------------------------------------------------------------------------
class BrowserBridge {
	constructor() {
		this.kind = 'browser';
		this.result = {};
		this.listeners = new Map();
	}

	on( event, fn ) {
		if ( !this.listeners.has( event ) )
			this.listeners.set( event, new Set() );
		this.listeners.get( event ).add( fn );
	}

	async getCvar() { return null; }
	async gameLanguage() { return null; }
	playSound() {}
	async inGame() { return false; }

	async loadState() {
		try { return JSON.parse( localStorage.getItem( LOCAL_KEY ) || 'null' ); } catch { return null; }
	}

	saveState( state ) {
		try { localStorage.setItem( LOCAL_KEY, JSON.stringify( state ) ); } catch { /* private mode */ }
		return Promise.resolve();
	}

	async battle() { return this.result; }

	async deploy() { return true; }

	// The page's stand-in for a round ending.
	simulate( ticket, winner ) { this.result = { ticket, winner: winner || 'none' }; }

	clearBattle() { this.result = {}; }
	resume() {}
	leaveBattle() {}
	openOptions() {}
	quit() { window.close(); }
}

export async function connectBridge() {
	const forced = new URLSearchParams( location.search ).get( 'bridge' );
	if ( forced !== 'browser' && location.protocol.startsWith( 'http' ) ) {
		if ( forced === 'game' || ( await fetchJSON( STATE_URL, {}, 1200 ) ) !== null )
			return new GameBridge();
	}
	return new BrowserBridge();
}
