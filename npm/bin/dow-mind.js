#!/usr/bin/env node
// Launcher shim: exec the native dow-mind binary downloaded by postinstall,
// forwarding all arguments, stdio, and the exit code.
'use strict'

const fs = require('fs')
const { spawnSync } = require('child_process')
const { binaryPath } = require('../lib/platform')

const bin = binaryPath()
if (!fs.existsSync(bin)) {
  process.stderr.write(
    'dow-mind: native binary not found. The postinstall download may have failed.\n' +
      'Reinstall (npm install -g dow-mind) or download from ' +
      'https://github.com/RivaldiMurpia/dow-mind/releases\n'
  )
  process.exit(1)
}

const res = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' })
if (res.error) {
  process.stderr.write(`dow-mind: ${res.error.message}\n`)
  process.exit(1)
}
process.exit(res.status === null ? 1 : res.status)
