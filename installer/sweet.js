#!/usr/bin/env node
// Sweet is a persona overlay; installation uses the maintained upstream engine.
process.env.CAVEMAN_REPO ||= 'HDomi/sweet-agent';
process.env.CAVEMAN_REF = process.env.SWEET_REF || process.env.CAVEMAN_REF || 'main';
const { main } = require('./install.js');
main().then(code => process.exit(code || 0)).catch(error => {
  process.stderr.write(String(error && error.stack || error) + '\n');
  process.exit(1);
});
