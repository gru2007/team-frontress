// The theater: the strategic map of the war, as one SVG.
//
// Territory is not drawn, it is derived. The theater is tiled with hexes and
// every hex belongs to the nearest sector node, so the ground follows the
// nodes: change an owner and the colours, the sector borders and the front
// line all move with it, and the map cannot disagree with the campaign.
//
// The SVG is built once per war state (see `key`) and kept; selection and
// hover only toggle classes on it.

import { svg } from '../core/dom.js';
import { NODES, EDGES, THEATER, TERRAIN } from '../game/scenario.js';
import { stagingFor, sectorStatus, neighbours } from '../game/campaign.js';
import { sectorName } from './names.js';
import { kindGlyph } from './glyphs.js';

const R = 21;                       // hex radius
const W = Math.sqrt( 3 ) * R;       // hex width
const ROW = 1.5 * R;

// Pointy-top corners, starting at the top and going clockwise.
const CORNERS = Array.from( { length: 6 }, ( _, i ) => {
	const a = ( -90 + 60 * i ) * Math.PI / 180;
	return [ Math.cos( a ) * R, Math.sin( a ) * R ];
} );

// Neighbour across edge i (corner i -> corner i+1), for odd-r offset rows.
function neighbour( c, r, edge ) {
	const odd = r & 1;
	switch ( edge ) {
	case 0: return [ c + odd, r - 1 ];      // NE
	case 1: return [ c + 1, r ];            // E
	case 2: return [ c + odd, r + 1 ];      // SE
	case 3: return [ c - 1 + odd, r + 1 ];  // SW
	case 4: return [ c - 1, r ];            // W
	default: return [ c - 1 + odd, r - 1 ]; // NW
	}
}

const mirrorX = ( s, x ) => ( s.faction === 'BLU' ? THEATER.width - x : x );
export const nodePos = ( s, n ) => [ mirrorX( s, n.x ), n.y ];

export function theaterKey( s ) {
	return JSON.stringify( [ s.faction, s.owners, s.op?.target, s.op?.stage, s.pressure, s.pending?.target, s.threat ] );
}

//-----------------------------------------------------------------------------
export function buildTheater( s, { onSelect, onHover } ) {
	const cols = Math.ceil( THEATER.width / W ) + 1;
	const rows = Math.ceil( THEATER.height / ROW ) + 1;
	const pos = Object.fromEntries( NODES.map( n => [ n.id, nodePos( s, n ) ] ) );

	// Which sector every hex belongs to.
	const grid = [];
	const byRegion = {};
	for ( let r = 0; r < rows; r++ ) {
		grid[ r ] = [];
		for ( let c = 0; c < cols; c++ ) {
			const x = c * W + ( r & 1 ? W / 2 : 0 );
			const y = r * ROW;
			let best = null, bestD = Infinity;
			for ( const n of NODES ) {
				const [ nx, ny ] = pos[ n.id ];
				const d = Math.hypot( nx - x, ny - y ) / ( n.weight || 1 );
				if ( d < bestD ) { bestD = d; best = n.id; }
			}
			grid[ r ][ c ] = { x, y, region: best };
		}
	}

	const at = ( c, r ) => grid[ r ]?.[ c ];
	const ownerOf = id => s.owners[ id ];

	// Hexes, sector borders and the front.
	const hexes = svg( 'g.hexes' );
	let borders = '', front = '';
	for ( let r = 0; r < rows; r++ ) {
		for ( let c = 0; c < cols; c++ ) {
			const hx = grid[ r ][ c ];
			const pts = CORNERS.map( ( [ dx, dy ] ) => `${ ( hx.x + dx ).toFixed( 1 ) },${ ( hx.y + dy ).toFixed( 1 ) }` ).join( ' ' );
			const cls = [ 'hex', ownerOf( hx.region ) ];
			if ( s.op?.target === hx.region ) cls.push( 'target' );
			if ( s.pressure[ hx.region ] ) cls.push( 'pressured' );
			const poly = svg( 'polygon', { points: pts, class: cls.join( ' ' ), 'data-region': hx.region } );
			hexes.appendChild( poly );
			( byRegion[ hx.region ] ??= [] ).push( poly );

			// Only three edges per hex, so each shared edge is visited once.
			for ( const e of [ 0, 1, 2 ] ) {
				const [ nc, nr ] = neighbour( c, r, e );
				const other = at( nc, nr );
				if ( !other || other.region === hx.region )
					continue;
				const [ ax, ay ] = CORNERS[ e ], [ bx, by ] = CORNERS[ ( e + 1 ) % 6 ];
				const seg = `M${ ( hx.x + ax ).toFixed( 1 ) } ${ ( hx.y + ay ).toFixed( 1 ) }L${ ( hx.x + bx ).toFixed( 1 ) } ${ ( hx.y + by ).toFixed( 1 ) }`;
				if ( ownerOf( other.region ) !== ownerOf( hx.region ) )
					front += seg;
				else
					borders += seg;
			}
		}
	}

	// Roads and rails between sectors.
	const roads = svg( 'g.roads' );
	for ( const [ a, b, rail ] of EDGES ) {
		const [ ax, ay ] = pos[ a ], [ bx, by ] = pos[ b ];
		const d = `M${ ax } ${ ay }L${ bx } ${ by }`;
		if ( rail ) {
			roads.appendChild( svg( 'path', { d, class: 'rail-bed' } ) );
			roads.appendChild( svg( 'path', { d, class: 'rail-ties' } ) );
		} else {
			roads.appendChild( svg( 'path', { d, class: 'road' } ) );
		}
	}

	// The operation's arrow: from where the column jumps off to the target.
	const arrows = svg( 'g.arrows' );
	const target = s.op?.target || ( s.pending?.kind !== 'defense' ? s.pending?.target : null );
	if ( target ) {
		const from = stagingFor( s, target );
		if ( from )
			arrows.appendChild( attackArrow( pos[ from ], pos[ target ], s.op?.stage || 1 ) );
	}
	// A counter-attack comes the other way.
	if ( s.threat ) {
		const from = neighbours( s.threat.node ).filter( m => s.owners[ m ] === 'enemy' )
			.sort( ( a, b ) => Math.abs( pos[ a ][ 1 ] - pos[ s.threat.node ][ 1 ] ) - Math.abs( pos[ b ][ 1 ] - pos[ s.threat.node ][ 1 ] ) )[ 0 ];
		if ( from )
			arrows.appendChild( attackArrow( pos[ from ], pos[ s.threat.node ], 1, 'enemy-attack' ) );
	}

	// Sector markers.
	const markers = svg( 'g.markers' );
	const markerById = {};
	for ( const n of NODES ) {
		const [ x, y ] = pos[ n.id ];
		const status = sectorStatus( s, n.id );
		const m = svg( 'g', {
			class: `marker ${ ownerOf( n.id ) } ${ status } kind-${ n.kind }`,
			transform: `translate(${ x } ${ y })`, 'data-region': n.id,
		},
			status === 'target' || status === 'operation' || status === 'threat'
				? svg( 'circle.pulse', { r: 26 } ) : null,
			svg( 'circle.ring', { r: n.kind === 'hq' ? 24 : 19 } ),
			svg( 'g.glyph', { transform: `scale(${ n.kind === 'hq' ? 1.15 : 0.95 })` }, kindGlyph( n.kind ) ),
			svg( 'g.label', { transform: `translate(0 ${ n.kind === 'hq' ? 44 : 38 })` },
				svg( 'rect.plate', { x: -80, y: -13, width: 160, height: 22, rx: 3 } ),
				svg( 'text.name', { 'text-anchor': 'middle', y: 3 }, sectorName( s, n.id ).toUpperCase() ) ),
		);
		markers.appendChild( m );
		markerById[ n.id ] = m;
	}

	const root = svg( 'svg', {
		class: 'theater', viewBox: `0 0 ${ THEATER.width } ${ THEATER.height }`,
		preserveAspectRatio: 'xMidYMid meet',
	},
		defs(),
		svg( 'rect.ground', { x: 0, y: 0, width: THEATER.width, height: THEATER.height } ),
		svg( 'rect.ground-noise', { x: 0, y: 0, width: THEATER.width, height: THEATER.height, filter: 'url(#paper)' } ),
		hexes,
		terrain( s ),
		svg( 'path.borders', { d: borders } ),
		svg( 'path.front-glow', { d: front } ),
		svg( 'path.front', { d: front } ),
		svg( 'path.front-teeth', { d: front } ),
		roads,
		arrows,
		markers,
		compass( s ),
	);

	// Fit label plates to their text once the fonts have laid them out.
	requestAnimationFrame( () => {
		for ( const m of Object.values( markerById ) ) {
			const text = m.querySelector( 'text.name' ), plate = m.querySelector( 'rect.plate' );
			try {
				const w = text.getComputedTextLength() + 18;
				plate.setAttribute( 'x', -w / 2 );
				plate.setAttribute( 'width', w );
			} catch { /* not laid out yet */ }
		}
	} );

	// Interaction: anything that knows its region selects it.
	let hovered = null;
	const setHover = id => {
		if ( id === hovered ) return;
		if ( hovered ) {
			byRegion[ hovered ]?.forEach( p => p.classList.remove( 'hover' ) );
			markerById[ hovered ]?.classList.remove( 'hover' );
		}
		hovered = id;
		if ( id ) {
			byRegion[ id ]?.forEach( p => p.classList.add( 'hover' ) );
			markerById[ id ]?.classList.add( 'hover' );
		}
		onHover?.( id );
	};
	root.addEventListener( 'mousemove', e => setHover( e.target.closest( '[data-region]' )?.getAttribute( 'data-region' ) || null ) );
	root.addEventListener( 'mouseleave', () => setHover( null ) );
	root.addEventListener( 'click', e => {
		const id = e.target.closest( '[data-region]' )?.getAttribute( 'data-region' );
		onSelect?.( id || null );
	} );

	return {
		el: root,
		select( id ) {
			root.querySelectorAll( '.selected' ).forEach( el => el.classList.remove( 'selected' ) );
			if ( id ) {
				byRegion[ id ]?.forEach( p => p.classList.add( 'selected' ) );
				markerById[ id ]?.classList.add( 'selected' );
			}
		},
	};
}

//-----------------------------------------------------------------------------
function defs() {
	return svg( 'defs', null,
		svg( 'filter', { id: 'paper', x: 0, y: 0, width: '100%', height: '100%' },
			svg( 'feTurbulence', { type: 'fractalNoise', baseFrequency: '0.9', numOctaves: 2, seed: 7 } ),
			svg( 'feColorMatrix', { values: '0 0 0 0 0.35  0 0 0 0 0.27  0 0 0 0 0.18  0 0 0 0.55 0' } ) ),
		svg( 'pattern', { id: 'hatch', width: 10, height: 10, patternUnits: 'userSpaceOnUse', patternTransform: 'rotate(45)' },
			svg( 'rect', { width: 4, height: 10, class: 'hatch-stroke' } ) ),
		svg( 'marker', { id: 'arrowhead', viewBox: '0 0 10 10', refX: 5, refY: 5, markerWidth: 3.2, markerHeight: 3.2, orient: 'auto-start-reverse' },
			svg( 'path', { d: 'M0 0 L10 5 L0 10 z', class: 'arrowhead' } ) ),
		svg( 'marker', { id: 'arrowhead-enemy', viewBox: '0 0 10 10', refX: 5, refY: 5, markerWidth: 3.2, markerHeight: 3.2, orient: 'auto-start-reverse' },
			svg( 'path', { d: 'M0 0 L10 5 L0 10 z', class: 'arrowhead enemy' } ) ),
	);
}

function terrain( s ) {
	const flip = s.faction === 'BLU' ? `translate(${ THEATER.width } 0) scale(-1 1)` : null;
	return svg( 'g.terrain', { transform: flip },
		TERRAIN.rivers.map( d => [ svg( 'path.river-bank', { d } ), svg( 'path.river', { d } ) ] ),
		TERRAIN.ridges.map( ( { x, y, s: k } ) => svg( 'path.ridge', {
			d: 'M-30 10 L-14 -8 L-6 2 L6 -16 L22 6 L30 10',
			transform: `translate(${ x } ${ y }) scale(${ k })`,
		} ) ),
	);
}

function attackArrow( [ ax, ay ], [ bx, by ], stage, extra = '' ) {
	// Stop short of both markers, and bow the shaft so it reads as a movement.
	const dx = bx - ax, dy = by - ay, len = Math.hypot( dx, dy );
	const ux = dx / len, uy = dy / len;
	const sx = ax + ux * 34, sy = ay + uy * 34, ex = bx - ux * 40, ey = by - uy * 40;
	const mx = ( sx + ex ) / 2 - uy * 40, my = ( sy + ey ) / 2 + ux * 40;
	const d = `M${ sx } ${ sy } Q${ mx } ${ my } ${ ex } ${ ey }`;
	return svg( 'g', { class: `attack stage-${ stage } ${ extra }` },
		svg( 'path.attack-shadow', { d } ),
		svg( 'path.attack-shaft', { d, 'marker-end': extra ? 'url(#arrowhead-enemy)' : 'url(#arrowhead)' } ),
		svg( 'path.attack-flow', { d } ),
	);
}

function compass() {
	return svg( 'g.compass', { transform: `translate(${ THEATER.width - 70 } 70)` },
		svg( 'circle', { r: 30 } ),
		svg( 'path', { d: 'M0 -26 L7 0 L0 26 L-7 0 Z' } ),
		svg( 'text', { y: -34, 'text-anchor': 'middle' }, 'N' ),
	);
}
