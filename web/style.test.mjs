import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import test from 'node:test';

const require = createRequire(import.meta.url);
const cssTree = require('css-tree');
const {parse} = require('parse5');
const [css, html, login] = await Promise.all(['styles.css', 'index.html', 'login.html'].map(name =>
  readFile(new URL(`./static/${name}`, import.meta.url), 'utf8')));
const rules = new Map();
const errors = [];
const tree = cssTree.parse(css, {onParseError: error => errors.push(error.message)});
cssTree.walk(tree, {visit: 'Rule', enter(node) {
  if (node.prelude.type !== 'SelectorList') return;
  const values = new Map();
  node.block.children.forEach(item => {
    if (item.type === 'Declaration') values.set(item.property, cssTree.generate(item.value));
  });
  node.prelude.children.forEach(selector => {
    const name = cssTree.generate(selector);
    if (!rules.has(name)) rules.set(name, []);
    rules.get(name).push(values);
  });
}});

function elements(source) {
  const ids = new Map();
  const tags = [];
  const visit = node => {
    if (node.tagName) {
      const attrs = Object.fromEntries(node.attrs.map(attr => [attr.name, attr.value]));
      tags.push({tag: node.tagName, attrs});
      if (attrs.id) {
        assert.equal(ids.has(attrs.id), false, `Duplicate ID: ${attrs.id}`);
        ids.set(attrs.id, {tag: node.tagName, attrs});
      }
    }
    for (const child of node.childNodes || []) visit(child);
  };
  visit(parse(source));
  return {ids, tags};
}

const page = elements(html);
const signIn = elements(login);
const first = selector => {
  assert.ok(rules.has(selector), `Missing rule: ${selector}`);
  return rules.get(selector)[0];
};

test('CSS parses and uses the NSMC light theme tokens', () => {
  assert.deepEqual(errors, []);
  const root = first(':root');
  for (const [name, value] of Object.entries({'--bg':'#fff', '--fg':'#24292f', '--line':'#d0d7de', '--soft':'#f6f8fa', '--primary':'#1f883d'})) {
    assert.equal(root.get(name), value);
  }
  assert.equal(first('body').get('font-size'), '13px');
});

test('login retains authentication controls in the NSMC compact form', () => {
  assert.equal(signIn.ids.get('portal-login-form').attrs.class, 'login-card');
  assert.equal(signIn.ids.get('portal-login-form').attrs.action, '/login');
  assert.equal(signIn.ids.get('portal-login-form').attrs.method, 'post');
  for (const name of ['username', 'password']) {
    assert.equal(signIn.ids.get(`portal-${name}`).attrs.name, name);
    assert.equal(signIn.ids.get(`portal-${name}`).attrs.placeholder, name);
    assert.equal(signIn.ids.get(`portal-${name}`).attrs['aria-label'], name);
  }
  assert.equal(signIn.ids.get('portal-password').attrs.type, 'password');
  assert.equal(first('.login-card').get('width'), '280px');
  assert.equal(first('.login-card').get('max-width'), '100%');
  assert.equal(first('.login-card .form-error:empty').get('display'), 'none');
});

test('all portal pages and independent scientific visualizations remain present', () => {
  for (const id of ['calendar-view', 'sources-view', 'downloads-view', 'sync-view', 'monitor-view', 'cli-view', 'access-view', 'coverage-views', 'month-grid', 'coverage-matrix', 'satellite-timelines', 'era5-form']) {
    assert.ok(page.ids.has(id), `Missing view or visualization: ${id}`);
  }
  const calendarCard = first('#calendar-view>.calendar-card');
  assert.equal(calendarCard.get('width'), '100%');
  assert.equal(calendarCard.get('max-width'), 'none');
  assert.equal(calendarCard.get('margin-inline'), 'auto');
  const calendarView = first('#calendar-view');
  assert.equal(calendarView.get('width'), '100%');
  assert.equal(calendarView.get('max-width'), 'none');
  assert.equal(calendarView.get('font-size'), '14px');
  assert.equal(first('#calendar-view .product-title').get('font-size'), '13px');
  assert.equal(first('#calendar-view .legend').get('font-size'), '12px');
  assert.equal(first('.with-sidebar').get('--sidebar-width'), '172px');
  assert.equal(first('.sidebar-collapsed.with-sidebar').get('--sidebar-width'), '52px');
  assert.equal(first('.coverage-year .c').get('grid-template-columns'), 'repeat(4,1fr)');
  assert.equal(first('.coverage-year .c').get('grid-template-rows'), 'repeat(4,1fr)');
  assert.equal(first('.month-ring').get('width'), '68px');
  assert.equal(first('.month-ring').get('height'), '68px');
  assert.equal(first('.month-hours').get('grid-template-columns'), 'repeat(24,minmax(0,1fr))');
  assert.equal(first('.matrix-scroll').get('overflow'), 'auto');
});

test('monitor surfaces are flat and do not use decorative gradients or lift on hover', () => {
  for (const selector of ['.monitor-link-card', '.monitor-link-card:hover', '.monitor-link-card.selected', '.monitor-metric', '.monitor-detail', '.monitor-chart-card']) {
    const style = first(selector);
    assert.equal(style.has('transform'), false);
    assert.equal(style.has('box-shadow'), false);
    assert.doesNotMatch(style.get('background') || '', /gradient/);
    assert.ok(!style.has('border-radius') || Number.parseFloat(style.get('border-radius')) <= 8);
  }
});

test('sidebar footer keeps logout on one line in a 52px rail', () => {
  const button = first('.portal-account button');
  assert.equal(button.get('width'), '32px');
  assert.equal(button.get('height'), '28px');
  assert.equal(button.get('flex'), '0 0 32px');
  assert.equal(button.get('white-space'), 'nowrap');
  assert.equal(first('.sidebar-collapsed .sidebar').get('padding-left'), '8px');
  assert.equal(first('.sidebar-collapsed .portal-account').get('padding'), '8px 0');
  assert.ok(rules.get('.portal-account').some(style => style.get('padding') === '8px 0' && style.get('justify-content') === 'center'));
  assert.ok(rules.get('.sidebar').some(style => style.get('padding-top') === '6px' && style.get('padding-bottom') === '6px'));
  assert.ok(rules.get('.sidebar-status').some(style => style.get('display') === 'none'));
});

test('calendar uses full width and timeline slots cannot collapse into hairlines', () => {
  assert.equal(first('main.calendar-wide').get('width'), '100%');
  assert.equal(first('.band-slot.matrix-cell').get('min-width'), '3px');
  assert.equal(first('.band-slot.matrix-cell').get('max-width'), '5px');
  assert.equal(first('.band-slot.matrix-cell').get('justify-self'), 'center');
  assert.equal(first('.band-slot.matrix-cell').get('height'), '28px');
  assert.equal(first('.coverage-matrix.full-day').get('grid-auto-rows'), '40px');
  assert.equal(first('.matrix-scroll').get('max-height'), 'none');
});

test('login and portal load the same current stylesheet', () => {
  const href = tags => tags.find(item => item.tag === 'link' && item.attrs.rel === 'stylesheet').attrs.href;
  assert.equal(href(page.tags), href(signIn.tags));
});
