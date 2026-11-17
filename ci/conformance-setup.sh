#!/usr/bin/env bash
set -euo pipefail

TARGET_DIR="testdata/conformance"
mkdir -p "${TARGET_DIR}"

TARBALL_URL="https://registry.npmjs.org/tus-js-client/-/tus-js-client-4.3.1.tgz"
TARBALL_SHA256="919f46808b2c689be9214df5c0f21879bee4a4599b34e212ada8f14de1db9bc0"
TARBALL_PATH="${TARGET_DIR}/tus-js-client-4.3.1.tgz"

if command -v curl >/dev/null 2>&1; then
  curl -sSL "${TARBALL_URL}" -o "${TARBALL_PATH}"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "${TARBALL_PATH}" "${TARBALL_URL}"
else
  echo "curl or wget required" >&2
  exit 1
fi

echo "${TARBALL_SHA256}  ${TARBALL_PATH}" | sha256sum -c -

(cd "${TARGET_DIR}" && npm install "$(basename "${TARBALL_PATH}")")

cat << 'EOF' > "${TARGET_DIR}/upload.mjs"
import * as fs from 'node:fs';
import * as tus from 'tus-js-client';

const endpoint = process.argv[2];
const filePath = process.argv[3];

if (!endpoint || !filePath) {
  console.error('Usage: node upload.mjs <endpoint> <filePath>');
  process.exit(1);
}

const fileStream = fs.createReadStream(filePath);
const size = fs.statSync(filePath).size;

const upload = new tus.Upload(fileStream, {
  endpoint,
  retryDelays: [0, 10, 10],
  chunkSize: 65536,
  uploadSize: size,
  metadata: {
    filename: 'conformance.bin'
  },
  onError(error) {
    console.error('Upload failed:', error);
    process.exit(1);
  },
  onSuccess() {
    process.exit(0);
  }
});

upload.start();
EOF
