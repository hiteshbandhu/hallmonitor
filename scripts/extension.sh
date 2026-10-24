#!/bin/zsh
# Packages an extension from extensions/<id> for the marketplace:
#   dist/hallmonitor-<id>-<v>.tar.gz   manifest.json and a universal bin/
# and prints the listing to put in extensions/index.json.
#
#   scripts/extension.sh spotify 1.0.0
set -euo pipefail
id=${1:?extension id, e.g. spotify} v=${2:?version, e.g. 1.0.0}
root=${0:A:h:h}
cd $root
src=extensions/$id
[[ -f $src/manifest.json ]] || { echo "no $src/manifest.json" >&2; exit 1; }
exe=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["executable"])' $src/manifest.json)
name=hallmonitor-$id-$v
tmp=dist/tmp-$id && rm -rf $tmp && mkdir -p $tmp/$name/${exe:h} dist

# The manifest, with this version.
python3 - $src/manifest.json $v $tmp/$name/manifest.json <<'PY'
import json, sys
m = json.load(open(sys.argv[1])); m["version"] = sys.argv[2]
json.dump(m, open(sys.argv[3], "w"), indent=2, ensure_ascii=False)
PY

# One program for both kinds of Mac. Go signs each slice ad hoc; lipo keeps them.
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o $tmp/$id-$arch ./$src
done
lipo -create $tmp/$id-arm64 $tmp/$id-amd64 -output $tmp/$name/$exe
codesign --force -s - $tmp/$name/$exe

COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C $tmp -czf dist/$name.tar.gz $name
rm -rf $tmp
sum=$(shasum -a 256 dist/$name.tar.gz | cut -d' ' -f1)
echo "dist/$name.tar.gz"
python3 - $src/manifest.json $v $sum <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
entry = {k: m[k] for k in ("id", "name", "summary", "docs", "symbol", "permissions") if k in m}
entry["version"] = sys.argv[2]
entry["url"] = f"https://github.com/hiteshbandhu/hallmonitor/releases/download/{m['id']}-v{sys.argv[2]}/hallmonitor-{m['id']}-{sys.argv[2]}.tar.gz"
entry["sha256"] = sys.argv[3]
entry["min_app"] = "0.6.0"
print(json.dumps(entry, indent=2, ensure_ascii=False))
PY
