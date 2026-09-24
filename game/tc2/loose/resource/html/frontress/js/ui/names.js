// How things are called on screen. The script speaks in 'ally'/'enemy'; the
// player reads RED and BLU.

import { t } from '../core/i18n.js';
import { node, enemyOf, stageInfo } from '../game/campaign.js';
import { CODENAMES } from '../game/scenario.js';

export const factionOf = ( s, owner ) => ( owner === 'ally' ? s.faction : enemyOf( s.faction ) );

export function sectorName( s, id ) {
	const n = node( id );
	if ( !n ) return id;
	if ( n.kind === 'hq' )
		return t( 'sector.hq', { faction: factionOf( s, n.owner ) } );
	return t( `sector.${ n.name }` );
}

export const codename = id => t( `code.${ CODENAMES[ id ] || 'hammer' }` );
export const stageName = stage => ( stage >= 4 ? t( 'stage.captured' ) : t( `stage.${ stageInfo( stage ).id }` ) );
export const modeName = mode => t( `mode.${ mode }` );
export const zoneName = id => t( `zone.${ id }` );
export const kindName = kind => t( `kind.${ kind }` );

// Everything a log line might need, resolved for the reader.
export function logVars( s, entry ) {
	const v = { ...entry.vars };
	if ( v.target ) {
		v.codename = codename( v.target );
		v.target = sectorName( s, v.target );
	}
	if ( v.stage ) v.stage = stageName( v.stage );
	if ( !v.faction ) v.faction = s.faction;
	return v;
}

// "cp_dustbowl" reads better as "Dustbowl".
export function mapTitle( map ) {
	return map.replace( /^(ctf|cp|pl|plr|koth|pd|arena)_/, '' )
		.replace( /_(final\d*|event|rc\d+|b\d+)$/, '' )
		.split( '_' ).map( w => w[ 0 ].toUpperCase() + w.slice( 1 ) ).join( ' ' );
}
