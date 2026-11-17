// fake-github.mjs: a local stand-in for GitHub's smart-HTTP git endpoint, for the
// daily-AO token simulation. It serves one bare repository at
// /<owner>/<repo>.git and authenticates like a private GitHub repository:
//
//   no credentials              -> 401 (git then asks its credential helpers)
//   unknown token               -> 401 (git then tells the helper to forget it)
//   token of ANOTHER account    -> 404 "Repository not found" (what GitHub says
//                                  to an account without access to a private repo)
//   read-only token             -> fetch/ls-remote work; pushing is 403
//   read-write token            -> everything works
//
// Usage: node fake-github.mjs <port> <bare-repo-dir> <owner/repo> <readToken> <writeToken> <otherAccountToken> [requestLog]
// Prints "listening <port>" when ready. The request log records method, path and
// which token KIND was used, never a token value.
import http from 'node:http';
import path from 'node:path';
import fs from 'node:fs';
import { spawn, execFileSync } from 'node:child_process';

const [, , portArg, bareDir, repoPath, readToken, writeToken, otherToken, logFile] = process.argv;
const backend = path.join(execFileSync('git', ['--exec-path']).toString().trim(), 'git-http-backend.exe');
const root = path.dirname(bareDir);
const name = path.basename(bareDir);

function tokenKind(header) {
  if (!header || !header.startsWith('Basic ')) return 'none';
  const pass = Buffer.from(header.slice(6), 'base64').toString().split(':').slice(1).join(':');
  if (pass === readToken) return 'read';
  if (pass === writeToken) return 'write';
  if (pass === otherToken) return 'other';
  return 'unknown';
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://x');
  const kind = tokenKind(req.headers.authorization);
  const isPush = url.pathname.endsWith('/git-receive-pack') || url.searchParams.get('service') === 'git-receive-pack';
  if (logFile) fs.appendFileSync(logFile, `${req.method} ${url.pathname}${url.search} token=${kind}\n`);
  const prefix = `/${repoPath}.git`;
  if (!url.pathname.startsWith(prefix)) { res.writeHead(404).end('Not Found'); return; }
  if (kind === 'none' || kind === 'unknown') { res.writeHead(401, { 'WWW-Authenticate': 'Basic realm="fake-github"' }).end('Bad credentials'); return; }
  if (kind === 'other') { res.writeHead(404).end('Repository not found.'); return; }
  if (kind === 'read' && isPush) { res.writeHead(403).end('Write access to repository not granted.'); return; }

  const env = {
    ...process.env,
    GIT_PROJECT_ROOT: root,
    GIT_HTTP_EXPORT_ALL: '1',
    REQUEST_METHOD: req.method,
    QUERY_STRING: url.search.slice(1),
    PATH_INFO: `/${name}${url.pathname.slice(prefix.length)}`,
    CONTENT_TYPE: req.headers['content-type'] || '',
    REMOTE_USER: 'fake',
    REMOTE_ADDR: '127.0.0.1',
  };
  if (req.headers['content-encoding']) env.HTTP_CONTENT_ENCODING = req.headers['content-encoding'];
  const chunks = [];
  req.on('data', c => chunks.push(c));
  req.on('end', () => {
    const body = Buffer.concat(chunks);
    env.CONTENT_LENGTH = String(body.length);
    const child = spawn(backend, [], { env });
    const out = [];
    child.stdout.on('data', c => out.push(c));
    child.stderr.on('data', () => {});
    child.on('close', () => {
      const buf = Buffer.concat(out);
      let split = buf.indexOf('\r\n\r\n'), skip = 4;
      if (split < 0) { split = buf.indexOf('\n\n'); skip = 2; }
      const head = buf.subarray(0, split).toString().split(/\r?\n/);
      const headers = {}; let status = 200;
      for (const line of head) {
        const i = line.indexOf(':'); if (i < 0) continue;
        const k = line.slice(0, i).trim(), v = line.slice(i + 1).trim();
        if (k.toLowerCase() === 'status') status = parseInt(v, 10); else headers[k] = v;
      }
      res.writeHead(status, headers).end(buf.subarray(split + skip));
    });
    child.stdin.end(body);
  });
});
server.listen(Number(portArg), '127.0.0.1', () => console.log(`listening ${portArg}`));
