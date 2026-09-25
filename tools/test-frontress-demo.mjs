// Rules of the demo war (game/tc2/loose/resource/html/frontress/js/game).
//
//   node --test tools/test-frontress-demo.mjs
//
// Pure functions only: no browser, no game.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const root = new URL( '../game/tc2/loose/resource/html/frontress/js/game/', import.meta.url );
const C = await import( new URL( 'campaign.js', root ) );
const { NODES, OPERATIONS, STAGES, DIRECTIONAL_MODES, MODIFIERS } = await import( new URL( 'scenario.js', root ) );
import { existsSync } from 'node:fs';

const fresh = faction => C.chooseFaction( C.initialState(), faction );

function fight( s, winnerIsAlly, target, stats = null ) {
	const r = target ? { target, stage: 1, zone: null } : C.recommend( s );
	s = C.startBattle( s, { ...r, cls: 'soldier' }, C.newTicket() );
	const winner = winnerIsAlly === null ? null : winnerIsAlly ? s.faction : C.enemyOf( s.faction );
	return C.resolveBattle( s, winner, { stats } );
}

const MVP = { score: 30, kills: 12, deaths: 2, damage: 4000, healing: 0, teamRank: 1 };
const AVERAGE = { score: 8, kills: 3, deaths: 6, damage: 900, healing: 0, teamRank: 4 };

test( 'every battlefield is a bot map', () => {
	const bots = new Set( readFileSync( new URL( '../game/tc2/cfg/mapcycle_quickplay_bots.txt', import.meta.url ), 'utf8' ).split( /\s+/ ).filter( Boolean ) );
	for ( const [ op, stages ] of Object.entries( OPERATIONS ) ) {
		assert.equal( stages.length, STAGES.length, op );
		for ( const zones of stages )
			for ( const z of zones )
				assert.ok( bots.has( z.map ), `${ op }/${ z.id }: ${ z.map } is not in the bot mapcycle` );
	}
} );

test( 'every condition the game runs has its cfg, and the reset undoes it', () => {
	const cfg = name => new URL( `../game/tc2/cfg/${ name }`, import.meta.url );
	const reset = readFileSync( cfg( 'frontress_demo.cfg' ), 'utf8' );
	for ( const [ id, m ] of Object.entries( MODIFIERS ) ) {
		if ( !m.cfg ) continue;
		assert.ok( existsSync( cfg( `frontress_mod_${ id }.cfg` ) ), `cfg/frontress_mod_${ id }.cfg is missing` );
		const lines = readFileSync( cfg( `frontress_mod_${ id }.cfg` ), 'utf8' ).split( '\n' ).filter( l => l.trim() && !l.startsWith( '//' ) );
		for ( const l of lines )
			assert.ok( reset.includes( l.split( /\s+/ )[ 0 ] + ' ' ), `frontress_demo.cfg does not reset ${ l.split( /\s+/ )[ 0 ] } (set by ${ id })` );
	}
} );

test( 'modes match their maps: directional modes on BLU-attacks maps, symmetric ones not', () => {
	for ( const stages of Object.values( OPERATIONS ) )
		for ( const zones of stages )
			for ( const z of zones ) {
				const directional = /^pl_/.test( z.map ) || [ 'cp_dustbowl', 'cp_gorge', 'cp_mossrock', 'cp_mercenarypark', 'cp_altitude' ].includes( z.map );
				assert.equal( DIRECTIONAL_MODES.includes( z.mode ), directional, `${ z.id }: ${ z.mode } on ${ z.map }` );
			}
} );

test( 'both sides fight the same battles; RED attacks as BLU in swapped uniforms', () => {
	const red = fresh( 'RED' ), blu = fresh( 'BLU' );
	for ( const target of Object.keys( red.owners ) )
		for ( const stage of [ 1, 2, 3 ] ) {
			const r = C.zonesFor( red, target, stage ), b = C.zonesFor( blu, target, stage );
			assert.deepEqual( r.map( z => z.map ), b.map( z => z.map ) );
			for ( const [ i, z ] of r.entries() ) {
				assert.equal( b[ i ].swap, false, 'BLU never needs a swap' );
				if ( DIRECTIONAL_MODES.includes( z.mode ) )
					assert.deepEqual( [ z.team, z.swap ], [ 'BLU', true ] );
				else
					assert.deepEqual( [ z.team, z.swap ], [ 'RED', false ] );
			}
		}
} );

test( 'the game reports a team; the war counts a side', () => {
	const red = fresh( 'RED' );
	const swapped = { team: 'BLU', swap: true };
	assert.equal( C.winnerSide( red, swapped, 'blue' ), 'RED' );
	assert.equal( C.winnerSide( red, swapped, 'red' ), 'BLU' );
	assert.equal( C.winnerSide( red, { team: 'RED' }, 'red' ), 'RED' );
	assert.equal( C.winnerSide( red, swapped, 'none' ), null );
	// A swapped battle ticket carries what the game needs.
	let s = red;
	s = fight( s, true );                 // stage 1: a symmetric mode
	s = C.startBattle( s, { ...C.recommend( s ), cls: 'any' }, 't2' );
	assert.equal( s.pending.mode, 'pl' );
	assert.deepEqual( [ s.pending.team, s.pending.swap ], [ 'BLU', true ] );
	s = C.resolveBattle( s, C.winnerSide( s, s.pending, 'blue' ) );
	assert.equal( s.debrief.outcome, 'win' );
} );

test( 'three wins capture a sector and move the front', () => {
	for ( const f of [ 'RED', 'BLU' ] ) {
		let s = fresh( f );
		const target = C.recommend( s ).target;
		const before = C.frontTargets( s );
		s = fight( s, true );
		assert.equal( s.op.stage, 2 );
		s = fight( s, true );
		assert.equal( s.op.stage, 3 );
		s = fight( s, true );
		assert.equal( s.op, null );
		assert.equal( s.owners[ target ], 'ally' );
		assert.equal( s.debrief.captured, true );
		assert.deepEqual( s.stats, { ...s.stats, battles: 3, wins: 3, losses: 0, captured: 1 } );
		assert.ok( s.supply > 2, 'wins pay supply' );
		assert.notDeepEqual( C.frontTargets( s ), before );
	}
} );

test( 'a loss pushes back a stage and costs momentum; three collapse the offensive', () => {
	let s = fresh( 'RED' );
	s = fight( s, true );
	s = fight( s, true );
	assert.equal( s.op.stage, 3 );
	s = fight( s, false );
	assert.equal( s.op.stage, 2 );
	assert.equal( s.op.momentum, 2 );
	s = fight( s, null, 'junction' );      // stalemate counts as repulsed
	assert.equal( s.op.stage, 1 );
	assert.equal( s.debrief.outcome, 'stalemate' );
	s = fight( s, false, 'junction' );     // stay on the attack, whatever else burns
	assert.equal( s.op, null );
	assert.equal( s.debrief.collapsed, true );
	assert.equal( s.stats.losses, 3 );
} );

test( 'a counter-attack nobody answers takes the sector', () => {
	let s = fresh( 'BLU' );
	for ( let i = 0; i < 3; i++ )
		s = fight( s, false, 'quarry' );
	assert.deepEqual( s.threat, { node: 'sawmill', left: 2 } );
	assert.equal( C.sectorStatus( s, 'sawmill' ), 'threat' );
	s = fight( s, false, 'quarry' );
	assert.equal( C.recommend( s ).kind, 'defense', 'the coordinator sends you to a sector about to fall' );
	s = fight( s, false, 'quarry' );           // ignore it anyway
	assert.equal( s.owners.sawmill, 'enemy' );
	assert.equal( s.debrief.threatLost, 'sawmill' );
	assert.equal( s.threat, null );
	assert.ok( C.frontTargets( s ).includes( 'sawmill' ) );
} );

test( 'defending: a win holds the sector and pays, a loss gives it up', () => {
	const threatened = [ 'W', 'W', 'W' ].reduce( s => fight( s, true ), fresh( 'RED' ) );
	assert.equal( threatened.threat?.node, 'sawmill' );
	const plan = { kind: 'defense', target: 'sawmill', zone: null, cls: 'any' };

	let s = C.startBattle( threatened, plan, 'def1' );
	assert.equal( s.pending.kind, 'defense' );
	assert.deepEqual( [ s.pending.team, s.pending.swap ], [ 'RED', false ], 'RED defends as the game\'s RED' );
	assert.ok( DIRECTIONAL_MODES.includes( s.pending.mode ) );
	const supply = s.supply;
	s = C.resolveBattle( s, 'RED' );
	assert.equal( s.threat, null );
	assert.equal( s.owners.sawmill, 'ally' );
	assert.equal( s.supply, supply + 2 + s.debrief.mods.length, '+2 for holding, +1 for the seasoned attackers' );
	assert.equal( s.stats.defended, 1 );

	let lost = C.resolveBattle( C.startBattle( threatened, plan, 'def2' ), 'BLU' );
	assert.equal( lost.owners.sawmill, 'enemy' );
	assert.equal( lost.debrief.sectorLost, 'sawmill' );

	const blu = C.startBattle( [ 'W', 'W', 'W' ].reduce( s => fight( s, true ), fresh( 'BLU' ) ), plan, 'def3' );
	assert.deepEqual( [ blu.pending.team, blu.pending.swap ], [ 'RED', true ], 'BLU defends in swapped uniforms' );
} );

test( 'conditions and assets decide who fights', () => {
	const s = fresh( 'RED' );
	const base = C.planBattle( s, { target: 'junction', stage: 1 } );
	assert.deepEqual( base.mods, [ 'grapples' ] );
	assert.equal( base.players, 8 );
	assert.deepEqual( [ base.roster.allies, base.roster.enemies ], [ 3, 4 ] );

	const snipers = C.planBattle( s, { target: 'junction', stage: 3 } );
	assert.deepEqual( snipers.roster.eclass, { sniper: 3 } );
	assert.equal( snipers.reward, 1 );

	const intel = C.planBattle( s, { target: 'junction', stage: 3, asset: 'intel' } );
	assert.deepEqual( intel.mods, [] );
	assert.equal( intel.cancelled, 'snipers' );

	const reinforced = C.planBattle( s, { target: 'junction', stage: 2, asset: 'reinforce' } );
	assert.equal( reinforced.roster.allies, 7 );

	const gamble = C.planBattle( s, { target: 'hq_enemy', stage: 3, asset: 'gamble' } );
	assert.ok( gamble.roster.eskill <= 3 && gamble.players <= 23 );
	assert.equal( gamble.roster.eskill, 3 );

	for ( const target of Object.keys( s.owners ) )
		for ( const stage of [ 1, 2, 3 ] )
			for ( const asset of [ null, 'reinforce', 'elite', 'intel', 'gamble' ] ) {
				const b = C.planBattle( s, { target, stage, asset } );
				assert.ok( b.players <= 23 && b.roster.enemies >= Object.values( b.roster.eclass ).reduce( ( a, c ) => a + c, 0 ), `${ target }/${ stage }/${ asset }` );
			}
} );

test( 'supply is spent on assets and refunded when the battle never happens', () => {
	let s = fresh( 'RED' );
	assert.equal( s.supply, 2 );
	s = C.startBattle( s, { ...C.recommend( s ), asset: 'reinforce', cls: 'any' }, 'a1' );
	assert.equal( s.supply, 0 );
	assert.equal( s.pending.asset, 'reinforce' );
	assert.equal( C.abandonBattle( s ).supply, 2 );
	const broke = C.startBattle( C.abandonBattle( s ), { ...C.recommend( s ), asset: 'reinforce' }, 'a2' );
	const poorer = C.startBattle( C.produce( broke, d => { d.pending = null; d.supply = 1; } ), { ...C.recommend( s ), asset: 'elite' }, 'a3' );
	assert.equal( poorer.pending.asset, null, 'an asset you cannot pay for is not bought' );
} );

test( 'the player\'s own battle counts', () => {
	let s = fresh( 'RED' );
	s = fight( s, true );
	s = fight( s, false, null, MVP );          // lost, but the best player on the field
	assert.equal( s.debrief.saved, true );
	assert.equal( s.op.momentum, 3, 'no momentum lost' );
	assert.ok( s.debrief.medals.includes( 'mvp' ) && s.debrief.medals.includes( 'slayer' ) && s.debrief.medals.includes( 'wrecker' ) );
	assert.equal( s.medals.mvp, 1 );
	s = fight( s, false, null, AVERAGE );
	assert.equal( s.op.momentum, 2 );
	s = fight( s, true, null, MVP );
	assert.equal( s.debrief.momentumBonus, true );
	assert.equal( s.op.momentum, 3 );
	assert.ok( s.xp > 0 );
	assert.equal( C.rankFor( 0 ).id, 'private' );
	assert.equal( C.rankFor( 100 ).id, 'sergeant' );
	assert.equal( C.rankFor( 10000 ).next, null );
} );

test( 'the war can be won, and then nothing else can be started', () => {
	let s = fresh( 'RED' );
	let guard = 0;
	while ( !s.finished && guard++ < 200 )
		s = fight( s, true );
	assert.ok( s.finished, 'war did not end' );
	assert.equal( s.owners.hq_enemy, 'ally' );
	assert.equal( C.recommend( s ), null );
	// Festival length: front line, second line, headquarters -- nine attacks,
	// plus the counter-attacks the coordinator sent the player to hold.
	assert.equal( s.stats.battles - s.stats.defended, 9 );
	assert.equal( s.stats.captured, 3 );
	assert.ok( s.stats.defended >= 1 );
} );

test( 'a result for another ticket, or none at all, changes nothing', () => {
	let s = fresh( 'RED' );
	s = C.startBattle( s, { ...C.recommend( s ), cls: 'any' }, 'ticket-a' );
	const abandoned = C.abandonBattle( s );
	assert.equal( abandoned.pending, null );
	assert.equal( abandoned.stats.battles, 0 );
	assert.equal( C.resolveBattle( abandoned, 'RED' ), abandoned );
} );

test( 'saves from other versions do not break the menu', () => {
	assert.equal( C.normalize( null ).faction, null );
	assert.equal( C.normalize( { v: 999, faction: 'RED' } ).faction, null );
	const v2 = C.normalize( { v: 2, faction: 'RED', stats: { battles: 4 } } );
	assert.deepEqual( [ v2.v, v2.faction, v2.supply, v2.xp, v2.stats.battles, v2.stats.defended ], [ C.SCHEMA, 'RED', 2, 0, 4, 0 ] );
	const s = C.normalize( { v: C.SCHEMA, faction: 'GRN', owners: { junction: 'ally' }, op: { target: 'nowhere' } } );
	assert.equal( s.faction, null );
	assert.equal( s.op, null );
	assert.equal( s.owners.junction, 'ally' );
	assert.equal( Object.keys( s.owners ).length, NODES.length );
} );

test( 'coming back after a break brings a bulletin', () => {
	let s = fresh( 'RED' );
	s = { ...s, lastSeen: Date.now() - 30 * 60 * 1000 };
	const back = C.arrive( s );
	assert.ok( back.away );
	assert.equal( back.away.minutes, 30 );
	assert.equal( C.arrive( back ).away, null );
} );
