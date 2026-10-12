#!/bin/bash
# Package zhenhanvn macOS .app bundle + zip (no DMG, no macOS required).
# Usage: ./package-osx-zhenhanvn.sh <arch> <publishOutputDir> <version>
#   arch: macos-64 | macos-arm64
#   publishOutputDir: output of `dotnet publish v2rayN.Desktop -r osx-x64|osx-arm64 -p:SelfContained=true`
#   version: e.g. 7.24.9
set -e

Arch="$1"
OutputPath="$2"
Version="$3"

AppDir="zhenhanvn.app"
WorkDir="zhenhanvn-pkg-${Arch}"
rm -rf "$WorkDir"
mkdir -p "$WorkDir"
cd "$WorkDir"

# 1) Download + extract upstream proxy cores (kept as upstream v2rayN-core-bin)
CoreZip="v2rayN-${Arch}.zip"
if command -v wget >/dev/null 2>&1; then
    wget -nv -O "$CoreZip" "https://github.com/2dust/v2rayN-core-bin/raw/refs/heads/master/$CoreZip"
else
    curl -fsSL -o "$CoreZip" "https://github.com/2dust/v2rayN-core-bin/raw/refs/heads/master/$CoreZip"
fi
if command -v 7z >/dev/null 2>&1; then
    7z x -y "$CoreZip" >/dev/null
elif command -v pwsh >/dev/null 2>&1; then
    pwsh -NoProfile -Command "Expand-Archive -Force '$CoreZip' '.'"
else
    python -c "import zipfile,sys; zipfile.ZipFile(sys.argv[1]).extractall('.')" "$CoreZip"
fi

# 2) Assemble .app bundle
mkdir -p "$AppDir/Contents/MacOS" "$AppDir/Contents/Resources"
cp -rf "$OutputPath"/* "$AppDir/Contents/MacOS/"
cp -rf "v2rayN-${Arch}"/* "$AppDir/Contents/MacOS/" 2>/dev/null || true
# icns keeps its original filename in publish output (AssemblyName rename doesn't affect it)
cp -f "$OutputPath/v2rayN.icns" "$AppDir/Contents/Resources/AppIcon.icns"
echo "When this file exists, app will not store configs under this folder" > "$AppDir/Contents/MacOS/NotStoreConfigHere.txt"
chmod +x "$AppDir/Contents/MacOS/zhenhanvn"

# 3) Info.plist (display name = zhenhanvn)
cat > "$AppDir/Contents/Info.plist" <<-EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleLocalizations</key>
  <array>
    <string>zh-Hans</string>
    <string>zh-Hant</string>
    <string>en</string>
    <string>fa</string>
    <string>fr</string>
    <string>ru</string>
    <string>hu</string>
  </array>
  <key>CFBundleDisplayName</key>
  <string>zhenhanvn</string>
  <key>CFBundleExecutable</key>
  <string>zhenhanvn</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>CFBundleIconName</key>
  <string>AppIcon</string>
  <key>CFBundleIdentifier</key>
  <string>com.boosoo.zhenhanvn</string>
  <key>CFBundleName</key>
  <string>zhenhanvn</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>${Version}</string>
  <key>CSResourcesFileMapped</key>
  <true/>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>LSMinimumSystemVersion</key>
  <string>10.15</string>
</dict>
</plist>
EOF

# 4) Zip (zip root contains zhenhanvn.app directly)
rm -f "../zhenhanvn-${Arch}.zip"
if command -v 7z >/dev/null 2>&1; then
    7z a -tzip "../zhenhanvn-${Arch}.zip" "$AppDir" >/dev/null
elif command -v pwsh >/dev/null 2>&1; then
    pwsh -NoProfile -Command "Compress-Archive -Force '$AppDir' '../zhenhanvn-${Arch}.zip'"
else
    python -c "import zipfile,os,sys; z=zipfile.ZipFile(sys.argv[1],'w',zipfile.ZIP_DEFLATED); [z.write(os.path.join(r,f),os.path.join(r,f)) for r,d,fs in os.walk(sys.argv[2]) for f in fs]; z.close()" "../zhenhanvn-${Arch}.zip" "$AppDir"
fi

cd ..
rm -rf "$WorkDir"
echo "OK: zhenhanvn-${Arch}.zip"
