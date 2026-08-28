#!/usr/bin/env node

// Four lists across three files have to agree: the types pr-title.yml accepts,
// the MAP in label-pr.yml, that workflow's labelDetails table, and the
// categories in release.yml. Drift between them is silent, so this checks it.
//
// It reads those files as text and never evaluates them. What it does NOT do is
// assert on how label-pr.yml's script is written: renaming a variable there is
// not a regression, and a check that fails on renames while passing on logic
// bugs is worse than no check.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const RESERVED_LABEL_PREFIX = 'release:';
const CATCH_ALL_LABEL = '*';

function sliceObjectLiteral(source, declaration) {
  const start = source.indexOf(declaration);
  if (start === -1) {
    throw new Error(`label workflow does not define ${declaration} ... };`);
  }
  const end = source.indexOf('\n            };', start);
  if (end === -1) {
    throw new Error(`label workflow ${declaration} has no closing };`);
  }
  return source.slice(start, end);
}

function parseMap(workflowSource) {
  const map = {};
  const entries = sliceObjectLiteral(workflowSource, 'const MAP = {');
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

function parseTitlePattern(workflowSource) {
  const match = workflowSource.match(/title\.match\(\/((?:\\.|[^/])*)\/\)/);
  if (!match) {
    throw new Error('label workflow does not parse the title with a literal regex');
  }
  return new RegExp(match[1]);
}

// Mirrors the two lines in label-pr.yml that turn a title into a label. Both the
// pattern and the map are read out of the workflow, so the fixtures below
// exercise the real ones.
function labelForTitle({ map, titlePattern }, title) {
  const match = titlePattern.exec(title);
  const type = match && match[1].toLowerCase();
  return type && Object.hasOwn(map, type) ? map[type] : undefined;
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
    // A key at column 0 or 2 ends the categories block.
    if (/^ {0,2}\S/.test(line) && !/^  categories:/.test(line)) {
      inCategories = false;
      inLabels = false;
      continue;
    }

    const title = line.match(/^ {4}- title:\s*(.+?)\s*$/);
    if (title) {
      currentTitle = unquote(title[1]);
      inLabels = false;
      continue;
    }
    if (/^ {6}labels:\s*$/.test(line)) {
      if (!currentTitle) {
        throw new Error('release category labels appear before a category title');
      }
      inLabels = true;
      continue;
    }
    if (inLabels) {
      const label = line.match(/^ {8}-\s+(.+?)\s*$/);
      if (label) {
        const name = unquote(label[1]);
        const owners = categories.get(name) || [];
        owners.push(currentTitle);
        categories.set(name, owners);
        continue;
      }
      if (/^ {4}- /.test(line)) inLabels = false;
    }
  }

  if (categories.size === 0) {
    throw new Error('release configuration contains no category labels (labels must be a block sequence indented by eight spaces)');
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

  // The label workflow deliberately leaves unconventional and unmapped titles
  // unlabelled. Once `categories:` exists, GitHub drops any pull request that
  // matches no category from the generated notes, so without the catch-all
  // those pull requests disappear from the release body altogether.
  const catchAll = categories.get(CATCH_ALL_LABEL) || [];
  if (catchAll.length === 0) {
    throw new Error('release configuration has no "*" catch-all category; unlabeled pull requests would be dropped from the generated notes');
  }
  if (catchAll.length > 1) {
    throw new Error(`the "*" catch-all appears in multiple release categories: ${catchAll.join(', ')}`);
  }
}

// provisionLabel spreads `labelDetails[name]`, and spreading undefined is a
// silent no-op: a mapped label with no details entry, or an entry GitHub
// rejects, is created with a colour GitHub picks and no description.
function verifyLabelDetails(map, workflowSource) {
  const table = sliceObjectLiteral(workflowSource, 'const labelDetails = {');
  const detailed = new Map();
  for (const match of table.matchAll(/^\s*'([^']+)':\s*\{([^}]*)\}/gm)) {
    detailed.set(match[1], match[2]);
  }
  for (const label of new Set(Object.values(map))) {
    const body = detailed.get(label);
    if (body === undefined) {
      throw new Error(`mapped label "${label}" has no labelDetails entry`);
    }
    const color = body.match(/\bcolor:\s*'([^']*)'/);
    if (!color || !/^[0-9a-fA-F]{6}$/.test(color[1])) {
      throw new Error(`labelDetails entry for "${label}" has no six-digit hex color`);
    }
    const description = body.match(/\bdescription:\s*'([^']*)'/);
    if (!description || description[1].length === 0) {
      throw new Error(`labelDetails entry for "${label}" has no description`);
    }
    if (description[1].length > 100) {
      throw new Error(`labelDetails description for "${label}" exceeds the 100-character API limit`);
    }
  }
  for (const label of detailed.keys()) {
    if (!Object.values(map).includes(label)) {
      throw new Error(`labelDetails defines "${label}", which no type maps to`);
    }
  }
}

function parsePrTitleTypes(prTitleSource) {
  const marker = '          types: |\n';
  const start = prTitleSource.indexOf(marker);
  if (start === -1) {
    throw new Error('pr-title workflow does not define an indented types: | list');
  }
  const types = [];
  for (const line of prTitleSource.slice(start + marker.length).split(/\r?\n/)) {
    // A blank line stays inside the block scalar; only a dedent ends it.
    if (line.trim() === '') continue;
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

// Wiring that carries the behaviour but is not expressible as a list: without
// `edited` a retitle never relabels, without `issues: write` the first ever run
// fails to provision, and an unpinned action gets a write token on
// pull_request_target.
function verifyWorkflowWiring(workflowSource) {
  const triggers = workflowSource.match(/^ {4}types: \[([^\]]*)\]$/m);
  if (!triggers) {
    throw new Error('label workflow does not declare pull_request_target types');
  }
  const declared = triggers[1].split(',').map((entry) => entry.trim());
  for (const required of ['opened', 'edited', 'reopened']) {
    if (!declared.includes(required)) {
      throw new Error(`label workflow does not run on "${required}"`);
    }
  }
  for (const scope of ['issues: write', 'pull-requests: write']) {
    if (!new RegExp(`^ {2}${scope}$`, 'm').test(workflowSource)) {
      throw new Error(`label workflow does not grant ${scope}`);
    }
  }
  for (const uses of workflowSource.matchAll(/^\s*- uses: (\S+)/gm)) {
    if (!/@[0-9a-f]{40}$/.test(uses[1])) {
      throw new Error(`label workflow action "${uses[1]}" is not pinned to a commit SHA`);
    }
  }
}

function runTitleFixtures(transition) {
  const mapped = {
    'feat(cli): add release labels': 'release:features',
    'feat!: x': 'release:features',
    'feat(cli)!: x': 'release:features',
    'FEAT: x': 'release:features',
    'fix: rename command': 'release:bug-fixes',
    'revert: bad idea': 'release:bug-fixes',
    'docs: explain labels': 'release:documentation',
    'ci: pin an action': 'release:maintenance',
  };
  for (const [title, expected] of Object.entries(mapped)) {
    assert.equal(labelForTitle(transition, title), expected, `${title} maps to ${expected}`);
  }
  const unlabelled = [
    'rename command',
    'unknown: rename command',
    'Revert "feat: x"',
    'feat : x',
    'feat_x: y',
    '123: x',
    // Object.prototype members: a bare lookup returns a truthy non-label.
    'constructor: x',
    'toString: x',
    'hasOwnProperty: x',
  ];
  for (const title of unlabelled) {
    assert.equal(labelForTitle(transition, title), undefined, `${title} maps to no label`);
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
  const transition = { map: parseMap(workflowSource), titlePattern: parseTitlePattern(workflowSource) };

  verifyWorkflowWiring(workflowSource);
  verifyLabelDetails(transition.map, workflowSource);
  verifyMappingCategories(transition.map, parseReleaseCategories(releaseSource));
  verifyPrTitleTypes(transition.map, parsePrTitleTypes(prTitleSource));
  runTitleFixtures(transition);
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
