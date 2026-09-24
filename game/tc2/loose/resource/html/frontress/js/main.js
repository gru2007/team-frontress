// The demo's controller: owns the campaign state and the screen, and is the
// only thing that talks to the bridge. Views get a `ctx` and call actions on
// it; they never change state themselves.

import { connectBridge } from './core/bridge.js';
import { setLanguage, guessLanguage, t } from './core/i18n.js';
import { mount } from './core/dom.js';
import * as C from './game/campaign.js';

import { renderTitle } from './views/title.js';
import { renderFaction } from './views/faction.js';
import { renderWar } from './views/war.js';
import { renderOverlay } from './views/overlays.js';
import { renderInGame } from './views/ingame.js';

const SOUNDS = {
	click: 'ui/buttonclick.wav',
	hover: 'ui/buttonrollover.wav',
	open: 'ui/panel_open.wav',
	close: 'ui/panel_close.wav',
	deploy: 'ui/mm_match_found.wav',
	win: 'misc/your_team_won.wav',
	loss: 'misc/your_team_lost.wav',
};

const params = new URLSearchParams( location.search );

class App {
	async start() {
		this.bridge = await connectBridge();
		this.state = C.arrive( C.normalize( await this.bridge.loadState() ) );

		// In the game, the game's language, always: the demo has no language
		// setting of its own. In a browser there is no game to ask.
		if ( this.bridge.kind === 'game' ) {
			// setLanguage reads any language we have no strings for as English.
			const gameLanguage = await this.bridge.gameLanguage();
			setLanguage( gameLanguage || guessLanguage( null ) );
		}
		else
			setLanguage( this.state.prefs.lang || guessLanguage( null ) );

		this.ui = {
			screen: 'title',
			tab: 'map',
			selected: null,
			zone: {},          // chosen landing zone per target, for this session
			overlay: null,     // { type, ... }
			inGame: false,
			dev: params.has( 'dev' ),
		};

		if ( this.state.away )
			this.ui.overlay = { type: 'away', away: this.state.away };
		if ( params.get( 'screen' ) )
			this.ui.screen = params.get( 'screen' );

		document.body.classList.add( `bridge-${ this.bridge.kind }` );
		this.root = document.getElementById( 'app' );
		this.layer = document.getElementById( 'overlay' );

		this.bridge.on( 'openedmenu', () => { this.checkBattle(); this.refreshInGame(); } );
		this.bridge.on( 'closedmenu', () => this.refreshInGame() );
		document.addEventListener( 'keydown', e => this.onKey( e ) );
		document.addEventListener( 'mouseover', e => {
			if ( e.target.closest?.( 'button:not([disabled]), .hoverable' ) && !e.target.closest( 'button' )?.contains( e.relatedTarget ) )
				this.sound( 'hover' );
		} );

		this.save();
		this.render();
		await this.checkBattle( true );
		this.refreshInGame();
		setInterval( () => { this.checkBattle(); this.refreshInGame(); }, 2000 );
	}

	//-------------------------------------------------------------------------
	get ctx() {
		const app = this;
		return {
			state: this.state,
			ui: this.ui,
			bridge: this.bridge.kind,
			set: patch => this.setUI( patch ),
			sound: name => this.sound( name ),
			act: this.actions,
			app,
		};
	}

	setUI( patch ) {
		Object.assign( this.ui, typeof patch === 'function' ? patch( this.ui ) : patch );
		this.render();
	}

	commit( next, { now = false } = {} ) {
		this.state = next;
		this.render();
		return this.save( now );
	}

	save( now = false ) {
		// `away` is a one-time bulletin, not part of the war.
		const { away, ...persist } = this.state;
		return this.bridge.saveState( persist, now );
	}

	sound( name ) {
		if ( this.state.prefs.sound !== false && SOUNDS[ name ] )
			this.bridge.playSound( SOUNDS[ name ] );
	}

	//-------------------------------------------------------------------------
	render() {
		const ctx = this.ctx;
		const s = this.state;
		document.body.dataset.faction = s.faction || 'none';
		document.body.dataset.screen = document.documentElement.dataset.screen = this.ui.inGame ? 'ingame' : this.ui.screen;

		let screen;
		if ( this.ui.inGame )
			screen = renderInGame( ctx );
		else if ( this.ui.screen === 'title' )
			screen = renderTitle( ctx );
		else if ( !s.faction || this.ui.screen === 'faction' )
			screen = renderFaction( ctx );
		else
			screen = renderWar( ctx );

		// Views are rebuilt on every change. Keep what the player would notice
		// being lost: scroll positions, and entrance animations only playing
		// when a screen or overlay actually appears.
		const scroll = {};
		document.querySelectorAll( '[data-scroll]' ).forEach( el => { scroll[ el.dataset.scroll ] = el.scrollTop; } );

		this.animate( this.root, document.body.dataset.screen + ( this.ui.screen === 'war' ? this.ui.tab : '' ), 'screenKey' );
		mount( this.root, screen );

		const overlay = this.ui.overlay && !this.ui.inGame ? renderOverlay( ctx ) : null;
		this.animate( this.layer, overlay ? this.ui.overlay.type + ( this.ui.overlay.phase || '' ) : '', 'overlayKey' );
		if ( overlay )
			mount( this.layer, overlay );
		else
			this.layer.replaceChildren();
		this.layer.classList.toggle( 'open', !!overlay );

		document.querySelectorAll( '[data-scroll]' ).forEach( el => {
			if ( scroll[ el.dataset.scroll ] ) el.scrollTop = scroll[ el.dataset.scroll ];
		} );
	}

	animate( el, key, slot ) {
		if ( this[ slot ] === key )
			return;
		this[ slot ] = key;
		el.classList.add( 'enter' );
		clearTimeout( this[ slot + 'Timer' ] );
		this[ slot + 'Timer' ] = setTimeout( () => el.classList.remove( 'enter' ), 900 );
	}

	onKey( e ) {
		if ( e.key !== 'Escape' )
			return;
		if ( this.ui.overlay && this.ui.overlay.type !== 'deploy' && this.ui.overlay.type !== 'debrief' ) {
			this.setUI( { overlay: null } );
		} else if ( this.ui.selected ) {
			this.setUI( { selected: null } );
		}
	}

	async refreshInGame() {
		const inGame = await this.bridge.inGame();
		if ( inGame === this.ui.inGame )
			return;
		this.setUI( { inGame } );

		// Back at the menu from a battle. A finished battle has published its
		// result by now and becomes a debrief; anything else (the player left,
		// the server went away) is asked about rather than left on "launching".
		if ( !inGame && this.state.pending ) {
			await this.checkBattle();
			if ( this.state.pending && this.ui.overlay?.type !== 'debrief' )
				this.setUI( { screen: 'war', overlay: { type: 'pending' } } );
		}
	}

	//-------------------------------------------------------------------------
	// The result of the battle the pending ticket was issued for. Anything
	// else the game has lying around belongs to some other launch.
	async checkBattle( boot = false ) {
		const p = this.state.pending;
		if ( !p || this.checking )
			return;
		this.checking = true;
		try {
			// Not while the battle map is still up: the result is read once the
			// game has brought the player back, so the pause menu never shows a
			// battle the war has already moved past.
			if ( await this.bridge.inGame() )
				return;
			const doc = await this.bridge.battle();
			if ( doc && doc.ticket === p.ticket ) {
				if ( doc.error ) {
					await this.commit( C.abandonBattle( this.state ) );
					this.bridge.clearBattle();
					this.setUI( { overlay: { type: 'error', message: t( 'coord.failed' ) + ` (${ doc.error }: ${ p.map })` } } );
				} else if ( doc.winner ) {
					await this.commit( C.resolveBattle( this.state, C.winnerSide( this.state, p, doc.winner ) ) );
					this.bridge.clearBattle();
					this.sound( this.state.debrief.outcome === 'win' ? 'win' : 'loss' );
					this.setUI( { screen: 'war', tab: 'map', selected: null, overlay: { type: 'debrief' } } );
				}
				return;
			}

			// A ticket with no battle behind it: the player left, or the game
			// was closed mid-fight. Ask, rather than guess.
			const stale = Date.now() - p.at > 90 * 1000;
			if ( this.bridge.kind === 'game' && !this.ui.inGame && ( boot || stale ) &&
			     this.ui.overlay?.type !== 'pending' ) {
				this.setUI( { screen: 'war', overlay: { type: 'pending' } } );
			}
		} finally {
			this.checking = false;
		}
	}

	//-------------------------------------------------------------------------
	actions = {
		enter: () => {
			this.sound( 'open' );
			this.setUI( { screen: this.state.faction ? 'war' : 'faction',
				overlay: this.state.finished ? { type: 'final' } : this.ui.overlay } );
		},

		chooseFaction: faction => {
			this.sound( 'deploy' );
			this.commit( C.chooseFaction( this.state, faction ) );
			this.setUI( { screen: 'war', tab: 'map', overlay: { type: 'welcome' } } );
		},

		select: id => {
			if ( id !== this.ui.selected )
				this.sound( id ? 'click' : 'close' );
			this.setUI( { selected: id } );
		},

		pickZone: ( target, stage, zone ) => {
			this.sound( 'click' );
			this.setUI( ui => ( { zone: { ...ui.zone, [ `${ target }:${ stage }` ]: zone } } ) );
		},

		pickClass: cls => {
			this.sound( 'click' );
			this.commit( C.produce( this.state, d => { d.prefs.cls = cls; } ) );
		},

		// DEPLOY with no choices made: the coordinator decides.
		autoDeploy: () => {
			const r = C.recommend( this.state );
			if ( r )
				this.actions.deploy( { ...r, zone: this.ui.zone[ `${ r.target }:${ r.stage }` ] || r.zone, cls: this.state.prefs.cls, auto: true } );
		},

		deploy: async plan => {
			if ( this.state.pending || this.state.finished )
				return;
			this.sound( 'deploy' );
			const ticket = C.newTicket();
			const next = C.startBattle( this.state, plan, ticket );
			this.setUI( { selected: null, overlay: { type: 'deploy', plan: next.pending, auto: !!plan.auto, phase: 'routing' } } );
			await this.commit( next, { now: true } );

			// Let the routing read before the loading screen takes over.
			await new Promise( r => setTimeout( r, 2600 ) );
			if ( this.ui.overlay?.type !== 'deploy' || this.state.pending?.ticket !== ticket )
				return;
			this.setUI( ui => ( { overlay: { ...ui.overlay, phase: this.bridge.kind === 'game' ? 'launching' : 'simulate' } } ) );
			if ( this.bridge.kind === 'game' )
				await this.bridge.deploy( this.state.pending );
		},

		cancelDeploy: async () => {
			await this.commit( C.abandonBattle( this.state ) );
			this.setUI( { overlay: null } );
		},

		simulate: async winnerSide => {
			const p = this.state.pending;
			if ( !p ) return;
			// What the game would report: the game team that won, which on a
			// swapped-uniform battle is not the war side's colour.
			const allyTeam = p.team || this.state.faction;
			const team = winnerSide === 'ally' ? allyTeam : winnerSide === 'enemy' ? C.enemyOf( allyTeam ) : null;
			this.bridge.simulate( p.ticket, team === 'RED' ? 'red' : team === 'BLU' ? 'blue' : 'none' );
			await this.checkBattle();
		},

		rejoin: async () => {
			const p = this.state.pending;
			if ( !p ) return;
			this.setUI( { overlay: { type: 'deploy', plan: p, phase: 'launching' } } );
			await this.commit( C.produce( this.state, d => { d.pending.at = Date.now(); } ), { now: true } );
			await this.bridge.deploy( this.state.pending );
		},

		abandon: async () => {
			this.sound( 'close' );
			await this.commit( C.abandonBattle( this.state ) );
			this.bridge.clearBattle();
			this.setUI( { overlay: null } );
		},

		closeDebrief: next => {
			const d = this.state.debrief;
			this.commit( C.markDebriefRead( this.state ) );
			if ( d?.warWon ) {
				this.setUI( { overlay: { type: 'final' } } );
			} else if ( next ) {
				this.setUI( { overlay: null } );
				this.actions.autoDeploy();
			} else {
				this.setUI( { overlay: null, selected: this.state.op?.target || null } );
			}
		},

		overlay: ( type, extra = {} ) => {
			this.sound( type ? 'open' : 'close' );
			this.setUI( { overlay: type ? { type, ...extra } : null } );
		},

		tab: tab => {
			this.sound( 'click' );
			this.setUI( { tab, selected: tab === 'map' ? this.ui.selected : null } );
		},

		setLanguage: lang => {
			setLanguage( lang );
			this.commit( C.produce( this.state, d => { d.prefs.lang = lang; } ) );
		},

		toggleSound: () => this.commit( C.produce( this.state, d => { d.prefs.sound = !d.prefs.sound; } ) ),

		reset: async () => {
			const prefs = this.state.prefs;
			await this.commit( C.produce( C.initialState(), d => { d.prefs = prefs; d.lastSeen = Date.now(); } ), { now: true } );
			this.bridge.clearBattle();
			this.setUI( { screen: 'faction', overlay: null, selected: null, zone: {} } );
		},

		options: () => { this.sound( 'click' ); this.bridge.openOptions(); },
		quit: () => { this.sound( 'close' ); this.bridge.quit(); },
		resume: () => this.bridge.resume(),
		leaveBattle: () => this.bridge.leaveBattle(),

		// Developer shortcuts, only reachable with ?dev.
		devResolve: side => this.actions.simulate( side ),
		devStage: stage => this.commit( C.produce( this.state, d => { if ( d.op ) d.op.stage = stage; } ) ),
	};
}

const app = new App();
window.frontress = app;
app.start().catch( err => {
	console.error( err );
	document.getElementById( 'app' ).textContent = 'Team Frontress failed to start: ' + err.message;
} );
