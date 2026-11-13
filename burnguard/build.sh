#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SDK_DIR=${ANDROID_SDK_ROOT:-/Users/pipipeng/.android-sdk}
BUILD_TOOLS=${BUILD_TOOLS:-"$SDK_DIR/build-tools/33.0.1"}
PLATFORM=${PLATFORM:-"$SDK_DIR/platforms/android-33/android.jar"}
OUT_DIR="$ROOT_DIR/build"
GEN_DIR="$OUT_DIR/gen"
CLASSES_DIR="$OUT_DIR/classes"
DEX_DIR="$OUT_DIR/dex"
KEYSTORE=${BURNGUARD_KEYSTORE:-"$OUT_DIR/burnguard-debug.keystore"}
ALIAS=${BURNGUARD_ALIAS:-burnguard}
PASS=${BURNGUARD_PASS:-android}

mkdir -p "$OUT_DIR" "$GEN_DIR" "$CLASSES_DIR" "$DEX_DIR"

"$BUILD_TOOLS/aapt2" compile --dir "$ROOT_DIR/res" -o "$OUT_DIR/resources.zip"
"$BUILD_TOOLS/aapt2" link \
  -o "$OUT_DIR/burnguard-unsigned.apk" \
  -I "$PLATFORM" \
  --manifest "$ROOT_DIR/AndroidManifest.xml" \
  -R "$OUT_DIR/resources.zip" \
  --auto-add-overlay \
  --java "$GEN_DIR" \
  --min-sdk-version 29 \
  --target-sdk-version 33

javac --release 8 \
  -classpath "$PLATFORM" \
  -d "$CLASSES_DIR" \
  $(find "$ROOT_DIR/src" "$GEN_DIR" -name '*.java' -print)

"$BUILD_TOOLS/d8" --release --min-api 29 --output "$DEX_DIR" \
  $(find "$CLASSES_DIR" -name '*.class' -print)

cp "$OUT_DIR/burnguard-unsigned.apk" "$OUT_DIR/burnguard-aligned-unsigned.apk"
(
  cd "$DEX_DIR"
  zip -q "$OUT_DIR/burnguard-aligned-unsigned.apk" classes.dex
)

"$BUILD_TOOLS/zipalign" -f 4 \
  "$OUT_DIR/burnguard-aligned-unsigned.apk" \
  "$OUT_DIR/burnguard-aligned.apk"

if [ ! -f "$KEYSTORE" ]; then
  keytool -genkeypair \
    -keystore "$KEYSTORE" \
    -storepass "$PASS" \
    -keypass "$PASS" \
    -alias "$ALIAS" \
    -keyalg RSA \
    -keysize 2048 \
    -validity 3650 \
    -dname "CN=VOLT FORGE Burn Guard, OU=Local Build, O=MyToolMan, C=CN"
fi

"$BUILD_TOOLS/apksigner" sign \
  --ks "$KEYSTORE" \
  --ks-key-alias "$ALIAS" \
  --ks-pass "pass:$PASS" \
  --key-pass "pass:$PASS" \
  --out "$OUT_DIR/burnguard.apk" \
  "$OUT_DIR/burnguard-aligned.apk"

"$BUILD_TOOLS/apksigner" verify --verbose "$OUT_DIR/burnguard.apk"
sha256sum "$OUT_DIR/burnguard.apk" 2>/dev/null || shasum -a 256 "$OUT_DIR/burnguard.apk"
