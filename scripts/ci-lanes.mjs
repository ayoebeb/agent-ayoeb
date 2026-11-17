import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';

const FRONTEND = /^(frontend\/(?!src\/docs\/|native\/)|packages\/(product-ui|shared|cloud-client)\/)/;

export const LANES = {
  go: [/^backend\//, /^pricing\//, /^go\.work(\.sum)?$/, /^contracts\/session-start-interface\.json$/, /^frontend\/scripts\/prepare-cloudflared\.mjs$/, /^frontend\/src\/api\/schema\.ts$/, /^package(-lock)?\.json$/, /^\.github\/scripts\/go-test-parallel\.sh$/],
  'cli-e2e': [/^backend\//, /^test\/cli\//],
  frontend: [FRONTEND],
  docs: [/^frontend\/src\/docs\//],
  boundary: [FRONTEND, /^backend\/internal\/httpd\//],
  cloud: [/^packages\/cloud-client\//, /^contracts\/cloud\//],
  'product-ui': [/^packages\/product-ui\//],
  mobile: [/^packages\/(mobile|shared)\//],
  'mac-native': [/^frontend\/native\//, /^frontend\/scripts\/build-(mac|update|wake|dictation)/, /^frontend\/assets\/(icon\.icns$|dmg\/)/],
  config: [/^\.github\//, /^scripts\//],
};

export const FRONTEND_WORKFLOW_LANES = ['frontend', 'docs', 'boundary', 'cloud', 'product-ui', 'mac-native'];

const EVERY_LANE = /^(\.github\/(workflows|actions)\/|scripts\/ci-lanes\.mjs$)/;
const ALL_VITEST = /^(frontend\/(package(-lock)?\.json|vite[^/]*\.ts|vitest[^/]*\.ts|tsconfig[^/]*\.json|src\/renderer\/test\/|scripts\/)|packages\/(product-ui|shared)\/)/;
const FILE_LIMIT = 3000;

// Paths known to be read by none of the heavy suites (Go, CLI E2E, frontend, the
// Electron boundary, native Windows and macOS). A push to main whose files all
// match one of these changes nothing those suites could see, so they were already
// run, in full, on the commit before it.
//
// This is an allowlist on purpose. A path that matches no lane rule is not proof
// that nothing reads it (contracts/, translations/, integrations/, flake.nix,
// desktop-version-floor.json and the like match no rule), and on a PR such a path
// runs no lane, so the full run on the push is the only check it gets. Anything
// not listed here runs in full.
const INDEPENDENT_PATHS = [
  /^(README|AGENTS|CLAUDE|CONTEXT|CONTRIBUTING|DESIGN)\.md$/,
  /^docs\/.+\.md$/,
  /^scripts\//,
  /^\.github\/(?!workflows\/|actions\/)/,
  /^frontend\/src\/docs\//,
  /^contracts\/cloud\//,
  /^packages\/mobile\//,
];

// The allowlisted paths that start a lane do not start a heavy one, but a path
// can be allowlisted and still match a heavy lane's rule (.github/scripts/
// go-test-parallel.sh is the Go lane's), so a push must also start only these.
const INDEPENDENT_LANES = ['config', 'docs', 'cloud', 'mobile'];

// `files` is null when the changed files are unknown (a push whose diff could not
// be read), which always runs every lane.
//
// `baselineGreen` says the commit a push builds on had a successful CI run. Skipping
// the heavy suites rests on that run having covered everything the push did not
// change, so without it every lane runs. A run that a newer push cancelled, or that
// failed, is not a baseline.
export function pickLanes({ event, draft = false, base = 'main', fork = false, labels = [], files = null, baselineGreen = false }) {
  if (event === 'pull_request' && !draft && base !== 'main' && fork) {
    throw new Error(`This fork PR targets ${base}. CI cannot re-run it when it moves to main, so ci-ok stays red: target main and push again.`);
  }
  if (event === 'pull_request' && (draft || base !== 'main')) {
    return { lanes: [], full: false, macos: false, vitest: '' };
  }
  const known = files ?? [];
  const paths = known.flatMap(file => [file.filename, file.previous_filename].filter(Boolean));
  const hit = lane => paths.some(path => LANES[lane].some(rule => rule.test(path)));
  const independentPush =
    event === 'push' &&
    baselineGreen &&
    known.length > 0 &&
    known.length < FILE_LIMIT &&
    paths.every(path => INDEPENDENT_PATHS.some(rule => rule.test(path))) &&
    !paths.some(path => EVERY_LANE.test(path)) &&
    Object.keys(LANES).every(lane => INDEPENDENT_LANES.includes(lane) || !hit(lane));
  const full = (event !== 'pull_request' && !independentPush) || labels.includes('ci:full');
  const every = full || known.length >= FILE_LIMIT || paths.some(path => EVERY_LANE.test(path));
  // Each native macOS job bills ten minutes for every one it runs. On a push to
  // main they run only when macOS paths change, when a workflow or the lane rules
  // change (nothing else tests those on macOS), or when the changed files are
  // unknown. The weekly schedule and manual runs always run them.
  const nativeAlways =
    event === 'pull_request'
      ? labels.includes('ci:full')
      : event !== 'push' || known.length === 0 || known.length >= FILE_LIMIT || paths.some(path => EVERY_LANE.test(path));
  const macos = nativeAlways || labels.includes('ci:macos') || hit('mac-native');
  const lanes = Object.keys(LANES).filter(lane => (lane === 'mac-native' ? macos : every || hit(lane)));
  return { lanes, full, macos, vitest: vitestTarget(lanes, every || paths.some(path => ALL_VITEST.test(path)), known) };
}

function vitestTarget(lanes, all, files) {
  if (!lanes.includes('frontend')) return '';
  const related = files
    .filter(file => file.status !== 'removed' && file.filename.startsWith('frontend/'))
    .map(file => file.filename.slice('frontend/'.length));
  if (all || related.length === 0 || related.some(path => /\s/.test(path))) return 'all';
  return related.join(' ');
}

export function outputs(pick) {
  return [
    `lanes=${JSON.stringify(pick.lanes)}`,
    `frontend=${pick.lanes.some(lane => FRONTEND_WORKFLOW_LANES.includes(lane))}`,
    `full=${pick.full}`,
    `macos=${pick.macos}`,
    `vitest=${pick.vitest}`,
  ].join('\n') + '\n';
}

function gh(...args) {
  return execFileSync('gh', args, { encoding: 'utf8', maxBuffer: 256 * 1024 * 1024 });
}

function readPullRequest(repo, number) {
  const pr = JSON.parse(gh('api', `repos/${repo}/pulls/${number}`));
  const files = gh('api', '--paginate', `repos/${repo}/pulls/${number}/files`, '--jq', '.[] | [.status, .filename, .previous_filename // ""] | @tsv')
    .split('\n')
    .filter(Boolean)
    .map(line => {
      const [status, filename, previous] = line.split('\t');
      return { status, filename, previous_filename: previous || undefined };
    });
  const fork = pr.head.repo?.full_name !== pr.base.repo.full_name;
  return { draft: pr.draft, base: pr.base.ref, fork, labels: pr.labels.map(label => label.name), files };
}

// What a push to main changed, and whether the commit it builds on had a
// successful CI run. Either one unknown (a first push, a force push, a compare or
// API call that fails, a run a newer push cancelled) makes every lane run.
function readPush(repo, before, sha) {
  const unknown = { files: null, baselineGreen: false };
  if (!before || /^0+$/.test(before)) return unknown;
  try {
    const lines = gh('api', '--paginate', `repos/${repo}/compare/${before}...${sha}`, '--jq', '.status, (.files[] | [.status, .filename, .previous_filename // ""] | @tsv)')
      .split('\n')
      .filter(Boolean);
    const statuses = lines.filter(line => !line.includes('\t'));
    if (statuses.length === 0 || statuses.some(status => status !== 'ahead' && status !== 'identical')) return unknown;
    const files = lines
      .filter(line => line.includes('\t'))
      .map(line => {
        const [status, filename, previous] = line.split('\t');
        return { status, filename, previous_filename: previous || undefined };
      });
    // The compare API lists at most 300 files a page, so a list that long may be
    // only the first page of a bigger push.
    if (files.length >= 300) return unknown;
    // Newest first. A push to main cancels the run still going for the one before
    // it, so that run only counts if it finished green.
    const conclusion = gh('api', `repos/${repo}/actions/workflows/ci.yml/runs?head_sha=${before}&event=push&per_page=1`, '--jq', '.workflow_runs[0].conclusion // ""').trim();
    return { files, baselineGreen: conclusion === 'success' };
  } catch (error) {
    console.error(`could not read the push, so every lane runs: ${error.message}`);
    return unknown;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const event = process.env.GITHUB_EVENT_NAME;
  const repo = process.env.GITHUB_REPOSITORY;
  const input =
    event === 'pull_request'
      ? { event, ...readPullRequest(repo, process.env.PR) }
      : event === 'push'
        ? { event, ...readPush(repo, process.env.BEFORE, process.env.GITHUB_SHA) }
        : { event };
  const pick = pickLanes(input);
  console.error(JSON.stringify({ ...input, files: input.files?.length, ...pick }));
  process.stdout.write(outputs(pick));
}
