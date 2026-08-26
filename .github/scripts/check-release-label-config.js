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
  for (const match of entries.matchAll(/([A-Za-z][A-Za-z0-9_-]*)\s*:\s*'([^']+)'/g)) {
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

function mappedLabelForTitle(title, map) {
  const match = title.match(/^(\w+)(\([^)]*\))?!?:/);
  return match && map[match[1].toLowerCase()];
}

function transitionLabels({ map, title, currentLabels, existingRepositoryLabels }) {
  const owned = new Set(Object.values(map));
  const target = mappedLabelForTitle(title, map);
  const labels = [];
  const operations = [];

  for (const name of currentLabels) {
    if (owned.has(name) && name !== target) {
      operations.push(['remove', name]);
    } else {
      labels.push(name);
    }
  }

  if (!target) return { labels, operations };

  if (!existingRepositoryLabels.has(target)) {
    operations.push(['provision', target]);
    existingRepositoryLabels.add(target);
  }
  if (!labels.includes(target)) {
    operations.push(['add', target]);
    labels.push(target);
  }
  return { labels, operations };
}

function verifyWorkflowTransitionGuards(workflowSource) {
  const owned = workflowSource.indexOf('const owned = new Set(Object.values(MAP));');
  const current = workflowSource.indexOf('github.paginate(');
  const cleanup = workflowSource.indexOf('await github.rest.issues.removeLabel');
  const invalid = workflowSource.indexOf('if (!m)');
  const unmapped = workflowSource.indexOf('if (!label)');
  const provision = workflowSource.indexOf('await provisionLabel(label);');
  const add = workflowSource.indexOf('await github.rest.issues.addLabels');

  if (owned === -1 || current === -1 || cleanup === -1 || invalid === -1 || unmapped === -1 || provision === -1 || add === -1) {
    throw new Error('label workflow is missing a required owned-label transition guard');
  }
  if (!/\.filter\(\(name\)\s*=>\s*owned\.has\(name\)\s*&&\s*name\s*!==\s*label\)/.test(workflowSource)) {
    throw new Error('label workflow cleanup does not remove only stale owned labels');
  }
  if (!workflowSource.includes('github.rest.issues.createLabel')) {
    throw new Error('label workflow does not provision missing release labels');
  }
  if (!(owned < current && current < cleanup && cleanup < invalid && cleanup < unmapped && provision < add)) {
    throw new Error('label workflow transition operations are ordered incorrectly');
  }
}

function runFixtureChecks() {
  const map = {
    feat: 'release:features',
    fix: 'release:bug-fixes',
  };

  assert.deepEqual(
    transitionLabels({
      map,
      title: 'feat(cli): add release labels',
      currentLabels: ['bug', 'release:bug-fixes'],
      existingRepositoryLabels: new Set(Object.values(map)),
    }),
    {
      labels: ['bug', 'release:features'],
      operations: [['remove', 'release:bug-fixes'], ['add', 'release:features']],
    },
    'a mapped title leaves one owned label and preserves unrelated labels',
  );
  assert.deepEqual(
    transitionLabels({
      map,
      title: 'fix: rename command',
      currentLabels: ['enhancement', 'release:features'],
      existingRepositoryLabels: new Set(Object.values(map)),
    }).labels,
    ['enhancement', 'release:bug-fixes'],
    'retitling between mapped types removes the old owned label',
  );
  for (const title of ['rename command', 'unknown: rename command']) {
    assert.deepEqual(
      transitionLabels({
        map,
        title,
        currentLabels: ['documentation', 'release:features'],
        existingRepositoryLabels: new Set(Object.values(map)),
      }).labels,
      ['documentation'],
      `${title} removes all owned labels and preserves unrelated labels`,
    );
  }
  assert.deepEqual(
    transitionLabels({
      map,
      title: 'feat: add release labels',
      currentLabels: [],
      existingRepositoryLabels: new Set(),
    }).operations,
    [['provision', 'release:features'], ['add', 'release:features']],
    'a missing owned label is provisioned before use',
  );
}

function main() {
  const repositoryRoot = path.resolve(__dirname, '..', '..');
  const workflowSource = fs.readFileSync(path.join(repositoryRoot, '.github/workflows/label-pr.yml'), 'utf8');
  const releaseSource = fs.readFileSync(path.join(repositoryRoot, '.github/release.yml'), 'utf8');
  const map = parseMap(workflowSource);

  verifyMappingCategories(map, parseReleaseCategories(releaseSource));
  verifyWorkflowTransitionGuards(workflowSource);
  runFixtureChecks();
  console.log('Release-label configuration verified.');
}

if (require.main === module) {
  try {
    main();
  } catch (error) {
    console.error(`Release-label configuration check failed: ${error.message}`);
    process.exitCode = 1;
  }
}

module.exports = {
  mappedLabelForTitle,
  parseMap,
  parseReleaseCategories,
  runFixtureChecks,
  transitionLabels,
  verifyMappingCategories,
  verifyWorkflowTransitionGuards,
};
