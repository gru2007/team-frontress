// Strings. In the game, the game's UI language decides; a browser preview can
// pick one in Settings.

import en from '../i18n/en.js';
import ru from '../i18n/ru.js';

const TABLES = { english: en, russian: ru };
let table = en;
let lang = 'english';

export const LANGUAGES = [ [ 'english', 'English' ], [ 'russian', 'Русский' ] ];

export function setLanguage( name ) {
	lang = TABLES[ name ] ? name : 'english';
	table = TABLES[ lang ];
	document.documentElement.lang = lang === 'russian' ? 'ru' : 'en';
}

export const language = () => lang;

// The game's language name ("english", "russian", ...), or the browser's, as
// one of ours. Anything we have no table for reads English.
export function guessLanguage( gameLanguage ) {
	if ( gameLanguage && TABLES[ gameLanguage ] )
		return gameLanguage;
	return ( navigator.language || '' ).toLowerCase().startsWith( 'ru' ) ? 'russian' : 'english';
}

export function t( key, vars ) {
	let s = table[ key ] ?? en[ key ] ?? key;
	if ( vars ) {
		s = s.replace( /\{(\w+)\}/g, ( m, k ) => ( vars[ k ] ?? m ) );
	}
	return s;
}

export const has = key => key in table || key in en;
