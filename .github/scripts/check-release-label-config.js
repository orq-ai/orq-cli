#!/usr/bin/env node

// This checker deliberately parses only the small, stable configuration shapes
// it owns. It reads workflow/configuration text and never evaluates it.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const RESERVED_LABEL_PREFIX = 'release:';

function parseMap(workflowSource) {
  const mapStart = workflowSource.indexOf('const MAP = {');
  if (mapStart === -1) {
    throw new Error('label workflow does not define const MAP = { ... };');
  }

  const mapEnd = workflowSource.indexOf('};', mapStart);
  if (mapEnd === -1) {
    throw new Error('label workflow MAP has no closing };');
  }

  const map = {};
  const entries = workflowSource.slice(mapStart, mapEnd);
  for (const match of entries.matchAll(/([A-Za-z][A-Za-z0-9_-]*)\s*:\s*['"]([^'"]+)['"]/g)) {
    const [, type, label] = match;
    if (Object.hasOwn(map, type)) {
      throw new Error(`label workflow maps type "${type}" more than once`);
    }
    map[type] = label;
  }

  if (Object.keys(map).length === 0) {
    throw new Error('label workflow MAP contains no type-to-label entries');
  }
  return map;
}

function unquote(value) {
  const trimmed = value.trim();
  if ((trimmed.startsWith("'") && trimmed.endsWith("'")) ||
      (trimmed.startsWith('"') && trimmed.endsWith('"'))) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

function parseReleaseCategories(releaseSource) {
  const categories = new Map();
  let inCategories = false;
  let currentTitle;
  let inLabels = false;

  for (const line of releaseSource.split(/\r?\n/)) {
    if (!inCategories) {
      if (/^  categories:\s*$/.test(line)) inCategories = true;
      continue;
    }

    const title = line.match(/^    - title:\s*(.+?)\s*$/);
    if (title) {
      currentTitle = unquote(title[1]);
      inLabels = false;
      continue;
    }
    if (/^      labels:\s*$/.test(line)) {
      if (!currentTitle) {
        throw new Error('release category labels appear before a category title');
      }
      inLabels = true;
      continue;
    }
    if (inLabels) {
      const label = line.match(/^        -\s+(.+?)\s*$/);
      if (label) {
        const name = unquote(label[1]);
        const owners = categories.get(name) || [];
        owners.push(currentTitle);
        categories.set(name, owners);
        continue;
      }
      if (/^    - /.test(line) || (/^  \S/.test(line) && !/^  categories:/.test(line))) {
        inLabels = false;
      }
    }
  }

  if (categories.size === 0) {
    throw new Error('release configuration contains no category labels');
  }
  return categories;
}

function verifyMappingCategories(map, categories) {
  const owned = new Set(Object.values(map));

  for (const label of owned) {
    if (!label.startsWith(RESERVED_LABEL_PREFIX)) {
      throw new Error(`mapped label "${label}" is outside the ${RESERVED_LABEL_PREFIX} namespace`);
    }
    const owners = categories.get(label) || [];
    if (owners.length === 0) {
      throw new Error(`mapped label "${label}" is missing from release categories`);
    }
    if (owners.length > 1) {
      throw new Error(`mapped label "${label}" appears in multiple release categories: ${owners.join(', ')}`);
    }
  }

  for (const [label, owners] of categories) {
    if (label.startsWith(RESERVED_LABEL_PREFIX) && !owned.has(label)) {
      throw new Error(`release category label "${label}" is not mapped by the label workflow`);
    }
    if (label.startsWith(RESERVED_LABEL_PREFIX) && owners.length > 1) {
      throw new Error(`reserved label "${label}" appears in multiple release categories: ${owners.join(', ')}`);
    }
  }
}

// provisionLabel spreads `labelDetails[name]`, and spreading undefined is a
// silent no-op: a mapped label with no details entry is created with a random
// colour and no description. Pin the two tables to each other.
function verifyLabelDetails(map, scriptSource) {
  const start = scriptSource.indexOf('const labelDetails = {');
  if (start === -1) {
    throw new Error('label workflow does not define const labelDetails = { ... };');
  }
  const detailed = new Set();
  for (const match of scriptSource.slice(start).matchAll(/^\s*'([^']+)':\s*\{/gm)) {
    detailed.add(match[1]);
  }
  for (const label of new Set(Object.values(map))) {
    if (!detailed.has(label)) {
      throw new Error(`mapped label "${label}" has no labelDetails entry`);
    }
  }
  for (const label of detailed) {
    if (!Object.values(map).includes(label)) {
      throw new Error(`labelDetails defines "${label}", which no type maps to`);
    }
  }
}

function extractLabelScript(workflowSource) {
  const scriptStart = workflowSource.indexOf('          script: |\n');
  if (scriptStart === -1) {
    throw new Error('label workflow does not contain an indented github-script body');
  }
  const lines = workflowSource.slice(scriptStart + '          script: |\n'.length).split(/\r?\n/);
  const scriptLines = [];
  for (const line of lines) {
    if (line === '') {
      scriptLines.push(line);
    } else if (line.startsWith('            ')) {
      scriptLines.push(line.slice(12));
    } else {
      break;
    }
  }
  if (scriptLines.length === 0) {
    throw new Error('label workflow github-script body is empty');
  }
  return scriptLines.join('\n');
}

function requireWorkflowStatement(scriptSource, pattern, description) {
  const match = scriptSource.match(pattern);
  if (!match) {
    throw new Error(`label workflow ${description}`);
  }
  return match;
}

function findWorkflowBlock(scriptSource, headerPattern, description) {
  const header = requireWorkflowStatement(scriptSource, headerPattern, description);
  const openBrace = scriptSource.indexOf('{', header.index);
  let depth = 0;
  let quote;
  let inLineComment = false;
  let inBlockComment = false;

  for (let index = openBrace; index < scriptSource.length; index += 1) {
    const character = scriptSource[index];
    const nextCharacter = scriptSource[index + 1];

    if (inLineComment) {
      if (character === '\n') inLineComment = false;
      continue;
    }
    if (inBlockComment) {
      if (character === '*' && nextCharacter === '/') {
        inBlockComment = false;
        index += 1;
      }
      continue;
    }
    if (quote) {
      if (character === '\\') {
        index += 1;
      } else if (character === quote) {
        quote = undefined;
      }
      continue;
    }
    if (character === '/' && nextCharacter === '/') {
      inLineComment = true;
      index += 1;
      continue;
    }
    if (character === '/' && nextCharacter === '*') {
      inBlockComment = true;
      index += 1;
      continue;
    }
    if (character === "'" || character === '"' || character === '`') {
      quote = character;
      continue;
    }
    if (character === '{') {
      depth += 1;
    } else if (character === '}') {
      depth -= 1;
      if (depth === 0) {
        return { index: header.index, contents: scriptSource.slice(openBrace + 1, index) };
      }
    }
  }
  throw new Error(`label workflow ${description} has no closing block`);
}

function parsePrTitleTypes(prTitleSource) {
  const marker = '          types: |\n';
  const start = prTitleSource.indexOf(marker);
  if (start === -1) {
    throw new Error('pr-title workflow does not define an indented types: | list');
  }
  const types = [];
  for (const line of prTitleSource.slice(start + marker.length).split(/\r?\n/)) {
    const entry = line.match(/^ {12}(\S+)\s*$/);
    if (!entry) break;
    types.push(entry[1]);
  }
  if (types.length === 0) {
    throw new Error('pr-title workflow types: | list is empty');
  }
  return types;
}

// A type accepted by pr-title.yml but absent from MAP passes title validation
// and then silently lands in the "*" catch-all category of .github/release.yml.
function verifyPrTitleTypes(map, types) {
  const mapped = new Set(Object.keys(map));
  for (const type of types) {
    if (!mapped.has(type)) {
      throw new Error(`pr-title allows type "${type}", which the label workflow does not map`);
    }
  }
  for (const type of mapped) {
    if (!types.includes(type)) {
      throw new Error(`label workflow maps type "${type}", which pr-title does not allow`);
    }
  }
  const duplicate = types.find((type, index) => types.indexOf(type) !== index);
  if (duplicate) {
    throw new Error(`pr-title lists type "${duplicate}" more than once`);
  }
}

function parseWorkflowTransition(workflowSource) {
  const scriptSource = extractLabelScript(workflowSource);
  const map = parseMap(scriptSource);
  verifyLabelDetails(map, scriptSource);
  const titleMatch = requireWorkflowStatement(
    scriptSource,
    /const m = title\.match\(\/((?:\\.|[^/])*)\/\);/,
    'does not use the expected conventional-title parser',
  );
  if (titleMatch[1] !== String.raw`^(\w+)(\([^)]*\))?!?:`) {
    throw new Error('label workflow uses a different conventional-title parser');
  }
  const titlePattern = new RegExp(titleMatch[1]);

  requireWorkflowStatement(
    scriptSource,
    /const owned = new Set\(Object\.values\(MAP\)\);/,
    'does not derive its owned-label set from MAP',
  );
  requireWorkflowStatement(
    scriptSource,
    /const current = await github\.paginate\(\s*github\.rest\.issues\.listLabelsOnIssue,\s*issue,\s*\);/s,
    'does not fetch the current issue labels before transitioning them',
  );
  requireWorkflowStatement(
    scriptSource,
    /const label = m && MAP\[m\[1\]\.toLowerCase\(\)\];/,
    'does not derive the target label from the parsed title and MAP',
  );
  requireWorkflowStatement(
    scriptSource,
    /await github\.rest\.issues\.getLabel\(\{ \.\.\.repository, name \}\);/,
    'does not look up an existing label before creating it',
  );
  requireWorkflowStatement(
    scriptSource,
    /await github\.rest\.issues\.createLabel\(\{[^;]*\.\.\.labelDetails\[name\][^;]*\}\);/,
    'does not create a missing target label from its label details',
  );

  // Everything that applies the target label lives inside `if (label) {`, so an
  // unconventional or unmapped title cannot reach it.
  const mapped = findWorkflowBlock(
    scriptSource,
    /if \(label\) \{/,
    'does not gate label application on a mapped title',
  );
  const provision = mapped.contents.indexOf('await provisionLabel(label);');
  if (provision === -1) {
    throw new Error('label workflow does not provision the target label');
  }
  const add = mapped.contents.search(
    /await github\.rest\.issues\.addLabels\(\{ \.\.\.issue, labels: \[label\] \}\);/,
  );
  if (add === -1) {
    throw new Error('label workflow does not add the missing target label');
  }
  if (!/if \(current\.some\(\(currentLabel\) => currentLabel\.name === label\)\) \{/.test(mapped.contents)) {
    throw new Error('label workflow does not avoid re-adding an already-applied target label');
  }
  if (provision > add) {
    throw new Error('label workflow provisions the target label after applying it');
  }

  // Cleanup runs last and unconditionally. Add-before-remove means a run
  // cancelled mid-transition leaves a superset of the correct labels rather
  // than a pull request with no release label at all.
  const cleanup = requireWorkflowStatement(
    scriptSource,
    /for \(const stale of current\s*\.map\(\(currentLabel\) => currentLabel\.name\)\s*\.filter\(\(name\) => owned\.has\(name\) && name !== label\)\) \{/s,
    'does not remove exactly stale owned labels',
  );
  if (cleanup.index < mapped.index) {
    throw new Error('label workflow removes stale labels before applying the target label');
  }
  const cleanupBlock = findWorkflowBlock(
    scriptSource.slice(cleanup.index),
    /for \(const stale of current/,
    'stale-label cleanup has no block',
  );
  if (!/await github\.rest\.issues\.removeLabel\(\{ \.\.\.issue, name: stale \}\);/.test(cleanupBlock.contents)) {
    throw new Error('label workflow does not remove stale owned labels');
  }
  if (!/if \(removeError\.status !== 404\) throw removeError;/.test(cleanupBlock.contents)) {
    throw new Error('label workflow does not tolerate an already-removed stale label');
  }
  return { map, titlePattern };
}
// Models the workflow's transition: provision, add, then remove stale.
function applyWorkflowTransition({ transition, title, currentLabels, existingRepositoryLabels }) {
  const owned = new Set(Object.values(transition.map));
  const match = transition.titlePattern.exec(title);
  const target = match && transition.map[match[1].toLowerCase()];
  const operations = [];

  if (target) {
    if (!existingRepositoryLabels.has(target)) {
      operations.push(['provision', target]);
      existingRepositoryLabels.add(target);
    }
    if (!currentLabels.includes(target)) {
      operations.push(['add', target]);
    }
  }
  for (const name of currentLabels) {
    if (owned.has(name) && name !== target) {
      operations.push(['remove', name]);
    }
  }

  const labels = currentLabels.filter((name) => !owned.has(name) || name === target);
  if (target && !labels.includes(target)) labels.push(target);
  return { labels, operations };
}

function runFixtureChecks(transition) {
  assert.deepEqual(
    applyWorkflowTransition({
      transition,
      title: 'feat(cli): add release labels',
      currentLabels: ['bug', 'release:bug-fixes'],
      existingRepositoryLabels: new Set(Object.values(transition.map)),
    }),
    {
      labels: ['bug', 'release:features'],
      operations: [['add', 'release:features'], ['remove', 'release:bug-fixes']],
    },
    'a mapped title adds the new owned label before removing the stale one',
  );
  assert.deepEqual(
    applyWorkflowTransition({
      transition,
      title: 'fix: rename command',
      currentLabels: ['enhancement', 'release:features'],
      existingRepositoryLabels: new Set(Object.values(transition.map)),
    }).labels,
    ['enhancement', 'release:bug-fixes'],
    'retitling between mapped types removes the old owned label',
  );
  for (const title of ['rename command', 'unknown: rename command', 'Revert "feat: x"', 'feat : x']) {
    assert.deepEqual(
      applyWorkflowTransition({
        transition,
        title,
        currentLabels: ['documentation', 'release:features'],
        existingRepositoryLabels: new Set(Object.values(transition.map)),
      }).labels,
      ['documentation'],
      `${title} removes all owned labels and preserves unrelated labels`,
    );
  }
  for (const title of ['feat!: x', 'feat(cli)!: x', 'FEAT: x']) {
    assert.deepEqual(
      applyWorkflowTransition({
        transition,
        title,
        currentLabels: [],
        existingRepositoryLabels: new Set(Object.values(transition.map)),
      }).labels,
      ['release:features'],
      `${title} maps to the feature label`,
    );
  }
  assert.deepEqual(
    applyWorkflowTransition({
      transition,
      title: 'feat: add release labels',
      currentLabels: ['release:features'],
      existingRepositoryLabels: new Set(Object.values(transition.map)),
    }).operations,
    [],
    'an already-applied owned label is left alone',
  );
  assert.deepEqual(
    applyWorkflowTransition({
      transition,
      title: 'feat: add release labels',
      currentLabels: [],
      existingRepositoryLabels: new Set(),
    }).operations,
    [['provision', 'release:features'], ['add', 'release:features']],
    'a missing owned label is provisioned before use',
  );
}

function runMutationHarness(workflowSource) {
  const mutations = [
    ['conventional-title parser', (source) => source.replace(
      'const m = title.match', 'const match = title.match',
    )],
    ['owned-label cleanup predicate', (source) => source.replace(
      'owned.has(name) && name !== label', 'owned.has(name)',
    )],
    ['mapped-title gate', (source) => source.replace('if (label) {', 'if (true) {')],
    ['already-removed tolerance', (source) => source.replace(
      'if (removeError.status !== 404) throw removeError;', 'core.info(String(removeError));',
    )],
    ['label lookup before creation', (source) => source.replace(
      'await github.rest.issues.getLabel({ ...repository, name });', 'return;',
    )],
    ['labelDetails coverage', (source) => source.replace(
      "              'release:documentation': {\n                color: '0075CA',\n                description: 'Automated release-notes label for documentation.',\n              },\n", '',
    )],
    ['target-label provisioning', (source) => source.replace(
      'await provisionLabel(label);', 'await provisionLabel();',
    )],
    ['target-label addition', (source) => source.replace(
      'await github.rest.issues.addLabels({ ...issue, labels: [label] });',
      'await github.rest.issues.setLabels({ ...issue, labels: [label] });',
    )],
    ['add-before-remove ordering', (source) => {
      const marker = '            // Unconditional: this is what clears';
      const anchor = '            // Add before removing.';
      const start = source.indexOf(marker);
      if (start === -1) throw new Error('mutation harness could not find the cleanup loop');
      return source.slice(0, start).replace(anchor, source.slice(start) + anchor);
    }],
  ];

  for (const [description, mutate] of mutations) {
    assert.throws(
      () => parseWorkflowTransition(mutate(workflowSource)),
      Error,
      `${description} mutation is accepted`,
    );
  }
}

function readSources() {
  const repositoryRoot = path.resolve(__dirname, '..', '..');
  const read = (relativePath) => fs.readFileSync(path.join(repositoryRoot, relativePath), 'utf8');
  return {
    workflowSource: read('.github/workflows/label-pr.yml'),
    releaseSource: read('.github/release.yml'),
    prTitleSource: read('.github/workflows/pr-title.yml'),
  };
}

function main() {
  const { workflowSource, releaseSource, prTitleSource } = readSources();
  const transition = parseWorkflowTransition(workflowSource);

  verifyMappingCategories(transition.map, parseReleaseCategories(releaseSource));
  verifyPrTitleTypes(transition.map, parsePrTitleTypes(prTitleSource));
  runFixtureChecks(transition);
  console.log('Release-label configuration verified.');
}

if (require.main === module) {
  try {
    if (process.argv.includes('--mutation-test')) {
      runMutationHarness(readSources().workflowSource);
      console.log('Workflow transition mutations are rejected.');
    } else {
      main();
    }
  } catch (error) {
    console.error(`Release-label configuration check failed: ${error.message}`);
    process.exitCode = 1;
  }
}

module.exports = {
  applyWorkflowTransition,
  extractLabelScript,
  parseMap,
  parsePrTitleTypes,
  parseReleaseCategories,
  parseWorkflowTransition,
  runMutationHarness,
  runFixtureChecks,
  verifyLabelDetails,
  verifyMappingCategories,
  verifyPrTitleTypes,
};
