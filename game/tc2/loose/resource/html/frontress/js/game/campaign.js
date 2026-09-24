// The rules of the demo war. Pure functions over a plain state object: no DOM,
// no network, no clock except what the caller passes in. Every change goes
// through `produce`, which hands the recipe a deep copy, so a view can never
// see a half-applied battle.
//
// The loop:  pick a sector -> start its operation -> deploy (ticket) ->
//            the game reports a winner -> resolveBattle -> world tick -> debrief

import { NODES, EDGES, OPERATIONS, STAGES, WORLD_EVENTS, AWAY_MINUTES, DIRECTIONAL_MODES } from './scenario.js';

// 2: the shortened theater. Older saves start a new war.
export const SCHEMA = 2;
export const MAX_MOMENTUM = 3;
const MAX_LOG = 60;

const NODE = Object.fromEntries( NODES.map( n => [ n.id, n ] ) );
const NEIGHBOURS = {};
for ( const [ a, b ] of EDGES ) {
	( NEIGHBOURS[ a ] ??= [] ).push( b );
	( NEIGHBOURS[ b ] ??= [] ).push( a );
}

export const node = id => NODE[ id ];
export const neighbours = id => NEIGHBOURS[ id ] || [];
export const enemyOf = f => ( f === 'RED' ? 'BLU' : 'RED' );

//-----------------------------------------------------------------------------
// State
//-----------------------------------------------------------------------------
export function initialState() {
	return {
		v: SCHEMA,
		faction: null,
		day: 1,
		owners: Object.fromEntries( NODES.map( n => [ n.id, n.owner ] ) ),
		pressure: {},
		op: null,
		pending: null,
		debrief: null,
		stats: { battles: 0, wins: 0, losses: 0, captured: 0, lost: 0 },
		worldDone: [],
		log: [],
		finished: false,
		lastSeen: 0,
		prefs: { cls: 'any', lang: null, sound: true },
	};
}

// Whatever came off disk: keep what is recognisable, fill in the rest. A save
// from a newer or broken build is not worth a crash at the main menu.
export function normalize( raw ) {
	const base = initialState();
	if ( !raw || typeof raw !== 'object' || raw.v !== SCHEMA )
		return base;

	const s = { ...base, ...raw };
	s.owners = { ...base.owners, ...( raw.owners || {} ) };
	s.stats = { ...base.stats, ...( raw.stats || {} ) };
	s.prefs = { ...base.prefs, ...( raw.prefs || {} ) };
	s.pressure = { ...( raw.pressure || {} ) };
	s.worldDone = Array.isArray( raw.worldDone ) ? raw.worldDone : [];
	s.log = Array.isArray( raw.log ) ? raw.log.slice( -MAX_LOG ) : [];
	if ( s.faction !== 'RED' && s.faction !== 'BLU' )
		s.faction = null;
	if ( s.op && !NODE[ s.op.target ] )
		s.op = null;
	return s;
}

export function produce( state, recipe ) {
	const draft = typeof structuredClone === 'function' ? structuredClone( state ) : JSON.parse( JSON.stringify( state ) );
	recipe( draft );
	return draft;
}

function log( s, key, vars = {}, tone = 'neutral', nodeId = null ) {
	s.log.push( { day: s.day, key, vars, tone, node: nodeId, at: Date.now() } );
	if ( s.log.length > MAX_LOG )
		s.log.splice( 0, s.log.length - MAX_LOG );
}

//-----------------------------------------------------------------------------
// Reading the war
//-----------------------------------------------------------------------------

// Enemy sectors touching ally ground: the only places an operation can start.
export function frontTargets( s ) {
	return NODES.filter( n => s.owners[ n.id ] === 'enemy' &&
		neighbours( n.id ).some( m => s.owners[ m ] === 'ally' ) ).map( n => n.id );
}

// Ally sectors touching enemy ground, where an attack would jump off from.
export function stagingFor( s, target ) {
	const from = neighbours( target ).filter( m => s.owners[ m ] === 'ally' );
	// The one nearest the ally HQ reads best as "where the column comes from".
	return from.sort( ( a, b ) => NODE[ a ].x - NODE[ b ].x )[ 0 ] || null;
}

export function controlShare( s ) {
	let ally = 0, total = 0;
	for ( const n of NODES ) {
		const w = n.kind === 'hq' ? 2 : 1;
		total += w;
		if ( s.owners[ n.id ] === 'ally' )
			ally += w;
	}
	return ally / total;
}

// What kind of place a sector is, for the dossier.
export function sectorStatus( s, id ) {
	if ( s.op?.target === id ) return 'operation';
	if ( s.owners[ id ] === 'ally' ) {
		return neighbours( id ).some( m => s.owners[ m ] === 'enemy' ) ? 'frontline' : 'secure';
	}
	return frontTargets( s ).includes( id ) ? 'target' : 'deep';
}

export function operationFor( id ) {
	return OPERATIONS[ NODE[ id ]?.op ] || OPERATIONS.industrial;
}

export function stageInfo( stage ) {
	return STAGES[ Math.max( 1, Math.min( 3, stage ) ) - 1 ];
}

// A landing zone resolved for the player's side: which map, which mode, and
// which of the game's teams the player's side plays as. On a directional map
// the attacker is always the game's BLU team; if the player's side is RED the
// battle is fought with uniforms swapped (greyline_uniform_swap).
export function zoneFor( s, target, stage, zoneId ) {
	const zones = operationFor( target )[ stage - 1 ];
	const z = zones.find( z => z.id === zoneId ) || zones[ 0 ];
	const faction = s.faction || 'BLU';
	const team = DIRECTIONAL_MODES.includes( z.mode ) ? 'BLU' : faction;
	return { id: z.id, index: zones.indexOf( z ), map: z.map, mode: z.mode, team, swap: team !== faction };
}

// The war side a game team's win belongs to, for the battle a ticket was for.
// 'red' | 'blue' | 'none' -> 'RED' | 'BLU' | null.
export function winnerSide( s, pending, gameWinner ) {
	const team = gameWinner === 'red' ? 'RED' : gameWinner === 'blue' ? 'BLU' : null;
	if ( !team )
		return null;
	return team === ( pending.team || s.faction ) ? s.faction : enemyOf( s.faction );
}

export function zonesFor( s, target, stage ) {
	return operationFor( target )[ stage - 1 ].map( z => zoneFor( s, target, stage, z.id ) );
}

// Hops from a sector to the enemy headquarters.
function hopsToEnemyHQ( id ) {
	const hq = NODES.find( n => n.kind === 'hq' && n.owner === 'enemy' ).id;
	const seen = new Set( [ id ] );
	let frontier = [ id ];
	for ( let hops = 0; frontier.length; hops++ ) {
		if ( frontier.includes( hq ) )
			return hops;
		frontier = frontier.flatMap( n => neighbours( n ) ).filter( n => !seen.has( n ) && seen.add( n ) );
	}
	return Infinity;
}

// What DEPLOY would do right now if the player does not choose. The current
// operation always wins; otherwise the front that leads towards the enemy
// headquarters, then the one the enemy holds weakest (allied squads have
// softened it up), then the one nearest the middle of the map.
export function recommend( s ) {
	if ( s.finished || !s.faction )
		return null;

	let target = s.op?.target;
	if ( !target ) {
		const targets = frontTargets( s );
		if ( !targets.length )
			return null;
		targets.sort( ( a, b ) =>
			( hopsToEnemyHQ( a ) - hopsToEnemyHQ( b ) ) ||
			( ( s.pressure[ a ] || 0 ) - ( s.pressure[ b ] || 0 ) ) ||
			( Math.abs( NODE[ a ].y - 450 ) - Math.abs( NODE[ b ].y - 450 ) ) );
		target = targets[ 0 ];
	}

	const stage = s.op?.target === target ? s.op.stage : 1;
	const zone = zoneFor( s, target, stage, s.op?.target === target ? s.op.zone : null );
	return { target, stage, zone: zone.id };
}

//-----------------------------------------------------------------------------
// Changing the war
//-----------------------------------------------------------------------------
export function chooseFaction( s, faction ) {
	return produce( s, d => {
		d.faction = faction;
		log( d, 'log.enlisted', { faction }, 'ally' );
		log( d, 'log.warBegins', {}, 'neutral' );
	} );
}

// A ticket for one battle. The game gets the map, side and size; the campaign
// keeps the ticket until a result with the same id comes back.
export function startBattle( s, { target, stage, zone, cls }, ticket ) {
	return produce( s, d => {
		if ( !d.op || d.op.target !== target ) {
			if ( d.op )
				log( d, 'log.opAbandoned', { target: d.op.target }, 'neutral', d.op.target );
			d.op = { target, stage: 1, momentum: MAX_MOMENTUM, zone, startedDay: d.day };
			log( d, 'log.opStarted', { target }, 'ally', target );
		}
		stage = d.op.stage;
		d.op.zone = zone;
		const z = zoneFor( d, target, stage, zone );
		const info = stageInfo( stage );
		d.pending = {
			ticket, target, stage, zone: z.id, map: z.map, mode: z.mode,
			team: z.team, swap: z.swap,
			players: info.players, cls: cls || 'any', at: Date.now(),
		};
		d.prefs.cls = cls || 'any';
	} );
}

// The deployment never produced a result (the player left, the game closed).
// Nothing strategic changes.
export function abandonBattle( s ) {
	return produce( s, d => {
		if ( d.pending )
			log( d, 'log.retreated', { target: d.pending.target }, 'neutral', d.pending.target );
		d.pending = null;
	} );
}

// winner: 'RED' | 'BLU' | null (stalemate). A stalemate is a repulsed attack.
export function resolveBattle( s, winner, now = Date.now() ) {
	if ( !s.pending )
		return s;

	return produce( s, d => {
		const p = d.pending;
		const outcome = winner === d.faction ? 'win' : ( winner ? 'loss' : 'stalemate' );
		const before = { stage: d.op.stage, momentum: d.op.momentum, owners: { ...d.owners } };
		const result = {
			ticket: p.ticket, outcome, target: p.target, map: p.map, mode: p.mode,
			stageBefore: before.stage, stageAfter: before.stage,
			momentumBefore: before.momentum, momentumAfter: before.momentum,
			captured: false, collapsed: false, warWon: false, world: null, read: false,
		};

		d.pending = null;
		d.stats.battles += 1;
		d.day += 1;

		if ( outcome === 'win' ) {
			d.stats.wins += 1;
			if ( d.op.stage >= 3 ) {
				d.owners[ p.target ] = 'ally';
				delete d.pressure[ p.target ];
				d.stats.captured += 1;
				result.captured = true;
				result.stageAfter = 4;
				log( d, 'log.captured', { target: p.target }, 'ally', p.target );
				if ( NODE[ p.target ].kind === 'hq' ) {
					d.finished = true;
					result.warWon = true;
					log( d, 'log.warWon', {}, 'ally', p.target );
				}
				d.op = null;
			} else {
				d.op.stage += 1;
				result.stageAfter = d.op.stage;
				log( d, 'log.advanced', { target: p.target, stage: d.op.stage }, 'ally', p.target );
			}
		} else {
			d.stats.losses += 1;
			d.op.momentum -= 1;
			result.momentumAfter = d.op.momentum;
			if ( d.op.momentum <= 0 ) {
				// The defence held long enough: the offensive runs out of steam
				// and has to be started again from the line.
				result.collapsed = true;
				result.stageAfter = 0;
				log( d, 'log.collapsed', { target: p.target }, 'enemy', p.target );
				d.pressure[ p.target ] = 0.35;
				d.op = null;
			} else {
				d.op.stage = Math.max( 1, d.op.stage - 1 );
				result.stageAfter = d.op.stage;
				log( d, before.stage > 1 ? 'log.pushedBack' : 'log.held',
					{ target: p.target, stage: d.op.stage }, 'enemy', p.target );
			}
		}

		if ( !d.finished )
			result.world = worldTick( d );

		result.front = frontDelta( before.owners, d.owners );
		d.debrief = result;
		d.lastSeen = now;
	} );
}

// Applies one world event to a draft; returns what happened for the debrief.
function worldTick( d ) {
	for ( const ev of WORLD_EVENTS ) {
		if ( d.worldDone.includes( ev.id ) || !ev.when( d ) )
			continue;
		ev.apply( d );
		if ( ev.node )
			d.worldDone.push( ev.id );
		if ( ev.id === 'sawmill_lost' ) d.stats.lost += 1;
		log( d, `world.${ ev.id }`, {}, ev.tone, ev.node );
		return { id: ev.id, node: ev.node, tone: ev.tone };
	}
	return null;
}

function frontDelta( a, b ) {
	return Object.keys( b ).filter( k => a[ k ] !== b[ k ] ).map( k => ( { node: k, owner: b[ k ] } ) );
}

// Called when the menu opens. A long break means the war moved on without
// the player; the page shows it as a bulletin.
export function arrive( s, now = Date.now() ) {
	return produce( s, d => {
		d.away = null;
		if ( d.faction && !d.finished && !d.pending && d.lastSeen &&
		     now - d.lastSeen >= AWAY_MINUTES * 60 * 1000 ) {
			const ev = worldTick( d );
			if ( ev )
				d.away = { ...ev, minutes: Math.round( ( now - d.lastSeen ) / 60000 ) };
		}
		d.lastSeen = now;
	} );
}

export function markDebriefRead( s ) {
	return produce( s, d => { if ( d.debrief ) d.debrief.read = true; } );
}

export function newTicket() {
	return 'fd' + Date.now().toString( 36 ) + Math.random().toString( 36 ).slice( 2, 6 );
}
