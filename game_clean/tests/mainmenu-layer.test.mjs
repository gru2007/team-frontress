import { readFileSync } from 'node:fs';
import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import vm from 'node:vm';

const source = readFileSync(new URL('../../game/tc2/loose/resource/html/_astro/mainmenu-layer.js', import.meta.url), 'utf8')
    .replace(/^import .*;$/m, '');

function menu() {
    const settings = { display: 'none' }, page = { display: 'none' };
    const calls = [], events = {};
    let observe;
    vm.runInNewContext(source, {
        document: { querySelectorAll: () => [settings, page] },
        getComputedStyle: panel => panel,
        rpc: (...args) => calls.push(args),
        onEngineEvent: (name, callback) => { events[name] = callback; },
        MutationObserver: class {
            constructor(callback) { observe = callback; }
            observe() {}
        },
    });
    return { settings, page, calls, events, update: () => observe() };
}

test('settings raises the native layer until its closing animation finishes', () => {
    const m = menu();
    assert.deepEqual(m.calls.pop(), ['uicmd', 'close_interactive_window']);
    m.settings.display = 'block'; m.update();
    assert.deepEqual(m.calls.pop(), ['uicmd', 'open_interactive_window']);
    m.update(); // closing class added; still displayed during the animation
    assert.equal(m.calls.length, 0);
    m.settings.display = 'none'; m.update();
    assert.deepEqual(m.calls.pop(), ['uicmd', 'close_interactive_window']);
});

test('nested page/modal transitions do not restore the layer too early', () => {
    const m = menu(); m.calls.length = 0;
    m.page.display = 'block'; m.update();
    m.settings.display = 'block'; m.update();
    m.page.display = 'none'; m.update();
    assert.deepEqual(m.calls, [['uicmd', 'open_interactive_window']]);
    m.settings.display = 'none'; m.update();
    assert.deepEqual(m.calls.at(-1), ['uicmd', 'close_interactive_window']);
});

test('menu activation resynchronizes native state after pause or reload', () => {
    const m = menu();
    m.settings.display = 'block'; m.update(); m.calls.length = 0;
    m.events.openedmenu();
    assert.deepEqual(m.calls, [['uicmd', 'open_interactive_window']]);
    m.settings.display = 'none'; m.update(); m.calls.length = 0;
    m.events.openedmenu();
    assert.deepEqual(m.calls, [['uicmd', 'close_interactive_window']]);
});
