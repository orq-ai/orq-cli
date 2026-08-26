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

function requireWorkflowReturnBlock(scriptSource, headerPattern, description) {
  const block = findWorkflowBlock(scriptSource, headerPattern, description);
  if (!/\breturn\s*;/.test(block.contents)) {
    throw new Error(`label workflow ${description}`);
  }
  return block;
}

function parseWorkflowTransition(workflowSource) {
  const scriptSource = extractLabelScript(workflowSource);
  const map = parseMap(scriptSource);
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
  const cleanup = requireWorkflowStatement(
    scriptSource,
    /for \(const stale of current\s*\.map\(\(currentLabel\) => currentLabel\.name\)\s*\.filter\(\(name\) => owned\.has\(name\) && name !== label\)\) \{\s*await github\.rest\.issues\.removeLabel\(\{ \.\.\.issue, name: stale \}\);/s,
    'does not remove exactly stale owned labels',
  );
  const invalid = requireWorkflowReturnBlock(
    scriptSource,
    /if \(!m\) \{/,
    'does not return after cleaning up an invalid title',
  );
  const unmapped = requireWorkflowReturnBlock(
    scriptSource,
    /if \(!label\) \{/,
    'does not return after cleaning up an unmapped title',
  );
  const provision = requireWorkflowStatement(
    scriptSource,
    /await provisionLabel\(label\);/,
    'does not provision the target label',
  );
  requireWorkflowStatement(
    scriptSource,
    /await github\.rest\.issues\.createLabel\(\{[\s\S]*?name,[\s\S]*?\.\.\.labelDetails\[name\],[\s\S]*?\}\);/,
    'does not create a missing target label from its label details',
  );
  const alreadyApplied = requireWorkflowStatement(
    scriptSource,
    /if \(current\.some\(\(currentLabel\) => currentLabel\.name === label\)\) \{[\s\S]*?return;\s*\}/,
    'does not avoid adding an already-applied target label',
  );
  const add = requireWorkflowStatement(
    scriptSource,
    /await github\.rest\.issues\.addLabels\(\{ \.\.\.issue, labels: \[label\] \}\);/,
    'does not add the missing target label',
  );

  if (!(cleanup.index < invalid.index && invalid.index < unmapped.index &&
        unmapped.index < provision.index && provision.index < alreadyApplied.index &&
        alreadyApplied.index < add.index)) {
    throw new Error('performs release-label transition operations in the wrong order');
  }
  return { map, titlePattern };
}

function applyWorkflowTransition({ transition, title, currentLabels, existingRepositoryLabels }) {
  const owned = new Set(Object.values(transition.map));
  const match = transition.titlePattern.exec(title);
  const target = match && transition.map[match[1].toLowerCase()];
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
      operations: [['remove', 'release:bug-fixes'], ['add', 'release:features']],
    },
    'a mapped title leaves one owned label and preserves unrelated labels',
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
  for (const title of ['rename command', 'unknown: rename command']) {
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

function main() {
  const repositoryRoot = path.resolve(__dirname, '..', '..');
  const workflowSource = fs.readFileSync(path.join(repositoryRoot, '.github/workflows/label-pr.yml'), 'utf8');
  const releaseSource = fs.readFileSync(path.join(repositoryRoot, '.github/release.yml'), 'utf8');
  const transition = parseWorkflowTransition(workflowSource);

  verifyMappingCategories(transition.map, parseReleaseCategories(releaseSource));
  runFixtureChecks(transition);
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
  applyWorkflowTransition,
  extractLabelScript,
  parseMap,
  parseReleaseCategories,
  parseWorkflowTransition,
  runFixtureChecks,
  verifyMappingCategories,
};
