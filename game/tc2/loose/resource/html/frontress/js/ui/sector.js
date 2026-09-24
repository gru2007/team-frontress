// The small map inside a sector: where the landing zones are and what they
// lead to. It is a diagram, not the battlefield -- the objective is drawn in
// the shape of the stage's mode so the choice reads at a glance.

import { svg } from '../core/dom.js';
import { zoneName } from './names.js';
import { t } from '../core/i18n.js';

const VB = { w: 480, h: 250 };
const LZ = [ [ 112, 66 ], [ 120, 180 ] ];     // landing zone A, B
const OBJ = [ 395, 125 ];

export function buildSectorMap( zones, selectedId, stage, onPick ) {
	const sel = zones.find( z => z.id === selectedId ) || zones[ 0 ];

	const root = svg( 'svg', { class: 'sector-map', viewBox: `0 0 ${ VB.w } ${ VB.h }` },
		svg( 'defs', null,
			svg( 'pattern', { id: 'sm-grid', width: 24, height: 24, patternUnits: 'userSpaceOnUse' },
				svg( 'path', { d: 'M24 0 L0 0 0 24', class: 'sm-grid' } ) ),
			svg( 'marker', { id: 'sm-head', viewBox: '0 0 10 10', refX: 6, refY: 5, markerWidth: 4, markerHeight: 4, orient: 'auto' },
				svg( 'path', { d: 'M0 0 L10 5 L0 10 z', class: 'sm-head' } ) ),
		),
		svg( 'rect.sm-bg', { width: VB.w, height: VB.h } ),
		svg( 'rect', { width: VB.w, height: VB.h, fill: 'url(#sm-grid)' } ),
		// Enemy-held ground on the right, the line the attack starts from on the left.
		svg( 'path.sm-enemy', { d: `M250 0 C 230 60, 270 110, 240 160 S 250 230, 262 ${ VB.h } L${ VB.w } ${ VB.h } L${ VB.w } 0 Z` } ),
		svg( 'path.sm-line', { d: `M250 0 C 230 60, 270 110, 240 160 S 250 230, 262 ${ VB.h }` } ),
		svg( 'path.sm-block', { d: 'M150 20 h40 v28 h-40z M320 190 h54 v30 h-54z M300 30 h30 v44 h-30z M160 200 h26 v26 h-26z' } ),
		objective( sel.mode ),
		zones.map( ( z, i ) => {
			const [ x, y ] = LZ[ i ] || LZ[ 0 ];
			const active = z.id === sel.id;
			const path = `M${ x + 18 } ${ y } C ${ x + 140 } ${ y }, ${ OBJ[ 0 ] - 150 } ${ OBJ[ 1 ] + ( y < OBJ[ 1 ] ? -40 : 40 ) }, ${ OBJ[ 0 ] - 34 } ${ OBJ[ 1 ] + ( y < OBJ[ 1 ] ? -10 : 10 ) }`;
			const g = svg( 'g', { class: `sm-zone ${ active ? 'active' : '' }`, 'data-zone': z.id },
				svg( 'path.sm-route', { d: path, 'marker-end': 'url(#sm-head)' } ),
				svg( 'circle.sm-lz-ring', { cx: x, cy: y, r: 17 } ),
				svg( 'path.sm-lz-chute', { d: `M${ x - 9 } ${ y - 4 } Q${ x } ${ y - 16 } ${ x + 9 } ${ y - 4 } L${ x } ${ y + 7 } Z` } ),
				svg( 'text.sm-lz-letter', { x: x - 26, y: y + 5, 'text-anchor': 'end' }, String.fromCharCode( 65 + i ) ),
				svg( 'text.sm-lz-name', { x, y: y + 34, 'text-anchor': 'middle' }, zoneName( z.id ).toUpperCase() ),
			);
			g.addEventListener( 'click', () => onPick?.( z.id ) );
			return g;
		} ),
		svg( 'text.sm-stage', { x: VB.w - 12, y: VB.h - 12, 'text-anchor': 'end' }, t( 'stage.n', { n: stage } ).toUpperCase() ),
	);
	return root;
}

function objective( mode ) {
	const [ x, y ] = OBJ;
	switch ( mode ) {
	case 'ctf':
		return svg( 'g.sm-obj', null,
			svg( 'rect.sm-base', { x: x - 26, y: y - 26, width: 52, height: 52, rx: 4 } ),
			svg( 'path.sm-flag-pole', { d: `M${ x - 6 } ${ y + 16 } L${ x - 6 } ${ y - 18 }` } ),
			svg( 'path.sm-flag', { d: `M${ x - 6 } ${ y - 18 } L${ x + 14 } ${ y - 11 } L${ x - 6 } ${ y - 4 } Z` } ) );
	case 'koth':
		return svg( 'g.sm-obj', null,
			svg( 'circle.sm-point-ring', { cx: x - 60, cy: y, r: 28 } ),
			svg( 'circle.sm-point', { cx: x - 60, cy: y, r: 12 } ) );
	case 'pl':
	case 'plr':
		return svg( 'g.sm-obj', null,
			svg( 'path.sm-track', { d: `M150 ${ y + 10 } C 230 ${ y + 60 }, 300 ${ y - 50 }, ${ x + 40 } ${ y }` } ),
			svg( 'path.sm-track-ties', { d: `M150 ${ y + 10 } C 230 ${ y + 60 }, 300 ${ y - 50 }, ${ x + 40 } ${ y }` } ),
			svg( 'rect.sm-cart', { x: 140, y: y - 2, width: 24, height: 16, rx: 2 } ),
			svg( 'circle.sm-point', { cx: x + 40, cy: y, r: 11 } ) );
	default:
		return svg( 'g.sm-obj', null,
			[ [ 280, 70 ], [ 330, 170 ], [ x + 20, y ] ].map( ( [ px, py ], i ) => [
				svg( 'circle.sm-point-ring', { cx: px, cy: py, r: 17 } ),
				svg( 'text.sm-point-letter', { x: px, y: py + 5, 'text-anchor': 'middle' }, String.fromCharCode( 65 + i ) ),
			] ) );
	}
}
