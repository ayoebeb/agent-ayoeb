import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import { LANES, outputs, pickLanes } from './ci-lanes.mjs';

const pr = (paths, extra = {}) => ({
  event: 'pull_request',
  files: paths.map(path => (typeof path === 'string' ? { status: 'modified', filename: path } : path)),
  ...extra,
});
const everyLane = Object.keys(LANES).filter(lane => lane !== 'mac-native');

const push = (paths, extra = {}) => ({
  event: 'push',
  baselineGreen: true,
  files: paths.map(path => (typeof path === 'string' ? { status: 'modified', filename: path } : path)),
  ...extra,
});
const allLanes = Object.keys(LANES);
const withoutMacos = allLanes.filter(lane => lane !== 'mac-native');

test('a push to main runs every lane in full', () => {
  assert.deepEqual(pickLanes({ event: 'push' }), { lanes: allLanes, full: true, macos: true, vitest: 'all' });
});

test('a weekly or manual run runs every lane in full, with macOS', () => {
  const full = { lanes: allLanes, full: true, macos: true, vitest: 'all' };
  assert.deepEqual(pickLanes({ event: 'schedule' }), full);
  assert.deepEqual(pickLanes({ event: 'workflow_dispatch' }), full);
});

test('a push whose files are unknown runs every lane in full, with macOS', () => {
  const full = { lanes: allLanes, full: true, macos: true, vitest: 'all' };
  assert.deepEqual(pickLanes({ event: 'push', files: null }), full);
  assert.deepEqual(pickLanes(push([])), full);
});

test('a push that changes code any heavy lane reads runs every lane in full, but only starts macOS for macOS paths', () => {
  const full = { lanes: withoutMacos, full: true, macos: false, vitest: 'all' };
  for (const path of ['backend/a.go', 'frontend/src/renderer/a.ts', 'packages/shared/a.ts', 'test/cli/Dockerfile', 'pricing/a.json', 'package.json']) {
    assert.deepEqual(pickLanes(push([path])), full, path);
  }
  assert.deepEqual(pickLanes(push(['README.md', 'backend/a.go'])), full);
  assert.deepEqual(pickLanes(push([{ status: 'renamed', filename: 'docs/a.go', previous_filename: 'backend/a.go' }])), full);
  for (const path of ['frontend/native/a.swift', 'frontend/assets/dmg/background.png', 'frontend/scripts/build-mac-dmg.mjs']) {
    assert.deepEqual(pickLanes(push([path])), { lanes: allLanes, full: true, macos: true, vitest: 'all' }, path);
  }
});

test('a push that changes a workflow or the lane rules runs every lane in full, with macOS', () => {
  const full = { lanes: allLanes, full: true, macos: true, vitest: 'all' };
  assert.deepEqual(pickLanes(push(['.github/workflows/go.yml'])), full);
  assert.deepEqual(pickLanes(push(['scripts/ci-lanes.mjs'])), full);
  assert.deepEqual(pickLanes(push(['.github/actions/setup-go-cached/action.yml', 'README.md'])), full);
});

test('a truncated push diff runs every lane in full, with macOS', () => {
  const files = Array.from({ length: 3000 }, (_, i) => `docs/${i}.md`);
  assert.deepEqual(pickLanes(push(files)), { lanes: allLanes, full: true, macos: true, vitest: 'all' });
});

test('a push whose base commit has no green CI run runs every lane in full', () => {
  const none = { lanes: [], full: false, macos: false, vitest: '' };
  assert.deepEqual(pickLanes(push(['README.md'])), none);
  const full = { lanes: withoutMacos, full: true, macos: false, vitest: 'all' };
  assert.deepEqual(pickLanes(push(['README.md'], { baselineGreen: false })), full);
  assert.deepEqual(pickLanes(push(['README.md'], { baselineGreen: undefined })), full);
  assert.deepEqual(pickLanes({ event: 'push', files: [{ status: 'modified', filename: 'README.md' }] }), full);
});

test('a push that changes a path nobody listed as independent runs every lane in full, even if no lane rule matches it', () => {
  const full = { lanes: withoutMacos, full: true, macos: false, vitest: 'all' };
  const unlisted = [
    'translations/a.json',
    'contracts/other.json',
    'contracts/session-start-interface.json',
    'integrations/a.md',
    'desktop-version-floor.json',
    'flake.nix',
    'flake.lock',
    '.gitmodules',
    'private/ao-cloud',
    'experiments/a.ts',
    'assets/icon.png',
    'LICENSE',
    'docs/assets/readme/a.png',
    '.github/scripts/go-test-parallel.sh',
  ];
  for (const path of unlisted) {
    assert.deepEqual(pickLanes(push([path])), full, path);
    assert.deepEqual(pickLanes(push(['README.md', path])), full, `README.md + ${path}`);
  }
  for (const path of unlisted) {
    // The same path on a PR runs no lane: the full push is what covers it.
    if (!/^(contracts\/session|\.github\/scripts\/go-test)/.test(path)) assert.deepEqual(pickLanes(pr([path])).lanes, [], `PR ${path}`);
  }
});

test('a push that only changes files no heavy lane reads runs only the lanes it touches, without macOS', () => {
  const none = { lanes: [], full: false, macos: false, vitest: '' };
  assert.deepEqual(pickLanes(push(['README.md', 'docs/ci.md', 'AGENTS.md', 'CONTRIBUTING.md'])), none);
  assert.deepEqual(pickLanes(push(['scripts/daily-ao/update.ps1'])), { ...none, lanes: ['config'] });
  assert.deepEqual(pickLanes(push(['.github/scripts/pr-review-leaderboard.mjs'])), { ...none, lanes: ['config'] });
  assert.deepEqual(pickLanes(push(['frontend/src/docs/content/a.mdx'])), { ...none, lanes: ['docs'] });
  assert.deepEqual(pickLanes(push(['contracts/cloud/openapi.yaml'])), { ...none, lanes: ['cloud'] });
  assert.deepEqual(pickLanes(push(['packages/mobile/a.ts', 'README.md'])), { ...none, lanes: ['mobile'] });
  assert.deepEqual(pickLanes(push(['scripts/a.mjs', 'frontend/src/docs/a.mdx'])), { ...none, lanes: ['docs', 'config'] });
});

test('drafts and stacked PRs run no lanes, even with ci:full', () => {
  const none = { lanes: [], full: false, macos: false, vitest: '' };
  assert.deepEqual(pickLanes(pr(['backend/a.go'], { draft: true, labels: ['ci:full'] })), none);
  assert.deepEqual(pickLanes(pr(['backend/a.go'], { base: 'feat/parent' })), none);
});

test('a docs-only PR runs no lanes', () => {
  assert.deepEqual(pickLanes(pr(['README.md', 'docs/ci.md', 'AGENTS.md'])).lanes, []);
});

test('drafts and stacked PRs from forks', () => {
  assert.deepEqual(pickLanes(pr(['backend/a.go'], { draft: true, base: 'feat/parent', fork: true })).lanes, []);
  assert.throws(() => pickLanes(pr(['backend/a.go'], { base: 'feat/parent', fork: true })), /target main and push again/);
  assert.deepEqual(pickLanes(pr(['backend/a.go'], { fork: true })).lanes, ['go', 'cli-e2e']);
});

test('a renderer change runs the frontend lane, with related vitest files and the Electron boundary', () => {
  const pick = pickLanes(pr(['frontend/src/renderer/a.ts', 'frontend/src/renderer/a.test.ts']));
  assert.deepEqual(pick.lanes, ['frontend', 'boundary']);
  assert.equal(pick.vitest, 'src/renderer/a.ts src/renderer/a.test.ts');
  assert.equal(pick.macos, false);
});

test('an httpd change runs Go, CLI E2E and the Electron boundary', () => {
  assert.deepEqual(pickLanes(pr(['backend/internal/httpd/server.go'])).lanes, ['go', 'cli-e2e', 'boundary']);
});

test('files backend tests and Go CI read from outside backend/ run the Go lane', () => {
  assert.deepEqual(pickLanes(pr(['contracts/session-start-interface.json'])).lanes, ['go']);
  assert.deepEqual(pickLanes(pr(['go.work'])).lanes, ['go']);
  assert.deepEqual(pickLanes(pr(['.github/scripts/go-test-parallel.sh'])).lanes, ['go', 'config']);
  assert.ok(pickLanes(pr(['frontend/scripts/prepare-cloudflared.mjs'])).lanes.includes('go'));
});

test('packages/shared reaches the frontend and mobile, with all vitest files', () => {
  const pick = pickLanes(pr(['packages/shared/theme/palette.ts']));
  assert.deepEqual(pick.lanes, ['frontend', 'boundary', 'mobile']);
  assert.equal(pick.vitest, 'all');
});

test('the renderer typechecks against cloud-client types', () => {
  assert.deepEqual(pickLanes(pr(['packages/cloud-client/src/types.ts'])).lanes, ['frontend', 'boundary', 'cloud']);
  assert.deepEqual(pickLanes(pr(['contracts/cloud/openapi.yaml'])).lanes, ['cloud']);
});

test('a workflow edit runs every lane except native macOS', () => {
  const pick = pickLanes(pr(['.github/workflows/ci.yml']));
  assert.deepEqual(pick.lanes, everyLane);
  assert.equal(pick.vitest, 'all');
  assert.equal(pick.macos, false);
});

test('labels select native coverage', () => {
  assert.deepEqual(pickLanes(pr(['frontend/src/main/a.ts'], { labels: ['ci:macos'] })).lanes, ['frontend', 'boundary', 'mac-native']);
  const full = pickLanes(pr(['README.md'], { labels: ['ci:full'] }));
  assert.deepEqual(full.lanes, Object.keys(LANES));
  assert.equal(full.full, true);
});

test('native helper paths start the macOS jobs without a label', () => {
  const pick = pickLanes(pr(['frontend/native/wake-helper/WakePolicy.swift']));
  assert.deepEqual(pick.lanes, ['mac-native']);
  assert.equal(pick.macos, true);
  assert.ok(pickLanes(pr(['frontend/assets/dmg/background.png'])).lanes.includes('mac-native'));
});

test('a rename counts its old path', () => {
  assert.deepEqual(pickLanes(pr([{ status: 'renamed', filename: 'docs/a.go', previous_filename: 'backend/a.go' }])).lanes, ['go', 'cli-e2e']);
});

test('a truncated file list runs every lane', () => {
  const files = Array.from({ length: 3000 }, (_, i) => `docs/${i}.md`);
  assert.deepEqual(pickLanes(pr(files)).lanes, everyLane);
});

test('vitest runs all files when related selection cannot be trusted', () => {
  assert.equal(pickLanes(pr(['frontend/src/renderer/test/setup.ts'])).vitest, 'all');
  assert.equal(pickLanes(pr(['frontend/package.json'])).vitest, 'all');
  assert.equal(pickLanes(pr(['frontend/src/renderer/my file.ts'])).vitest, 'all');
  assert.equal(pickLanes(pr([{ status: 'removed', filename: 'frontend/src/renderer/a.ts' }])).vitest, 'all');
});

test('outputs are one GitHub output line each', () => {
  const text = outputs(pickLanes(pr(['frontend/src/docs/content/a.mdx'])));
  assert.equal(text, 'lanes=["docs"]\nfrontend=true\nfull=false\nmacos=false\nvitest=\n');
});

test('every lane is used by a workflow', () => {
  const workflows = ['ci.yml', 'frontend.yml'].map(name => readFileSync(new URL(`../.github/workflows/${name}`, import.meta.url), 'utf8')).join('\n');
  for (const lane of Object.keys(LANES)) {
    assert.ok(workflows.includes(`'${lane}')`), `no workflow job runs lane ${lane}`);
  }
});

test('every always-run vitest file exists', () => {
  const list = readFileSync(new URL('../frontend/scripts/ci-vitest-always-run.txt', import.meta.url), 'utf8').split('\n').filter(Boolean);
  assert.ok(list.length > 0);
  for (const path of list) {
    assert.doesNotThrow(() => readFileSync(new URL(`../frontend/${path}`, import.meta.url)), `missing ${path}`);
  }
});
