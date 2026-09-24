// The war journal: the campaign's own history, newest day first.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import { logVars } from '../ui/names.js';

export function renderJournal( ctx ) {
	const { state: s, act } = ctx;

	const days = new Map();
	for ( const e of [ ...s.log ].reverse() ) {
		if ( !days.has( e.day ) ) days.set( e.day, [] );
		days.get( e.day ).push( e );
	}

	return h( 'div.page.journal-page', { 'data-scroll': 'journal' },
		h( 'header.page-head', null,
			h( 'h1', null, t( 'journal.title' ) ),
			h( 'p', null, t( 'journal.sub' ) ) ),
		days.size === 0 ? h( 'p.empty', null, t( 'journal.empty' ) ) : null,
		h( 'div.timeline', null,
			[ ...days ].map( ( [ day, entries ] ) => h( 'section.day', null,
				h( 'div.day-marker', null, t( 'journal.day', { day } ) ),
				h( 'ol', null, entries.map( e => h( 'li', {
					class: `entry tone-${ e.tone } ${ e.node ? 'hoverable' : '' }`,
					onclick: e.node ? () => { act.tab( 'map' ); act.select( e.node ); } : null,
				},
					h( 'span.entry-dot' ),
					h( 'span.entry-text', null, t( e.key, logVars( s, e ) ) ),
				) ) ),
			) ),
		),
	);
}
