#!/bin/sh
# Copies the Kuana extension's language files and snippets into the Zed extension and pins its
# grammar to the last commit that changed tree-sitter-lyth (Zed builds the grammar from the repository).
set -e
cd "$(dirname "$0")"
rm -rf zed-lyth/languages zed-lyth/snippets
mkdir -p zed-lyth/languages/lyth zed-lyth/snippets
cp kuana-lyth/languages/lyth/config.toml kuana-lyth/languages/lyth/tasks.json zed-lyth/languages/lyth/
for q in kuana-lyth/languages/lyth/*.scm; do
    # Zed's themes color escapes as string.escape, and Zed has no parameter text objects.
    sed -E -e 's/@escape([^a-zA-Z0-9_.]|$)/@string.escape\1/g' -e '/@parameter\./d' "$q" > "zed-lyth/languages/lyth/$(basename "$q")"
done
cp kuana-lyth/snippets/lyth.json zed-lyth/snippets/
rev=$(git log -1 --format=%H -- tree-sitter-lyth)
sed -E "s/^rev = \".*\"/rev = \"$rev\"/" zed-lyth/extension.toml > zed-lyth/extension.toml.new
mv zed-lyth/extension.toml.new zed-lyth/extension.toml
if [ -n "$(git status --porcelain -- tree-sitter-lyth)" ]; then
    echo "warning: tree-sitter-lyth has uncommitted changes; Zed builds the grammar at $rev." >&2
    echo "Commit them, then run this script again." >&2
fi
echo "Zed extension updated: $(pwd)/zed-lyth (grammar at $rev)"
