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
const { NODES, OPERATIONS, STAGES, DIRECTIONAL_MODES } = await import( new URL( 'scenario.js', root ) );

const fresh = faction => C.chooseFaction( C.initialState(), faction );

function fight( s, winnerIsAlly, target ) {
	const r = target ? { target, stage: 1, zone: null } : C.recommend( s );
	s = C.startBattle( s, { ...r, cls: 'soldier' }, C.newTicket() );
	const winner = winnerIsAlly === null ? null : winnerIsAlly ? s.faction : C.enemyOf( s.faction );
	return C.resolveBattle( s, winner );
}

test( 'every battlefield is a bot map', () => {
	const bots = new Set( readFileSync( new URL( '../game/tc2/cfg/mapcycle_quickplay_bots.txt', import.meta.url ), 'utf8' ).split( /\s+/ ).filter( Boolean ) );
	for ( const [ op, stages ] of Object.entries( OPERATIONS ) ) {
		assert.equal( stages.length, STAGES.length, op );
		for ( const zones of stages )
			for ( const z of zones )
				assert.ok( bots.has( z.map ), `${ op }/${ z.id }: ${ z.map } is not in the bot mapcycle` );
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
	s = fight( s, null );                  // stalemate counts as repulsed
	assert.equal( s.op.stage, 1 );
	assert.equal( s.debrief.outcome, 'stalemate' );
	s = fight( s, false );
	assert.equal( s.op, null );
	assert.equal( s.debrief.collapsed, true );
	assert.equal( s.stats.losses, 3 );
} );

test( 'every battle moves the world, and the scripted counter-attack can take ground back', () => {
	let s = fresh( 'BLU' );
	for ( let i = 0; i < 5; i++ )
		s = fight( s, false, 'quarry' );    // stay away from the Sawmill
	assert.equal( s.owners.sawmill, 'enemy' );
	assert.ok( C.frontTargets( s ).includes( 'sawmill' ) );
	assert.ok( s.log.some( e => e.key === 'world.sawmill_lost' ) );
} );

test( 'the war can be won, and then nothing else can be started', () => {
	let s = fresh( 'RED' );
	let guard = 0;
	while ( !s.finished && guard++ < 200 )
		s = fight( s, true );
	assert.ok( s.finished, 'war did not end' );
	assert.equal( s.owners.hq_enemy, 'ally' );
	assert.equal( C.recommend( s ), null );
	// Festival length: front line, second line, headquarters.
	assert.equal( s.stats.battles, 9 );
	assert.equal( s.stats.captured, 3 );
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
