const assert = require('node:assert/strict');
const { test } = require('node:test');

const { MAP, labelDetails, labelForTitle, run } = require('./label-pr.js');

// Octokit rejects with an error carrying `status`, and createLabel's validation
// failures carry response.data.errors[].code. The fakes have to match that
// shape or the tolerated/rethrown split below proves nothing.
function apiError(status, errors) {
  const error = new Error(`fake ${status}`);
  error.status = status;
  if (errors) error.response = { data: { errors } };
  return error;
}

// pages: label-name arrays, one per page of listLabelsOnIssue.
// responses: per-method override, either a value to resolve or an Error to throw.
function makeFakes({ title, pages = [[]], responses = {} } = {}) {
  const calls = [];
  const record = (method) => async (args) => {
    calls.push([method, args]);
    const response = responses[method];
    const result = typeof response === 'function' ? response(args) : response;
    if (result instanceof Error) throw result;
    return result;
  };

  const listLabelsOnIssue = record('listLabelsOnIssue');
  const github = {
    rest: {
      issues: {
        listLabelsOnIssue,
        getLabel: record('getLabel'),
        createLabel: record('createLabel'),
        addLabels: record('addLabels'),
        removeLabel: record('removeLabel'),
      },
    },
    // Asserting identity catches the workflow being rewired to paginate over
    // the wrong endpoint, which no assertion on the results could see.
    paginate: async (endpoint, args) => {
      assert.equal(endpoint, listLabelsOnIssue, 'paginates listLabelsOnIssue');
      calls.push(['paginate', args]);
      return pages.flat().map((name) => ({ name }));
    },
  };

  const context = {
    repo: { owner: 'orq-ai', repo: 'orq-cli' },
    payload: { pull_request: { number: 7, title } },
  };
  const logs = [];
  const core = { info: (message) => logs.push(message) };
  return { github, context, core, calls, logs };
}

const methodsOf = (calls) => calls.map(([method]) => method);
const argsOf = (calls, method) => calls.filter(([name]) => name === method).map(([, args]) => args);

test('provisions a missing label with its configured colour and description', async () => {
  const fakes = makeFakes({
    title: 'feat: add release labels',
    responses: { getLabel: apiError(404) },
  });
  await run(fakes);

  const [created] = argsOf(fakes.calls, 'createLabel');
  assert.deepEqual(created, {
    owner: 'orq-ai',
    repo: 'orq-cli',
    name: 'release:features',
    ...labelDetails['release:features'],
  });
  assert.deepEqual(argsOf(fakes.calls, 'addLabels'), [
    { owner: 'orq-ai', repo: 'orq-cli', issue_number: 7, labels: ['release:features'] },
  ]);
});

test('does not create a label that already exists in the repository', async () => {
  const fakes = makeFakes({ title: 'feat: x' });
  await run(fakes);

  assert.equal(argsOf(fakes.calls, 'getLabel').length, 1);
  assert.equal(argsOf(fakes.calls, 'createLabel').length, 0);
});

test('rethrows a label lookup failure that is not a 404', async () => {
  const fakes = makeFakes({
    title: 'feat: x',
    responses: { getLabel: apiError(500) },
  });
  // A 500 read as "the label exists" would skip provisioning, and addLabels
  // would then create it with a colour GitHub picks.
  await assert.rejects(run(fakes), { status: 500 });
  assert.equal(argsOf(fakes.calls, 'createLabel').length, 0);
});

test('tolerates a concurrent create and still applies the label', async () => {
  const fakes = makeFakes({
    title: 'feat: x',
    responses: {
      getLabel: apiError(404),
      createLabel: apiError(422, [{ code: 'already_exists' }]),
    },
  });
  await run(fakes);

  assert.deepEqual(argsOf(fakes.calls, 'addLabels'), [
    { owner: 'orq-ai', repo: 'orq-cli', issue_number: 7, labels: ['release:features'] },
  ]);
});

test('rethrows a 422 that is not an already-exists race', async () => {
  for (const errors of [[{ code: 'invalid' }], undefined]) {
    const fakes = makeFakes({
      title: 'feat: x',
      responses: { getLabel: apiError(404), createLabel: apiError(422, errors) },
    });
    // A malformed labelDetails entry 422s the same way. Swallowing it would
    // ship a label with a random colour and a green check.
    await assert.rejects(run(fakes), { status: 422 });
  }
});

test('adds the new label before removing the stale one', async () => {
  const fakes = makeFakes({ title: 'feat: x', pages: [['release:bug-fixes']] });
  await run(fakes);

  const methods = methodsOf(fakes.calls);
  assert.ok(
    methods.indexOf('addLabels') < methods.indexOf('removeLabel'),
    `expected addLabels before removeLabel, got ${methods.join(' ')}`,
  );
  assert.deepEqual(argsOf(fakes.calls, 'removeLabel'), [
    { owner: 'orq-ai', repo: 'orq-cli', issue_number: 7, name: 'release:bug-fixes' },
  ]);
});

test('is idempotent when the right label is already applied', async () => {
  const fakes = makeFakes({ title: 'feat: x', pages: [['release:features']] });
  await run(fakes);

  assert.equal(argsOf(fakes.calls, 'addLabels').length, 0);
  assert.equal(argsOf(fakes.calls, 'removeLabel').length, 0);
});

test('an unparseable title applies nothing and clears owned labels', async () => {
  const fakes = makeFakes({ title: 'not conventional', pages: [['release:features']] });
  await run(fakes);

  assert.equal(argsOf(fakes.calls, 'addLabels').length, 0);
  assert.deepEqual(argsOf(fakes.calls, 'removeLabel').map((args) => args.name), ['release:features']);
  assert.match(fakes.logs.join('\n'), /Title not conventional/);
});

test('a parseable title with an unmapped type applies nothing and clears owned labels', async () => {
  const fakes = makeFakes({ title: 'unknown: x', pages: [['release:features']] });
  await run(fakes);

  assert.equal(argsOf(fakes.calls, 'addLabels').length, 0);
  assert.deepEqual(argsOf(fakes.calls, 'removeLabel').map((args) => args.name), ['release:features']);
  assert.match(fakes.logs.join('\n'), /No mapping for type "unknown"/);
});

test('an already-removed stale label does not stop the remaining removals', async () => {
  const fakes = makeFakes({
    title: 'not conventional',
    pages: [['release:features', 'release:bug-fixes']],
    responses: {
      removeLabel: (args) => (args.name === 'release:features' ? apiError(404) : undefined),
    },
  });
  await run(fakes);

  assert.deepEqual(
    argsOf(fakes.calls, 'removeLabel').map((args) => args.name),
    ['release:features', 'release:bug-fixes'],
  );
});

test('rethrows a removal failure that is not a 404', async () => {
  const fakes = makeFakes({
    title: 'not conventional',
    pages: [['release:features']],
    responses: { removeLabel: apiError(403) },
  });
  await assert.rejects(run(fakes), { status: 403 });
});

test('never touches labels outside the release namespace', async () => {
  const fakes = makeFakes({
    title: 'fix: x',
    pages: [['bug', 'ignore-for-release', 'release:features']],
  });
  await run(fakes);

  assert.deepEqual(argsOf(fakes.calls, 'removeLabel').map((args) => args.name), ['release:features']);
});

test('removes a stale label found on a later page of results', async () => {
  const fakes = makeFakes({
    title: 'docs: x',
    pages: [['bug'], ['release:maintenance']],
  });
  await run(fakes);

  assert.deepEqual(argsOf(fakes.calls, 'removeLabel').map((args) => args.name), ['release:maintenance']);
  assert.deepEqual(argsOf(fakes.calls, 'addLabels').map((args) => args.labels), [['release:documentation']]);
});

test('labels exactly the titles the pr-title validator accepts', () => {
  const mapped = {
    'feat(cli): add release labels': 'release:features',
    'feat!: x': 'release:features',
    'feat(cli)!: x': 'release:features',
    'feat:  x': 'release:features',
    'fix: rename command': 'release:bug-fixes',
    'revert: bad idea': 'release:bug-fixes',
    'docs: explain labels': 'release:documentation',
    'ci: pin an action': 'release:maintenance',
  };
  for (const [title, expected] of Object.entries(mapped)) {
    assert.equal(labelForTitle(title), expected, title);
  }

  const unlabelled = [
    // Rejected by the validator too: wrong case, no colon-space, no subject.
    'FEAT: x', 'Feat: x', 'feat:x', 'feat:', 'feat: ', 'feat:   ',
    'rename command', 'unknown: rename command', 'Revert "feat: x"', 'feat : x',
    'feat_x: y', '123: x',
    // Object.prototype members: a bare MAP lookup returns a truthy non-label.
    'constructor: x', 'toString: x', 'hasOwnProperty: x',
  ];
  for (const title of unlabelled) {
    assert.equal(labelForTitle(title), undefined, title);
  }
});

test('run applies the tightened pattern end to end', async () => {
  const rejected = makeFakes({ title: 'FEAT: x', pages: [['release:features']] });
  await run(rejected);
  assert.equal(argsOf(rejected.calls, 'addLabels').length, 0);
  assert.deepEqual(argsOf(rejected.calls, 'removeLabel').map((args) => args.name), ['release:features']);

  const accepted = makeFakes({ title: 'feat!: subject' });
  await run(accepted);
  assert.deepEqual(argsOf(accepted.calls, 'addLabels').map((args) => args.labels), [['release:features']]);
});

test('every mapped label has a usable labelDetails entry', () => {
  for (const label of new Set(Object.values(MAP))) {
    const details = labelDetails[label];
    assert.ok(details, `${label} has no labelDetails entry`);
    assert.match(details.color, /^[0-9a-fA-F]{6}$/, `${label} colour`);
    assert.ok(details.description.length > 0 && details.description.length <= 100, `${label} description`);
  }
});
