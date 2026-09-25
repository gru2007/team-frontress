// The rules of the demo war. Pure functions over a plain state object: no DOM,
// no network, no clock except what the caller passes in. Every change goes
// through `produce`, which hands the recipe a deep copy, so a view can never
// see a half-applied battle.
//
// The loop:  pick a sector -> plan the battle (conditions, supply) ->
//            deploy (ticket) -> the game reports a winner and the player's
//            scoreboard -> resolveBattle -> threats and world tick -> debrief

import {
	NODES, EDGES, OPERATIONS, STAGES, WORLD_EVENTS, AWAY_MINUTES, DIRECTIONAL_MODES,
	THREAT_BATTLES, MODIFIERS, OPERATION_MODIFIERS, PRESSURE_MODIFIERS, DEFENSE_MODIFIERS,
	SUPPLY_START, SUPPLY_WIN, SUPPLY_DEFENSE_WIN, ASSETS, SKILLS, BASE_SKILL,
	MEDALS, MEDAL_XP, RANKS,
} from './scenario.js';

// 3: conditions, supply, counter-attacks and the player's record.
// A v2 save (the war without them) is carried over with the new fields empty.
export const SCHEMA = 3;
export const MAX_MOMENTUM = 3;
const MAX_LOG = 60;
const MAX_BOTS = 22;              // maxplayers 24: the player plus a spare slot
const DEFENSE_PLAYERS = 12;

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
		threat: null,            // { node, left }: a counter-attack on an ally sector
		pending: null,
		debrief: null,
		supply: SUPPLY_START,
		xp: 0,
		medals: {},
		stats: { battles: 0, wins: 0, losses: 0, captured: 0, lost: 0, defended: 0 },
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
	if ( !raw || typeof raw !== 'object' || ( raw.v !== SCHEMA && raw.v !== 2 ) )
		return base;

	const s = { ...base, ...raw, v: SCHEMA };
	s.owners = { ...base.owners, ...( raw.owners || {} ) };
	s.stats = { ...base.stats, ...( raw.stats || {} ) };
	s.prefs = { ...base.prefs, ...( raw.prefs || {} ) };
	s.medals = { ...( raw.medals || {} ) };
	s.pressure = { ...( raw.pressure || {} ) };
	s.worldDone = Array.isArray( raw.worldDone ) ? raw.worldDone : [];
	s.log = Array.isArray( raw.log ) ? raw.log.slice( -MAX_LOG ) : [];
	if ( s.faction !== 'RED' && s.faction !== 'BLU' )
		s.faction = null;
	if ( s.op && !NODE[ s.op.target ] )
		s.op = null;
	if ( s.threat && ( !NODE[ s.threat.node ] || s.owners[ s.threat.node ] !== 'ally' ) )
		s.threat = null;
	if ( !Number.isFinite( s.supply ) )
		s.supply = SUPPLY_START;
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
	if ( s.threat?.node === id ) return 'threat';
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

export function zonesFor( s, target, stage ) {
	return operationFor( target )[ stage - 1 ].map( z => zoneFor( s, target, stage, z.id ) );
}

// Holding an ally sector is fought on the maps an attack on it would use --
// the Payload and Attack/Defend ones -- from the side that defends them, the
// game's RED team. So it is BLU's turn to wear the other colours.
export function defenseZones( s, id ) {
	const ops = operationFor( id );
	return [ ops[ 1 ][ 0 ], ops[ 2 ][ 0 ] ].map( ( z, i ) => {
		const faction = s.faction || 'BLU';
		return { id: z.id, index: i, map: z.map, mode: z.mode, team: 'RED', swap: faction !== 'RED' };
	} );
}

export function defenseZoneFor( s, id, zoneId ) {
	const zones = defenseZones( s, id );
	return zones.find( z => z.id === zoneId ) || zones[ 0 ];
}

// The war side a game team's win belongs to, for the battle a ticket was for.
// 'red' | 'blue' | 'none' -> 'RED' | 'BLU' | null.
export function winnerSide( s, pending, gameWinner ) {
	const team = gameWinner === 'red' ? 'RED' : gameWinner === 'blue' ? 'BLU' : null;
	if ( !team )
		return null;
	return team === ( pending.team || s.faction ) ? s.faction : enemyOf( s.faction );
}

//-----------------------------------------------------------------------------
// The battle itself: conditions, assets, who fights.
//-----------------------------------------------------------------------------

// The conditions a battle will be fought under, before any asset.
export function modifiersFor( s, kind, target, stage ) {
	const ids = kind === 'defense'
		? [ ...DEFENSE_MODIFIERS ]
		: [ ...( OPERATION_MODIFIERS[ NODE[ target ]?.op ] || OPERATION_MODIFIERS.industrial )[ stage - 1 ] ];
	if ( kind !== 'defense' ) {
		if ( ( s.pressure[ target ] || 0 ) > 0 ) ids.push( PRESSURE_MODIFIERS.enemy );
		if ( ( s.pressure[ target ] || 0 ) < 0 ) ids.push( PRESSURE_MODIFIERS.ally );
	}
	return [ ...new Set( ids ) ].filter( id => MODIFIERS[ id ] );
}

export const canAfford = ( s, asset ) => !asset || ( ASSETS[ asset ] && s.supply >= ASSETS[ asset ].cost );

// Everything the game needs to set the battle up, and everything the page
// shows about it. Pure: the same plan always makes the same battle.
export function planBattle( s, { kind = 'attack', target, stage, zone, asset = null } ) {
	const defense = kind === 'defense';
	stage = defense ? 2 : stage;
	const z = defense ? defenseZoneFor( s, target, zone ) : zoneFor( s, target, stage, zone );
	const players = defense ? DEFENSE_PLAYERS : stageInfo( stage ).players;

	let mods = modifiersFor( s, kind, target, stage );
	const a = asset && ASSETS[ asset ] ? ASSETS[ asset ] : null;
	let cancelled = null;
	if ( a?.cancels ) {
		cancelled = mods.find( id => MODIFIERS[ id ].tone === a.cancels ) || null;
		mods = mods.filter( id => id !== cancelled );
	}

	const half = players / 2;
	const roster = { allies: half - 1, enemies: half, askill: BASE_SKILL, eskill: BASE_SKILL, eclass: {} };
	const apply = r => {
		if ( !r ) return;
		roster.allies += r.allies || 0;
		roster.enemies += r.enemies || 0;
		roster.askill += r.askill || 0;
		roster.eskill += r.eskill || 0;
		if ( r.eclass ) roster.eclass[ r.eclass[ 0 ] ] = ( roster.eclass[ r.eclass[ 0 ] ] || 0 ) + r.eclass[ 1 ];
	};
	mods.forEach( id => apply( MODIFIERS[ id ].roster ) );
	apply( a?.roster );

	roster.askill = Math.max( 0, Math.min( SKILLS.length - 1, roster.askill ) );
	roster.eskill = Math.max( 0, Math.min( SKILLS.length - 1, roster.eskill ) );
	const forced = Object.values( roster.eclass ).reduce( ( n, c ) => n + c, 0 );
	roster.enemies = Math.max( roster.enemies, forced );
	// Never more bots than the server has slots for; the enemy gives way first.
	const over = roster.allies + roster.enemies - MAX_BOTS;
	if ( over > 0 ) roster.enemies -= over;

	return {
		kind, target, stage, zone: z, mods, cancelled, asset: a ? asset : null,
		cost: a ? a.cost : 0, roster, players: 1 + roster.allies + roster.enemies,
		reward: mods.reduce( ( n, id ) => n + ( MODIFIERS[ id ].reward || 0 ), 0 ) + ( a?.reward || 0 ),
	};
}

//-----------------------------------------------------------------------------
// The coordinator
//-----------------------------------------------------------------------------

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

// What DEPLOY would do right now if the player does not choose. A sector about
// to fall comes first; then the current operation; otherwise the front that
// leads towards the enemy headquarters, then the one the enemy holds weakest
// (allied squads have softened it up), then the one nearest the middle.
export function recommend( s ) {
	if ( s.finished || !s.faction )
		return null;

	if ( s.threat && s.threat.left <= 1 )
		return { kind: 'defense', target: s.threat.node, stage: 2, zone: defenseZones( s, s.threat.node )[ 0 ].id };

	let target = s.op?.target;
	if ( !target ) {
		const targets = frontTargets( s );
		if ( !targets.length )
			return s.threat ? { kind: 'defense', target: s.threat.node, stage: 2, zone: defenseZones( s, s.threat.node )[ 0 ].id } : null;
		targets.sort( ( a, b ) =>
			( hopsToEnemyHQ( a ) - hopsToEnemyHQ( b ) ) ||
			( ( s.pressure[ a ] || 0 ) - ( s.pressure[ b ] || 0 ) ) ||
			( Math.abs( NODE[ a ].y - 450 ) - Math.abs( NODE[ b ].y - 450 ) ) );
		target = targets[ 0 ];
	}

	const stage = s.op?.target === target ? s.op.stage : 1;
	const zone = zoneFor( s, target, stage, s.op?.target === target ? s.op.zone : null );
	return { kind: 'attack', target, stage, zone: zone.id };
}

//-----------------------------------------------------------------------------
// The player's record
//-----------------------------------------------------------------------------
export function rankFor( xp ) {
	let i = 0;
	while ( i + 1 < RANKS.length && xp >= RANKS[ i + 1 ].xp ) i++;
	const next = RANKS[ i + 1 ] || null;
	const from = RANKS[ i ].xp;
	return { id: RANKS[ i ].id, index: i, next: next?.id || null,
		progress: next ? ( xp - from ) / ( next.xp - from ) : 1, toNext: next ? next.xp - xp : 0 };
}

export const medalsFor = st => ( st ? MEDALS.filter( m => m.test( st ) ).map( m => m.id ) : [] );

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

// A ticket for one battle. The game gets the map, sides, roster and
// conditions; the campaign keeps the ticket until a result with the same id
// comes back. The asset is paid for now and refunded if the battle never
// happens.
export function startBattle( s, plan, ticket ) {
	return produce( s, d => {
		const kind = plan.kind === 'defense' ? 'defense' : 'attack';
		const target = plan.target;
		let stage = 2;

		if ( kind === 'attack' ) {
			if ( !d.op || d.op.target !== target ) {
				if ( d.op )
					log( d, 'log.opAbandoned', { target: d.op.target }, 'neutral', d.op.target );
				d.op = { target, stage: 1, momentum: MAX_MOMENTUM, zone: plan.zone, startedDay: d.day };
				log( d, 'log.opStarted', { target }, 'ally', target );
			}
			stage = d.op.stage;
			d.op.zone = plan.zone;
		}

		const asset = canAfford( d, plan.asset ) ? plan.asset || null : null;
		const b = planBattle( d, { kind, target, stage, zone: plan.zone, asset } );
		d.supply -= b.cost;

		d.pending = {
			ticket, kind, target, stage, zone: b.zone.id, map: b.zone.map, mode: b.zone.mode,
			team: b.zone.team, swap: b.zone.swap,
			players: b.players, roster: b.roster, mods: b.mods, cancelled: b.cancelled,
			cfg: b.mods.filter( id => MODIFIERS[ id ].cfg ),
			asset: b.asset, cost: b.cost, reward: b.reward,
			cls: plan.cls || 'any', at: Date.now(),
		};
		d.prefs.cls = plan.cls || 'any';
	} );
}

// The deployment never produced a result (the player left, the game closed).
// Nothing strategic changes, and the asset is given back.
export function abandonBattle( s ) {
	return produce( s, d => {
		if ( d.pending ) {
			log( d, 'log.retreated', { target: d.pending.target }, 'neutral', d.pending.target );
			d.supply += d.pending.cost || 0;
		}
		d.pending = null;
	} );
}

// winner: 'RED' | 'BLU' | null (stalemate). stats: the player's line of the
// scoreboard, when the game sent one ({ score, kills, deaths, damage,
// healing, teamRank }).
export function resolveBattle( s, winner, { now = Date.now(), stats = null } = {} ) {
	if ( !s.pending )
		return s;

	return produce( s, d => {
		const p = d.pending;
		const defense = p.kind === 'defense';
		// Holding is the defender's job: running the attacker's clock out is a win.
		const outcome = winner === d.faction ? 'win' : !winner ? ( defense ? 'win' : 'stalemate' ) : 'loss';
		const won = outcome === 'win';

		const medals = medalsFor( stats );
		const mvp = medals.includes( 'mvp' );
		const result = {
			ticket: p.ticket, kind: p.kind || 'attack', outcome, target: p.target, map: p.map, mode: p.mode,
			mods: p.mods || [], asset: p.asset || null,
			stageBefore: d.op?.stage ?? 0, stageAfter: d.op?.stage ?? 0,
			momentumBefore: d.op?.momentum ?? 0, momentumAfter: d.op?.momentum ?? 0,
			captured: false, collapsed: false, warWon: false, defended: false, sectorLost: null,
			threatLost: null, opCut: null, saved: false, momentumBonus: false,
			supplyGain: 0, xpGain: 0, medals, stats, rankBefore: rankFor( d.xp ).id, rankAfter: null,
			world: null, read: false,
		};

		d.pending = null;
		d.stats.battles += 1;
		d.day += 1;
		if ( won ) d.stats.wins += 1;
		else d.stats.losses += 1;

		if ( defense ) {
			d.threat = null;
			if ( won ) {
				d.stats.defended += 1;
				result.defended = true;
				result.supplyGain += SUPPLY_DEFENSE_WIN + ( p.reward || 0 );
				log( d, 'log.defended', { target: p.target }, 'ally', p.target );
			} else {
				loseSector( d, p.target );
				result.sectorLost = p.target;
			}
		} else if ( won ) {
			result.supplyGain += SUPPLY_WIN + ( p.reward || 0 );
			if ( p.asset && ASSETS[ p.asset ]?.momentum ) d.op.momentum = Math.min( MAX_MOMENTUM, d.op.momentum + ASSETS[ p.asset ].momentum );
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
			// The best player on the losing side kept the attack together:
			// pushed back a stage, but no momentum lost.
			result.saved = mvp;
			if ( !mvp ) d.op.momentum -= 1;
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
				log( d, result.stageBefore > 1 ? 'log.pushedBack' : 'log.held',
					{ target: p.target, stage: d.op.stage }, 'enemy', p.target );
			}
		}

		// The winner of a battle was its best player: one more push.
		if ( won && mvp && d.op && d.op.momentum < MAX_MOMENTUM ) {
			d.op.momentum += 1;
			result.momentumBonus = true;
		}
		if ( d.op ) result.momentumAfter = d.op.momentum;

		// A counter-attack nobody answered.
		if ( !defense && d.threat && !d.finished ) {
			d.threat.left -= 1;
			if ( d.threat.left <= 0 ) {
				result.threatLost = d.threat.node;
				loseSector( d, d.threat.node );
				d.threat = null;
			}
		}

		// The operation's target may have been cut off by what fell.
		if ( d.op && !frontTargets( d ).includes( d.op.target ) ) {
			result.opCut = d.op.target;
			log( d, 'log.opCut', { target: d.op.target }, 'enemy', d.op.target );
			d.op = null;
		}

		// The player's record.
		result.xpGain = ( stats?.score || 0 ) + medals.length * MEDAL_XP;
		d.xp += result.xpGain;
		for ( const m of medals ) d.medals[ m ] = ( d.medals[ m ] || 0 ) + 1;
		result.rankAfter = rankFor( d.xp ).id;
		if ( result.rankAfter !== result.rankBefore )
			log( d, 'log.promoted', { rank: result.rankAfter }, 'ally' );
		d.supply += result.supplyGain;

		if ( !d.finished )
			result.world = worldTick( d );

		result.front = frontDelta( s.owners, d.owners );
		d.debrief = result;
		d.lastSeen = now;
	} );
}

function loseSector( d, id ) {
	d.owners[ id ] = 'enemy';
	delete d.pressure[ id ];
	d.stats.lost += 1;
	log( d, 'log.sectorLost', { target: id }, 'enemy', id );
}

// Applies one world event to a draft; returns what happened for the debrief.
function worldTick( d ) {
	for ( const ev of WORLD_EVENTS ) {
		if ( d.worldDone.includes( ev.id ) || !ev.when( d ) )
			continue;
		ev.apply( d );
		if ( ev.threat )
			d.threat = { node: ev.node, left: THREAT_BATTLES };
		if ( ev.node )
			d.worldDone.push( ev.id );
		log( d, `world.${ ev.id }`, {}, ev.tone, ev.node );
		return { id: ev.id, node: ev.node, tone: ev.tone, threat: !!ev.threat };
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
