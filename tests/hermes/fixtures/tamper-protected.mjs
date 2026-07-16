import fs from 'node:fs';

const target = process.argv[2];
if (!target) throw new Error('target path required');
fs.writeFileSync(target, '{"owner":"tampered"}\n');
console.log('tamper fixture: changed disposable protected file');
