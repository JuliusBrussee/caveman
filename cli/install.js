#!/usr/bin/env node
// caveman — compatibility shim.
//
// The unified installer moved from cli/install.js to bin/install.js in
// Caveman 2.0.0. This shim keeps `node cli/install.js [flags]` working for
// older docs, scripts, and clones. All flags are forwarded unchanged.

'use strict';

require('../bin/install.js');
