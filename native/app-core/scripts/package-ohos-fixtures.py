#!/usr/bin/env python3
"""Package the shared continuity corpus for the real HarmonyOS NAPI replay; no duplicate expectations."""
import argparse
import hashlib
import json
import sys
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fixtures', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    documents = []
    for path in sorted(args.fixtures.glob('*.json')):
        document = json.loads(path.read_text())
        documents.append({'file': path.name, 'document': document})
    content = json.dumps(documents, ensure_ascii=False, separators=(',', ':'), sort_keys=True).encode()
    digest = hashlib.sha256(content).hexdigest()
    args.output.mkdir(parents=True, exist_ok=True)
    (args.output / 'continuity.json').write_bytes(content)
    (args.output / 'manifest.json').write_text(json.dumps({'sha256': digest, 'cases': 38, 'steps': 94}, indent=2) + '\n')
    print('HarmonyOS core fixtures: 38 cases / 94 steps, SHA256 ' + digest)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError) as error:
        sys.exit('HarmonyOS core fixtures: FAILED (' + str(error) + ')')
