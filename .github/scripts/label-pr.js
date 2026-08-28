// Applies a release:* label to a pull request based on its conventional-commit
// title, so GitHub's auto-generated release notes (.github/release.yml)
// categorize it. Called by .github/workflows/label-pr.yml through
// actions/github-script; unit-tested in label-pr.test.js.
//
// The release:* namespace is reserved for this automation. Ordinary
// human-applied labels are never touched.

const MAP = {
  feat: 'release:features',
  fix: 'release:bug-fixes', perf: 'release:bug-fixes', revert: 'release:bug-fixes',
  docs: 'release:documentation',
  chore: 'release:maintenance', ci: 'release:maintenance', refactor: 'release:maintenance',
  build: 'release:maintenance', test: 'release:maintenance', style: 'release:maintenance',
};

const labelDetails = {
  'release:features': {
    color: '0E8A16',
    description: 'Automated release-notes label for features.',
  },
  'release:bug-fixes': {
    color: 'D73A4A',
    description: 'Automated release-notes label for bug fixes.',
  },
  'release:documentation': {
    color: '0075CA',
    description: 'Automated release-notes label for documentation.',
  },
  'release:maintenance': {
    color: '6F42C1',
    description: 'Automated release-notes label for maintenance.',
  },
};

// Deliberately mirrors what amannn/action-semantic-pull-request accepts in
// .github/workflows/pr-title.yml: a lowercase type (its type list is matched
// case-sensitively), a literal ": ", and a subject with at least one non-space
// character (it raises "No subject found" otherwise). Titles the validator
// rejects therefore get no label either.
//
// The scope group is `\(.*\)`, not `\([^)]*\)`, because conventional-commits-parser
// (the validator's real parser, via conventional-changelog-conventionalcommits)
// captures the scope with a greedy `(.*)`. So it accepts a nested-paren scope such
// as `feat(a(b)): x`; `[^)]*` would not, and that PR would pass the title check
// while silently going unlabelled. `\(.*\)` mirrors the validator rather than
// over- or under-accepting it — this is not a decision to make the labeler a
// superset.
const TITLE_PATTERN = /^([a-z]+)(\(.*\))?!?: (.*\S)/;

// Object.hasOwn, because a bare lookup reads through the prototype: a title
// like `constructor:` would resolve to a truthy non-label and crash the run.
function labelForTitle(title) {
  const match = TITLE_PATTERN.exec(title);
  const type = match && match[1];
  return type && Object.hasOwn(MAP, type) ? MAP[type] : undefined;
}

async function run({ github, context, core }) {
  const issue = {
    owner: context.repo.owner,
    repo: context.repo.repo,
    issue_number: context.payload.pull_request.number,
  };
  const owned = new Set(Object.values(MAP));

  // `addLabels` would create a missing label on its own, but with a colour
  // GitHub picks and no description. Provisioning first is what makes both
  // ours. Looking it up first keeps the common already-exists case a clean 200,
  // so the 422 below is reserved for the genuine race.
  const provisionLabel = async (name) => {
    const repository = { owner: context.repo.owner, repo: context.repo.repo };
    try {
      await github.rest.issues.getLabel({ ...repository, name });
      return;
    } catch (lookupError) {
      if (lookupError.status !== 404) throw lookupError;
    }
    try {
      await github.rest.issues.createLabel({ ...repository, name, ...labelDetails[name] });
      core.info(`Provisioned release label: ${name}`);
    } catch (createError) {
      // Only an already-exists 422 is another run creating it in between.
      // Every other 422 is a malformed labelDetails entry.
      const errors = createError.response?.data?.errors || [];
      if (!errors.some((entry) => entry.code === 'already_exists')) throw createError;
      core.info(`Release label created concurrently: ${name}`);
    }
  };

  const current = await github.paginate(
    github.rest.issues.listLabelsOnIssue,
    issue,
  );
  const title = context.payload.pull_request.title || '';
  const label = labelForTitle(title);

  // Add before removing; see AGENTS.md.
  if (label) {
    await provisionLabel(label);
    if (current.some((currentLabel) => currentLabel.name === label)) {
      core.info(`Release label already applied: ${label}`);
    } else {
      await github.rest.issues.addLabels({ ...issue, labels: [label] });
      core.info(`Applied label: ${label}`);
    }
  } else {
    const match = TITLE_PATTERN.exec(title);
    core.info(match
      ? `No mapping for type "${match[1]}"`
      : `Title not conventional, no label: "${title}"`);
  }

  // Unconditional: this is what clears stale automation labels when a title is
  // retitled, becomes invalid, or loses its mapping.
  for (const stale of current
    .map((currentLabel) => currentLabel.name)
    .filter((name) => owned.has(name) && name !== label)) {
    try {
      await github.rest.issues.removeLabel({ ...issue, name: stale });
      core.info(`Removed stale label: ${stale}`);
    } catch (removeError) {
      if (removeError.status !== 404) throw removeError;
      core.info(`Stale label already removed: ${stale}`);
    }
  }
}

module.exports = { MAP, labelDetails, TITLE_PATTERN, labelForTitle, run };
