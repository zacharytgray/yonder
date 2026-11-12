#!/bin/sh
# checks that each darwin release archive holds a yonder binary signed with a developer id
# from the given team, with hardened runtime, a secure timestamp and a notarization ticket.
# runs on macos (codesign). usage: check-darwin-signature.sh <team-id> <archive.tar.gz>...
set -eu

[ "$(uname -s)" = Darwin ] || { echo "needs macos (codesign)" >&2; exit 2; }
[ $# -ge 2 ] || { echo "usage: $0 <team-id> <archive.tar.gz>..." >&2; exit 2; }
team=$1
shift

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

bad() {
	echo "FAIL $name: $1" >&2
	this=1
}

fail=0
n=0
for archive in "$@"; do
	n=$((n + 1))
	this=0
	name=$(basename "$archive")
	dir="$work/$n"
	mkdir -p "$dir"
	tar -xzf "$archive" -C "$dir" yonder
	bin="$dir/yonder"

	if ! codesign --verify --strict --verbose=2 "$bin"; then
		bad "signature does not verify"
		fail=1
		continue
	fi
	info=$(codesign -dv --verbose=2 "$bin" 2>&1)
	echo "$info" | grep -qx "TeamIdentifier=$team" || bad "team is not $team"
	echo "$info" | grep -q "^Authority=Developer ID Application: .* ($team)\$" || bad "not a developer id application cert for $team"
	echo "$info" | grep -q '^CodeDirectory .*flags=.*runtime' || bad "hardened runtime is off"
	echo "$info" | grep -q '^Timestamp=' || bad "no secure timestamp"

	# the ticket lookup is online and can lag the accepted status by a little
	ok=0
	tries=${NOTARY_TRIES:-6}
	while [ "$tries" -gt 0 ]; do
		tries=$((tries - 1))
		if codesign --verify -R='notarized' --check-notarization "$bin" 2>/dev/null; then
			ok=1
			break
		fi
		[ "$tries" -eq 0 ] || sleep 20
	done
	[ "$ok" = 1 ] || bad "no notarization ticket"
	# -t exec rejects any bare binary as "not an app"; -t install still names the source
	spctl -a -vv -t install "$bin" 2>&1 | grep -qx 'source=Notarized Developer ID' || bad "gatekeeper does not see it as notarized"

	if [ "$this" = 1 ]; then fail=1; else echo "ok   $name"; fi
done
exit "$fail"
