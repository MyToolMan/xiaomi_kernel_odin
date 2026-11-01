#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

ROOT_FLAVOR="${ROOT_FLAVOR:-sukisu}"
JOBS="${JOBS:-$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN)}"
BUILD_DIR="${BUILD_DIR:-$REPO_ROOT/out-odin}"
KERNEL_OUT="$BUILD_DIR/kernel"
ARTIFACT_DIR="$BUILD_DIR/artifacts"
BUILD_LOG="$ARTIFACT_DIR/kernel.log"

case "$ROOT_FLAVOR" in
  sukisu)       ROOT_SOURCE="$REPO_ROOT/scripts/root/SukiSU-Ultra/kernelsu" ;;
  kernelsu)     ROOT_SOURCE="$REPO_ROOT/scripts/root/Kernelsu/kernelsu" ;;
  kernelsu-next) ROOT_SOURCE="$REPO_ROOT/scripts/root/Kernelsu-next/kernelsu" ;;
  mksu)         ROOT_SOURCE="$REPO_ROOT/scripts/root/MKSU/kernelsu" ;;
  none)         ROOT_SOURCE="" ;;
  *)
    echo "Unsupported ROOT_FLAVOR=$ROOT_FLAVOR" >&2
    exit 2
    ;;
esac

rm -rf "$BUILD_DIR"
mkdir -p "$KERNEL_OUT" "$ARTIFACT_DIR"

if [[ -n "$ROOT_SOURCE" ]]; then
  [[ -d "$ROOT_SOURCE" ]] || { echo "Missing root source: $ROOT_SOURCE" >&2; exit 2; }
  rm -rf drivers/kernelsu
  cp -a "$ROOT_SOURCE" drivers/kernelsu
fi

CLANG_DIR="$REPO_ROOT/scripts/clang-r383902b1"
GCC64_DIR="$REPO_ROOT/scripts/aarch64-linux-android-4.9"
GCC_DIR="$REPO_ROOT/scripts/arm-linux-androideabi-4.9"
export PATH="$CLANG_DIR/bin:$GCC64_DIR/bin:$GCC_DIR/bin:$PATH"
export KBUILD_BUILD_USER="odin-ci"
export KBUILD_BUILD_HOST="github-actions"

MAKE_ARGS=(
  ARCH=arm64
  O="$KERNEL_OUT"
  CC=clang
  LD=ld.lld
  AR=llvm-ar
  NM=llvm-nm
  OBJCOPY=llvm-objcopy
  OBJDUMP=llvm-objdump
  READELF=llvm-readelf
  STRIP=llvm-strip
  CLANG_TRIPLE=aarch64-linux-gnu-
  CROSS_COMPILE=aarch64-linux-gnu-
  CROSS_COMPILE_COMPAT=arm-linux-gnueabi-
  LLVM=1
  LLVM_IAS=1
  HOSTCC=gcc
  HOSTCXX=g++
)

run_logged() {
  "$@" 2>&1 | tee -a "$BUILD_LOG"
}

{
  echo "repository=$(git rev-parse HEAD)"
  echo "branch=$(git rev-parse --abbrev-ref HEAD)"
  echo "root_flavor=$ROOT_FLAVOR"
  echo "jobs=$JOBS"
  echo "host=$(uname -a)"
  "$CLANG_DIR/bin/clang" --version
  "$CLANG_DIR/bin/ld.lld" --version
} | tee "$BUILD_LOG"

run_logged make "${MAKE_ARGS[@]}" odin_defconfig

if [[ "$ROOT_FLAVOR" != "none" ]]; then
  run_logged "$REPO_ROOT/scripts/config" --file "$KERNEL_OUT/.config" \
    --enable MODULES \
    --enable MODVERSIONS \
    --enable KSU \
    --enable KSU_SUSFS \
    --enable KSU_SUSFS_HAS_MAGIC_MOUNT

  if [[ "$ROOT_FLAVOR" == "sukisu" ]]; then
    run_logged "$REPO_ROOT/scripts/config" --file "$KERNEL_OUT/.config" \
      --enable KPM
  fi
else
  run_logged "$REPO_ROOT/scripts/config" --file "$KERNEL_OUT/.config" \
    --disable KSU \
    --disable KPM
fi

run_logged make "${MAKE_ARGS[@]}" olddefconfig

{
  echo "--- selected configuration ---"
  grep -E \
    '^(CONFIG_(LOCALVERSION|MODULES|MODVERSIONS|KSU|KSU_|KPM|SUSFS|QCOM_KGSL|DRM_MSM|SCHED_WALT)=|# CONFIG_(DRM_MSM|SCHED_WALT) is not set)' \
    "$KERNEL_OUT/.config" || true
} | tee -a "$BUILD_LOG"

run_logged make "${MAKE_ARGS[@]}" -j"$JOBS" Image

IMAGE="$KERNEL_OUT/arch/arm64/boot/Image"
[[ -s "$IMAGE" ]] || { echo "Kernel Image was not generated" >&2; exit 1; }

cp -a "$IMAGE" "$ARTIFACT_DIR/Image"
cp -a "$KERNEL_OUT/.config" "$ARTIFACT_DIR/defconfig"

PACKAGE_DIR="$BUILD_DIR/anykernel"
cp -a "$REPO_ROOT/scripts/AnyKernel3" "$PACKAGE_DIR"
cp -a "$IMAGE" "$PACKAGE_DIR/Image"
chmod 755 "$PACKAGE_DIR/META-INF/com/google/android/update-binary"
chmod 755 "$PACKAGE_DIR/tools/busybox"

MODULE_COUNT="$(grep -c '=m$' "$KERNEL_OUT/.config" || true)"
if [[ "$MODULE_COUNT" -gt 0 ]]; then
  run_logged make "${MAKE_ARGS[@]}" INSTALL_MOD_PATH="$BUILD_DIR/modules" \
    INSTALL_MOD_STRIP=1 modules modules_install
  MODULE_DEST="$PACKAGE_DIR/modules/vendor/lib/modules"
  mkdir -p "$MODULE_DEST"
  find "$BUILD_DIR/modules/lib/modules" -type f \
    \( -name '*.ko' -o -name 'modules.alias' -o -name 'modules.dep' \
       -o -name 'modules.softdep' -o -name 'modules.order' \) \
    -exec cp -a {} "$MODULE_DEST/" \;
  sed -i 's/do.modules=0/do.modules=1/' "$PACKAGE_DIR/anykernel.sh"
fi

SHORT_SHA="$(git rev-parse --short=12 HEAD)"
PACKAGE_NAME="odin-full-${ROOT_FLAVOR}-5.4.281-g${SHORT_SHA}.zip"
sed -i \
  "s|^kernel.string=.*|kernel.string=Mi MIX4 (odin) 5.4.281 ${ROOT_FLAVOR} g${SHORT_SHA}|" \
  "$PACKAGE_DIR/anykernel.sh"

(
  cd "$PACKAGE_DIR"
  zip -q -r9 "$ARTIFACT_DIR/$PACKAGE_NAME" .
)

{
  echo "repo=MyToolMan/xiaomi_kernel_odin"
  echo "commit=$SHORT_SHA"
  echo "root_flavor=$ROOT_FLAVOR"
  echo "kernel=$(strings "$IMAGE" | grep -m1 '^Linux version ' || true)"
  echo "modules=$MODULE_COUNT"
} | tee "$ARTIFACT_DIR/build-info.txt"

(
  cd "$ARTIFACT_DIR"
  sha256sum Image defconfig kernel.log build-info.txt "$PACKAGE_NAME" > SHA256SUMS
)

cat "$ARTIFACT_DIR/SHA256SUMS"
