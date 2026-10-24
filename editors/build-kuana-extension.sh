#!/bin/sh
# Regenerates the tree-sitter parser and copies it with the queries into the Kuana extension.
set -e
cd "$(dirname "$0")/tree-sitter-lyth"
[ -x node_modules/.bin/tree-sitter ] || npm install --no-audit --no-fund
./node_modules/.bin/tree-sitter generate
cd ..
rm -rf kuana-lyth/grammars/lyth
mkdir -p kuana-lyth/grammars/lyth
cp -R tree-sitter-lyth/src kuana-lyth/grammars/lyth/src
rm -f kuana-lyth/grammars/lyth/src/grammar.json kuana-lyth/grammars/lyth/src/node-types.json
for q in highlights outline indents brackets textobjects runnables; do
    cp "tree-sitter-lyth/queries/$q.scm" "kuana-lyth/languages/lyth/$q.scm"
done
echo "Kuana extension updated: $(pwd)/kuana-lyth"
