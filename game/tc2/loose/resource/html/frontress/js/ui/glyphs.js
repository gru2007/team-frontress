// Small line-art icons, drawn in a 24-unit box centred on 0,0 so they sit in
// map markers and in HTML alike.

import { svg } from '../core/dom.js';

const P = ( d, cls = 'g-stroke' ) => svg( 'path', { d, class: cls } );

const KIND = {
	hq: () => [ P( 'M-6 11 L-6 -12', 'g-stroke' ), P( 'M-6 -12 L9 -7 L-6 -2 Z', 'g-fill' ), P( 'M-11 11 L3 11' ) ],
	rail: () => [ P( 'M-5 -12 L-8 12 M5 -12 L8 12' ), P( 'M-8 -7 L8 -7 M-9 0 L9 0 M-10 7 L10 7' ) ],
	factory: () => [ P( 'M-11 11 L-11 -2 L-4 3 L-4 -2 L3 3 L3 -12 L8 -12 L8 11 Z', 'g-fill' ) ],
	mine: () => [ P( 'M-10 -6 Q0 -14 10 -6' ), P( 'M0 -10 L0 12' ), P( 'M-12 12 L12 12' ) ],
	lumber: () => [ P( 'M0 -12 L-9 4 L9 4 Z', 'g-fill' ), P( 'M0 4 L0 12' ) ],
	dam: () => [ P( 'M-11 -4 Q-6 -9 -1 -4 T9 -4 M-11 3 Q-6 -2 -1 3 T9 3 M-11 10 Q-6 5 -1 10 T9 10' ) ],
	power: () => [ P( 'M2 -13 L-8 2 L-1 2 L-3 13 L8 -3 L1 -3 Z', 'g-fill' ) ],
	depot: () => [ P( 'M-10 -8 L10 -8 L10 10 L-10 10 Z' ), P( 'M-10 -8 L10 10 M10 -8 L-10 10' ) ],
	lab: () => [ P( 'M-4 -12 L4 -12 M-3 -12 L-3 -3 L-10 10 L10 10 L3 -3 L3 -12' ) ],
	farm: () => [ P( 'M-11 11 L-11 -2 L0 -11 L11 -2 L11 11 Z' ), P( 'M-4 11 L-4 3 L4 3 L4 11' ) ],
};

export const kindGlyph = kind => ( KIND[ kind ] || KIND.depot )();

// Mode icons for HTML use.
const MODE = {
	flag: () => [ P( 'M-7 12 L-7 -12' ), P( 'M-7 -12 L10 -7 L-7 -1 Z', 'g-fill' ) ],
	point: () => [ svg( 'circle', { r: 10, class: 'g-stroke' } ), svg( 'circle', { r: 4.5, class: 'g-fill' } ) ],
	cart: () => [ P( 'M-11 -6 L11 -6 L8 5 L-8 5 Z', 'g-fill' ), svg( 'circle', { cx: -5, cy: 9, r: 3, class: 'g-fill' } ), svg( 'circle', { cx: 5, cy: 9, r: 3, class: 'g-fill' } ) ],
	cp: () => [ svg( 'circle', { cx: -8, r: 4.5, class: 'g-fill' } ), svg( 'circle', { r: 4.5, class: 'g-stroke' } ), svg( 'circle', { cx: 8, r: 4.5, class: 'g-stroke' } ) ],
};

const MODE_ICON = { ctf: 'flag', koth: 'point', pl: 'cart', plr: 'cart', ad: 'cp', cp: 'cp' };

export function icon( name, cls = '' ) {
	const draw = MODE[ name ] || KIND[ name ];
	return svg( 'svg', { class: `glyph ${ cls }`, viewBox: '-14 -14 28 28', 'aria-hidden': 'true' }, draw ? draw() : null );
}

export const modeIcon = ( mode, cls ) => icon( MODE_ICON[ mode ] || 'cp', cls );
