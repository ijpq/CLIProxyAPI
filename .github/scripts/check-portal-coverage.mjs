import {readFileSync} from 'node:fs';

const path = process.argv[2];
const minimum = Number(process.argv[3] ?? 95);
if (!path || !Number.isFinite(minimum) || minimum < 0 || minimum > 100) {
  throw Error('Usage: node check-portal-coverage.mjs <coverprofile> [minimum-percent]');
}
let statements = 0, covered = 0;
for (const line of readFileSync(path, 'utf8').trim().split('\n').slice(1)) {
  if (!line.includes('/internal/api/modules/portal/')) continue;
  const match = line.match(/\s(\d+)\s+(\d+)$/);
  if (!match) throw Error(`Malformed coverage entry: ${line}`);
  const count = Number(match[1]);
  statements += count;
  if (Number(match[2]) > 0) covered += count;
}
if (statements === 0) throw Error('Portal statement coverage is missing');
const actual = covered * 100 / statements;
console.log(`Portal Go statement coverage: ${actual.toFixed(2)}% (${covered}/${statements}); required: ${minimum}%`);
if (covered * 100 < statements * minimum) process.exitCode = 1;
