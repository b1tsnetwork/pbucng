#!/bin/sh
# Baut alle Container-Images dieses Repositorys und schiebt sie optional in eine Registry.
#
#   ./build-images.sh                         # lokal bauen (aktuelle Plattform), Version 1.0.0-rc.1
#   ./build-images.sh --push                  # bauen und veröffentlichen (amd64 + arm64)
#   REGISTRY=registry.firma.local/telefonbuch VERSION=1.0.0-rc.1 ./build-images.sh --push
#   ./build-images.sh --only phonebook,ucng   # nur einzelne Images
#   ./build-images.sh --dry-run               # nur anzeigen, was gebaut würde
#
# Vorher bei der Registry anmelden (z. B. „docker login ghcr.io“ mit einem Token, das
# packages:write darf). In GitHub erledigt das der Workflow release.yml bei jedem Tag v*.
set -eu
cd "$(dirname "$0")"

REGISTRY="${REGISTRY:-ghcr.io/b1tsnetwork}"
VERSION="${VERSION:-1.0.0-rc.1}"
UCNG_TAGS="${UCNG_TAGS:-sqlite mysql}"          # Speicher-Treiber der Kommunikation
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
SOURCE_URL="${SOURCE_URL:-}"                    # AGPL §13: Adresse des Quellcodes dieser Fassung (ucng/)
push=0; dry=0; only=""
while [ $# -gt 0 ]; do
	case "$1" in
	--push) push=1 ;;
	--dry-run) dry=1 ;;
	--only) only="$2"; shift ;;
	--platform) PLATFORMS="$2"; shift ;;
	-h|--help) sed -n '2,12p' "$0"; exit 0 ;;
	*) echo "Unbekannte Option: $1" >&2; exit 2 ;;
	esac
	shift
done
if [ -z "$SOURCE_URL" ] && command -v git >/dev/null 2>&1 && git rev-parse HEAD >/dev/null 2>&1; then
	url="$(git config --get remote.origin.url 2>/dev/null || true)"
	case "$url" in git@github.com:*) url="https://github.com/${url#git@github.com:}" ;; esac
	[ -n "$url" ] && SOURCE_URL="${url%.git}/tree/$(git rev-parse HEAD)/ucng"
fi

# Name | Kontext | Dockerfile
IMAGES="phonebook|phonebook|phonebook/Dockerfile
ucng|ucng|ucng/Dockerfile
ucng-voice|ucng|ucng/Dockerfile.voice
ucng-beacon|ucng|ucng/Dockerfile.beacon
ucng-bot-music|ucng|ucng/Dockerfile.music"

if [ "$push" = 1 ]; then out="--push --platform $PLATFORMS"; else out="--load"; fi
latest=""
case "$VERSION" in *-*) ;; *) latest=1 ;; esac   # Vorabversionen bekommen kein „latest“

echo "$IMAGES" | while IFS='|' read -r name ctx file; do
	if [ -n "$only" ]; then case ",$only," in *",$name,"*) ;; *) continue ;; esac; fi
	tags="-t $REGISTRY/$name:$VERSION"
	[ -n "$latest" ] && tags="$tags -t $REGISTRY/$name:latest"
	cmd="docker buildx build $out $tags -f $file --build-arg VERSION=$VERSION"
	if [ "$ctx" = ucng ]; then
		cmd="$cmd --build-arg TAGS=\"$UCNG_TAGS\""
		[ -n "$SOURCE_URL" ] && cmd="$cmd --build-arg SOURCE_URL=$SOURCE_URL"
	fi
	cmd="$cmd $ctx"
	echo "==> $name"
	echo "    $cmd"
	[ "$dry" = 1 ] || eval "$cmd"
done
[ "$dry" = 1 ] || echo "Fertig: $REGISTRY/*:$VERSION"
