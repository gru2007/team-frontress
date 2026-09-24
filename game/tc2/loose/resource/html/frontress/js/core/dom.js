// A very small hyperscript. Views build real nodes and replace their subtree
// when state changes; the demo's screens are small enough that this is fast.

export function h( tag, props, ...children ) {
	const [ name, ...classes ] = tag.split( '.' );
	const el = name === 'svg' || SVG_TAGS.has( name )
		? document.createElementNS( 'http://www.w3.org/2000/svg', name )
		: document.createElement( name || 'div' );
	if ( classes.length )
		el.setAttribute( 'class', classes.join( ' ' ) );

	if ( props ) {
		for ( const [ k, v ] of Object.entries( props ) ) {
			if ( v == null || v === false )
				continue;
			if ( k === 'class' )
				el.setAttribute( 'class', ( el.getAttribute( 'class' ) ? el.getAttribute( 'class' ) + ' ' : '' ) + v );
			else if ( k === 'style' && typeof v === 'object' )
				for ( const [ sk, sv ] of Object.entries( v ) ) el.style.setProperty( sk, sv );
			else if ( k.startsWith( 'on' ) && typeof v === 'function' )
				el.addEventListener( k.slice( 2 ).toLowerCase(), v );
			else if ( k === 'html' )
				el.innerHTML = v;
			else
				el.setAttribute( k, v === true ? '' : v );
		}
	}

	append( el, children );
	return el;
}

function append( el, children ) {
	for ( const c of children ) {
		if ( c == null || c === false )
			continue;
		if ( Array.isArray( c ) )
			append( el, c );
		else
			el.appendChild( c instanceof Node ? c : document.createTextNode( String( c ) ) );
	}
}

const SVG_TAGS = new Set( [
	'g', 'path', 'circle', 'rect', 'line', 'polyline', 'polygon', 'text', 'tspan', 'defs', 'pattern',
	'linearGradient', 'radialGradient', 'stop', 'clipPath', 'mask', 'use', 'filter', 'feTurbulence',
	'feColorMatrix', 'feComposite', 'feGaussianBlur', 'feBlend', 'feDisplacementMap', 'marker', 'symbol',
	'animate', 'animateTransform', 'ellipse',
] );

export const svg = ( tag, props, ...children ) => h( tag, props, ...children );

export function mount( root, node ) {
	root.replaceChildren( node );
	return node;
}

export const $ = ( sel, root = document ) => root.querySelector( sel );
