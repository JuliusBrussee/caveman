#!/usr/bin/env node
// Sweet is a persona overlay; installation uses the maintained upstream engine.
process.env.CAVEMAN_REPO ||= 'HDomi/sweet-agent';
process.env.CAVEMAN_REF = process.env.SWEET_REF || process.env.CAVEMAN_REF || 'main';
require('./install.js');
