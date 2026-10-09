#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Buck2 has finished the run target's build before it starts this command.
./buck2 audit visibility //... toolchains//...
if [[ -n ${BUCK2_REPORT_DIR:-} ]]; then
  mkdir -p "$BUCK2_REPORT_DIR"
  ./buck2 build //... --build-report "$BUCK2_REPORT_DIR/buck2-build-report.json"
  ./buck2 test //... --build-report "$BUCK2_REPORT_DIR/buck2-test-build-report.json"
else
  ./buck2 build //...
  ./buck2 test //...
fi
