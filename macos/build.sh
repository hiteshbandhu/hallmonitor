#!/bin/zsh
# Builds HallMonitor.app (menu bar + notch) with the Command Line Tools; no
# Xcode project needed. The hallmonitor CLI is bundled in Contents/Helpers so
# the app works on its own.
#
#   macos/build.sh            # -> macos/build/HallMonitor.app
#   macos/build.sh --install  # also copy to ~/Applications and launch
set -euo pipefail
root=${0:A:h:h}
cd "$root/macos"
out=build/HallMonitor.app

rm -rf build && mkdir -p "$out/Contents/MacOS" "$out/Contents/Helpers" "$out/Contents/Resources"
cp Info.plist "$out/Contents/Info.plist"
cp AppIcon.icns "$out/Contents/Resources/"
cp -R Fonts "$out/Contents/Resources/"

swiftc -O -parse-as-library -target arm64-apple-macos14.0 \
  -framework SwiftUI -framework AppKit -framework UserNotifications \
  Sources/*.swift -o "$out/Contents/MacOS/HallMonitor"

(cd "$root" && go build -o "macos/$out/Contents/Helpers/hallmonitor" ./cmd/hallmonitor)

codesign --force --deep -s - "$out" >/dev/null
echo "built $out"

# A dev build shares the real app's bundle id: keep Launch Services (and so
# the login item) on the installed app, not this copy.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -u "$PWD/$out" 2>/dev/null || true

if [[ ${1:-} == --install ]]; then
  pkill -x HallMonitor 2>/dev/null || true
  mkdir -p ~/Applications
  rm -rf ~/Applications/HallMonitor.app
  cp -R "$out" ~/Applications/
  open ~/Applications/HallMonitor.app
  echo "installed ~/Applications/HallMonitor.app"
fi
