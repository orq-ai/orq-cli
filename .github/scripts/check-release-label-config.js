#!/usr/bin/env node

// Five lists across four files have to agree: the types pr-title.yml accepts,
// MAP and labelDetails in label-pr.js, the categories in release.yml, and the
// table contributors actually read in AGENTS.md. Drift between them is silent,
// so this checks it.
//
// MAP and labelDetails are imported, not parsed. The three files with no JS to
// import are read as text and never evaluated. What this does NOT do is assert
// on how label-pr.js is written: renaming a variable there is not a regression,
// and a check that fails on renames while passing on logic bugs is worse than
// no check. The behaviour of that module is covered by label-pr.test.js.
//
// One accepted loss versus the old text parser: a duplicate type key in MAP is
// now last-wins rather than an error. verifyPrTitleTypes still catches a lost
// type, just not a duplicated one.
const fs = require('node:fs');
const path = require('node:path');

const { MAP, labelDetails } = require('./label-pr.js');

const RESERVED_LABEL_PREFIX = 'release:';
const CATCH_ALL_LABEL = '*';

// YAML comment lines, dropped before the security greps below. Those files
// document the invariants they are being checked against, and a check a comment
// can trip is a check people route around.
function stripComments(source) {
  return source.replace(/^[^\S\r\n]*#.*$/gm, '');
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
function verifyLabelDetails(map, details) {
  if (Object.keys(map).length === 0) {
    throw new Error('label module MAP contains no type-to-label entries');
  }
  for (const label of new Set(Object.values(map))) {
    const entry = details[label];
    if (!entry) {
      throw new Error(`mapped label "${label}" has no labelDetails entry`);
    }
    if (!/^[0-9a-fA-F]{6}$/.test(entry.color || '')) {
      throw new Error(`labelDetails entry for "${label}" has no six-digit hex color`);
    }
    if (!entry.description) {
      throw new Error(`labelDetails entry for "${label}" has no description`);
    }
    if (entry.description.length > 100) {
      throw new Error(`labelDetails description for "${label}" exceeds the 100-character API limit`);
    }
  }
  for (const label of Object.keys(details)) {
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
// pull_request_target. Also covers the security invariants of both
// pull_request_target workflows — the trigger itself, and that neither job ever
// pulls pull request code into its privileged context.
function verifyWorkflowWiring(workflowSource, prTitleSource, repositoryRoot) {
  // Pin the trigger. Every other assertion here is about what the privileged job
  // does; none of them notice the job ceasing to be privileged-but-safe. On
  // `pull_request` the checkout default is the pull request's merge ref, so
  // flipping the trigger runs fork-authored code under this job's write token
  // while passing every ref:/persist-credentials check below.
  if (!/^ {2}pull_request_target:$/m.test(workflowSource)) {
    throw new Error('label workflow is not triggered by pull_request_target; a pull_request trigger would check out pull request code into a job holding a write token');
  }

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

  // The checkout exists only to read label-pr.js. A `ref:` on it would check out
  // the pull request's own code into a job holding a write token, which is the
  // whole hazard of pull_request_target. The key is matched with optional quotes
  // because YAML permits `"ref":` and `'ref':`, which an unquoted-only pattern
  // would wave through, and against comment-stripped source because the workflow
  // documents this very invariant in prose.
  const workflowSteps = stripComments(workflowSource);
  if (/^\s*["']?ref["']?\s*:/m.test(workflowSteps)) {
    throw new Error('label workflow checkout declares a ref:; on pull_request_target that runs pull request code with a write token');
  }
  // Same key, written as a brace-flow mapping (`with: {ref: ...}`), which is not
  // line-anchored and so slips past the check above.
  if (/\bwith\s*:\s*\{[^}]*\bref\s*:/m.test(workflowSteps)) {
    throw new Error('label workflow checkout declares a ref: in a flow mapping (with: {ref: ...}); on pull_request_target that runs pull request code with a write token');
  }
  if (!/^\s*persist-credentials: false$/m.test(workflowSource)) {
    throw new Error('label workflow checkout does not set persist-credentials: false');
  }
  // The load-bearing link between the workflow and the module it runs. Assert
  // the path resolves on disk, not just that the call is spelled right: the
  // failure this prevents is a rename that leaves the workflow pointing at a
  // file that is no longer there, which only shows up on a live pull request.
  const required = workflowSource.match(/require\('\.\/([^']+)'\)/);
  if (!required) {
    throw new Error("label workflow does not require() a label module from the repository checkout");
  }
  if (!fs.existsSync(path.join(repositoryRoot, required[1]))) {
    throw new Error(`label workflow requires "./${required[1]}", which does not exist`);
  }

  // pr-title.yml is the same pull_request_target hazard with none of the
  // mitigations to check: it reads PR metadata only, and its "do not add a
  // checkout" comment was the sole thing holding that. Assert it instead.
  if (!/^ {2}pull_request_target:$/m.test(prTitleSource)) {
    throw new Error('pr-title workflow is not triggered by pull_request_target; forked pull requests would then run a workflow definition taken from the pull request itself');
  }
  // Comment lines dropped first: this file's own prose names the action, and a
  // check that a comment can trip is a check people work around.
  const prTitleSteps = stripComments(prTitleSource);
  if (/actions\/checkout/.test(prTitleSteps)) {
    throw new Error('pr-title workflow checks out a repository; it reads pull request metadata only, and a checkout on pull_request_target puts pull request code inside the job');
  }
  if (/^\s*["']?ref["']?\s*:/m.test(prTitleSteps) || /\bwith\s*:\s*\{[^}]*\bref\s*:/m.test(prTitleSteps)) {
    throw new Error('pr-title workflow declares a ref:; it must not fetch or run pull request code in its pull_request_target context');
  }
}

// The table contributors read in AGENTS.md is the fourth copy of MAP. Parsed
// leniently: anchored on release:, indifferent to column alignment, so
// reformatting cannot fail CI but a wrong label or section can.
function verifyAgentsTable(map, categories, agentsSource) {
  const rows = agentsSource
    .split(/\r?\n/)
    .filter((line) => line.startsWith('|') && line.includes(RESERVED_LABEL_PREFIX));
  if (rows.length === 0) {
    throw new Error('AGENTS.md has no release-label table rows');
  }

  const documented = {};
  for (const row of rows) {
    const cells = row.split('|').slice(1, -1).map((cell) => cell.trim());
    const label = row.match(/release:[a-z-]+/)[0];
    for (const [, type] of cells[0].matchAll(/`([a-z]+)`/g)) {
      if (Object.hasOwn(documented, type)) {
        throw new Error(`AGENTS.md lists type "${type}" in more than one table row`);
      }
      documented[type] = label;
    }
    const section = cells[cells.length - 1];
    const [expectedSection] = categories.get(label) || [];
    if (section !== expectedSection) {
      throw new Error(`AGENTS.md files "${label}" under "${section}", but release.yml uses "${expectedSection}"`);
    }
  }

  for (const [type, label] of Object.entries(map)) {
    if (documented[type] !== label) {
      throw new Error(`AGENTS.md documents type "${type}" as "${documented[type]}", but the label module maps it to "${label}"`);
    }
  }
  for (const type of Object.keys(documented)) {
    if (!Object.hasOwn(map, type)) {
      throw new Error(`AGENTS.md documents type "${type}", which the label module does not map`);
    }
  }
}

function readSources() {
  const repositoryRoot = path.resolve(__dirname, '..', '..');
  const read = (relativePath) => fs.readFileSync(path.join(repositoryRoot, relativePath), 'utf8');
  return {
    repositoryRoot,
    workflowSource: read('.github/workflows/label-pr.yml'),
    releaseSource: read('.github/release.yml'),
    prTitleSource: read('.github/workflows/pr-title.yml'),
    agentsSource: read('AGENTS.md'),
  };
}

function main() {
  const { repositoryRoot, workflowSource, releaseSource, prTitleSource, agentsSource } = readSources();
  const categories = parseReleaseCategories(releaseSource);

  verifyWorkflowWiring(workflowSource, prTitleSource, repositoryRoot);
  verifyLabelDetails(MAP, labelDetails);
  verifyMappingCategories(MAP, categories);
  verifyPrTitleTypes(MAP, parsePrTitleTypes(prTitleSource));
  verifyAgentsTable(MAP, categories, agentsSource);
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
