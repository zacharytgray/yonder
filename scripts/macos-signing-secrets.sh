#!/bin/sh
# one-time setup: puts the macos signing and notarization secrets on the github repo, so
# release.yml can sign and notarize the darwin binaries.
#
# run it yourself, on the mac whose login keychain holds the "Developer ID Application"
# identity. it never prints a secret: values go from files and prompts straight into
# `gh secret set` over stdin, and every temp file is deleted on exit.
#
# usage: scripts/macos-signing-secrets.sh [owner/repo]
#
# it sets:
#   MACOS_SIGN_P12          the developer id cert and its key, as a base64 .p12
#   MACOS_SIGN_PASSWORD     a random password for that .p12 (you never need to know it)
#   MACOS_NOTARY_KEY        an app store connect api key (.p8), base64
#   MACOS_NOTARY_KEY_ID     that key's id
#   MACOS_NOTARY_ISSUER_ID  your app store connect issuer id
#
# the api key: App Store Connect -> Users and Access -> Integrations -> App Store Connect API
# -> Team Keys -> "+". Access "Developer" is enough to notarize. The issuer id is shown above
# the keys table. Apple lets you download the .p8 once, right after you create it. A notarytool
# keychain profile can't be reused here: CI needs the .p8 file itself.
set -eu

repo=${1:-zacharytgray/yonder}
openssl=/usr/bin/openssl # libressl reads the legacy .p12 that `security export` writes

die() {
	echo "error: $*" >&2
	exit 1
}

[ "$(uname -s)" = Darwin ] || die "run this on the mac that holds the developer id identity"
command -v gh >/dev/null || die "gh is not installed"
gh auth status >/dev/null 2>&1 || die "gh is not logged in (gh auth login)"
gh secret list --repo "$repo" >/dev/null || die "gh can't manage secrets on $repo"

# pick the identity
ids=$(security find-identity -v -p codesigning | grep '"Developer ID Application: ' || true)
[ -n "$ids" ] || die "no valid Developer ID Application identity in the keychain"
if [ "$(echo "$ids" | wc -l)" -gt 1 ]; then
	echo "$ids"
	die "more than one Developer ID Application identity; remove the stale ones first"
fi
name=$(echo "$ids" | sed -E 's/.*"(.*)"/\1/')
team=$(echo "$name" | sed -E 's/.*\(([A-Z0-9]+)\)$/\1/')
echo "identity: $name"

tmp=$(mktemp -d)
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' INT TERM HUP
umask 077

# security export can only export every identity in a keychain at once, so export them all
# under a throwaway password, then repack just the developer id cert and its key
exportpass=$($openssl rand -hex 24)
p12pass=$($openssl rand -hex 24)
export exportpass p12pass
keychain=${KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}

echo "exporting identities from $keychain (macos may ask you to allow each key)"
security export -k "$keychain" -t identities -f pkcs12 -P "$exportpass" -o "$tmp/all.p12" >/dev/null
dump() { $openssl pkcs12 -in "$tmp/all.p12" -passin env:exportpass -nodes 2>/dev/null; }
dump >/dev/null || die "could not read the exported identities"

# the developer id cert and its key share a localKeyID
bagid=$(dump | awk -v team="$team" '
	/localKeyID:/ { sub(/.*localKeyID: */, ""); id = $0 }
	/^subject=/ && /Developer ID Application: / && index($0, "(" team ")") { print id; exit }
')
[ -n "$bagid" ] || die "the developer id cert is not in $keychain (set KEYCHAIN=...)"
dump | awk -v want="$bagid" -v cert="$tmp/cert.pem" -v key="$tmp/key.pem" '
	/localKeyID:/ { sub(/.*localKeyID: */, ""); id = $0 }
	/^-----BEGIN CERTIFICATE-----/ { out = (id == want) ? cert : "" }
	/^-----BEGIN .*PRIVATE KEY-----/ { out = (id == want) ? key : "" }
	out != "" { print > out }
	/^-----END / { out = "" }
'
[ -s "$tmp/cert.pem" ] && [ -s "$tmp/key.pem" ] || die "could not pair the developer id cert with its key"

$openssl pkcs12 -export -in "$tmp/cert.pem" -inkey "$tmp/key.pem" -name "$name" \
	-passout env:p12pass -out "$tmp/signing.p12"
rm -f "$tmp/all.p12" "$tmp/key.pem"

# the repacked .p12 must hold exactly the developer id cert
$openssl pkcs12 -in "$tmp/signing.p12" -passin env:p12pass -nokeys -clcerts 2>/dev/null |
	$openssl x509 -noout -subject | grep -q "Developer ID Application: .*($team)" ||
	die "the repacked .p12 does not hold the developer id cert"

# the notarization api key
printf "path to the App Store Connect API key (AuthKey_XXXXXXXXXX.p8): "
read -r p8
case $p8 in "~/"*) p8="$HOME/${p8#"~/"}" ;; esac
[ -f "$p8" ] || die "no file at $p8"
grep -q 'BEGIN PRIVATE KEY' "$p8" || die "$p8 is not a .p8 private key"
guess=$(basename "$p8" | sed -nE 's/^AuthKey_([A-Z0-9]+)\.p8$/\1/p')
printf "key id [%s]: " "$guess"
read -r keyid
keyid=${keyid:-$guess}
[ -n "$keyid" ] || die "a key id is needed"
printf "issuer id (a uuid, above the keys table): "
read -r issuer
[ -n "$issuer" ] || die "an issuer id is needed"

echo "checking the api key with apple (notarytool history)"
xcrun notarytool history --key "$p8" --key-id "$keyid" --issuer "$issuer" >/dev/null 2>&1 ||
	die "apple rejected the api key, key id or issuer id"

echo "setting secrets on $repo"
base64 -i "$tmp/signing.p12" | tr -d '\n' | gh secret set MACOS_SIGN_P12 --repo "$repo"
printf %s "$p12pass" | gh secret set MACOS_SIGN_PASSWORD --repo "$repo"
base64 -i "$p8" | tr -d '\n' | gh secret set MACOS_NOTARY_KEY --repo "$repo"
printf %s "$keyid" | gh secret set MACOS_NOTARY_KEY_ID --repo "$repo"
printf %s "$issuer" | gh secret set MACOS_NOTARY_ISSUER_ID --repo "$repo"

echo "done. team $team. release.yml checks every darwin binary is signed by it."
gh secret list --repo "$repo" | grep '^MACOS_' || true
